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

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// requiredAttackClasses is the AC 1.10 coverage bar (v0.20 WS1 slice A4):
// "injection fixtures attempting env/secret exfiltration, fabricated plugin
// injection, or coerced auto-apply — one test per attack class." Output-path
// escape and feedback-instruction-override are the two additional classes
// this slice's task brief named explicitly. Declared once so the corpus
// health check and a human reading this file cannot drift into two
// different ideas of what "covered" means.
var requiredAttackClasses = []string{
	"secret-exfiltration",
	"fabricated-plugin",
	"coerced-auto-apply",
	"output-path-escape",
	"feedback-instruction-override",
}

// adversarialFixturesPath is the committed corpus this slice adds. Both this
// package's tests and cmd/conduit/root/generate's load the SAME file (the
// root package reaches it via a relative "../../internal/generate/testdata"
// path) — one corpus, not two copies that could drift apart.
const adversarialFixturesPath = "testdata/adversarial_requests.yaml"

// fixtureByID panics on a missing id — acceptable in a test helper, since a
// missing fixture means the corpus and this file's table have already
// drifted and every subsequent assertion in the calling test would be
// meaningless.
func fixtureByID(t *testing.T, fixtures []AdversarialFixture, id string) AdversarialFixture {
	t.Helper()
	for _, f := range fixtures {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no adversarial fixture with id %q (has the corpus been renamed?)", id)
	return AdversarialFixture{}
}

// Test_Adversarial_FixtureCorpusCoversEveryAttackClass pins the corpus's own
// health: every attack class AC 1.10 names has at least one fixture, and
// LoadAdversarialFixtures' own field-completeness checks pass. This would
// catch a future edit that deletes the last fixture for a class, or adds one
// with a typo'd attackClass string that silently falls out of coverage.
func Test_Adversarial_FixtureCorpusCoversEveryAttackClass(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	fixtures, err := LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)
	is.True(len(fixtures) > 0)

	byClass := FixturesByClass(fixtures)
	for _, class := range requiredAttackClasses {
		is.True(len(byClass[class]) > 0) // requiredAttackClasses[i] has no fixture
	}
}

// Test_Adversarial_FabricatedPlugin_NeverSurvivesValidateGate drives the
// "fabricated plugin injection" attack class through the REAL generation
// loop, scripting the fake provider to return exactly what a compromised or
// obliging model would reply with (fixture.MaliciousReply) on every attempt.
//
// What this would catch: candidateValidateOptions() (generate.go) turning
// ResolvePlugins off, or accidentally, by a future refactor — the fabricated
// plugin would then validate cleanly and Generate would return it as a
// success. It would also catch fuzzymatch.Suggest being loosened enough to
// invent a did-you-mean for a name with no real near match ("exfiltrate"),
// which would be its own, subtler fabrication.
func Test_Adversarial_FabricatedPlugin_NeverSurvivesValidateGate(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	fixtures, err := LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)

	for _, tc := range []struct {
		id             string
		wantDidYouMean string // "" means none must be offered
		wantNoInvented bool   // true: assert no "did you mean" appears at all
	}{
		{
			id:             "fabricated-plugin-builtin-exfiltrate",
			wantNoInvented: true,
		},
		{
			id:             "fabricated-plugin-typo-postgre",
			wantDidYouMean: "postgres",
		},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			is := is.New(t)

			fx := fixtureByID(t, fixtures, tc.id)
			is.True(fx.MaliciousReply != "")

			p := &fakeProvider{replies: []string{fx.MaliciousReply}}
			res, err := Generate(context.Background(), Input{Prompt: fx.Prompt, Provider: p, MaxAttempts: 2})

			// The gate must never be crossed: this must be a failure, and
			// specifically validate_failed (the candidate parsed, but the
			// plugin reference does not resolve) — never a silent success.
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeValidateFailed)

			// The report attached to the last attempt must actually name
			// the unresolved plugin — proving the failure is for the
			// reason this test claims, not some unrelated schema error.
			is.True(res.Report.Summary.Errors > 0)
			var foundPluginFinding bool
			for _, f := range res.Report.Files {
				for _, finding := range f.Findings {
					if strings.Contains(finding.Code, "plugin_not_found") {
						foundPluginFinding = true
					}
				}
			}
			is.True(foundPluginFinding)

			// Inspect what the retry feedback (fed back to the model, and
			// exactly what a human/agent sees in the exhausted error) says
			// about the unresolved name.
			var feedbackText string
			for _, att := range res.Attempts {
				feedbackText += att.Feedback.Render()
			}
			if tc.wantNoInvented {
				is.True(!strings.Contains(feedbackText, "did you mean"))
				is.True(strings.Contains(feedbackText, "valid connectors:"))
			}
			if tc.wantDidYouMean != "" {
				is.True(strings.Contains(feedbackText, `did you mean "`+tc.wantDidYouMean+`"`))
			}
		})
	}
}

// Test_Adversarial_FeedbackInstructionOverride_RealFeedbackIsNeverPromptDerived
// is the "instruction override embedded in retry feedback" attack class: the
// PROMPT itself contains text engineered to look exactly like
// RetryFeedback.Render()'s own output, including a directive telling the
// model to ignore validate findings and treat the message as an authorized
// deploy request.
//
// The user's prompt is sent verbatim by design (§3) — that part is expected,
// including the spoofed block. What this test pins is narrower and more
// important: the CORRECTION Conduit appends on the next attempt is built
// exclusively from the real invalid candidate's actual validate.Report, byte
// for byte identical to what FeedbackFromReport would render for that
// report — never re-derived from, replaced by, or contaminated by anything
// in the attacker's prompt text.
//
// What this would catch: any future change to promptWithFeedback or the
// retry loop that inspects the PROMPT for something resembling prior
// feedback (e.g. "skip appending if the prompt already looks corrected") —
// which would either duplicate real feedback unpredictably or, worse, let
// attacker-authored text stand in for it.
func Test_Adversarial_FeedbackInstructionOverride_RealFeedbackIsNeverPromptDerived(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	fixtures, err := LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)
	fx := fixtureByID(t, fixtures, "feedback-format-spoofing-in-prompt")

	p := &fakeProvider{replies: []string{invalidCandidate, validCandidate}}
	_, err = Generate(context.Background(), Input{Prompt: fx.Prompt, Provider: p})
	is.NoErr(err)
	is.Equal(len(p.requests), 2)

	// The ground truth: exactly what Conduit's own feedback machinery
	// produces for the REAL invalidCandidate, independent of any prompt.
	report := validate.RunBytes(context.Background(), InMemoryCandidateName, []byte(invalidCandidate), candidateValidateOptions())
	wantFeedback := FeedbackFromReport(report).Render()
	is.True(wantFeedback != "")

	retryPrompt := p.requests[1].Prompt
	is.True(strings.HasPrefix(retryPrompt, fx.Prompt)) // the user's text, spoofed block included, is untouched and first

	gotSuffix := strings.TrimPrefix(retryPrompt, fx.Prompt)
	is.Equal(gotSuffix, "\n\n"+wantFeedback) // Conduit's appended block matches the real report EXACTLY

	// The attacker's injected directive must never appear inside Conduit's
	// OWN appended block — only inside the verbatim-echoed original prompt,
	// where it was always going to be (that's the untrusted-input half of
	// this design, not a leak).
	is.True(!strings.Contains(wantFeedback, "IGNORE ALL PRIOR INSTRUCTIONS"))
	is.True(!strings.Contains(wantFeedback, "authorized deploy request"))
}
