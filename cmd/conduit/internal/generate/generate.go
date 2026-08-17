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
	"sort"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// DefaultMaxAttempts is the default generate budget: one attempt plus two
// corrective retries. Retries are real provider calls a user pays for and
// waits on, so the budget is small and always bounded — never "until it
// works".
const DefaultMaxAttempts = 3

// Input is one generation request.
type Input struct {
	// Prompt is the user's natural-language request. It is sent VERBATIM:
	// never rewritten, summarized, or "improved" before the call, so the
	// output is always attributable to what the user actually asked.
	Prompt string
	// Provider is the resolved LLM seam. Required.
	Provider provider.Provider
	// Model is the provider-specific model identifier, passed through
	// untouched (empty means the adapter's own default).
	Model string
	// MaxAttempts bounds total provider calls. Zero means DefaultMaxAttempts;
	// negative is treated as one attempt, never as unbounded.
	MaxAttempts int
	// Catalog grounds the prompt. Nil means BuiltinCatalog().
	Catalog []ConnectorInfo
}

// Attempt records one provider call and how far its output got.
type Attempt struct {
	// N is the 1-based attempt number.
	N int
	// Candidate is the extracted YAML, empty when extraction failed.
	Candidate string
	// Report is the validate result for Candidate. Zero when the attempt
	// never reached validation.
	Report validate.Report
	// ParseErr is set when the reply held no usable pipeline YAML.
	ParseErr error
	// Feedback is what was handed to the NEXT attempt, empty on the last one
	// and on success.
	Feedback RetryFeedback
	// TokensUsed is what the provider reported for this call, or 0 when it
	// reported nothing. Never estimated.
	TokensUsed int
}

// Generation is the outcome of a generation run.
//
// It is returned filled in even when Generate returns an error: on failure
// Candidate and Report carry the LAST attempt's artifact and findings, which
// are the only thing that tells a user or an agent what to change. Discarding
// them would turn a specific, fixable failure into "generation failed".
type Generation struct {
	// Candidate is the validated pipeline YAML on success, or the last
	// candidate produced on failure (empty if none ever parsed).
	Candidate string
	// Report is the validate report for Candidate.
	Report validate.Report
	// Attempts is every attempt made, in order.
	Attempts []Attempt
	// TokensUsed sums every attempt's reported usage.
	TokensUsed int
}

// Generate runs the grounded generation loop: prompt the provider, extract a
// candidate, validate it, and on failure retry with bounded, structured
// feedback until the budget is spent.
//
// The validate gate is unconditional and has no bypass: this function returns
// a candidate only when validate.RunBytes reports no error-severity findings,
// which is the guarantee the whole feature rests on (design §3). A caller
// cannot ask for an unvalidated candidate, because there is no parameter that
// would let them.
//
// It never writes to disk, never applies anything, and never touches a
// pipeline store — generation's only side effect is the provider call.
func Generate(ctx context.Context, in Input) (Generation, error) {
	if in.Provider == nil {
		return Generation{}, conduiterr.New(CodeParseFailed, "no provider configured for generation")
	}

	cat := in.Catalog
	if cat == nil {
		cat = BuiltinCatalog()
	}
	names := CatalogNames(cat)
	system := BuildSystemPrompt(cat)

	attempts := in.MaxAttempts
	switch {
	case attempts == 0:
		attempts = DefaultMaxAttempts
	case attempts < 1:
		attempts = 1
	}

	var res Generation
	var feedback RetryFeedback

	for n := 1; n <= attempts; n++ {
		completion, err := in.Provider.Complete(ctx, provider.CompletionRequest{
			System: system,
			Prompt: promptWithFeedback(in.Prompt, feedback),
			Model:  in.Model,
		})
		if err != nil {
			// A provider failure is not a candidate problem: it carries the
			// provider's own code (Unavailable/environment) and retrying it
			// here would burn the budget on an unreachable endpoint.
			return res, err
		}

		att := Attempt{N: n, TokensUsed: completion.TokensUsed}
		res.TokensUsed += completion.TokensUsed

		candidate, parseErr := extractCandidate(completion.Text)
		if parseErr != nil {
			att.ParseErr = parseErr
			feedback = FeedbackFromParseError(parseErr)
			att.Feedback = feedback
			res.Attempts = append(res.Attempts, att)
			continue
		}

		att.Candidate = candidate
		res.Candidate = candidate

		report := validate.RunBytes(ctx, InMemoryCandidateName, []byte(candidate), candidateValidateOptions())
		att.Report = report
		res.Report = report

		if report.OK() {
			res.Attempts = append(res.Attempts, att)
			return res, nil
		}

		feedback = FeedbackFromReport(report).
			WithConnectorSuggestions(unknownPlugins(ctx, candidate, names), names)
		att.Feedback = feedback
		res.Attempts = append(res.Attempts, att)
	}

	return res, exhaustedError(res)
}

