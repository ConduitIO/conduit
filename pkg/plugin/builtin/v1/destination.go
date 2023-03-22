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
	s.logger.Trace(ctx).Msg("calling Configure")
	_, err := runSandbox(s.impl.Configure, s.withLogger(ctx), toplugin.DestinationConfigureRequest(cfg))
	return err
}

func (s *destinationPluginAdapter) Start(ctx context.Context) error {
	if s.stream != nil {
		return cerrors.New("plugin already running")
	}

	req := toplugin.DestinationStartRequest()
	s.logger.Trace(ctx).Msg("calling Start")
	_, err := runSandbox(s.impl.Start, s.withLogger(ctx), req)
	if err != nil {
		return err
	}

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

	return nil
}

func (s *destinationPluginAdapter) Write(ctx context.Context, r record.Record) error {
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
	_, err := runSandbox(s.impl.Stop, s.withLogger(ctx), toplugin.DestinationStopRequest(lastPosition))
	return err
}

func (s *destinationPluginAdapter) Teardown(ctx context.Context) error {
	s.logger.Trace(ctx).Msg("calling Teardown")
	_, err := runSandbox(s.impl.Teardown, s.withLogger(ctx), toplugin.DestinationTeardownRequest())
	return err
}

func (s *destinationPluginAdapter) LifecycleOnCreated(ctx context.Context, cfg map[string]string) error {
	s.logger.Trace(ctx).Msg("calling LifecycleOnCreated")
	_, err := runSandbox(s.impl.LifecycleOnCreated, s.withLogger(ctx), toplugin.DestinationLifecycleOnCreatedRequest(cfg))
	return err
}

func (s *destinationPluginAdapter) LifecycleOnUpdated(ctx context.Context, cfgBefore, cfgAfter map[string]string) error {
	s.logger.Trace(ctx).Msg("calling LifecycleOnUpdated")
	_, err := runSandbox(s.impl.LifecycleOnUpdated, s.withLogger(ctx), toplugin.DestinationLifecycleOnUpdatedRequest(cfgBefore, cfgAfter))
	return err
}

func (s *destinationPluginAdapter) LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error {
	s.logger.Trace(ctx).Msg("calling LifecycleOnDeleted")
	_, err := runSandbox(s.impl.LifecycleOnDeleted, s.withLogger(ctx), toplugin.DestinationLifecycleOnDeletedRequest(cfg))
	return err
}

func newDestinationRunStream(ctx context.Context) *stream[cpluginv1.DestinationRunRequest, cpluginv1.DestinationRunResponse] {
	return &stream[cpluginv1.DestinationRunRequest, cpluginv1.DestinationRunResponse]{
		ctx:      ctx,
		stopChan: make(chan struct{}),
		reqChan:  make(chan cpluginv1.DestinationRunRequest),
		respChan: make(chan cpluginv1.DestinationRunResponse),
	}
}
