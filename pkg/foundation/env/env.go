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
//
// # Security boundary
//
// Settings reach the engine from the API as well as from YAML files, so the
// text of a setting is attacker-influenced. Expanding it against the whole
// process environment would let any API caller read every secret the engine
// holds (for example a webhook URL ending in ?k=${AWS_SECRET_ACCESS_KEY}).
// Expansion is therefore deny-by-default: only variable names in an
// operator-controlled [Allowlist] (conduit.yaml / flag `env.allow`, never an
// API field) are expanded. Any other reference is refused with
// [CodeEnvVarNotAllowed], before the environment is consulted, so the
// refusal does not reveal whether the variable exists.
//
// The accepted syntax is deliberately narrow: ${NAME} and $NAME where NAME is
// [A-Za-z_][A-Za-z0-9_]*. Defaults (${NAME:-x}), nesting, and anything else
// are refused as [CodeEnvRefMalformed] rather than interpreted, and an
// expanded value is never scanned again, so an allowlisted variable whose
// value contains "${OTHER}" cannot smuggle a second expansion.
package env

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// AllowConfigPath is the operator config key that controls the allowlist.
const AllowConfigPath = "env.allow"

// DocsPath is the operator runbook for this control.
const DocsPath = "docs/operations/env-allowlist.md"

// CodeEnvVarMissing is raised when a ${VAR} reference cannot be resolved
// because the environment variable is not set. The fix is to set the variable
// and restart the pipeline.
var CodeEnvVarMissing = conduiterr.Register("config.env_var_missing", codes.InvalidArgument)

// CodeEnvVarNotAllowed is raised when a setting references an environment
// variable that is not in the operator's env.allow list. The fix is for the
// operator to add the name to env.allow in conduit.yaml (or pass --env.allow)
// and restart; it cannot be changed through the API.
var CodeEnvVarNotAllowed = conduiterr.Register("config.env_var_not_allowed", codes.PermissionDenied)

// CodeEnvRefMalformed is raised when a setting contains a "$" reference that
// is not a plain ${NAME} or $NAME (defaults such as ${NAME:-x}, nested
// references, an unterminated "${", or "$" followed by a digit or a shell
// special such as $1 and $*). Write the plain form, or use $$ for a literal
// dollar sign. A "$" at the end of a value or before a space or punctuation is
// a literal dollar sign and is not an error.
var CodeEnvRefMalformed = conduiterr.Register("config.env_ref_malformed", codes.InvalidArgument)

// Allowlist is the set of environment variable names a setting may reference.
// The zero value and a nil *Allowlist allow nothing.
type Allowlist struct {
	names map[string]struct{}
}

// NewAllowlist validates names and returns the allowlist. A name must be a
// plain identifier ([A-Za-z_][A-Za-z0-9_]*); patterns and wildcards are not
// supported on purpose, since a prefix like AWS_* would include the secrets
// the allowlist exists to protect. Entries may be comma or space separated.
func NewAllowlist(entries []string) (*Allowlist, error) {
	a := &Allowlist{names: make(map[string]struct{})}
	for _, e := range entries {
		for _, name := range strings.FieldsFunc(e, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		}) {
			if !validName(name) {
				ce := conduiterr.New(CodeEnvRefMalformed,
					fmt.Sprintf("%q config value %q is not a valid environment variable name", AllowConfigPath, name))
				ce.ConfigPath = AllowConfigPath
				ce.Suggestion = "list exact variable names ([A-Za-z_][A-Za-z0-9_]*); wildcards are not supported (see " + DocsPath + ")"
				return nil, ce
			}
			a.names[name] = struct{}{}
		}
	}
	return a, nil
}

// Allows reports whether name may be referenced. It is safe on a nil receiver.
func (a *Allowlist) Allows(name string) bool {
	if a == nil {
		return false
	}
	_, ok := a.names[name]
	return ok
}

// Names returns the allowed names, sorted.
func (a *Allowlist) Names() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.names))
	for n := range a.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

