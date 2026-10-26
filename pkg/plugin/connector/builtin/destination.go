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

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
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
	// schemaService is the owning runtime's connector schema service. It is
	// attached to the context of each call to the plugin, see
	// schema_scope.go. Nil when the adapter is used without a runtime.
	schemaService pconnutils.SchemaService
	// policy is the operator's ambient-credentials policy, attached to the
	// context of each call to the plugin.
	policy hostpolicy.Policy
	// decorate, when not nil, adds engine-owned values to every call context.
	decorate func(context.Context) context.Context
}

var _ connector.DestinationPlugin = (*destinationPluginAdapter)(nil)

func newDestinationPluginAdapter(impl pconnector.DestinationPlugin, logger log.CtxLogger, policy hostpolicy.Policy) *destinationPluginAdapter {
	return &destinationPluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtin.destinationPluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent(),
		policy:    policy,
	}
}

// pluginContext returns the context for a call into the plugin: it carries
// the plugin logger, this runtime's schema service and the operator's
// ambient-credentials policy.
func (d *destinationPluginAdapter) pluginContext(ctx context.Context) context.Context {
	// Invariant 6: the connector resolves and registers schemas in its own
	// runtime's registry, never in another runtime's in the same process.
	// Invariant (ambient credentials): this is the only place the policy
	// enters a built-in connector, and it comes from the engine, never from
	// the request.
	ctx = hostpolicy.WithPolicy(contextWithSchemaService(d.ctxLogger.WithContext(ctx), d.schemaService), d.policy)
	if d.decorate != nil {
		// Invariant (settings boundary): engine-owned, never from the request.
		ctx = d.decorate(ctx)
	}
	return ctx
}

func (d *destinationPluginAdapter) Configure(ctx context.Context, in pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	// in.Config routinely carries secrets (DB urls with embedded passwords,
	// SASL credentials, access keys). There is no per-parameter sensitivity
	// metadata yet, so log.RedactAll redacts every value - see
	// pkg/foundation/log/redact.go.
	d.logger.Debug(ctx).Any("config", log.RedactAll(in.Config)).Msg("calling Configure") //nolint:forbidigo // in.Config is wrapped in log.RedactAll before logging
	out, err := runSandbox(d.impl.Configure, d.pluginContext(ctx), in.Clone(), d.logger, "Configure")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) Open(ctx context.Context, in pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
	d.logger.Debug(ctx).Msg("calling Open")
	out, err := runSandbox(d.impl.Open, d.pluginContext(ctx), in.Clone(), d.logger, "Open")
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
		err := d.impl.Run(d.pluginContext(ctx), stream)
		if err != nil {
			// No convertPolicyError here, unlike the source: the SDK turns a
			// Write error into an ack error string and keeps running, so Run
			// only ever returns stream errors. A policy refusal on Write reaches
			// the engine as an ack error whose text starts with the stable code
			// (hostpolicy.Error.Error), pinned by
			// TestAmbientPolicy_DestinationWriteRefusalIsAckString.
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
	d.logger.Debug(ctx).Int("position_size", len(in.LastPosition)).Str(log.RecordPositionHashField, log.KeyHash(in.LastPosition)).Msg("calling Stop")
	out, err := runSandbox(d.impl.Stop, d.pluginContext(ctx), in.Clone(), d.logger, "Stop")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) Teardown(ctx context.Context, in pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	d.logger.Debug(ctx).Any("request", in).Msg("calling Teardown")
	out, err := runSandbox(d.impl.Teardown, d.pluginContext(ctx), in.Clone(), d.logger, "Teardown")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) LifecycleOnCreated(ctx context.Context, in pconnector.DestinationLifecycleOnCreatedRequest) (pconnector.DestinationLifecycleOnCreatedResponse, error) {
	// See the redaction note in Configure above.
	d.logger.Debug(ctx).Any("config", log.RedactAll(in.Config)).Msg("calling LifecycleOnCreated") //nolint:forbidigo // in.Config is wrapped in log.RedactAll before logging
	out, err := runSandbox(d.impl.LifecycleOnCreated, d.pluginContext(ctx), in.Clone(), d.logger, "LifecycleOnCreated")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) LifecycleOnUpdated(ctx context.Context, in pconnector.DestinationLifecycleOnUpdatedRequest) (pconnector.DestinationLifecycleOnUpdatedResponse, error) {
	// See the redaction note in Configure above.
	d.logger.Debug(ctx).
		Any("configBefore", log.RedactAll(in.ConfigBefore)). //nolint:forbidigo // in.ConfigBefore is wrapped in log.RedactAll before logging
		Any("configAfter", log.RedactAll(in.ConfigAfter)).   //nolint:forbidigo // in.ConfigAfter is wrapped in log.RedactAll before logging
		Msg("calling LifecycleOnUpdated")
	out, err := runSandbox(d.impl.LifecycleOnUpdated, d.pluginContext(ctx), in.Clone(), d.logger, "LifecycleOnUpdated")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) LifecycleOnDeleted(ctx context.Context, in pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
	// See the redaction note in Configure above.
	d.logger.Debug(ctx).Any("config", log.RedactAll(in.Config)).Msg("calling LifecycleOnDeleted") //nolint:forbidigo // in.Config is wrapped in log.RedactAll before logging
	out, err := runSandbox(d.impl.LifecycleOnDeleted, d.pluginContext(ctx), in.Clone(), d.logger, "LifecycleOnDeleted")
	return out.Clone(), err
}

func (d *destinationPluginAdapter) NewStream() pconnector.DestinationRunStream {
	return &InMemoryDestinationRunStream{}
}
