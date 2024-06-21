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

// destinationPluginAdapter implements the destination plugin interface used
// internally in Conduit and relays the calls to a destination plugin defined in
// conduit-connector-protocol (pconnector). This adapter needs to make sure it
// behaves in the same way as the standalone plugin adapter, which communicates
// with the plugin through gRPC, so that the caller can use both of them
// interchangeably.
// All methods of destinationPluginAdapter use runSandbox to catch panics and
// convert them into errors. The adapter also logs the calls and clones the
// request and response objects to avoid any side effects.
type destinationPluginAdapter struct {
	impl pconnector.DestinationPlugin
	// logger is used as the internal logger of destinationPluginAdapter.
	logger log.CtxLogger
	// ctxLogger is attached to the context of each call to the plugin.
	ctxLogger zerolog.Logger
}

var _ connector.DestinationPlugin = (*destinationPluginAdapter)(nil)

func newDestinationPluginAdapter(impl pconnector.DestinationPlugin, logger log.CtxLogger) *destinationPluginAdapter {
	return &destinationPluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtin.destinationPluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent()}
}

func (s *destinationPluginAdapter) withLogger(ctx context.Context) context.Context {
	return s.ctxLogger.WithContext(ctx)
}

func (s *destinationPluginAdapter) Configure(ctx context.Context, in pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Configure")
	out, err := runSandbox(s.impl.Configure, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) Open(ctx context.Context, in pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Open")
	out, err := runSandbox(s.impl.Open, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) Run(ctx context.Context, stream pconnector.DestinationRunStream) error {
	inmemStream, ok := stream.(*InMemoryDestinationRunStream)
	if !ok {
		return fmt.Errorf("invalid stream type, expected %T, got %T", s.NewStream(), stream)
	}
	if inmemStream.stream != nil {
		return fmt.Errorf("stream has already been initialized")
	}

	inmemStream.Init(ctx)

	s.logger.Debug(ctx).Msg("calling Run")
	go func() {
		err := runSandboxNoResp(s.impl.Run, s.withLogger(ctx), stream, s.logger)
		if err != nil {
			if inmemStream.Close(err) {
				s.logger.Err(ctx, err).Msg("stream already stopped")
			}
		} else {
			inmemStream.Close(io.EOF)
		}
		s.logger.Debug(ctx).Msg("Run stopped")
	}()

	return nil
}

func (s *destinationPluginAdapter) Stop(ctx context.Context, in pconnector.DestinationStopRequest) (pconnector.DestinationStopResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Stop")
	out, err := runSandbox(s.impl.Stop, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) Teardown(ctx context.Context, in pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Teardown")
	out, err := runSandbox(s.impl.Teardown, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) LifecycleOnCreated(ctx context.Context, in pconnector.DestinationLifecycleOnCreatedRequest) (pconnector.DestinationLifecycleOnCreatedResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnCreated")
	out, err := runSandbox(s.impl.LifecycleOnCreated, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) LifecycleOnUpdated(ctx context.Context, in pconnector.DestinationLifecycleOnUpdatedRequest) (pconnector.DestinationLifecycleOnUpdatedResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnUpdated")
	out, err := runSandbox(s.impl.LifecycleOnUpdated, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) LifecycleOnDeleted(ctx context.Context, in pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnDeleted")
	out, err := runSandbox(s.impl.LifecycleOnDeleted, s.withLogger(ctx), in.Clone(), s.logger)
	return out.Clone(), err
}

func (s *destinationPluginAdapter) NewStream() pconnector.DestinationRunStream {
	return &InMemoryDestinationRunStream{}
}
