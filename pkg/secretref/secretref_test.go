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
	"bytes"
	stdjson "encoding/json" //nolint:depguard // the leak test must cover the standard library encoder too
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/yaml/v3"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
)

// canary is alphanumeric on purpose: json escapes <, > and &, so a canary
// with them could leak in escaped form and still pass a byte scan.
const canary = "CanaryS3cretValue7781"

func fakeEnv(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func TestResolve(t *testing.T) {
	env := fakeEnv(map[string]string{
		"DB_PASSWORD": "pw",
		"HOST":        "db.internal",
		"EMPTY":       "",
	})

	testCases := []struct {
		name      string
		settings  map[string]string
		refs      map[string]string
		want      Resolved
		wantUnset []Unset
	}{{
		name: "both nil stays nil",
		want: nil,
	}, {
		name:     "empty settings without refs is an empty map, not nil",
		settings: map[string]string{},
		want:     Resolved{},
	}, {
		name:     "settings only are copied",
		settings: map[string]string{"path": "/data"},
		want:     Resolved{"path": "/data"},
	}, {
		name: "refs only",
		refs: map[string]string{"url": "postgres://app:${DB_PASSWORD}@$HOST/app"},
		want: Resolved{"url": "postgres://app:pw@db.internal/app"},
	}, {
		name:     "merge",
		settings: map[string]string{"table": "users"},
		refs:     map[string]string{"password": "${DB_PASSWORD}"},
		want:     Resolved{"table": "users", "password": "pw"},
	}, {
		name:     "refs win over a duplicate settings key",
		settings: map[string]string{"password": "stale"},
		refs:     map[string]string{"password": "${DB_PASSWORD}"},
		want:     Resolved{"password": "pw"},
	}, {
		name: "set to empty is not unset",
		refs: map[string]string{"opt": "${EMPTY}"},
		want: Resolved{"opt": ""},
	}, {
		name:      "unset resolves to empty and is reported once per key",
		refs:      map[string]string{"b": "${MISSING}-${MISSING}", "a": "$OTHER ${DB_PASSWORD}"},
		want:      Resolved{"a": " pw", "b": "-"},
		wantUnset: []Unset{{Key: "a", Var: "OTHER"}, {Key: "b", Var: "MISSING"}},
	}, {
		name:      "shell special names are references, as in v0.20",
		refs:      map[string]string{"price": "costs $5"},
		want:      Resolved{"price": "costs "},
		wantUnset: []Unset{{Key: "price", Var: "5"}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			settingsBefore := clone(tc.settings)
			refsBefore := clone(tc.refs)

			got, unset := resolve(tc.settings, tc.refs, env)
			is.Equal(map[string]string(got), map[string]string(tc.want))
			is.Equal(got == nil, tc.want == nil)
			is.Equal(unset, tc.wantUnset)

			// Inputs are never modified.
			is.Equal(tc.settings, settingsBefore)
			is.Equal(tc.refs, refsBefore)
		})
	}
}

func TestResolve_DoesNotAliasInputs(t *testing.T) {
	is := is.New(t)
	settings := map[string]string{"a": "1"}
	got, _ := resolve(settings, nil, fakeEnv(nil))
	got["a"] = "changed"
	got["b"] = "added"
	is.Equal(settings, map[string]string{"a": "1"})
}

func TestResolve_ProcessEnvironment(t *testing.T) {
	is := is.New(t)
	t.Setenv("SECRETREF_TEST_SET", canary)
	is.NoErr(os.Unsetenv("SECRETREF_TEST_UNSET"))

	got, unset := Resolve(nil, map[string]string{
		"a": "${SECRETREF_TEST_SET}",
		"b": "${SECRETREF_TEST_UNSET}",
	})
	is.Equal(got["a"], canary)
	is.Equal(got["b"], "")
	is.Equal(unset, []Unset{{Key: "b", Var: "SECRETREF_TEST_UNSET"}})
}

func TestIsReference(t *testing.T) {
	testCases := map[string]bool{
		"":                 false,
		"plain":            false,
		"$VAR":             true,
		"${VAR}":           true,
		"prefix-${VAR}-x":  true,
		"$1":               true, // shell special, os.Expand calls the mapper
		"$$":               true,
		"trailing $":       false,
		"${}":              false, // invalid syntax, os.Expand eats it silently
		"cost: 5$":         false,
		"a$ b":             false, // "$ " is not a name
		"${unterminated":   false, // bad syntax, no mapper call
		"$ {VAR}":          false,
		"${VAR:-default}":  true, // Go reads the whole thing as a name
		"${vault:path#k}":  true, // v0.22 grammar, still an env lookup in v0.21
		"postgres://a:b@h": false,
	}
	for in, want := range testCases {
		t.Run(in, func(t *testing.T) {
			if got := IsReference(in); got != want {
				t.Fatalf("IsReference(%q) = %v, want %v", in, got, want)
			}
			// Cross-check against the definition: os.Expand calls the mapper.
			called := false
			os.Expand(in, func(string) string { called = true; return "" })
			if called != want {
				t.Fatalf("test case %q disagrees with os.Expand (mapper called: %v)", in, called)
			}
		})
	}
}

// TestResolved_NeverPrintsValues covers every way a Resolved map could reach
// a log line, an error message or an encoder (Invariant R1).
func TestResolved_NeverPrintsValues(t *testing.T) {
	r := Resolved{"password": canary, "url": "postgres://app:" + canary + "@db/app"}

	var zbuf bytes.Buffer
	zl := zerolog.New(&zbuf)
	zl.Info().Interface("i", r).Object("o", r).Fields(map[string]any{"f": r}).Msg("")

	goccy, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	std, err := stdjson.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	type wrapper struct{ Settings Resolved }

	yamlDirect, err := yaml.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	yamlNested, err := yaml.Marshal(wrapper{r})
	if err != nil {
		t.Fatal(err)
	}
	var slogText, slogJSON bytes.Buffer
	slog.New(slog.NewTextHandler(&slogText, nil)).Info("m", "direct", r, "nested", wrapper{r})
	slog.New(slog.NewJSONHandler(&slogJSON, nil)).Info("m", "direct", r, "nested", wrapper{r})

	outputs := map[string]string{
		"String":            r.String(),
		"GoString":          r.GoString(),
		"%v":                fmt.Sprintf("%v", r),
		"%+v":               fmt.Sprintf("%+v", r),
		"%#v":               fmt.Sprintf("%#v", r),
		"%s":                fmt.Sprintf("%s", r),
		"%q":                fmt.Sprintf("%q", r),
		"%x":                fmt.Sprintf("%x", r),
		"%d":                fmt.Sprintf("%d", r),
		"Sprint":            fmt.Sprint(r),
		"nested %v":         fmt.Sprintf("%v", wrapper{r}),
		"nested %+v":        fmt.Sprintf("%+v", wrapper{r}),
		"nested %#v":        fmt.Sprintf("%#v", wrapper{r}),
		"pointer %v":        fmt.Sprintf("%v", &wrapper{r}),
		"goccy json":        string(goccy),
		"encoding/json":     string(std),
		"errorf %v":         fmt.Errorf("configure: %v", r).Error(),
		"zerolog":           zbuf.String(),
		"goccy json nested": mustJSON(t, wrapper{r}),
		"yaml":              string(yamlDirect),
		"yaml nested":       string(yamlNested),
		"slog text":         slogText.String(),
		"slog json":         slogJSON.String(),
	}
	for name, out := range outputs {
		if strings.Contains(out, canary) {
			t.Errorf("%s leaked the value: %s", name, out)
		}
		if !strings.Contains(out, "password") {
			t.Errorf("%s dropped the key, which makes the output useless: %s", name, out)
		}
	}

	// The explicit conversion at the plugin boundary is the only way out.
	if map[string]string(r)["password"] != canary {
		t.Fatal("explicit conversion must keep the value")
	}
}

// TestResolved_UnexportedFieldIsNotProtected pins the documented limit of
// the redaction: fmt cannot call methods on a value in an unexported field,
// so it prints the raw map. If this test starts failing because fmt got
// smarter, narrow rule R1b in doc.go; until then, R1b is what protects it.
func TestResolved_UnexportedFieldIsNotProtected(t *testing.T) {
	type holder struct{ settings Resolved }
	out := fmt.Sprintf("%+v", holder{Resolved{"password": canary}})
	if !strings.Contains(out, canary) {
		t.Fatalf("fmt now redacts unexported fields (%s); update rule R1b in doc.go", out)
	}
}

func TestResolved_NilForms(t *testing.T) {
	is := is.New(t)
	var r Resolved
	is.Equal(r.String(), "map[]")
	is.Equal(r.GoString(), "secretref.Resolved(nil)")
	b, err := json.Marshal(r)
	is.NoErr(err)
	is.Equal(string(b), "null")
}

func TestNewUnsetError(t *testing.T) {
	is := is.New(t)
	t.Setenv("SECRETREF_SET", canary)

	_, unset := Resolve(
		map[string]string{"literal": canary},
		map[string]string{"password": "${DB_PASSWORD}", "url": "postgres://" + canary + "@${HOST}/x ${SECRETREF_SET}"},
	)
	got := NewUnsetError(`connector "pg-source"`, unset)
	err, ok := conduiterr.Get(got)
	is.True(ok)

	is.Equal(err.Code, CodeEnvUnset)
	is.Equal(err.Code.Reason(), "secretref.env_unset")
	is.Equal(err.Code.GRPCCode(), codes.FailedPrecondition)
	is.Equal(err.ConfigPath, "settings.password")
	is.True(err.Suggestion != "")
	is.Equal(err.Error(),
		`connector "pg-source": settings reference environment variables that are not set: `+
			`DB_PASSWORD (settings.password), HOST (settings.url)`)

	// No value and no template text ever reaches the error.
	for _, s := range []string{err.Error(), err.Suggestion, err.ConfigPath, fmt.Sprintf("%+v", err)} {
		is.True(!strings.Contains(s, canary))
		is.True(!strings.Contains(s, "postgres://"))
	}
}

func TestNewUnsetError_PanicsWithoutEntries(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	_ = NewUnsetError("x", nil)
}

func TestCodeEnvUnset_Registered(t *testing.T) {
	is := is.New(t)
	c, ok := conduiterr.LookupCode("secretref.env_unset")
	is.True(ok)
	is.Equal(c, CodeEnvUnset)
}

// FuzzResolve pins the grammar to v0.20. For any template and any
// environment in which every referenced variable is set, Resolve yields what
// os.ExpandEnv yielded, IsReference agrees with "os.Expand called the
// mapper", and the unset list is empty. With the variables removed, Resolve
// still matches os.ExpandEnv (unset expands to ""), and reports exactly the
// names os.Expand asked for.
func FuzzResolve(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "$A", "${A}", "x${A}y$B", "$1", "$$", "${}", "trailing$",
		"${unterminated", "${A:-d}", "${vault:p#k}", "a$ b", "$-", "${A}${A}",
		"postgres://u:${PW}@${HOST}:5432/db?sslmode=require",
	} {
		f.Add(seed, "value")
	}
	f.Fuzz(func(t *testing.T, tmpl, value string) {
		names := map[string]bool{}
		os.Expand(tmpl, func(n string) string { names[n] = true; return "" })

		if IsReference(tmpl) != (len(names) > 0) {
			t.Fatalf("IsReference(%q) disagrees with os.Expand", tmpl)
		}

		// Every variable set: equal to os.ExpandEnv in an environment with
		// the same values.
		env := map[string]string{}
		for n := range names {
			env[n] = value + n
		}
		got, unset := resolve(nil, map[string]string{"k": tmpl}, fakeEnv(env))
		want := os.Expand(tmpl, func(n string) string { return env[n] })
		if got["k"] != want {
			t.Fatalf("resolve(%q) = %q, os.Expand = %q", tmpl, got["k"], want)
		}
		if len(unset) != 0 {
			t.Fatalf("resolve(%q) reported unset %v with every variable set", tmpl, unset)
		}

		// Nothing set: v0.20 expanded to "", and every name is reported once.
		got, unset = resolve(nil, map[string]string{"k": tmpl}, fakeEnv(nil))
		want = os.Expand(tmpl, func(string) string { return "" })
		if got["k"] != want {
			t.Fatalf("resolve(%q) unset = %q, os.Expand = %q", tmpl, got["k"], want)
		}
		if len(unset) != len(names) {
			t.Fatalf("resolve(%q) reported %d unset, want %d", tmpl, len(unset), len(names))
		}
		for _, u := range unset {
			if !names[u.Var] || u.Key != "k" {
				t.Fatalf("resolve(%q) reported unexpected %+v", tmpl, u)
			}
		}

		// A Resolved map never prints a value, whatever the value is.
		r := Resolved{"k": value}
		for _, verb := range []string{"%v", "%+v", "%s", "%d", "%x", "%q"} {
			if got := fmt.Sprintf(verb, r); got != "map[k:***]" {
				t.Fatalf("Sprintf(%s) = %q", verb, got)
			}
		}
		if got := fmt.Sprintf("%#v", r); got != `secretref.Resolved{"k":"***"}` {
			t.Fatalf("Sprintf(%%#v) = %q", got)
		}
		if got := mustJSON(t, r); got != `{"k":"***"}` {
			t.Fatalf("json = %q", got)
		}
	})
}