// ResolveEnv expands ${NAME} and $NAME references in s against the current
// process environment, for names in allow only. It fails on the first
// reference that is malformed, not allowed, or not set. An empty environment
// variable resolves to the empty string. The expanded values are not scanned
// again.
func ResolveEnv(allow *Allowlist, s string) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' { // $$ is a literal $ and expands nothing
			b.WriteByte('$')
			i += 2
			continue
		}
		if literalDollar(s, i) {
			b.WriteByte('$')
			i++
			continue
		}
		name, next, err := parseRef(s, i)
		if err != nil {
			return "", err
		}
		// Invariant (security boundary): the allowlist is consulted before the
		// environment, so a refusal never reveals whether the variable is set.
		if !allow.Allows(name) {
			ce := conduiterr.New(CodeEnvVarNotAllowed,
				"environment variable "+name+" is not in the operator allowlist (env.allow)")
			ce.ConfigPath = AllowConfigPath
			ce.Suggestion = "an operator must add " + name + " to " + AllowConfigPath +
				" in conduit.yaml (or pass --" + AllowConfigPath + " " + name + ") and restart; this cannot be changed through the API (see " + DocsPath + ")"
			return "", ce
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			ce := conduiterr.New(CodeEnvVarMissing, "environment variable "+name+" is not set")
			ce.Suggestion = "set " + name + " in the Conduit process environment and restart the pipeline"
			return "", ce
		}
		b.WriteString(v)
		i = next
	}
	return b.String(), nil
}

// literalDollar reports whether the "$" at s[i] is just a dollar sign: at the
// end of the value, or followed by something that cannot start a reference (a
// space, a dot, punctuation). That is how a "$" like this behaved before
// references were restricted (os.Expand left it alone), so stored values such as
// "price$" and "a$." keep working. A "$" that looks like the start of a
// reference ("${", "$NAME", "$1", "$@") is never a literal.
func literalDollar(s string, i int) bool {
	if i+1 >= len(s) {
		return true
	}
	c := s[i+1]
	if c == '{' || isNameStart(c) || (c >= '0' && c <= '9') {
		return false
	}
	switch c { // the shell's special parameters: os.Expand treated them as variables
	case '*', '#', '@', '?', '!', '-', '$':
		return false
	}
	return true
}

// parseRef parses the reference starting at s[i] == '$' and returns the
// variable name and the index just past the reference.
func parseRef(s string, i int) (name string, next int, err error) {
	rest := s[i+1:]
	if rest == "" {
		return "", 0, malformed(s[i:], "a trailing $ is not a reference")
	}
	if rest[0] == '{' {
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return "", 0, malformed(s[i:], "unterminated ${")
		}
		name = rest[1:end]
		if !validName(name) {
			return "", 0, malformed(s[i:i+1+end+1],
				"only ${NAME} is supported: no defaults, no nesting, no other syntax")
		}
		return name, i + 1 + end + 1, nil
	}
	if !isNameStart(rest[0]) {
		return "", 0, malformed(s[i:i+2], "$ must be followed by a variable name")
	}
	j := 1
	for j < len(rest) && isNameChar(rest[j]) {
		j++
	}
	return rest[:j], i + 1 + j, nil
}

func malformed(ref, why string) error {
	ce := conduiterr.New(CodeEnvRefMalformed, fmt.Sprintf("malformed environment reference %q: %s", ref, why))
	ce.Suggestion = "use a plain ${NAME}; references are only expanded for names in the operator allowlist (see " + DocsPath + ")"
	return ce
}

