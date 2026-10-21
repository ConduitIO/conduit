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
	// schemaService is the owning runtime's connector schema service. It is
	// attached to the context of each call to the plugin, see
	// schema_scope.go. Nil when the adapter is used without a runtime.
	schemaService pconnutils.SchemaService
	// policy is the operator's ambient-credentials policy, attached to the
	// context of each call to the plugin.
	policy hostpolicy.Policy
}

var _ connector.SourcePlugin = (*sourcePluginAdapter)(nil)

func newSourcePluginAdapter(impl pconnector.SourcePlugin, logger log.CtxLogger, policy hostpolicy.Policy) *sourcePluginAdapter {
	return &sourcePluginAdapter{
		impl:      impl,
		logger:    logger.WithComponent("builtin.sourcePluginAdapter"),
		ctxLogger: logger.WithComponent("plugin").ZerologWithComponent(),
		policy:    policy,
	}
}

// pluginContext returns the context for a call into the plugin: it carries
// the plugin logger, this runtime's schema service and the operator's
// ambient-credentials policy.
func (s *sourcePluginAdapter) pluginContext(ctx context.Context) context.Context {
	// Invariant 6: the connector resolves and registers schemas in its own
	// runtime's registry, never in another runtime's in the same process.
	// Invariant (ambient credentials): this is the only place the policy
	// enters a built-in connector, and it comes from the engine, never from
	// the request.
	return hostpolicy.WithPolicy(contextWithSchemaService(s.ctxLogger.WithContext(ctx), s.schemaService), s.policy)
}

func (s *sourcePluginAdapter) Configure(ctx context.Context, in pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
	// in.Config routinely carries secrets (DB urls with embedded passwords,
	// SASL credentials, access keys). There is no per-parameter sensitivity
	// metadata yet, so log.RedactAll redacts every value - see
	// pkg/foundation/log/redact.go.
	s.logger.Debug(ctx).Any("config", log.RedactAll(in.Config)).Msg("calling Configure") //nolint:forbidigo // in.Config is wrapped in log.RedactAll before logging
	out, err := runSandbox(s.impl.Configure, s.pluginContext(ctx), in.Clone(), s.logger, "Configure")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) Open(ctx context.Context, in pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
	s.logger.Debug(ctx).Int("position_size", len(in.Position)).Str(log.RecordPositionHashField, log.KeyHash(in.Position)).Msg("calling Start")
	out, err := runSandbox(s.impl.Open, s.pluginContext(ctx), in.Clone(), s.logger, "Start")
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
		err := s.impl.Run(s.pluginContext(ctx), stream)
		if err != nil {
			if !inmemStream.Close(convertPolicyError(err)) {
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
	out, err := runSandbox(s.impl.Stop, s.pluginContext(ctx), in.Clone(), s.logger, "Stop")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) Teardown(ctx context.Context, in pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	s.logger.Debug(ctx).Any("request", in).Msg("calling Teardown")
	out, err := runSandbox(s.impl.Teardown, s.pluginContext(ctx), in.Clone(), s.logger, "Teardown")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) LifecycleOnCreated(ctx context.Context, in pconnector.SourceLifecycleOnCreatedRequest) (pconnector.SourceLifecycleOnCreatedResponse, error) {
	// See the redaction note in Configure above.
	s.logger.Debug(ctx).Any("config", log.RedactAll(in.Config)).Msg("calling LifecycleOnCreated") //nolint:forbidigo // in.Config is wrapped in log.RedactAll before logging
	out, err := runSandbox(s.impl.LifecycleOnCreated, s.pluginContext(ctx), in.Clone(), s.logger, "LifecycleOnCreated")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) LifecycleOnUpdated(ctx context.Context, in pconnector.SourceLifecycleOnUpdatedRequest) (pconnector.SourceLifecycleOnUpdatedResponse, error) {
	// See the redaction note in Configure above.
	s.logger.Debug(ctx).
		Any("configBefore", log.RedactAll(in.ConfigBefore)). //nolint:forbidigo // in.ConfigBefore is wrapped in log.RedactAll before logging
		Any("configAfter", log.RedactAll(in.ConfigAfter)).   //nolint:forbidigo // in.ConfigAfter is wrapped in log.RedactAll before logging
		Msg("calling LifecycleOnUpdated")
	out, err := runSandbox(s.impl.LifecycleOnUpdated, s.pluginContext(ctx), in.Clone(), s.logger, "LifecycleOnUpdated")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) LifecycleOnDeleted(ctx context.Context, in pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
	// See the redaction note in Configure above.
	s.logger.Debug(ctx).Any("config", log.RedactAll(in.Config)).Msg("calling LifecycleOnDeleted") //nolint:forbidigo // in.Config is wrapped in log.RedactAll before logging
	out, err := runSandbox(s.impl.LifecycleOnDeleted, s.pluginContext(ctx), in.Clone(), s.logger, "LifecycleOnDeleted")
	return out.Clone(), err
}

func (s *sourcePluginAdapter) NewStream() pconnector.SourceRunStream {
	return &InMemorySourceRunStream{}
}
