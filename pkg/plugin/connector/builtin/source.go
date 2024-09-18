// Copyright © 2024 Meroxa, Inc.
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

package builtin

import (
	"context"
	"fmt"
	"io"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/rs/zerolog"
)

// sourcePluginAdapter implements connector.SourcePlugin used internally in
// Conduit and relays the calls to a source plugin defined in
// conduit-connector-protocol (pconnector). This adapter needs to make sure it
// behaves in the same way as the standalone plugin adapter, which communicates
// with the plugin through gRPC, so that the caller can use both of them
// interchangeably.
// All methods of sourcePluginAdapter use runSandbox to catch panics and convert
// them into errors. The adapter also logs the calls and clones the request and
// response objects to avoid any side effects.
type sourcePluginAdapter struct {
	impl pconnector.SourcePlugin
	// logger is used as the internal logger of sourcePluginAdapter.
	logger log.CtxLogger
	// ctxLogger is attached to the context of each call to the plugin.
	ctxLogger zerolog.Logger
}

var _ connector.SourcePlugin = (*sourcePluginAdapter)(nil)

func newSourcePluginAdapter(impl pconnector.SourcePlugin, logger log.CtxLogger) *sourcePluginAdapter {
	return &sourcePluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtin.sourcePluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent(),
	}
}

func (s *sourcePluginAdapter) withLogger(ctx context.Context) context.Context {
	return s.ctxLogger.WithContext(ctx)
}

func (s *sourcePluginAdapter) Configure(ctx context.Context, in pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Configure")
	out, err := runSandbox(s.impl.Configure, s.withLogger(ctx), in.Clone(), s.logger, "Configure")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) Open(ctx context.Context, in pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Start")
	out, err := runSandbox(s.impl.Open, s.withLogger(ctx), in.Clone(), s.logger, "Start")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) Run(ctx context.Context, stream pconnector.SourceRunStream) error {
	inmemStream, ok := stream.(*InMemorySourceRunStream)
	if !ok {
		return fmt.Errorf("invalid stream type, expected %T, got %T", s.NewStream(), stream)
	}
	if inmemStream.stream != nil {
		return fmt.Errorf("stream has already been initialized")
	}

	inmemStream.Init(ctx)

	s.logger.Debug(ctx).Msg("calling Run")
	go func() {
		err := runSandboxNoResp(s.impl.Run, s.withLogger(ctx), stream, s.logger, "Run")
		if err != nil {
			if !inmemStream.Close(err) {
				s.logger.Err(ctx, err).Msg("stream already stopped")
			}
		} else {
			inmemStream.Close(io.EOF)
		}
		s.logger.Debug(ctx).Msg("Run stopped")
	}()

	return nil
}

func (s *sourcePluginAdapter) Stop(ctx context.Context, in pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Stop")
	out, err := runSandbox(s.impl.Stop, s.withLogger(ctx), in.Clone(), s.logger, "Stop")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) Teardown(ctx context.Context, in pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Teardown")
	out, err := runSandbox(s.impl.Teardown, s.withLogger(ctx), in.Clone(), s.logger, "Teardown")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) LifecycleOnCreated(ctx context.Context, in pconnector.SourceLifecycleOnCreatedRequest) (pconnector.SourceLifecycleOnCreatedResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnCreated")
	out, err := runSandbox(s.impl.LifecycleOnCreated, s.withLogger(ctx), in.Clone(), s.logger, "LifecycleOnCreated")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) LifecycleOnUpdated(ctx context.Context, in pconnector.SourceLifecycleOnUpdatedRequest) (pconnector.SourceLifecycleOnUpdatedResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnUpdated")
	out, err := runSandbox(s.impl.LifecycleOnUpdated, s.withLogger(ctx), in.Clone(), s.logger, "LifecycleOnUpdated")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) LifecycleOnDeleted(ctx context.Context, in pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnDeleted")
	out, err := runSandbox(s.impl.LifecycleOnDeleted, s.withLogger(ctx), in.Clone(), s.logger, "LifecycleOnDeleted")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) NewStream() pconnector.SourceRunStream {
	return &InMemorySourceRunStream{}
}
