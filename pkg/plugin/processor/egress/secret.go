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
	"context"
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// EnvSecretPrefix namespaces the process environment variables from which
// EnvSecretResolver reads egress credentials. A granted secret name maps to
// EnvSecretPrefix + the normalized (uppercased, non-alphanumerics → '_') name:
// e.g. "openai_key" → "CONDUIT_SECRET_OPENAI_KEY".
const EnvSecretPrefix = "CONDUIT_SECRET_"

// EnvSecretResolver resolves a granted secret name to an Authorization header
// value from a namespaced process environment variable (see EnvSecretPrefix).
//
// The namespace is the security boundary: a guest can only ever cause a lookup
// of names the operator granted via the egress secret-refs allowlist (the grant
// check in buildHTTPRequest runs BEFORE Resolve), and even a granted name can
// only read the deliberately-exported CONDUIT_SECRET_* set — never an arbitrary
// process environment variable. The resolved value is used verbatim as the
// Authorization header, so the operator must set the COMPLETE header value,
// including any scheme, e.g.:
//
//	CONDUIT_SECRET_OPENAI_KEY="Bearer sk-..."
//
// This is host-side only: the credential is read in the engine process and
// injected into the outbound request; it is never placed in processor config
// (which would reach the WASM guest) and the guest never sees the value.
type EnvSecretResolver struct{}

// Resolve returns the Authorization header value for the named secret, read
// from EnvSecretPrefix+normalize(name). A missing or empty variable is a
// non-silent error (the request fails closed rather than sending an empty or
// unauthenticated Authorization header).
func (EnvSecretResolver) Resolve(_ context.Context, name string) (string, error) {
	envName := EnvSecretPrefix + normalizeSecretName(name)
	v, ok := os.LookupEnv(envName)
	if !ok || v == "" {
		// Deliberately does not echo the value; names the expected variable so
		// the operator can fix the deployment.
		return "", cerrors.Errorf("egress secret %q is not set in the environment (expected %s)", name, envName)
	}
	return v, nil
}

// normalizeSecretName maps a secret-ref name to the environment-variable suffix:
// uppercased, with every character outside [A-Z0-9] replaced by '_'. This keeps
// the pipeline-facing name (e.g. "openai.key" or "openai-key") a stable,
// filesystem/env-safe identifier regardless of punctuation.
func normalizeSecretName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
