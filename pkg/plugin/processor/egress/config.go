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

package egress

import (
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Host-reserved processor config keys that opt a standalone processor into
// egress. They are operator-authored (the pipeline YAML author is the operator),
// read HOST-SIDE at processor-open time, and never trusted from guest runtime
// input — a guest cannot widen its own policy by echoing these back.
const (
	// ConfigKeyAllow is a comma/space-separated allowlist of host[:port] or
	// scheme://host[:port] entries. Presence opts the processor into egress.
	ConfigKeyAllow = "sdk.egress.allow"
	// ConfigKeyTimeout is a Go duration (e.g. "30s") for the per-call timeout.
	ConfigKeyTimeout = "sdk.egress.timeout"
	// ConfigKeyMaxResponseBytes caps the buffered (decompressed) response body.
	ConfigKeyMaxResponseBytes = "sdk.egress.maxResponseBytes"
	// ConfigKeySecretRefs is a comma/space-separated list of secret names this
	// processor may reference via HTTPRequest.AuthSecretRef.
	ConfigKeySecretRefs = "sdk.egress.secretRefs" //nolint:gosec // config key name, not a credential value
)

// splitList splits a comma/whitespace-separated config value into trimmed,
// non-empty tokens.
func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	return fields
}

// PolicyFromSettings builds an unresolved per-processor Policy from a processor's
// config Settings. It does NOT apply the engine ceiling — call ResolvePolicy with
// the returned policy and the ceiling to get the effective, clamped policy. A
// Settings map with no ConfigKeyAllow yields a deny-all policy (egress off).
func PolicyFromSettings(settings map[string]string) (Policy, error) {
	allowRaw, ok := settings[ConfigKeyAllow]
	if !ok || strings.TrimSpace(allowRaw) == "" {
		return DenyAll(), nil
	}

	allow, err := ParseAllowlist(allowRaw)
	if err != nil {
		return Policy{}, cerrors.Errorf("invalid %s: %w", ConfigKeyAllow, err)
	}

	p := Policy{
		Enabled:          true,
		Allowlist:        allow,
		Timeout:          DefaultTimeout,
		MaxResponseBytes: DefaultMaxResponseBytes,
	}

	if v := strings.TrimSpace(settings[ConfigKeyTimeout]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Policy{}, cerrors.Errorf("invalid %s: %q", ConfigKeyTimeout, v)
		}
		p.Timeout = d
	}
	if v := strings.TrimSpace(settings[ConfigKeyMaxResponseBytes]); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Policy{}, cerrors.Errorf("invalid %s: %q", ConfigKeyMaxResponseBytes, v)
		}
		p.MaxResponseBytes = n
	}
	if v := strings.TrimSpace(settings[ConfigKeySecretRefs]); v != "" {
		refs := splitList(v)
		p.SecretRefs = make(map[string]struct{}, len(refs))
		for _, r := range refs {
			p.SecretRefs[r] = struct{}{}
		}
	}

	return p, nil
}
