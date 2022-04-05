// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package builtinv1

import (
	"context"
	"io"
	"sync"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin/v1/internal/fromplugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin/v1/internal/toplugin"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

// destinationPluginAdapter implements the destination plugin interface used
// internally in Conduit and relays the calls to a destination plugin defined in
// conduit-connector-protocol (cpluginv1). This adapter needs to make sure it behaves in the
// same way as the standalone plugin adapter, which communicates with the plugin
// through gRPC, so that the caller can use both of them interchangeably.
// TODO make sure a panic in a plugin doesn't crash Conduit
type destinationPluginAdapter struct {
	impl cpluginv1.DestinationPlugin
	// logger is used as the internal logger of destinationPluginAdapter.
	logger log.CtxLogger
	// ctxLogger is attached to the context of each call to the plugin.
	ctxLogger zerolog.Logger

	stream *destinationRunStream
}

var _ plugin.DestinationPlugin = (*destinationPluginAdapter)(nil)

func newDestinationPluginAdapter(impl cpluginv1.DestinationPlugin, logger log.CtxLogger) *destinationPluginAdapter {
	return &destinationPluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtinv1.destinationPluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent()}
}

func (s *destinationPluginAdapter) withLogger(ctx context.Context) context.Context {
	return s.logger.WithContext(ctx)
}

func (s *destinationPluginAdapter) Configure(ctx context.Context, cfg map[string]string) error {
	req, err := toplugin.DestinationConfigureRequest(cfg)
	if err != nil {
		return err
	}
	s.logger.Trace(ctx).Msg("calling Configure")
	resp, err := s.impl.Configure(s.withLogger(ctx), req)
	if err != nil {
		// TODO create new errors as strings, this happens when they are
		//  transmitted through gRPC and we don't want to falsely rely on types
		//  in built-in plugins
		return err
	}
	_ = resp // empty response
	return err
}

func (s *destinationPluginAdapter) Start(ctx context.Context) error {
	if s.stream != nil {
		return cerrors.New("plugin already running")
	}

	req := toplugin.DestinationStartRequest()
	s.logger.Trace(ctx).Msg("calling Start")
	resp, err := s.impl.Start(s.withLogger(ctx), req)
	if err != nil {
		return err
	}
	_ = resp // empty response

	s.stream = newDestinationRunStream(ctx)
	go func() {
		s.logger.Trace(ctx).Msg("calling Run")
		err := s.impl.Run(s.withLogger(ctx), s.stream)
		if err != nil {
			s.stream.stopAll(cerrors.Errorf("error in run: %w", err))
		} else {
			s.stream.stopAll(plugin.ErrStreamNotOpen)
		}
		s.logger.Trace(ctx).Msg("Run stopped")
	}()

	return err
}

func (s *destinationPluginAdapter) Write(ctx context.Context, r record.Record) (err error) {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	req, err := toplugin.DestinationRunRequest(r)
	if err != nil {
		return err
	}

	s.logger.Trace(ctx).Msg("sending record")
	err = s.stream.sendInternal(req)
	if err != nil {
		return cerrors.Errorf("builtin plugin send failed: %w", err)
	}

	return nil
}

func (s *destinationPluginAdapter) Ack(ctx context.Context) (record.Position, error) {
	if s.stream == nil {
		return nil, plugin.ErrStreamNotOpen
	}

	s.logger.Trace(ctx).Msg("receiving ack")
	resp, err := s.stream.recvInternal()
	if err != nil {
		return nil, err
	}

	position, reason := fromplugin.DestinationRunResponse(resp)
	if reason != "" {
		return position, cerrors.New(reason)
	}

	return position, nil
}

func (s *destinationPluginAdapter) Stop(ctx context.Context) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	s.stream.stopSend()

	s.logger.Trace(ctx).Msg("calling Stop")
	resp, err := s.impl.Stop(s.withLogger(ctx), toplugin.DestinationStopRequest())
	if err != nil {
		return err
	}
	_ = resp // empty response

	return nil
}

func (s *destinationPluginAdapter) Teardown(ctx context.Context) error {
	s.logger.Trace(ctx).Msg("calling Teardown")
	resp, err := s.impl.Teardown(s.withLogger(ctx), toplugin.DestinationTeardownRequest())
	if err != nil {
		return err
	}
	_ = resp // empty response

	return nil
}

func newDestinationRunStream(ctx context.Context) *destinationRunStream {
	sendCtx, sendCancel := context.WithCancel(ctx)
	recvCtx, recvCancel := context.WithCancel(ctx)
	return &destinationRunStream{
		sendCtx:   sendCtx,
		recvCtx:   recvCtx,
		closeSend: sendCancel,
		closeRecv: recvCancel,
		reqChan:   make(chan cpluginv1.DestinationRunRequest),
		respChan:  make(chan cpluginv1.DestinationRunResponse),
	}
}

type destinationRunStream struct {
	sendCtx   context.Context
	recvCtx   context.Context
	closeSend context.CancelFunc
	closeRecv context.CancelFunc
	reqChan   chan cpluginv1.DestinationRunRequest
	respChan  chan cpluginv1.DestinationRunResponse

	reason error
	m      sync.RWMutex
}

func (s *destinationRunStream) Send(resp cpluginv1.DestinationRunResponse) error {
	select {
	case <-s.sendCtx.Done():
		return io.EOF
	case s.respChan <- resp:
		return nil
	}
}

func (s *destinationRunStream) Recv() (cpluginv1.DestinationRunRequest, error) {
	select {
	case <-s.recvCtx.Done():
		return cpluginv1.DestinationRunRequest{}, io.EOF
	case req := <-s.reqChan:
		return req, nil
	}
}

func (s *destinationRunStream) recvInternal() (cpluginv1.DestinationRunResponse, error) {
	// note that contexts are named from the perspective of the server, while
	// this function is used from the perspective of the client
	select {
	case <-s.sendCtx.Done():
		return cpluginv1.DestinationRunResponse{}, s.reason
	case resp := <-s.respChan:
		return resp, nil
	}
}

func (s *destinationRunStream) sendInternal(req cpluginv1.DestinationRunRequest) error {
	// note that contexts are named from the perspective of the server, while
	// this function is used from the perspective of the client
	select {
	case <-s.recvCtx.Done():
		return plugin.ErrStreamNotOpen
	case s.reqChan <- req:
		return nil
	}
}

func (s *destinationRunStream) stopAll(reason error) {
	s.reason = reason
	s.closeSend()
	s.closeRecv()
}

func (s *destinationRunStream) stopSend() {
	// we want to stop the stream towards the server, so we close the receiving
	// context from the servers perspective
	s.closeRecv()
}
