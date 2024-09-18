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

// destinationPluginAdapter implements connector.DestinationPlugin used
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
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent(),
	}
}

func (d *destinationPluginAdapter) withLogger(ctx context.Context) context.Context {
	return d.ctxLogger.WithContext(ctx)
}

func (d *destinationPluginAdapter) Configure(ctx context.Context, in pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling Configure")
	out, err := runSandbox(d.impl.Configure, d.withLogger(ctx), in.Clone(), d.logger, "Configure")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) Open(ctx context.Context, in pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling Open")
	out, err := runSandbox(d.impl.Open, d.withLogger(ctx), in.Clone(), d.logger, "Open")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) Run(ctx context.Context, stream pconnector.DestinationRunStream) error {
	inmemStream, ok := stream.(*InMemoryDestinationRunStream)
	if !ok {
		return fmt.Errorf("invalid stream type, expected %T, got %T", d.NewStream(), stream)
	}
	if inmemStream.stream != nil {
		return fmt.Errorf("stream has already been initialized")
	}

	inmemStream.Init(ctx)

	d.logger.Debug(ctx).Msg("calling Run")
	go func() {
		err := runSandboxNoResp(d.impl.Run, d.withLogger(ctx), stream, d.logger, "Run")
		if err != nil {
			if !inmemStream.Close(err) {
				d.logger.Err(ctx, err).Msg("stream already stopped")
			}
		} else {
			inmemStream.Close(io.EOF)
		}
		d.logger.Debug(ctx).Msg("Run stopped")
	}()

	return nil
}

func (d *destinationPluginAdapter) Stop(ctx context.Context, in pconnector.DestinationStopRequest) (pconnector.DestinationStopResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling Stop")
	out, err := runSandbox(d.impl.Stop, d.withLogger(ctx), in.Clone(), d.logger, "Stop")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) Teardown(ctx context.Context, in pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling Teardown")
	out, err := runSandbox(d.impl.Teardown, d.withLogger(ctx), in.Clone(), d.logger, "Teardown")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) LifecycleOnCreated(ctx context.Context, in pconnector.DestinationLifecycleOnCreatedRequest) (pconnector.DestinationLifecycleOnCreatedResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnCreated")
	out, err := runSandbox(d.impl.LifecycleOnCreated, d.withLogger(ctx), in.Clone(), d.logger, "LifecycleOnCreated")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) LifecycleOnUpdated(ctx context.Context, in pconnector.DestinationLifecycleOnUpdatedRequest) (pconnector.DestinationLifecycleOnUpdatedResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnUpdated")
	out, err := runSandbox(d.impl.LifecycleOnUpdated, d.withLogger(ctx), in.Clone(), d.logger, "LifecycleOnUpdated")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) LifecycleOnDeleted(ctx context.Context, in pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling LifecycleOnDeleted")
	out, err := runSandbox(d.impl.LifecycleOnDeleted, d.withLogger(ctx), in.Clone(), d.logger, "LifecycleOnDeleted")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) NewStream() pconnector.DestinationRunStream {
	return &InMemoryDestinationRunStream{}
}
