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

	"github.com/conduitio/conduit-plugin/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin/v1/internal/fromplugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin/v1/internal/toplugin"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

// TODO make sure a panic in a plugin doesn't crash Conduit
type sourcePluginAdapter struct {
	impl cpluginv1.SourcePlugin
	// logger is used as the internal logger of sourcePluginAdapter.
	logger log.CtxLogger
	// ctxLogger is attached to the context of each call to the plugin.
	ctxLogger zerolog.Logger

	stream *sourceRunStream
}

var _ plugin.SourcePlugin = (*sourcePluginAdapter)(nil)

func newSourcePluginAdapter(impl cpluginv1.SourcePlugin, logger log.CtxLogger) *sourcePluginAdapter {
	return &sourcePluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtinv1.sourcePluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent(),
	}
}

func (s *sourcePluginAdapter) withLogger(ctx context.Context) context.Context {
	return s.ctxLogger.WithContext(ctx)
}

func (s *sourcePluginAdapter) Configure(ctx context.Context, cfg map[string]string) error {
	req, err := toplugin.SourceConfigureRequest(cfg)
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

func (s *sourcePluginAdapter) Start(ctx context.Context, p record.Position) error {
	if s.stream != nil {
		return cerrors.New("plugin already running")
	}

	req, err := toplugin.SourceStartRequest(p)
	if err != nil {
		return err
	}

	s.logger.Trace(ctx).Msg("calling Start")
	resp, err := s.impl.Start(s.withLogger(ctx), req)
	if err != nil {
		return err
	}
	_ = resp // empty response

	s.stream = newSourceRunStream(ctx)
	go func() {
		s.logger.Trace(ctx).Msg("calling Run")
		err := s.impl.Run(s.withLogger(ctx), s.stream)
		if err != nil {
			s.stream.stop(cerrors.Errorf("error in run: %w", err))
		} else {
			s.stream.stop(plugin.ErrStreamNotOpen)
		}
		s.logger.Trace(ctx).Msg("Run stopped")
	}()

	return err
}

func (s *sourcePluginAdapter) Read(ctx context.Context) (record.Record, error) {
	if s.stream == nil {
		return record.Record{}, plugin.ErrStreamNotOpen
	}

	s.logger.Trace(ctx).Msg("receiving record")
	resp, err := s.stream.recvInternal()
	if err != nil {
		return record.Record{}, cerrors.Errorf("builtin plugin receive failed: %w", err)
	}

	out, err := fromplugin.SourceRunResponse(resp)
	if err != nil {
		return record.Record{}, err
	}

	return out, nil
}

func (s *sourcePluginAdapter) Ack(ctx context.Context, p record.Position) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	req, err := toplugin.SourceRunRequest(p)
	if err != nil {
		return err
	}

	s.logger.Trace(ctx).Msg("sending ack")
	err = s.stream.sendInternal(req)
	if err != nil {
		return cerrors.Errorf("builtin plugin send failed: %w", err)
	}

	return nil
}

func (s *sourcePluginAdapter) Stop(ctx context.Context) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	s.logger.Trace(ctx).Msg("calling Stop")
	resp, err := s.impl.Stop(s.withLogger(ctx), toplugin.SourceStopRequest())
	if err != nil {
		return err
	}
	_ = resp // empty response

	return nil
}

func (s *sourcePluginAdapter) Teardown(ctx context.Context) error {
	s.logger.Trace(ctx).Msg("calling Teardown")
	resp, err := s.impl.Teardown(s.withLogger(ctx), toplugin.SourceTeardownRequest())
	if err != nil {
		return err
	}
	_ = resp // empty response

	return nil
}

func newSourceRunStream(ctx context.Context) *sourceRunStream {
	return &sourceRunStream{
		ctx:      ctx,
		stopChan: make(chan struct{}),
		reqChan:  make(chan cpluginv1.SourceRunRequest),
		respChan: make(chan cpluginv1.SourceRunResponse),
	}
}

type sourceRunStream struct {
	ctx      context.Context
	stopChan chan struct{}
	reqChan  chan cpluginv1.SourceRunRequest
	respChan chan cpluginv1.SourceRunResponse

	reason error
	m      sync.RWMutex
}

func (s *sourceRunStream) Send(resp cpluginv1.SourceRunResponse) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.stopChan:
		return io.EOF
	case s.respChan <- resp:
		return nil
	}
}

func (s *sourceRunStream) Recv() (cpluginv1.SourceRunRequest, error) {
	select {
	case <-s.ctx.Done():
		return cpluginv1.SourceRunRequest{}, s.ctx.Err()
	case <-s.stopChan:
		return cpluginv1.SourceRunRequest{}, io.EOF
	case req := <-s.reqChan:
		return req, nil
	}
}

func (s *sourceRunStream) recvInternal() (cpluginv1.SourceRunResponse, error) {
	select {
	case <-s.ctx.Done():
		return cpluginv1.SourceRunResponse{}, cerrors.New(s.ctx.Err().Error())
	case <-s.stopChan:
		return cpluginv1.SourceRunResponse{}, s.reason
	case resp := <-s.respChan:
		return resp, nil
	}
}

func (s *sourceRunStream) sendInternal(req cpluginv1.SourceRunRequest) error {
	select {
	case <-s.ctx.Done():
		return cerrors.New(s.ctx.Err().Error()) // TODO should this be s.ctx.Err()?
	case <-s.stopChan:
		return s.reason
	case s.reqChan <- req:
		return nil
	}
}

func (s *sourceRunStream) stop(reason error) {
	s.reason = reason
	close(s.stopChan)
}
