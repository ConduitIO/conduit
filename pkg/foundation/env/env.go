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

// Package env resolves ${VAR} references in connector/processor settings at
// runtime. The pipeline config parser stores the reference (see the
// secrets-at-rest design 20261016-secrets-at-rest.md); this package is the
// runtime half: it expands the reference against the process environment and
// fails loudly when a variable is missing, so a secret is never persisted in
// expanded form.
package env

import (
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeEnvVarMissing is raised when a ${VAR} reference cannot be resolved
// because the environment variable is not set. The fix is to set the variable
// and restart the pipeline.
var CodeEnvVarMissing = conduiterr.Register("config.env_var_missing", codes.InvalidArgument)

// ResolveEnv expands ${VAR} references in s against the current process
// environment, failing on the first variable that is not set. An empty
// environment variable resolves to the empty string.
func ResolveEnv(s string) (string, error) {
	var missing string
	resolved := os.Expand(s, func(name string) string {
		v, ok := os.LookupEnv(name)
		if !ok && missing == "" {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", conduiterr.New(CodeEnvVarMissing, "environment variable "+missing+" is not set")
	}
	return resolved, nil
}

// ResolveSettings resolves every ${VAR} reference in settings. It returns a new
// map; the input is not modified.
func ResolveSettings(settings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(settings))
	for k, v := range settings {
		rv, err := ResolveEnv(v)
		if err != nil {
			return nil, cerrors.Errorf("failed to resolve setting %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}
