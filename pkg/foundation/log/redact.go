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

package log

import (
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/rs/zerolog"
)

// Redacted is written in place of a config value that has been redacted from
// log output.
const Redacted = "***"

// RedactedConfig wraps a connector config.Config so it can be passed to
// zerolog's Any (or Object/EmbedObject) without leaking secret values.
//
// connector settings (config.Config, a map[string]string) routinely contain
// secrets: database URLs with embedded passwords, SASL credentials, access
// keys. A central zerolog Hook cannot redact these, because a zerolog.Event
// is write-only once fields have been added to it - redaction has to happen
// at the log call site, by wrapping the value before it reaches Any/Object.
//
// Interim behavior: conduit-commons config.Parameter has no field marking a
// parameter as secret yet (no ParameterTypeSecret, no Parameter.Sensitive),
// so there is currently no reliable way to know which keys in a given Config
// are safe to print. Until that metadata exists upstream, every value is
// redacted and only keys are left visible - keys are not secret and are
// useful on their own for debugging ("which parameter is set" vs "to what").
// See https://github.com/ConduitIO/conduit/issues/2566 for the planned
// keyed-on-sensitivity version.
type RedactedConfig struct {
	// Params holds the parameter specification for Config, if known. It is
	// reserved for a future version of this type that redacts only the
	// values Params (or a name heuristic) marks as sensitive.
	//
	// RedactAll always sets this to nil, which selects the interim
	// redact-everything behavior: MarshalZerologObject redacts every value
	// in Config regardless of Params when Params is nil.
	Params config.Parameters
	// Config is the configuration being logged.
	Config config.Config
}

var _ zerolog.LogObjectMarshaler = RedactedConfig{}

// RedactAll wraps cfg so that every value is redacted when logged and every
// key stays visible. This is the only constructor in use today: nothing in
// this codebase currently has per-parameter sensitivity metadata to pass as
// Params (see the RedactedConfig doc comment), so every log call site that
// would otherwise print a raw config.Config should go through RedactAll.
func RedactAll(cfg config.Config) RedactedConfig {
	return RedactedConfig{Params: nil, Config: cfg}
}

// MarshalZerologObject implements zerolog.LogObjectMarshaler. Config keys are
// written in sorted order for deterministic output. A value is redacted
// (replaced with Redacted) unless Params is non-nil and explicitly marks the
// key as non-sensitive; see isSensitive.
//
// This method never mutates r.Config and never writes a value verbatim when
// Params is nil, which is the only mode RedactAll produces today.
func (r RedactedConfig) MarshalZerologObject(e *zerolog.Event) {
	keys := make([]string, 0, len(r.Config))
	for k := range r.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if r.Params == nil || isSensitive(r.Params, k) {
			e.Str(k, Redacted)
			continue
		}
		e.Str(k, r.Config[k])
	}
}

// isSensitive reports whether key should be treated as a secret.
//
// It is not reached by RedactAll (RedactAll always passes Params as nil,
// which redacts every key unconditionally) - it exists so the durable,
// keyed-on-sensitivity version of RedactedConfig can be introduced later
// without changing this file's public surface, and so it can be unit tested
// on its own ahead of that migration. The planned upstream work (tracked in
// https://github.com/ConduitIO/conduit/issues/2566) is to add
// a sensitivity flag to conduit-commons config.Parameter (e.g.
// ParameterTypeSecret or Parameter.Sensitive), thread it through the
// connector protocol and SDK (paramgen tag), mark built-in connectors'
// secret parameters, and then have isSensitive consult params first before
// falling back to the name heuristic below.
func isSensitive(params config.Parameters, key string) bool {
	if p, ok := params[key]; ok && isSensitiveParameter(p) {
		return true
	}

	lower := strings.ToLower(key)
	for _, needle := range sensitiveKeyNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// isSensitiveParameter reports whether p is marked as carrying a secret
// value. conduit-commons config.Parameter does not have such a field yet
// (see https://github.com/ConduitIO/conduit/issues/2566), so this always
// returns false today; it is factored out so that adding the field
// upstream is a one-line change here.
func isSensitiveParameter(_ config.Parameter) bool {
	return false
}

// sensitiveKeyNeedles is a case-insensitive, best-effort list of substrings
// that tend to appear in connector config keys carrying secret material
// (e.g. "sasl.password", "aws.secret_access_key", "url" for a DB connection
// string with an embedded password). It is a heuristic, not a guarantee -
// used only by isSensitive, which itself is not on the path RedactAll takes.
var sensitiveKeyNeedles = []string{
	"password",
	"passphrase",
	"secret",
	"token",
	"credential",
	"private",
	"sasl",
	"auth",
	"apikey",
	"api_key",
	"access_key",
	"dsn",
	"url",
	"connection_string",
}
