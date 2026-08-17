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

package generate

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// validCandidate is a pipeline that passes validate — the generator/log pair,
// which needs no external system.
const validCandidate = `version: "2.2"
pipelines:
  - id: test-pipeline
    status: running
    connectors:
      - id: source
        type: source
        plugin: "builtin:generator"
        settings:
          format.type: structured
      - id: destination
        type: destination
        plugin: "builtin:log"
        settings:
          level: info
`

// invalidCandidate parses as YAML but fails validate: a connector with no
// plugin reference (config.CodeFieldRequired). A pipeline with NO connectors
// at all is not usable, but it is schema-valid — using that here would have
// made every retry test pass vacuously.
const invalidCandidate = `version: "2.2"
pipelines:
  - id: broken-pipeline
    status: running
    connectors:
      - id: source
        type: source
`

// unknownPluginCandidate names a builtin connector that does not exist, one
// edit away from a real one.
const unknownPluginCandidate = `version: "2.2"
pipelines:
  - id: typo-pipeline
    status: running
    connectors:
      - id: source
        type: source
        plugin: "builtin:postgre"
        settings:
          url: postgres://localhost:5432/db
          tables: t
      - id: destination
        type: destination
        plugin: "builtin:log"
        settings:
          level: info
`

// fakeProvider replays scripted replies and records every request it was
// given, so a test can assert on what was actually sent to the model.
type fakeProvider struct {
	replies  []string
	err      error
	requests []provider.CompletionRequest
	tokens   int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return provider.CompletionResult{}, f.err
	}
	i := len(f.requests) - 1
	if i >= len(f.replies) {
		i = len(f.replies) - 1 // repeat the last scripted reply
	}
	return provider.CompletionResult{Text: f.replies[i], TokensUsed: f.tokens}, nil
}

func TestGenerate_HappyPath_ValidatesAndReturnsOnFirstAttempt(t *testing.T) {
	is := is.New(t)
	p := &fakeProvider{replies: []string{"```yaml\n" + validCandidate + "```"}, tokens: 7}

	res, err := Generate(context.Background(), Input{Prompt: "read from the generator and write to the log", Provider: p})

	is.NoErr(err)
	is.Equal(len(p.requests), 1)
	is.Equal(len(res.Attempts), 1)
	is.True(res.Report.OK())
	is.Equal(res.TokensUsed, 7)
	is.True(strings.Contains(res.Candidate, "builtin:generator"))
}

// The user's prompt reaches the provider verbatim. A rewritten prompt makes
// the output unattributable to what the user asked for.
func TestGenerate_SendsThePromptVerbatim(t *testing.T) {
	is := is.New(t)
	prompt := "stream new orders from postgres into a kafka topic, only orders over $100"
	p := &fakeProvider{replies: []string{validCandidate}}

	_, err := Generate(context.Background(), Input{Prompt: prompt, Provider: p})

	is.NoErr(err)
	is.Equal(p.requests[0].Prompt, prompt)
}

// The grounding is a system prompt built from the compiled-in catalog, not
// something the caller supplies or a file read at runtime.
func TestGenerate_GroundsWithTheCatalog(t *testing.T) {
	is := is.New(t)
	p := &fakeProvider{replies: []string{validCandidate}}

	_, err := Generate(context.Background(), Input{Prompt: "anything", Provider: p})

	is.NoErr(err)
	is.True(strings.Contains(p.requests[0].System, "builtin:"))
	for _, name := range CatalogNames(BuiltinCatalog()) {
		is.True(strings.Contains(p.requests[0].System, name))
	}
}

// A failed validate becomes bounded, structured feedback appended BELOW the
// user's original prompt, and the next attempt succeeds.
func TestGenerate_RetriesWithFeedbackAfterFailedValidation(t *testing.T) {
	is := is.New(t)
	prompt := "generator to log"
	p := &fakeProvider{replies: []string{invalidCandidate, validCandidate}}

	res, err := Generate(context.Background(), Input{Prompt: prompt, Provider: p})

	is.NoErr(err)
	is.Equal(len(p.requests), 2)
	is.Equal(len(res.Attempts), 2)

	retry := p.requests[1].Prompt
	is.True(strings.HasPrefix(retry, prompt))             // original first, verbatim
	is.True(strings.Contains(retry, "failed validation")) // the correction is attached
	is.True(len(res.Attempts[0].Feedback.Items) > 0)      // and recorded on the attempt
	is.True(res.Attempts[0].Report.Summary.Errors > 0)    // driven by real findings
	is.True(len(res.Attempts[1].Feedback.Items) == 0)     // none needed after success
}

