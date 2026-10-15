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

package secretref

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
)

// redacted replaces every value when a Resolved map is printed or encoded.
// It is the marker the API and the log redaction use for settings (#2926).
const redacted = log.Redacted

// CodeEnvUnset is raised when a settings reference names an environment
// variable that is not set in the engine's environment. The message names the
// variable, the entity and the settings key, never a value. Export the
// variable in the engine's environment, or set it to the empty string if an
// empty value is intended. FailedPrecondition: the engine's environment is
// missing a value.
var CodeEnvUnset = conduiterr.Register("secretref.env_unset", codes.FailedPrecondition)

// Unset identifies one environment variable that a reference names and that
// is not set. It carries names only, so it is safe to log.
type Unset struct {
	// Key is the settings key whose template references the variable.
	Key string
	// Var is the name of the variable as os.Expand parsed it.
	Var string
}

// Resolved is a settings map with every reference resolved. It exists only
// for the duration of a plugin call (Invariant R1). Printing or encoding it
// with fmt, json, yaml, slog or zerolog yields "***" for every value, except
// when it sits in an unexported struct field, which fmt prints raw (never
// store it in one, R1b). The plugin boundary converts it explicitly with
// map[string]string(r).
type Resolved map[string]string

// Resolve returns settings merged with every reference in refs resolved from
// the process environment, plus every variable a reference names that is not
// set. An unset variable expands to the empty string, as os.ExpandEnv does;
// deciding whether that is a warning or an error is the caller's job (see
// NewUnsetError). A variable set to the empty string is not unset.
//
// Neither input map is modified, and the result never aliases them. A key
// present in both maps takes its value from refs: only the config parser
// writes refs, and it never writes both for one key. The result is nil only
// when settings is nil and refs is empty, so a caller that hands the result
// to a plugin sees the same nil-ness as before references existed.
//
// The returned slice is sorted by Key, then Var, with no duplicates.
func Resolve(settings, refs map[string]string) (Resolved, []Unset) {
	return resolve(settings, refs, os.LookupEnv)
}

// resolve is Resolve with an injectable lookup, so tests and the fuzz target
// do not have to mutate the process environment.
func resolve(settings, refs map[string]string, lookup func(string) (string, bool)) (Resolved, []Unset) {
	if settings == nil && len(refs) == 0 {
		return nil, nil
	}

	out := make(Resolved, len(settings)+len(refs))
	for k, v := range settings {
		out[k] = v
	}

	var unset []Unset
	for k, tmpl := range refs {
		seen := map[string]bool{}
		out[k] = os.Expand(tmpl, func(name string) string {
			v, ok := lookup(name)
			if !ok && !seen[name] {
				seen[name] = true
				unset = append(unset, Unset{Key: k, Var: name})
			}
			return v
		})
	}

	sort.Slice(unset, func(i, j int) bool {
		if unset[i].Key != unset[j].Key {
			return unset[i].Key < unset[j].Key
		}
		return unset[i].Var < unset[j].Var
	})
	return out, unset
}

// IsReference reports whether s contains at least one environment reference.
// It is true exactly when os.Expand, given s, calls its mapping function, so
// it accepts the same grammar os.ExpandEnv did in v0.20: $VAR, ${VAR}, and
// Go's shell-special names such as $1 or $$. Text with a "$" that os.Expand
// does not treat as a name (a trailing "$", "${}") is not a reference.
//
// A non-reference is not necessarily unchanged by os.Expand ("x${}y" expands
// to "xy"). Callers that store non-references must store os.ExpandEnv(s), as
// v0.20 did, to keep the meaning of existing configs (see the package doc).
func IsReference(s string) bool {
	if !strings.Contains(s, "$") {
		return false
	}
	found := false
	os.Expand(s, func(string) string {
		found = true
		return ""
	})
	return found
}

// NewUnsetError builds the coded error (CodeEnvUnset) for unset variables.
// entity names the owner for a human, for example `connector "pg-source"`.
// The message lists every entry. ConfigPath is "settings.<key>" of the first
// entry, relative to the entity: a caller that knows where the entity sits in
// the pipeline config must prefix it (for example "connectors.pg-source.").
// The returned error is always a *conduiterr.ConduitError. It panics if unset
// is empty, which is a caller bug.
func NewUnsetError(entity string, unset []Unset) error {
	if len(unset) == 0 {
		panic("secretref: NewUnsetError called without unset variables")
	}
	parts := make([]string, len(unset))
	for i, u := range unset {
		parts[i] = fmt.Sprintf("%s (settings.%s)", u.Var, u.Key)
	}
	msg := fmt.Sprintf(
		"%s: settings reference environment variables that are not set: %s",
		entity, strings.Join(parts, ", "),
	)
	err := conduiterr.New(CodeEnvUnset, msg)
	err.ConfigPath = "settings." + unset[0].Key
	err.Suggestion = "export the variable in the engine's environment, " +
		"or set it to the empty string if an empty value is intended"
	return err
}

// String prints the keys with every value replaced by "***".
func (r Resolved) String() string {
	keys := r.sortedKeys()
	var b strings.Builder
	b.WriteString("map[")
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(redacted)
	}
	b.WriteByte(']')
	return b.String()
}

// GoString is the %#v form, with every value replaced by "***".
func (r Resolved) GoString() string {
	if r == nil {
		return "secretref.Resolved(nil)"
	}
	keys := r.sortedKeys()
	var b strings.Builder
	b.WriteString("secretref.Resolved{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(k))
		b.WriteByte(':')
		b.WriteString(strconv.Quote(redacted))
	}
	b.WriteByte('}')
	return b.String()
}

// Format makes every fmt verb redact, not only %v and %s: without it, verbs
// such as %d or %x would fall back to printing the map's values.
func (r Resolved) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = f.Write([]byte(r.GoString()))
		return
	}
	_, _ = f.Write([]byte(r.String()))
}

// MarshalJSON encodes the keys with every value replaced by "***", so a
// Resolved map passed to a structured logger or json.Marshal cannot leak.
func (r Resolved) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	keys := r.sortedKeys()
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteString(`:"` + redacted + `"`)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// MarshalYAML implements the conduitio/yaml (yaml.v3) Marshaler: every value
// is "***".
func (r Resolved) MarshalYAML() (any, error) {
	if r == nil {
		return nil, nil
	}
	out := make(map[string]string, len(r))
	for k := range r {
		out[k] = redacted
	}
	return out, nil
}

// LogValue implements slog.LogValuer: a group with every value "***".
func (r Resolved) LogValue() slog.Value {
	keys := r.sortedKeys()
	attrs := make([]slog.Attr, len(keys))
	for i, k := range keys {
		attrs[i] = slog.String(k, redacted)
	}
	return slog.GroupValue(attrs...)
}

var (
	_ zerolog.LogObjectMarshaler = Resolved{}
	_ slog.LogValuer             = Resolved{}
)

// MarshalZerologObject writes every key with the value "***", so
// logger.Object("settings", r) is as safe as logger.Interface.
func (r Resolved) MarshalZerologObject(e *zerolog.Event) {
	for _, k := range r.sortedKeys() {
		e.Str(k, redacted)
	}
}

func (r Resolved) sortedKeys() []string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
