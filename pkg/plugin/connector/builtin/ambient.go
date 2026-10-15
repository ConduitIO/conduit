// Copyright © 2026 Meroxa, Inc.
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

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

// The ambient-credentials policy reaches built-in connectors only through this
// package: the Registry holds the operator's hostpolicy.Policy, each dispensed
// source/destination adapter puts it on the context of every plugin call, and
// refusals coming back are converted to coded ConduitErrors. See
// docs/design-documents/20261012-builtin-connector-ambient-credentials.md.
//
// Standalone plugins run in another process and get no context value, so a
// cooperating connector there sees the zero Policy and denies.

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithAmbientCredentialsPolicy sets the operator's ambient-credentials policy
// handed to built-in connectors. Without it the Registry hands out the zero
// Policy, which denies. Only the engine runtime (pkg/conduit) calls this, with
// the policy built from conduit.Config; a test asserts no other caller exists.
func WithAmbientCredentialsPolicy(p hostpolicy.Policy) RegistryOption {
	return func(r *Registry) { r.ambient = p }
}

const ambientLoggerComponent = "connector.AmbientCredentials"

// knownOutcomes bounds the metric's outcome label to the values hostpolicy
// defines, so a connector cannot create arbitrary series.
var knownOutcomes = map[hostpolicy.Outcome]bool{
	hostpolicy.OutcomeUsed:            true,
	hostpolicy.OutcomeDeniedDisabled:  true,
	hostpolicy.OutcomeDeniedEndpoint:  true,
	hostpolicy.OutcomeDeniedRedirect:  true,
	hostpolicy.OutcomeDeniedLinkLocal: true,
	hostpolicy.OutcomeDeniedResource:  true,
	hostpolicy.OutcomeDeniedProxy:     true,
}

// policyFor returns the Registry's policy bound to one connector instance:
// its Observe hook writes the audit log line and the metric with the plugin
// name and connector ID the engine knows (a connector cannot spoof them).
func (r *Registry) policyFor(pluginName, connectorID string) hostpolicy.Policy {
	p := r.ambient
	if !p.AmbientCredentials {
		// Still observe denials: an operator wants to see pipelines that tried.
		p = hostpolicy.Policy{}
	}
	logger := r.logger.WithComponent(ambientLoggerComponent)
	p.Observe = func(ctx context.Context, d hostpolicy.Decision) {
		if !knownOutcomes[d.Outcome] {
			return
		}
		measure.AmbientCredentialsCounter.WithValues(pluginName, string(d.Outcome)).Inc()
		if d.Outcome == hostpolicy.OutcomeUsed {
			// Audit line. Never keys, session tokens or role ARNs.
			logger.Info(ctx).
				Str(log.ConnectorIDField, connectorID).
				Str("plugin", pluginName).
				Str("region", d.Region).
				Str("endpoint_host", d.EndpointHost).
				Msg("ambient credentials in use")
			return
		}
		ev := logger.Warn(ctx).
			Str("decision", "deny").
			Str(log.ConnectorIDField, connectorID).
			Str("plugin", pluginName)
		if d.Err != nil {
			ev = ev.Str("reason", string(d.Err.Code)).Str("config_path", d.Err.ConfigPath)
		}
		ev.Msg("ambient credentials denied")
	}
	return p
}

// convertPolicyError turns a hostpolicy refusal anywhere in err's chain into a
// ConduitError carrying the registered code, config path and suggestion, so
// the API, CLI and MCP surfaces report it machine-actionably. The original
// error stays in the chain. Errors that are not refusals pass through.
func convertPolicyError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := conduiterr.Get(err); ok {
		return err
	}
	he, ok := hostpolicy.AsError(err)
	if !ok {
		return err
	}
	code, ok := conduiterr.LookupCode(string(he.Code))
	if !ok {
		return err
	}
	ce := conduiterr.Wrap(code, he.Error(), err)
	ce.ConfigPath = he.ConfigPath
	ce.Suggestion = he.Suggestion
	return ce
}