// An unknown builtin plugin gets a did-you-mean correction inside the retry
// budget rather than a terminal failure (design §7).
func TestGenerate_UnknownPluginBecomesADidYouMeanCorrection(t *testing.T) {
	is := is.New(t)
	p := &fakeProvider{replies: []string{unknownPluginCandidate, validCandidate}}

	res, err := Generate(context.Background(), Input{Prompt: "postgres to log", Provider: p})

	is.NoErr(err)
	is.Equal(len(p.requests), 2)

	retry := p.requests[1].Prompt
	is.True(strings.Contains(retry, "postgres")) // the real connector is named
	var suggested bool
	for _, item := range res.Attempts[0].Feedback.Items {
		if item.Code == "connector.plugin_not_found" && strings.Contains(item.Suggestion, "postgres") {
			suggested = true
		}
	}
	is.True(suggested)
}

// Exhausting the budget on validation failures is generate.validate_failed —
// and the last report travels back, because the findings are the only thing
// that tells the user what to change.
func TestGenerate_ExhaustedValidation_KeepsTheLastReport(t *testing.T) {
	is := is.New(t)
	p := &fakeProvider{replies: []string{invalidCandidate}}

	res, err := Generate(context.Background(), Input{Prompt: "x", Provider: p, MaxAttempts: 2})

	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeValidateFailed)
	is.Equal(len(p.requests), 2)
	is.True(res.Candidate != "")           // the artifact is not discarded
	is.True(res.Report.Summary.Errors > 0) // nor are its findings
	is.Equal(len(res.Attempts), 2)
}

// A reply that never contains a pipeline config is a boundary failure, not a
// validation failure: different code, different exit bucket, different fix.
func TestGenerate_NeverParsed_IsParseFailed(t *testing.T) {
	is := is.New(t)
	p := &fakeProvider{replies: []string{"I'd be happy to help you build a pipeline!"}}

	res, err := Generate(context.Background(), Input{Prompt: "x", Provider: p, MaxAttempts: 2})

	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeParseFailed)
	is.Equal(res.Candidate, "")
	is.True(res.Attempts[0].ParseErr != nil)
}

// A provider failure is returned as-is and does not consume the retry budget:
// retrying an unreachable endpoint just spends the user's time.
func TestGenerate_ProviderError_DoesNotRetry(t *testing.T) {
	is := is.New(t)
	boom := cerrors.New("dial tcp: connection refused")
	p := &fakeProvider{replies: []string{validCandidate}, err: boom}

	_, err := Generate(context.Background(), Input{Prompt: "x", Provider: p, MaxAttempts: 3})

	is.True(cerrors.Is(err, boom))
	is.Equal(len(p.requests), 1)
}

// The attempt budget is always bounded, including when a caller passes
// nonsense: a negative budget is one attempt, never unbounded.
func TestGenerate_AttemptBudgetIsAlwaysBounded(t *testing.T) {
	is := is.New(t)

	for _, tc := range []struct {
		name  string
		max   int
		calls int
	}{
		{"zero means the default", 0, DefaultMaxAttempts},
		{"negative means one", -5, 1},
		{"explicit is honored", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{replies: []string{invalidCandidate}}
			_, err := Generate(context.Background(), Input{Prompt: "x", Provider: p, MaxAttempts: tc.max})
			is.True(err != nil)
			is.Equal(len(p.requests), tc.calls)
		})
	}
}

// The validate gate has no bypass: every success path returns a candidate
// whose report is OK. This is the guarantee the feature rests on, so it is
// asserted directly rather than inferred from the happy-path test.
func TestGenerate_SuccessAlwaysCarriesAPassingReport(t *testing.T) {
	is := is.New(t)

	for _, reply := range []string{
		validCandidate,
		"```yaml\n" + validCandidate + "```",
		"Here you go:\n\n" + validCandidate,
	} {
		p := &fakeProvider{replies: []string{reply}}
		res, err := Generate(context.Background(), Input{Prompt: "x", Provider: p})
		is.NoErr(err)
		is.True(res.Report.OK())
	}
}

func TestGenerate_NoProvider_IsAnError(t *testing.T) {
	is := is.New(t)

	_, err := Generate(context.Background(), Input{Prompt: "x"})

	is.True(err != nil)
}
