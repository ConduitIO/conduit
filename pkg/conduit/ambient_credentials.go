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

package conduit

import (
	"fmt"
	"strings"

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
)

// Config paths of the ambient-credentials policy. They are the operator's
// keys; no connector parameter maps to them.
//
//nolint:gosec // G101 false positive: config key names, not credentials
const (
	AmbientCredentialsEnabledKey        = "connectors.ambient-credentials.enabled"
	AmbientCredentialsAllowEndpointsKey = "connectors.ambient-credentials.allow-endpoints"
	AmbientCredentialsAllowResourcesKey = "connectors.ambient-credentials.allow-resources"
	AmbientCredentialsAllowProxyKey     = "connectors.ambient-credentials.allow-proxy"

	// AmbientCredentialsDocsPath is the operator runbook.
	AmbientCredentialsDocsPath = "docs/operations/ambient-credentials.md"
)

// AmbientCredentialsPolicy translates connectors.ambient-credentials.* into the
// hostpolicy.Policy the built-in connector registry hands to every built-in
// connector call. It is the single source of truth for that translation:
// Config.Validate calls it to fail startup on a bad config, createServices
// calls it to build the policy actually used, and `conduit doctor` calls it to
// show the effective policy.
//
// getenv is consulted only for the proxy variables (pass os.Getenv).
//
// SECURITY: a disabled config yields the zero Policy, which denies. A
// malformed or non-https endpoint, a wildcard or ARN/alias resource entry,
// enabled with no resources, and enabled with a proxy variable but no
// allow-proxy are startup errors, never a silent disable or a silent "allow
// everything". The returned warnings are for inert or risky-but-valid settings.
func (c Config) AmbientCredentialsPolicy(getenv func(string) string) (hostpolicy.Policy, []string, error) {
	a := c.Connectors.AmbientCredentials

	// Parse both lists whether or not the policy is enabled, so a typo fails
	// startup now rather than on the day someone flips enabled=true.
	endpoints := make([]hostpolicy.Endpoint, 0, len(a.AllowEndpoints))
	for _, raw := range splitList(a.AllowEndpoints) {
		ep, err := parseAmbientEndpoint(raw)
		if err != nil {
			return hostpolicy.Policy{}, nil, ambientConfigErr(conduiterr.CodeInvalidArgument,
				AmbientCredentialsAllowEndpointsKey,
				fmt.Sprintf("%q config value is invalid: %v", AmbientCredentialsAllowEndpointsKey, err),
				"list exact https://host[:port] entries; http, paths and wildcards are not accepted")
		}
		endpoints = append(endpoints, ep)
	}

	resources := splitList(a.AllowResources)
	for _, r := range resources {
		if strings.Contains(r, "*") {
			return hostpolicy.Policy{}, nil, ambientConfigErr(conduiterr.CodeAmbientResourceWildcard,
				AmbientCredentialsAllowResourcesKey,
				fmt.Sprintf("%q entry %q contains a wildcard; wildcard entries are not allowed because bucket names are globally registrable",
					AmbientCredentialsAllowResourcesKey, r),
				"list exact resource names")
		}
		if hostpolicy.IsReservedResource(r) {
			return hostpolicy.Policy{}, nil, ambientConfigErr(conduiterr.CodeInvalidArgument,
				AmbientCredentialsAllowResourcesKey,
				fmt.Sprintf("%q entry %q is an ARN or alias form, which ambient credentials never permit",
					AmbientCredentialsAllowResourcesKey, r),
				"list plain bucket names; access points, aliases and directory buckets are not supported with ambient credentials")
		}
	}

	if !a.Enabled {
		var warnings []string
		if len(endpoints) > 0 || len(resources) > 0 || a.AllowProxy {
			warnings = append(warnings, fmt.Sprintf(
				"connectors.ambient-credentials.* allowlists are set but %s is false; they have no effect", AmbientCredentialsEnabledKey,
			))
		}
		return hostpolicy.Policy{}, warnings, nil
	}

	if len(resources) == 0 {
		return hostpolicy.Policy{}, nil, ambientConfigErr(conduiterr.CodeInvalidArgument,
			AmbientCredentialsAllowResourcesKey,
			fmt.Sprintf("%q is required when %s=true; an empty list never means \"any\"",
				AmbientCredentialsAllowResourcesKey, AmbientCredentialsEnabledKey),
			"list the exact bucket names the host identity may use")
	}

	var warnings []string
	if set := hostpolicy.SetProxyEnvVars(getenv); len(set) > 0 {
		if !a.AllowProxy {
			return hostpolicy.Policy{}, nil, ambientConfigErr(conduiterr.CodeAmbientProxyEnv,
				AmbientCredentialsAllowProxyKey,
				fmt.Sprintf("proxy variables are set (%s) while %s=true", strings.Join(set, ", "), AmbientCredentialsEnabledKey),
				"unset the proxy variables, or set "+AmbientCredentialsAllowProxyKey+"=true if the proxy is trusted")
		}
	}
	if a.AllowProxy {
		warnings = append(warnings, AmbientCredentialsAllowProxyKey+
			" is true: requests may go through an HTTP proxy, and the link-local/metadata address refusal cannot apply to the real destination")
	}

	return hostpolicy.Policy{
		AmbientCredentials: true,
		AllowEndpoints:     endpoints,
		AllowResources:     resources,
		AllowProxy:         a.AllowProxy,
	}, warnings, nil
}

// parseAmbientEndpoint parses an allow-endpoints entry with the same parser as
// processors.egress.allow (exact hosts, no wildcard, no path), then requires
// https and normalizes it the way hostpolicy matches it.
func parseAmbientEndpoint(raw string) (hostpolicy.Endpoint, error) {
	e, err := egress.ParseAllowEntry(raw)
	if err != nil {
		return hostpolicy.Endpoint{}, err //nolint:wrapcheck // message is wrapped by the caller with the config path
	}
	if e.Scheme != schemeHTTPS {
		return hostpolicy.Endpoint{}, fmt.Errorf("entry %q is not https; ambient credentials are only sent over https", raw)
	}
	ep, err := hostpolicy.NewEndpoint(e.Host, e.Port)
	if err != nil {
		return hostpolicy.Endpoint{}, fmt.Errorf("entry %q: %w", raw, err)
	}
	return ep, nil
}

// splitList flattens a repeatable flag / yaml list / comma-separated env value
// into its entries, the same way processors.egress.allow is read.
func splitList(in []string) []string {
	var out []string
	for _, raw := range in {
		out = append(out, strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		})...)
	}
	return out
}

const schemeHTTPS = "https"

func ambientConfigErr(code conduiterr.Code, path, msg, suggestion string) error {
	ce := conduiterr.New(code, msg)
	ce.ConfigPath = path
	ce.Suggestion = suggestion + " (see " + AmbientCredentialsDocsPath + ")"
	return ce
}
