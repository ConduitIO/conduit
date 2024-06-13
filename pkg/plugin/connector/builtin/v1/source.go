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

	cschema "github.com/conduitio/conduit-connector-protocol/conduit/schema"
	"github.com/conduitio/conduit-connector-protocol/conduit/schema/client"
	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin/v1/internal/fromplugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin/v1/internal/toplugin"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

// sourcePluginAdapter implements the source plugin interface used internally in
// Conduit and relays the calls to a source plugin defined in
// conduit-connector-protocol (cpluginv1). This adapter needs to make sure it
// behaves in the same way as the standalone plugin adapter, which communicates
// with the plugin through gRPC, so that the caller can use both of them
// interchangeably.
type sourcePluginAdapter struct {
	impl cpluginv1.SourcePlugin
	// logger is used as the internal logger of sourcePluginAdapter.
	logger log.CtxLogger
	// ctxLogger is attached to the context of each call to the plugin.
	ctxLogger     zerolog.Logger
	schemaService cschema.Service
	stream        *stream[cpluginv1.SourceRunRequest, cpluginv1.SourceRunResponse]
}

var _ connector.SourcePlugin = (*sourcePluginAdapter)(nil)

func newSourcePluginAdapter(
	impl cpluginv1.SourcePlugin,
	logger log.CtxLogger,
	schemaService cschema.Service,
) *sourcePluginAdapter {
	return &sourcePluginAdapter{
		impl:          impl,
		schemaService: schemaService,
		logger:        logger.WithComponent("builtinv1.sourcePluginAdapter"),
		ctxLogger:     logger.WithComponent("plugin").ZerologWithComponent(),
	}
}

func (s *sourcePluginAdapter) withLogger(ctx context.Context) context.Context {
	return s.ctxLogger.WithContext(ctx)
}

func (s *sourcePluginAdapter) withSchemaService(ctx context.Context) context.Context {
	return client.WithSchemaService(ctx, s.schemaService)
}

func (s *sourcePluginAdapter) Configure(ctx context.Context, cfg map[string]string) error {
	s.logger.Trace(ctx).Msg("calling Configure")
	_, err := runSandbox(s.impl.Configure, s.withSchemaService(s.withLogger(ctx)), toplugin.SourceConfigureRequest(cfg), s.logger)
	return err
}

func (s *sourcePluginAdapter) Start(ctx context.Context, p record.Position) error {
	if s.stream != nil {
		return cerrors.New("plugin already running")
	}

	req := toplugin.SourceStartRequest(p)

	s.logger.Trace(ctx).Msg("calling Start")
	resp, err := runSandbox(s.impl.Start, s.withSchemaService(s.withLogger(ctx)), req, s.logger)
	if err != nil {
		return err
	}
	_ = resp // empty response

	s.stream = newSourceRunStream(ctx)
	go func() {
		s.logger.Trace(ctx).Msg("calling Run")
		err := runSandboxNoResp(s.impl.Run, s.withSchemaService(s.withLogger(ctx)), cpluginv1.SourceRunStream(s.stream), s.logger)
		if err != nil {
			if !s.stream.stop(err) {
				s.logger.Err(ctx, err).Msg("stream already stopped")
			}
		} else {
			s.stream.stop(connector.ErrStreamNotOpen)
		}
		s.logger.Trace(ctx).Msg("Run stopped")
	}()

	return nil
}

func (s *sourcePluginAdapter) Read(ctx context.Context) (record.Record, error) {
	if s.stream == nil {
		return record.Record{}, connector.ErrStreamNotOpen
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
		return connector.ErrStreamNotOpen
	}

	req := toplugin.SourceRunRequest(p)

	s.logger.Trace(ctx).Msg("sending ack")
	err := s.stream.sendInternal(req)
	if err != nil {
		return cerrors.Errorf("builtin plugin send failed: %w", err)
	}

	return nil
}

func (s *sourcePluginAdapter) Stop(ctx context.Context) (record.Position, error) {
	s.logger.Trace(ctx).Msg("calling Stop")
	resp, err := runSandbox(s.impl.Stop, s.withSchemaService(s.withLogger(ctx)), toplugin.SourceStopRequest(), s.logger)
	if err != nil {
		return nil, err
	}
	return fromplugin.SourceStopResponse(resp)
}

func (s *sourcePluginAdapter) Teardown(ctx context.Context) error {
	s.logger.Trace(ctx).Msg("calling Teardown")
	_, err := runSandbox(s.impl.Teardown, s.withSchemaService(s.withLogger(ctx)), toplugin.SourceTeardownRequest(), s.logger)
	return err
}

func (s *sourcePluginAdapter) LifecycleOnCreated(ctx context.Context, cfg map[string]string) error {
	s.logger.Trace(ctx).Msg("calling LifecycleOnCreated")
	_, err := runSandbox(s.impl.LifecycleOnCreated, s.withSchemaService(s.withLogger(ctx)), toplugin.SourceLifecycleOnCreatedRequest(cfg), s.logger)
	return err
}

func (s *sourcePluginAdapter) LifecycleOnUpdated(ctx context.Context, cfgBefore, cfgAfter map[string]string) error {
	s.logger.Trace(ctx).Msg("calling LifecycleOnUpdated")
	_, err := runSandbox(s.impl.LifecycleOnUpdated, s.withSchemaService(s.withLogger(ctx)), toplugin.SourceLifecycleOnUpdatedRequest(cfgBefore, cfgAfter), s.logger)
	return err
}

func (s *sourcePluginAdapter) LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error {
	s.logger.Trace(ctx).Msg("calling LifecycleOnDeleted")
	_, err := runSandbox(s.impl.LifecycleOnDeleted, s.withSchemaService(s.withLogger(ctx)), toplugin.SourceLifecycleOnDeletedRequest(cfg), s.logger)
	return err
}

func newSourceRunStream(ctx context.Context) *stream[cpluginv1.SourceRunRequest, cpluginv1.SourceRunResponse] {
	return &stream[cpluginv1.SourceRunRequest, cpluginv1.SourceRunResponse]{
		ctx:      ctx,
		stopChan: make(chan struct{}),
		reqChan:  make(chan cpluginv1.SourceRunRequest),
		respChan: make(chan cpluginv1.SourceRunResponse),
	}
}