// FuzzStoreThenResolve is the contract the config parser must keep (package
// doc, PR 2): store a reference verbatim in SettingRefs and anything else as
// os.ExpandEnv(v) in Settings, then resolve. The result must equal
// os.ExpandEnv(v), which is what v0.20 stored, for every v. It also shows
// why storing a non-reference raw would be wrong: "x${}y" is not a
// reference but expands to "xy".
func FuzzStoreThenResolve(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "x${}y", "x${ay", "trailing$", "$A", "${A}b", "a$ b", "$$",
		"postgres://u:${PW}@h/db",
	} {
		f.Add(seed, "val")
	}
	f.Fuzz(func(t *testing.T, v, value string) {
		env := map[string]string{}
		os.Expand(v, func(n string) string { env[n] = value + n; return "" })
		expand := func(s string) string { return os.Expand(s, func(n string) string { return env[n] }) }

		var settings, refs map[string]string
		if IsReference(v) {
			refs = map[string]string{"k": v}
		} else {
			settings = map[string]string{"k": expand(v)}
		}
		got, unset := resolve(settings, refs, fakeEnv(env))
		if got["k"] != expand(v) {
			t.Fatalf("store-then-resolve(%q) = %q, os.ExpandEnv = %q", v, got["k"], expand(v))
		}
		if len(unset) != 0 {
			t.Fatalf("unexpected unset %v", unset)
		}
	})
}

func clone(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
