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
	"fmt"
	"os"
	"regexp"
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
// By design this is a log/finding code, not an error code: no API or CLI call
// fails with it. It is registered in conduiterr so the code set that
// llms.txt publishes, and that alerting can rely on, stays complete.
var CodeEnvPassedToPlugins = conduiterr.Register("plugin.standalone_env_passed", codes.FailedPrecondition)

// EnvWarningDocsPath is the in-repo documentation for the warning.
const EnvWarningDocsPath = "docs/operations/standalone-plugin-environment.md"

// envWarnOnce makes the startup warning fire once per process, however many
// registries are created or plugins are spawned.
var envWarnOnce sync.Once

// SensitiveEnv is the outcome of classifying a process environment.
type SensitiveEnv struct {
	// Names are the sorted, de-duplicated names (never values) that match a
	// sensitive pattern and are safe to print: they match standardNameRE.
	Names []string
	// NonStandard counts matching variables whose names are not printable as
	// plain identifiers (for example, names with control characters). They are
	// counted but never printed.
	NonStandard int
}

// Total is the number of matching variables, printable or not.
func (s SensitiveEnv) Total() int { return len(s.Names) + s.NonStandard }

// standardNameRE bounds what may be echoed into logs and findings.
var standardNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// classifyEnv classifies the entries in environ (KEY=VALUE form, as returned
// by os.Environ). Names are matched case-insensitively on every OS, so that the
// result does not depend on which platform produced the environment.
//
// This only classifies names for an advisory message; it does not filter the
// environment standalone plugins receive.
func classifyEnv(environ []string) SensitiveEnv {
	seen := make(map[string]struct{})
	var out SensitiveEnv
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			// Windows keeps entries such as "=C:=C:\dir"; they have an empty name.
			continue
		}
		if !isSensitiveEnvName(name) {
			continue
		}
		if !standardNameRE.MatchString(name) {
			out.NonStandard++
			continue
		}
		seen[name] = struct{}{}
	}
	out.Names = make([]string, 0, len(seen))
	for n := range seen {
		out.Names = append(out.Names, n)
	}
	sort.Strings(out.Names)
	return out
}

var (
	// substrings that mark a name sensitive wherever they appear.
	sensitiveSubstrings = []string{"PASSWORD", "PASSWD", "TOKEN", "SECRET"}
	// cloud provider prefixes.
	sensitivePrefixes = []string{"AWS_", "AZURE_", "GOOGLE_", "GCP_"}
	sensitiveSuffixes = []string{
		"_KEY", "_CREDENTIALS", "_PASS", "_PWD", "_DSN", "_APIKEY",
		"_CONNECTION_STRING", "_JAAS_CONFIG",
		// DATABASE_URL, DATABASE_URI, DB_URL, DB_URI and prefixed variants.
		"DATABASE_URL", "DATABASE_URI", "DB_URL", "DB_URI",
	}
	// store names whose connection URLs commonly embed credentials.
	urlStores  = []string{"REDIS", "MONGO", "AMQP"}
	exactNames = map[string]struct{}{
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {},
	}
	// excludedNames contain a sensitive-looking substring or suffix but carry
	// no credential: common shell variables and a tuning flag whose name
	// contains TOKEN.
	excludedNames = map[string]struct{}{"PWD": {}, "OLDPWD": {}, "TOKENIZERS_PARALLELISM": {}}
)

// isSensitiveEnvName reports whether name looks like it carries a credential.
//
// CONDUIT_* names get no special treatment: they are reported only when they
// match a pattern below, so CONDUIT_LOG_LEVEL is not reported while
// CONDUIT_DB_POSTGRES_CONNECTION_STRING and
// CONDUIT_SCHEMA_REGISTRY_CONFLUENT_CONNECTION_STRING are (via
// _CONNECTION_STRING). CONDUIT_CONNECTOR_* names are not excluded either: the
// engine sets some of them for each plugin, but go-plugin appends the host
// environment after those values, so a host-defined CONDUIT_CONNECTOR_* value
// is what the plugin sees.
func isSensitiveEnvName(name string) bool {
	name = strings.ToUpper(name)
	if _, ok := excludedNames[name]; ok {
		return false
	}
	if _, ok := exactNames[name]; ok {
		return true
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(name, s) {
			return true
		}
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
	if strings.HasSuffix(name, "_URL") || strings.HasSuffix(name, "_URI") {
		for _, st := range urlStores {
			if strings.Contains(name, st) {
				return true
			}
		}
	}
	return false
}

// ClassifyHostEnv classifies the current process environment. It is shared by
// the startup warning and by `conduit doctor`.
func ClassifyHostEnv() SensitiveEnv {
	return classifyEnv(os.Environ())
}

// NonStandardSummary renders the count of unprintable names, or "" if none.
func (s SensitiveEnv) NonStandardSummary() string {
	if s.NonStandard == 0 {
		return ""
	}
	return fmt.Sprintf("%d variable(s) with non-standard names", s.NonStandard)
}

// warnSensitiveEnv logs the warning at most once per process. It logs names
// only, never values.
func warnSensitiveEnv(ctx context.Context, logger log.CtxLogger, environ []string) {
	envWarnOnce.Do(func() {
		found := classifyEnv(environ)
		if found.Total() == 0 {
			return
		}
		ev := logger.Warn(ctx).
			Str("code", CodeEnvPassedToPlugins.Reason()).
			Strs("env_names", found.Names)
		if found.NonStandard > 0 {
			ev = ev.Int("env_non_standard_names", found.NonStandard)
		}
		ev.Str("docs", EnvWarningDocsPath).
			Msg("host environment variables are passed to standalone plugin processes; " +
				"v0.21 will restrict the plugin environment to an allowlist " +
				"(set connectors.standalone.env-warning=false to silence this after reviewing the list)")
	})
}
