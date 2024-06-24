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
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin/v1/internal/fromplugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin/v1/internal/toplugin"
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
	stream    *stream[cpluginv1.DestinationRunRequest, cpluginv1.DestinationRunResponse]
}

var _ connector.DestinationPlugin = (*destinationPluginAdapter)(nil)

func newDestinationPluginAdapter(impl cpluginv1.DestinationPlugin, logger log.CtxLogger) *destinationPluginAdapter {
	return &destinationPluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtinv1.destinationPluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent()}
}

func (d *destinationPluginAdapter) withLogger(ctx context.Context) context.Context {
	return d.ctxLogger.WithContext(ctx)
}

func (d *destinationPluginAdapter) Configure(ctx context.Context, cfg map[string]string) error {
	d.logger.Trace(ctx).Msg("calling Configure")
	_, err := runSandbox(d.impl.Configure, d.withLogger(ctx), toplugin.DestinationConfigureRequest(cfg), d.logger)
	return err
}

func (d *destinationPluginAdapter) Start(ctx context.Context) error {
	if d.stream != nil {
		return cerrors.New("plugin already running")
	}

	req := toplugin.DestinationStartRequest()
	d.logger.Trace(ctx).Msg("calling Start")
	_, err := runSandbox(d.impl.Start, d.withLogger(ctx), req, d.logger)
	if err != nil {
		return err
	}

	d.stream = newDestinationRunStream(ctx)
	go func() {
		d.logger.Trace(ctx).Msg("calling Run")
		err := runSandboxNoResp(d.impl.Run, d.withLogger(ctx), cpluginv1.DestinationRunStream(d.stream), d.logger)
		if err != nil {
			if !d.stream.stop(err) {
				d.logger.Err(ctx, err).Msg("stream already stopped")
			}
		} else {
			d.stream.stop(connector.ErrStreamNotOpen)
		}
		d.logger.Trace(ctx).Msg("Run stopped")
	}()

	return nil
}

func (d *destinationPluginAdapter) Write(ctx context.Context, r record.Record) error {
	if d.stream == nil {
		return connector.ErrStreamNotOpen
	}

	req, err := toplugin.DestinationRunRequest(r)
	if err != nil {
		return err
	}

	d.logger.Trace(ctx).Msg("sending record")
	err = d.stream.sendInternal(req)
	if err != nil {
		return cerrors.Errorf("builtin plugin send failed: %w", err)
	}

	return nil
}

func (d *destinationPluginAdapter) Ack(ctx context.Context) (record.Position, error) {
	if d.stream == nil {
		return nil, connector.ErrStreamNotOpen
	}

	d.logger.Trace(ctx).Msg("receiving ack")
	resp, err := d.stream.recvInternal()
	if err != nil {
		return nil, err
	}

	position, reason := fromplugin.DestinationRunResponse(resp)
	if reason != "" {
		return position, cerrors.New(reason)
	}

	return position, nil
}

func (d *destinationPluginAdapter) Stop(ctx context.Context, lastPosition record.Position) error {
	d.logger.Trace(ctx).Bytes(log.RecordPositionField, lastPosition).Msg("calling Stop")
	_, err := runSandbox(d.impl.Stop, d.withLogger(ctx), toplugin.DestinationStopRequest(lastPosition), d.logger)
	return err
}

func (d *destinationPluginAdapter) Teardown(ctx context.Context) error {
	d.logger.Trace(ctx).Msg("calling Teardown")
	_, err := runSandbox(d.impl.Teardown, d.withLogger(ctx), toplugin.DestinationTeardownRequest(), d.logger)
	return err
}

func (d *destinationPluginAdapter) LifecycleOnCreated(ctx context.Context, cfg map[string]string) error {
	d.logger.Trace(ctx).Msg("calling LifecycleOnCreated")
	_, err := runSandbox(d.impl.LifecycleOnCreated, d.withLogger(ctx), toplugin.DestinationLifecycleOnCreatedRequest(cfg), d.logger)
	return err
}

func (d *destinationPluginAdapter) LifecycleOnUpdated(ctx context.Context, cfgBefore, cfgAfter map[string]string) error {
	d.logger.Trace(ctx).Msg("calling LifecycleOnUpdated")
	_, err := runSandbox(d.impl.LifecycleOnUpdated, d.withLogger(ctx), toplugin.DestinationLifecycleOnUpdatedRequest(cfgBefore, cfgAfter), d.logger)
	return err
}

func (d *destinationPluginAdapter) LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error {
	d.logger.Trace(ctx).Msg("calling LifecycleOnDeleted")
	_, err := runSandbox(d.impl.LifecycleOnDeleted, d.withLogger(ctx), toplugin.DestinationLifecycleOnDeletedRequest(cfg), d.logger)
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
