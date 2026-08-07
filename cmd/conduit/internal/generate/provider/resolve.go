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
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// EnvProvider selects a provider explicitly, for CI and agent use where there
// is no flag to pass.
const EnvProvider = "CONDUIT_GENERATE_PROVIDER"

// Env vars whose presence makes a hosted provider a candidate.
const (
	EnvAnthropicKey = "ANTHROPIC_API_KEY"
	EnvOpenAIKey    = "OPENAI_API_KEY"
	EnvOllamaHost   = "OLLAMA_HOST"
)

// DefaultOllamaHost is where a local Ollama server listens.
const DefaultOllamaHost = "http://localhost:11434"

// Env abstracts environment lookup so resolution is testable without mutating
// real process state. Tests that set real env vars cannot run in parallel and
// leak into each other; this seam is the reason the tests here can.
type Env func(key string) string

// Probe reports whether a provider that has no API key — currently only
// ollama — is actually reachable. It is a func so resolution never performs
// network I/O in a test.
//
// Reachability is deliberately part of candidacy for ollama and NOT for the
// hosted providers: a stale OLLAMA_HOST pointing at nothing would otherwise
// make every invocation ambiguous on a machine that also has an API key set,
// which is the single most likely misconfiguration on a developer laptop.
type Probe func(host string) bool

// ResolveInput is everything that decides which provider an invocation uses.
type ResolveInput struct {
	// Flag is --provider. Highest precedence.
	Flag string
	// Config is generate.provider from conduit.yaml.
	Config string
	// Env looks up environment variables. Required.
	Env Env
	// ProbeOllama reports whether an Ollama server is reachable. When nil,
	// ollama is never auto-detected as a candidate — an unset probe means "we
	// did not look", and treating "did not look" as "not there" is the safe
	// direction: it can cause a no_provider_configured error the user can fix,
	// never a silent wrong pick.
	ProbeOllama Probe
}

// Resolve returns the provider name this invocation should use.
//
// Precedence, per design doc §1:
//
//  1. --provider flag
//  2. generate.provider in conduit.yaml
//  3. CONDUIT_GENERATE_PROVIDER
//  4. auto-detect, and only when EXACTLY ONE candidate is resolvable
//
// An explicit selection (1-3) is honoured without probing anything: if a user
// names a provider, the job is to use it and let the call fail with the
// provider's own error, not to second-guess them with a reachability check
// whose failure mode is a confusing refusal.
//
// Zero candidates and more than one candidate are both errors, and the
// more-than-one case is the important one — see the package doc for why a
// silent pick is not an option.
func Resolve(in ResolveInput) (string, error) {
	if in.Env == nil {
		return "", conduiterr.New(conduiterr.CodeInternal, "provider resolution requires an Env lookup")
	}

	for _, explicit := range []struct{ source, value string }{
		{"--provider", in.Flag},
		{"generate.provider", in.Config},
		{EnvProvider, in.Env(EnvProvider)},
	} {
		v := strings.TrimSpace(explicit.value)
		if v == "" {
			continue
		}
		if !Valid(v) {
			e := conduiterr.New(codeUnknownProvider,
				fmt.Sprintf("unknown generation provider %q (set via %s)", v, explicit.source))
			e.Suggestion = "valid providers: " + strings.Join(Names, ", ")
			return "", e
		}
		return v, nil
	}

	candidates := Candidates(in)

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		e := conduiterr.New(CodeNoProviderConfigured, "no generation provider configured")
		e.Suggestion = fmt.Sprintf(
			"set one of %s or %s, run a local Ollama server (%s), or select one explicitly with --provider",
			EnvAnthropicKey, EnvOpenAIKey, DefaultOllamaHost,
		)
		return "", e
	default:
		e := conduiterr.New(CodeAmbiguousProvider,
			fmt.Sprintf("more than one generation provider is configured: %s", strings.Join(candidates, ", ")))
		e.Suggestion = fmt.Sprintf(
			"choose one with --provider, generate.provider in conduit.yaml, or %s", EnvProvider,
		)
		return "", e
	}
}

// Candidates returns every auto-detectable provider, in the fixed order of
// Names.
//
// Order is for reporting only. Resolve never uses it to break a tie — see the
// package doc.
func Candidates(in ResolveInput) []string {
	if in.Env == nil {
		return nil
	}

	var found []string
	if strings.TrimSpace(in.Env(EnvAnthropicKey)) != "" {
		found = append(found, NameAnthropic)
	}
	if strings.TrimSpace(in.Env(EnvOpenAIKey)) != "" {
		found = append(found, NameOpenAI)
	}
	if in.ProbeOllama != nil {
		host := strings.TrimSpace(in.Env(EnvOllamaHost))
		if host == "" {
			host = DefaultOllamaHost
		}
		if in.ProbeOllama(host) {
			found = append(found, NameOllama)
		}
	}

	// Sorting by the fixed Names order, not alphabetically: the two happen to
	// coincide today, and pinning it here means a future rename of a provider
	// cannot silently reorder every error message and doctor line.
	sort.SliceStable(found, func(i, j int) bool {
		return indexOfName(found[i]) < indexOfName(found[j])
	})

	return found
}

func indexOfName(name string) int {
	for i, n := range Names {
		if n == name {
			return i
		}
	}
	return len(Names)
}
