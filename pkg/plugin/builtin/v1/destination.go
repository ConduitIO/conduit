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
// conduit-connector-protocol (cpluginv1). This adapter needs to make sure it
// behaves in the same way as the standalone plugin adapter, which communicates
// with the plugin through gRPC, so that the caller can use both of them
// interchangeably.
type destinationPluginAdapter struct {
	impl cpluginv1.DestinationPlugin
	// logger is used as the internal logger of destinationPluginAdapter.
	logger log.CtxLogger
	// ctxLogger is attached to the context of each call to the plugin.
	ctxLogger zerolog.Logger

	stream *stream[cpluginv1.DestinationRunRequest, cpluginv1.DestinationRunResponse]
}

var _ plugin.DestinationPlugin = (*destinationPluginAdapter)(nil)

func newDestinationPluginAdapter(impl cpluginv1.DestinationPlugin, logger log.CtxLogger) *destinationPluginAdapter {
	return &destinationPluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtinv1.destinationPluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent()}
}

func (s *destinationPluginAdapter) withLogger(ctx context.Context) context.Context {
	return s.ctxLogger.WithContext(ctx)
}

func (s *destinationPluginAdapter) Configure(ctx context.Context, cfg map[string]string) error {
	req, err := toplugin.DestinationConfigureRequest(cfg)
	if err != nil {
		return err
	}
	s.logger.Trace(ctx).Msg("calling Configure")
	resp, err := runSandbox(s.impl.Configure, s.withLogger(ctx), req)
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
	resp, err := runSandbox(s.impl.Start, s.withLogger(ctx), req)
	if err != nil {
		return err
	}
	_ = resp // empty response

	s.stream = newDestinationRunStream(ctx)
	go func() {
		s.logger.Trace(ctx).Msg("calling Run")
		err := runSandboxNoResp(s.impl.Run, s.withLogger(ctx), cpluginv1.DestinationRunStream(s.stream))
		if err != nil {
			if !s.stream.stop(err) {
				s.logger.Err(ctx, err).Msg("stream already stopped")
			}
		} else {
			s.stream.stop(plugin.ErrStreamNotOpen)
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

func (s *destinationPluginAdapter) Stop(ctx context.Context, lastPosition record.Position) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	s.logger.Trace(ctx).Bytes(log.RecordPositionField, lastPosition).Msg("calling Stop")
	resp, err := runSandbox(s.impl.Stop, s.withLogger(ctx), toplugin.DestinationStopRequest(lastPosition))
	if err != nil {
		return err
	}
	_ = resp // empty response

	return nil
}

func (s *destinationPluginAdapter) Teardown(ctx context.Context) error {
	s.logger.Trace(ctx).Msg("calling Teardown")
	resp, err := runSandbox(s.impl.Teardown, s.withLogger(ctx), toplugin.DestinationTeardownRequest())
	if err != nil {
		return err
	}
	_ = resp // empty response

	return nil
}

func newDestinationRunStream(ctx context.Context) *stream[cpluginv1.DestinationRunRequest, cpluginv1.DestinationRunResponse] {
	return &stream[cpluginv1.DestinationRunRequest, cpluginv1.DestinationRunResponse]{
		ctx:      ctx,
		stopChan: make(chan struct{}),
		reqChan:  make(chan cpluginv1.DestinationRunRequest),
		respChan: make(chan cpluginv1.DestinationRunResponse),
	}
}
