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

package provisioning

import (
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// resolveEnv expands ${VAR} references in s against the current process
// environment, failing on the first variable that is not set. An empty
// environment variable resolves to the empty string.
func resolveEnv(s string) (string, error) {
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

// resolveSettings resolves every ${VAR} reference in settings. It returns a new
// map; the input is not modified.
func resolveSettings(settings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(settings))
	for k, v := range settings {
		rv, err := resolveEnv(v)
		if err != nil {
			return nil, cerrors.Errorf("failed to resolve setting %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}
