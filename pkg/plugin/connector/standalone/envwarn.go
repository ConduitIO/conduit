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
	sensitiveSubstrings = []string{"PASSWORD", "PASSWD", "PASSPHRASE", "TOKEN", "SECRET"}
	// cloud provider prefixes.
	sensitivePrefixes = []string{"AWS_", "AZURE_", "GOOGLE_", "GCP_"}
	sensitiveSuffixes = []string{
		"_KEY", "_CREDENTIAL", "_CREDENTIALS", "_CREDS", "_AUTH", "_PASS", "_PW", "_PWD", "_DSN", "_PAT",
		"APIKEY", "APIKEYS", "API_KEYS", // bare APIKEY and API_KEYS as well as FOO_APIKEY
		"PRIVATEKEY",            // SSH_PRIVATEKEY; FOO_PRIVATE_KEY is covered by _KEY
		"_JWT", "AUTHORIZATION", // CI_JOB_JWT, HTTP_AUTHORIZATION
		// Confluent SCHEMA_REGISTRY_BASIC_AUTH_USER_INFO and the
		// CONNECT_*_SCHEMA_REGISTRY_BASIC_AUTH_USER_INFO forms carry user:password.
		"BASIC_AUTH_USER_INFO",
		// JDBC, connection and broker URLs commonly embed credentials.
		"JDBC_URL", "CONNECTION_URL", "BROKER_URL", "BROKER_URI",
		"_CONNECTION_STRING", "_JAAS_CONFIG",
		// DATABASE_URL, DATABASE_URI, DB_URL, DB_URI and prefixed variants.
		"DATABASE_URL", "DATABASE_URI", "DB_URL", "DB_URI",
		// SLACK_WEBHOOK_URL and friends: the URL is the credential.
		"WEBHOOK_URL", "WEBHOOK_URI",
		// HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, FTP_PROXY, SOCKS_PROXY, ...:
		// proxy URLs can embed credentials. NO_PROXY is excluded below.
		"_PROXY",
	}
	// store and broker names (matched anywhere in the name) whose connection
	// URLs commonly embed credentials. A name must also end in _URL or _URI.
	urlStores = []string{
		"REDIS", "MONGO", "AMQP", "RABBITMQ", "KAFKA",
		"POSTGRES", "MYSQL", "MARIADB", "MSSQL", "COCKROACH",
		"CLICKHOUSE", "ELASTIC", "OPENSEARCH", "CASSANDRA", "MEMCACHE",
	}
	// urlStoreTokens are store names too short to match as a substring
	// (PG would match PGP): they must be a whole "_"-delimited token, as in
	// PG_URL.
	urlStoreTokens = []string{"PG", "NATS"}
	exactNames     = map[string]struct{}{
		// libpq accepts no URL variable itself, but PGURI/PGURL are common
		// conventions. The other PG* variables (PGHOST, PGUSER, PGPORT,
		// PGDATABASE, PGSSLMODE, PGSSLCERT, PGPASSFILE) name a location or a
		// setting, not a secret, and are not reported; PGPASSWORD and
		// PGSSLPASSWORD match PASSWORD.
		"PROXY": {}, "PGURI": {}, "PGURL": {}, "PGDSN": {}, "JWT": {},
		// Package index URLs can embed a token (https://user:token@host/simple).
		"PIP_INDEX_URL": {}, "PIP_EXTRA_INDEX_URL": {}, "UV_INDEX_URL": {}, "UV_EXTRA_INDEX_URL": {},
		// Docker registry credentials as inline JSON.
		"DOCKER_AUTH_CONFIG": {},
	}
	// switchWords, as the first word of NAME_AUTH or NAME_PROXY, mark a boolean
	// switch (SKIP_AUTH, USE_PROXY) rather than a credential.
	switchWords = map[string]struct{}{
		"NO": {}, "SKIP": {}, "DISABLE": {}, "ENABLE": {}, "USE": {},
		"REQUIRE": {}, "TRUST": {}, "BEHIND": {},
	}
	// excludedNames contain a sensitive-looking substring or suffix but carry
	// no credential: common shell variables, a tuning flag whose name
	// contains TOKEN, (NO_PROXY, a host list, is covered by switchWords).
	excludedNames = map[string]struct{}{
		"PWD": {}, "OLDPWD": {}, "TOKENIZERS_PARALLELISM": {},
	}
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
	for _, suf := range []string{"_AUTH", "_PROXY"} {
		if w, ok := strings.CutSuffix(name, suf); ok {
			if _, sw := switchWords[w]; sw {
				return false
			}
		}
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
		for _, tok := range strings.Split(name, "_") {
			for _, st := range urlStoreTokens {
				if tok == st {
					return true
				}
			}
		}
	}
	return false
}

// ClassifyEnv classifies environ (KEY=VALUE entries, as returned by
// os.Environ). It is shared by the startup warning and by `conduit doctor`, and exists so callers and tests can supply a fixed environment
// instead of reading the process's own.
func ClassifyEnv(environ []string) SensitiveEnv {
	return classifyEnv(environ)
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
				"a future release will restrict the plugin environment to an allowlist " +
				"(set connectors.standalone.env-warning=false to silence this after reviewing the list)")
	})
}
