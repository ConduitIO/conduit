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

// Package provider is the LLM seam for `conduit generate`: a two-method
// interface plus the deterministic rules that decide which implementation a
// given invocation uses.
//
// # No default vendor, ever
//
// Conduit's broker-neutrality principle ("Switzerland — no broker required at
// all") extends to model vendors. Picking a permanent default LLM vendor would
// be the same favoritism the roadmap forbids for brokers, so there isn't one:
// with several providers configured and no explicit selection, Resolve
// REFUSES rather than picking.
//
// A silent pick would also make a real support question — "why did it use
// provider X?" — return a wrong answer every time the ordering changed.
//
// # No new dependencies
//
// Adapters reuse what the tree already vendors: the openai adapter wraps the
// same client the `openai` built-in processor uses, and the anthropic and
// ollama adapters are thin net/http JSON clients, following the `ollama`
// processor's existing hand-rolled client rather than forking a second
// pattern. See docs/design-documents/20260722-conduit-generate.md §1.
package provider

import (
	"context"
)

// Provider is a single-turn text completion. Implementations never parse the
// response into a pipeline config — that is generate's job, so an adapter never
// needs to know the pipeline schema and a schema change never touches an
// adapter.
type Provider interface {
	// Name is the stable identifier used in config, --provider, and error
	// messages.
	Name() string

	// Complete sends a grounded prompt and returns the raw model text.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error)
}

// CompletionRequest is one grounded call.
type CompletionRequest struct {
	// System carries the grounding: schema, connector catalog, few-shot
	// examples. Built from llms-full.txt, never hand-maintained here.
	System string
	// Prompt is the user's natural-language request, verbatim. It is never
	// rewritten before being sent — a rewrite would make the model's output
	// unattributable to what the user actually asked.
	Prompt string
	// Model is the provider-specific model identifier.
	Model string
}

// CompletionResult is one raw completion.
type CompletionResult struct {
	Text string
	// TokensUsed is what the provider reported, or 0 when it reported nothing.
	// Never estimated: a fabricated token count would be indistinguishable from
	// a real one in --json output that users may bill against.
	TokensUsed int
}

// Known provider names. These are the only values --provider,
// generate.provider, and CONDUIT_GENERATE_PROVIDER accept.
const (
	NameAnthropic = "anthropic"
	NameOpenAI    = "openai"
	NameOllama    = "ollama"
)

// Names lists every known provider in the fixed order Resolve reports
// candidates in.
//
// The order is for REPORTING ONLY and implies no preference — Resolve never
// uses it to break a tie, it refuses instead. It exists so that error messages
// and doctor output list providers the same way every run, which is what makes
// them assertable.
var Names = []string{NameAnthropic, NameOpenAI, NameOllama}

// Valid reports whether name is a known provider.
func Valid(name string) bool {
	for _, n := range Names {
		if n == name {
			return true
		}
	}
	return false
}
