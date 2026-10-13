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

package standalone

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"google.golang.org/grpc/codes"
)

// CodeEnvPassedToPlugins is the stable code carried by the startup log event
// and by the `plugins.standalone_env` doctor finding when the host environment
// contains variables that standalone connector plugin processes will inherit
// and whose names look like credentials or configuration secrets. Operators
// can alert on the log field `code` or on the doctor finding code.
var CodeEnvPassedToPlugins = conduiterr.Register("plugin.standalone_env_passed", codes.FailedPrecondition)

// EnvWarningDocsPath is the in-repo documentation for the warning.
const EnvWarningDocsPath = "docs/operations/standalone-plugin-environment.md"

// envWarnOnce makes the startup warning fire once per process, however many
// registries are created or plugins are spawned.
var envWarnOnce sync.Once

// sensitiveEnvNames returns the sorted, de-duplicated names (never values) of
// the entries in environ (KEY=VALUE form, as returned by os.Environ) that
// match the sensitive name patterns. When foldCase is true, names are matched
// case-insensitively (Windows environment variable names are case-insensitive).
//
// This only classifies names for an advisory message; it does not filter the
// environment standalone plugins receive.
func sensitiveEnvNames(environ []string, foldCase bool) []string {
	seen := make(map[string]struct{})
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			// Windows keeps entries such as "=C:=C:\dir"; they have an empty name.
			continue
		}
		if isSensitiveEnvName(name, foldCase) {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

var (
	sensitivePrefixes = []string{"AWS_", "AZURE_", "GOOGLE_", "GCP_", "CONDUIT_"}
	sensitiveSuffixes = []string{"_TOKEN", "_SECRET", "_PASSWORD", "_KEY", "_CREDENTIALS"}
	// databaseURLSuffixes covers DATABASE_URL, DATABASE_URI, DB_URL and
	// prefixed variants such as ANALYTICS_DATABASE_URL.
	databaseURLSuffixes = []string{"DATABASE_URL", "DATABASE_URI", "DB_URL", "DB_URI"}
	// engineOwnedPrefix is set by the engine itself for each plugin it spawns,
	// so it is not an inherited host variable worth reporting.
	engineOwnedPrefix = "CONDUIT_CONNECTOR_"
)

func isSensitiveEnvName(name string, foldCase bool) bool {
	if foldCase {
		name = strings.ToUpper(name)
	}
	if strings.HasPrefix(name, engineOwnedPrefix) {
		return false
	}
	for _, p := range sensitivePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	for _, s := range databaseURLSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// SensitiveHostEnvNames returns the names of host environment variables that
// standalone plugin processes inherit and that match the sensitive patterns.
// It is shared by the startup warning and by `conduit doctor`.
func SensitiveHostEnvNames() []string {
	return sensitiveEnvNames(os.Environ(), runtime.GOOS == "windows")
}

// warnSensitiveEnv logs the warning at most once per process. It logs names
// only, never values.
func warnSensitiveEnv(ctx context.Context, logger log.CtxLogger, environ []string, foldCase bool) {
	envWarnOnce.Do(func() {
		names := sensitiveEnvNames(environ, foldCase)
		if len(names) == 0 {
			return
		}
		logger.Warn(ctx).
			Str("code", CodeEnvPassedToPlugins.Reason()).
			Strs("env_names", names).
			Str("docs", EnvWarningDocsPath).
			Msg("host environment variables are passed to standalone plugin processes; " +
				"v0.21 will restrict the plugin environment to an allowlist " +
				"(set connectors.standalone.env-warning=false to silence this after reviewing the list)")
	})
}
