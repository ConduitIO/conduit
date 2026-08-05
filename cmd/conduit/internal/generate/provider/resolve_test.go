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

package provider

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// env builds an Env from a map. Tests never touch real process environment, so
// they stay parallel-safe and cannot leak into each other.
func env(kv map[string]string) Env {
	return func(k string) string { return kv[k] }
}

func reachable(bool2 bool) Probe { return func(string) bool { return bool2 } }

func codeOf(t *testing.T, err error) conduiterr.Code {
	t.Helper()
	ce, ok := conduiterr.Get(err)
	if !ok {
		t.Fatalf("error is not a ConduitError: %v", err)
	}
	return ce.Code
}

// Test_Resolve_Precedence pins the documented order. Each case sets a LOWER
// precedence source to a different provider, so a test only passes if the
// higher one actually wins rather than coinciding.
func Test_Resolve_Precedence(t *testing.T) {
	t.Parallel()

	full := map[string]string{
		EnvProvider:     NameOllama,
		EnvAnthropicKey: "sk-ant",
		EnvOpenAIKey:    "sk-oai",
	}

	t.Run("flag beats everything", func(t *testing.T) {
		is := is.New(t)
		got, err := Resolve(ResolveInput{Flag: NameOpenAI, Config: NameAnthropic, Env: env(full), ProbeOllama: reachable(true)})
		is.NoErr(err)
		is.Equal(got, NameOpenAI)
	})

	t.Run("config beats env", func(t *testing.T) {
		is := is.New(t)
		got, err := Resolve(ResolveInput{Config: NameAnthropic, Env: env(full), ProbeOllama: reachable(true)})
		is.NoErr(err)
		is.Equal(got, NameAnthropic)
	})

	t.Run("env beats auto-detect", func(t *testing.T) {
		is := is.New(t)
		got, err := Resolve(ResolveInput{Env: env(full), ProbeOllama: reachable(true)})
		is.NoErr(err)
		is.Equal(got, NameOllama)
	})
}

// Test_Resolve_ExplicitSkipsProbing pins that naming a provider is honoured
// without a reachability check. Second-guessing an explicit choice would turn a
// clear instruction into a confusing refusal; the provider's own call should
// fail instead, with its own error.
func Test_Resolve_ExplicitSkipsProbing(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	probed := false
	got, err := Resolve(ResolveInput{
		Flag:        NameOllama,
		Env:         env(nil),
		ProbeOllama: func(string) bool { probed = true; return false },
	})
	is.NoErr(err)
	is.Equal(got, NameOllama)
	is.True(!probed) // explicit selection must not probe
}

// Test_Resolve_SingleCandidate is the 5-minute-wow path: a laptop with one
// thing configured just works, with no flag.
func Test_Resolve_SingleCandidate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   ResolveInput
		want string
	}{
		{"only anthropic", ResolveInput{Env: env(map[string]string{EnvAnthropicKey: "sk-ant"})}, NameAnthropic},
		{"only openai", ResolveInput{Env: env(map[string]string{EnvOpenAIKey: "sk-oai"})}, NameOpenAI},
		{"only ollama", ResolveInput{Env: env(nil), ProbeOllama: reachable(true)}, NameOllama},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got, err := Resolve(tc.in)
			is.NoErr(err)
			is.Equal(got, tc.want)
		})
	}
}

// Test_Resolve_RefusesToPickAVendor is the load-bearing one.
//
// Conduit's broker-neutrality principle extends to model vendors. With several
// configured and no explicit choice, resolution must REFUSE — a silent pick is
// hidden favoritism, and it makes "why did it use provider X?" unanswerable.
func Test_Resolve_RefusesToPickAVendor(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	_, err := Resolve(ResolveInput{
		Env:         env(map[string]string{EnvAnthropicKey: "sk-ant", EnvOpenAIKey: "sk-oai"}),
		ProbeOllama: reachable(true),
	})
	is.True(err != nil)
	is.Equal(codeOf(t, err), CodeAmbiguousProvider)

	// The message must name what it found, or the user cannot act on it.
	for _, want := range []string{NameAnthropic, NameOpenAI, NameOllama} {
		is.True(contains(err.Error(), want))
	}
}

// Test_Resolve_ZeroCandidates pins that the error names every way to fix it —
// an "environment" failure the user's command did nothing wrong to cause.
func Test_Resolve_ZeroCandidates(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	_, err := Resolve(ResolveInput{Env: env(nil), ProbeOllama: reachable(false)})
	is.True(err != nil)
	is.Equal(codeOf(t, err), CodeNoProviderConfigured)

	ce, _ := conduiterr.Get(err)
	for _, want := range []string{EnvAnthropicKey, EnvOpenAIKey, "--provider"} {
		is.True(contains(ce.Suggestion, want))
	}
}

// Test_Resolve_NilProbeNeverAutoDetectsOllama pins the safe direction: an unset
// probe means "we did not look", and treating that as "reachable" could
// silently pick a provider that is not there.
func Test_Resolve_NilProbeNeverAutoDetectsOllama(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	_, err := Resolve(ResolveInput{Env: env(map[string]string{EnvOllamaHost: DefaultOllamaHost})})
	is.True(err != nil)
	is.Equal(codeOf(t, err), CodeNoProviderConfigured)
}

// Test_Resolve_UnreachableOllamaIsNotACandidate pins the most likely laptop
// misconfiguration: a stale OLLAMA_HOST pointing at nothing must not make every
// invocation ambiguous on a machine that also has an API key.
func Test_Resolve_UnreachableOllamaIsNotACandidate(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	got, err := Resolve(ResolveInput{
		Env:         env(map[string]string{EnvAnthropicKey: "sk-ant", EnvOllamaHost: "http://localhost:1"}),
		ProbeOllama: reachable(false),
	})
	is.NoErr(err)
	is.Equal(got, NameAnthropic)
}

func Test_Resolve_UnknownProviderName(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	_, err := Resolve(ResolveInput{Flag: "gpt5-turbo-max", Env: env(nil)})
	is.True(err != nil)
	is.Equal(codeOf(t, err), conduiterr.CodeInvalidArgument)

	ce, _ := conduiterr.Get(err)
	is.True(contains(ce.Suggestion, NameAnthropic)) // lists the valid ones
}

// Test_Candidates_DeterministicOrder pins that reporting order is stable.
// Error messages and doctor output are asserted on and read by agents; an
// order that varied per run could not be.
func Test_Candidates_DeterministicOrder(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	in := ResolveInput{
		Env:         env(map[string]string{EnvOpenAIKey: "sk-oai", EnvAnthropicKey: "sk-ant"}),
		ProbeOllama: reachable(true),
	}
	want := []string{NameAnthropic, NameOpenAI, NameOllama}
	for range 20 {
		is.Equal(Candidates(in), want)
	}
}

// Test_Resolve_BlankValuesAreNotSelections pins that whitespace-only config
// does not count as an explicit choice — an empty CONDUIT_GENERATE_PROVIDER=""
// in CI must fall through to auto-detect, not fail as an unknown provider.
func Test_Resolve_BlankValuesAreNotSelections(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	got, err := Resolve(ResolveInput{
		Flag:   "   ",
		Config: "",
		Env:    env(map[string]string{EnvProvider: "  ", EnvAnthropicKey: "sk-ant"}),
	})
	is.NoErr(err)
	is.Equal(got, NameAnthropic)
}

func Test_Resolve_RequiresEnv(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	_, err := Resolve(ResolveInput{})
	is.True(err != nil)
	is.Equal(codeOf(t, err), conduiterr.CodeInternal)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
