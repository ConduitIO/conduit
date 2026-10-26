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

package env

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func mustAllow(t *testing.T, names ...string) *Allowlist {
	t.Helper()
	a, err := NewAllowlist(names)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func wantCode(t *testing.T, err error, code conduiterr.Code) *conduiterr.ConduitError {
	t.Helper()
	is := is.New(t)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, code)
	return ce
}

func TestResolveEnv_AllowedExpands(t *testing.T) {
	is := is.New(t)
	t.Setenv("SET_VAR", "value")
	t.Setenv("EMPTY_VAR", "")
	a := mustAllow(t, "SET_VAR", "EMPTY_VAR")

	got, err := ResolveEnv(a, "prefix/${SET_VAR}/suffix/$SET_VAR.")
	is.NoErr(err)
	is.Equal(got, "prefix/value/suffix/value.")

	// an empty variable resolves to the empty string, not an error
	got, err = ResolveEnv(a, "[${EMPTY_VAR}]")
	is.NoErr(err)
	is.Equal(got, "[]")

	// text without a reference is returned untouched
	got, err = ResolveEnv(nil, "plain text, 100% no refs")
	is.NoErr(err)
	is.Equal(got, "plain text, 100% no refs")
}

func TestResolveEnv_MissingAllowedVariable(t *testing.T) {
	// an allowed but unset variable fails loudly with the stable code
	_, err := ResolveEnv(mustAllow(t, "MISSING_VAR"), "${MISSING_VAR}")
	_ = wantCode(t, err, CodeEnvVarMissing)
}

// TestResolveEnv_DeniedByDefault is the C2a regression: nothing expands unless
// the operator listed it.
func TestResolveEnv_DeniedByDefault(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "super-secret")

	for name, allow := range map[string]*Allowlist{
		"nil allowlist":   nil,
		"empty allowlist": mustAllow(t),
		"other names":     mustAllow(t, "SOMETHING_ELSE"),
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			got, err := ResolveEnv(allow, "http://attacker.example/x?k=${AWS_SECRET_ACCESS_KEY}")
			ce := wantCode(t, err, CodeEnvVarNotAllowed)
			is.Equal(got, "")
			is.True(!strings.Contains(ce.Error(), "super-secret"))
			is.True(strings.Contains(ce.Error(), "AWS_SECRET_ACCESS_KEY"))                // names the variable
			is.True(strings.Contains(ce.Suggestion, "env.allow"))                         // says how to allow it
			is.True(strings.Contains(ce.Suggestion, "cannot be changed through the API")) // and who can
		})
	}
}

// A refusal must not reveal whether the variable exists.
func TestResolveEnv_RefusalDoesNotRevealExistence(t *testing.T) {
	is := is.New(t)
	t.Setenv("EXISTS_VAR", "x")
	_, e1 := ResolveEnv(nil, "${EXISTS_VAR}")
	_, e2 := ResolveEnv(nil, "${NOPE_VAR_Z}")
	c1 := wantCode(t, e1, CodeEnvVarNotAllowed)
	c2 := wantCode(t, e2, CodeEnvVarNotAllowed)
	is.Equal(c1.Code, c2.Code)
}

// TestResolveEnv_Tricks covers syntax that tries to slip past the allowlist.
func TestResolveEnv_Tricks(t *testing.T) {
	t.Setenv("ALLOWED", "ok")
	t.Setenv("SECRET", "s3cr3t")
	t.Setenv("OUTER", "${SECRET}") // value that itself looks like a reference
	a := mustAllow(t, "ALLOWED", "OUTER")

	cases := []struct {
		name string
		in   string
		code conduiterr.Code
	}{
		{"default value syntax", "${ALLOWED:-x}", CodeEnvRefMalformed},
		{"default on a denied var", "${SECRET:-x}", CodeEnvRefMalformed},
		{"alternate value", "${ALLOWED:+x}", CodeEnvRefMalformed},
		{"nested reference", "${${ALLOWED}}", CodeEnvRefMalformed},
		{"nested denied reference", "${ALLOWED${SECRET}}", CodeEnvRefMalformed},
		{"unterminated", "${ALLOWED", CodeEnvRefMalformed},
		{"empty braces", "${}", CodeEnvRefMalformed},
		{"dollar digit", "$1", CodeEnvRefMalformed},
		{"escape does not hide the next reference", "$$$SECRET", CodeEnvVarNotAllowed},
		{"special var", "$*", CodeEnvRefMalformed},
		{"space in braces", "${ ALLOWED }", CodeEnvRefMalformed},
		{"lowercase of allowed name", "${allowed}", CodeEnvVarNotAllowed},
		{"prefix of allowed name", "${ALLOWE}", CodeEnvVarNotAllowed},
		{"bare denied", "$SECRET", CodeEnvVarNotAllowed},
		{"bare denied followed by text", "x$SECRET.y", CodeEnvVarNotAllowed},
		{"denied after allowed", "${ALLOWED}/${SECRET}", CodeEnvVarNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveEnv(a, tc.in)
			_ = wantCode(t, err, tc.code)
			if got != "" {
				t.Fatalf("got %q on refusal, want empty", got)
			}
		})
	}

	// An expanded value is never scanned again: OUTER's value "${SECRET}" is
	// returned literally, SECRET is not expanded.
	is := is.New(t)
	got, err := ResolveEnv(a, "${OUTER}")
	is.NoErr(err)
	is.Equal(got, "${SECRET}")
}