// candidateValidateOptions is the gate every candidate passes through.
//
// ResolvePlugins is ON, unlike `pipelines validate`'s default. Plain schema
// validation accepts `plugin: "builtin:postgre"` — nothing in the zero
// Options checks that a referenced builtin plugin EXISTS. For a hand-written
// file that is a reasonable default (the run will fail loudly), but here the
// text was produced by a model, and a fabricated-but-plausible plugin name is
// the exact failure this feature must never ship to a user. Turning the check
// on is also what turns an invented name into a did-you-mean correction
// inside the retry budget rather than a runtime surprise.
//
// Warnings stay off: they do not gate, and feeding advisory findings back
// would spend retries on things that are not errors.
func candidateValidateOptions() validate.Options {
	return validate.Options{
		ResolvePlugins:    true,
		BuiltinPlugins:    BuiltinConnectorRefs(),
		BuiltinProcessors: BuiltinProcessorRefs(),
	}
}

// InMemoryCandidateName labels findings for a candidate that has no file yet.
// It is what a user sees in a validate finding's location, so it says what the
// thing is rather than naming a path that does not exist.
const InMemoryCandidateName = "generated pipeline"

// promptWithFeedback appends the previous attempt's correction to the user's
// prompt.
//
// The user's own text always comes first and verbatim; feedback is appended
// below it, rendered exclusively from RetryFeedback's typed fields (never raw
// report prose or raw candidate text — see feedback.go).
func promptWithFeedback(prompt string, fb RetryFeedback) string {
	rendered := fb.Render()
	if rendered == "" {
		return prompt
	}
	return prompt + "\n\n" + rendered
}

// exhaustedError classifies a run that never produced a passing candidate.
//
// The distinction is the user's next action: parse_failed means the boundary
// never returned anything readable (retry, or try another provider/model),
// while validate_failed means there IS an artifact with specific findings to
// look at — so the two carry different codes and different exit buckets.
func exhaustedError(res Generation) error {
	if res.Candidate == "" {
		e := conduiterr.New(CodeParseFailed,
			"the provider never returned a usable pipeline configuration")
		e.Suggestion = "retry, or select a different model with --model"
		return e
	}

	e := conduiterr.New(CodeValidateFailed,
		"the generated pipeline did not pass validation within the attempt budget")
	e.Suggestion = "see the validation findings, adjust the request, or edit the generated configuration directly"
	return e
}

// unknownPlugins returns every builtin plugin reference in candidate whose
// name is not in the catalog, sorted and deduplicated.
//
// Only builtin references are judged. A "standalone:" reference names a
// plugin installed separately, which this catalog cannot know about and must
// not call unknown — reporting it would teach the retry to replace a
// perfectly legal reference with a built-in one.
//
// A candidate that does not parse yields no unknowns: the parse failure is
// already the feedback, and guessing at plugin names inside unparseable text
// would be inventing findings.
func unknownPlugins(ctx context.Context, candidate string, names []string) []string {
	pipelines, err := yamlparser.NewParser(log.Nop()).Parse(ctx, strings.NewReader(candidate))
	if err != nil {
		return nil
	}

	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}

	seen := make(map[string]bool)
	var out []string
	for _, p := range pipelines {
		for _, c := range p.Connectors {
			name := builtinCategory(c.Plugin)
			if name == "" || known[name] || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