// ResolveSettings resolves every ${VAR} reference in settings against allow. It
// returns a new map; the input is not modified. A refusal carries the config
// path of the offending setting ("settings.<key>").
func ResolveSettings(allow *Allowlist, settings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(settings))
	for k, v := range settings {
		rv, err := ResolveEnv(allow, v)
		if err != nil {
			if ce, ok := conduiterr.Get(err); ok {
				// Keep the setting path next to the allowlist path: ConfigPath
				// names the field that holds the reference, the suggestion
				// names the operator key that fixes it.
				if ce.Code == CodeEnvVarNotAllowed {
					ce.Suggestion = "setting \"" + k + "\": " + ce.Suggestion
				}
				ce.ConfigPath = "settings." + k
			}
			return nil, cerrors.Errorf("failed to resolve setting %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}

// Expansion is one environment variable whose value was substituted into a
// setting.
type Expansion struct {
	Name  string
	Value string
}

// minRedactLen is the shortest expanded value that is redacted from messages.
// Replacing a one or two character value everywhere would only wreck the
// message without hiding anything.
const minRedactLen = 3

// Expansions lists the variables the references in settings expand to (names in
// allow that are set), without the value being looked at twice per name. It is
// what Redact needs: the values to keep out of error messages.
func Expansions(allow *Allowlist, settings map[string]string) []Expansion {
	seen := map[string]bool{}
	var out []Expansion
	for _, v := range settings {
		for i := 0; i < len(v); {
			if v[i] != '$' {
				i++
				continue
			}
			if i+1 < len(v) && v[i+1] == '$' {
				i += 2
				continue
			}
			if literalDollar(v, i) {
				i++
				continue
			}
			name, next, err := parseRef(v, i)
			if err != nil {
				break
			}
			i = next
			if seen[name] || !allow.Allows(name) {
				continue
			}
			if val, ok := os.LookupEnv(name); ok && len(val) >= minRedactLen {
				seen[name] = true
				out = append(out, Expansion{Name: name, Value: val})
			}
		}
	}
	return out
}

// Redact replaces the expanded values in s by their references ("${NAME}"),
// longest value first. Use it on every message that can reach a settings writer.
func Redact(exps []Expansion, s string) string {
	if len(exps) == 0 || s == "" {
		return s
	}
	sorted := append([]Expansion(nil), exps...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Value) > len(sorted[j].Value) })
	for _, e := range sorted {
		if e.Value != "" {
			s = strings.ReplaceAll(s, e.Value, "${"+e.Name+"}")
		}
	}
	return s
}

// RedactError returns err with every substituted environment value replaced by
// its reference (see [Redact]). An error that does not contain one is returned
// unchanged. The code, config path and remediation of a coded error are kept;
// the original is not reachable through the result's chain (its text must not
// leak through a log that walks the chain), but errors.Is on it still works.
func RedactError(err error, exps []Expansion) error {
	if err == nil || len(exps) == 0 {
		return err
	}
	text := err.Error()
	if Redact(exps, text) == text {
		if ce, ok := conduiterr.Get(err); !ok || (Redact(exps, ce.Message)+Redact(exps, ce.Suggestion)+Redact(exps, ce.ConfigPath) == ce.Message+ce.Suggestion+ce.ConfigPath) {
			return err
		}
	}
	red := &redactedError{msg: Redact(exps, text), cause: err}
	ce, ok := conduiterr.Get(err)
	if !ok {
		return red
	}
	n := conduiterr.Wrap(ce.Code, Redact(exps, ce.Message), cerrors.New(Redact(exps, ce.Message)))
	n.ConfigPath = Redact(exps, ce.ConfigPath)
	n.Suggestion = Redact(exps, ce.Suggestion)
	n.DocsURL = ce.DocsURL
	if ce.Fix != nil {
		f := *ce.Fix
		f.Value = Redact(exps, f.Value)
		f.ConfigPath = Redact(exps, f.ConfigPath)
		n.Fix = &f
	}
	if text == ce.Message {
		return n
	}
	prefix := strings.TrimSuffix(strings.TrimSuffix(red.msg, Redact(exps, ce.Message)), ": ")
	return cerrors.Errorf("%s: %w", prefix, n)
}

// redactedError carries redacted text and answers errors.Is for the error it
// replaced without exposing it through Unwrap.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string        { return e.msg }
func (e *redactedError) Is(target error) bool { return cerrors.Is(e.cause, target) }