func TestNewAllowlist(t *testing.T) {
	is := is.New(t)

	a, err := NewAllowlist([]string{"A_B, C1", "D"})
	is.NoErr(err)
	is.Equal(a.Names(), []string{"A_B", "C1", "D"})

	for _, bad := range []string{"AWS_*", "A=B", "1ABC", "A-B", "${A}", "A.B"} {
		_, err := NewAllowlist([]string{bad})
		ce := wantCode(t, err, CodeEnvRefMalformed)
		is.Equal(ce.ConfigPath, "env.allow")
	}

	var nilAllow *Allowlist
	is.True(!nilAllow.Allows("X"))
	is.Equal(len(nilAllow.Names()), 0)
}

func TestResolveSettings(t *testing.T) {
	is := is.New(t)
	t.Setenv("AWS_SECRET", "s3cr3t")
	a := mustAllow(t, "AWS_SECRET")

	got, err := ResolveSettings(a, map[string]string{
		"aws.secret": "${AWS_SECRET}",
		"plain":      "no-env",
	})
	is.NoErr(err)
	is.Equal(got, map[string]string{"aws.secret": "s3cr3t", "plain": "no-env"})

	// the input is not modified
	in := map[string]string{"k": "${AWS_SECRET}"}
	_, err = ResolveSettings(a, in)
	is.NoErr(err)
	is.Equal(in["k"], "${AWS_SECRET}")

	// a missing variable fails and names the setting
	_, err = ResolveSettings(mustAllow(t, "MISSING"), map[string]string{"aws.key": "${MISSING}"})
	ce := wantCode(t, err, CodeEnvVarMissing)
	is.Equal(ce.ConfigPath, "settings.aws.key")

	// a denied variable fails with the setting path and the operator fix
	_, err = ResolveSettings(a, map[string]string{"webhook.url": "http://x/?k=${OTHER_SECRET}"})
	ce = wantCode(t, err, CodeEnvVarNotAllowed)
	is.Equal(ce.ConfigPath, "settings.webhook.url")
	is.True(strings.Contains(ce.Suggestion, "env.allow"))
}

// A literal "$": "$$" is the escape and expands nothing; a "$" that cannot start
// a reference (end of value, followed by a space, dot or punctuation) is left as
// it is, as it was before references were restricted. Anything that looks like a
// reference stays strict.
func TestResolveEnv_LiteralDollar(t *testing.T) {
	t.Setenv("A", "av")
	t.Setenv("SECRET", "s3cr3t")
	a := mustAllow(t, "A")
	cases := []struct{ in, want string }{
		{"pa$$word", "pa$word"},
		{"$$", "$"},
		{"$$$$", "$$"},
		{"$${A}", "${A}"},
		{"$$A", "$A"},
		{"${A}$$x", "av$x"},
		{"$A$$", "av$"},
		{"abc$", "abc$"},
		{"a$.", "a$."},
		{"price: $ 5", "price: $ 5"},
		{"100$ (approx)", "100$ (approx)"},
		{"$$SECRET", "$SECRET"}, // the escape hides the name: nothing is looked up
	}
	for _, c := range cases {
		got, err := ResolveEnv(a, c.in)
		if err != nil || got != c.want {
			t.Errorf("ResolveEnv(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"$1", "$*", "$@", "${A:-x}", "${", "$-"} {
		if _, err := ResolveEnv(a, in); err == nil {
			t.Errorf("ResolveEnv(%q) accepted", in)
		}
	}
	// the value is not scanned again, so a "$$" in an expanded value stays
	t.Setenv("B", "x$$y")
	a2 := mustAllow(t, "B")
	if got, _ := ResolveEnv(a2, "${B}"); got != "x$$y" {
		t.Errorf("expanded value was scanned again: %q", got)
	}
}
