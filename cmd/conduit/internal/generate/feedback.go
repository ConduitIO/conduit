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
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/fuzzymatch"
)

// Feedback bounds, from design doc §3 ("The retry feedback must be bounded,
// not passed through raw").
//
// The retry loop feeds a failed candidate's findings back into the next
// prompt, and that text is partly derived from the MODEL'S OWN PRIOR OUTPUT —
// finding messages quote the config paths, connector IDs, and values the
// candidate contained. Unbounded, that is two hazards at once:
//
//	(a) prompt growth — a candidate with dozens of findings balloons every
//	    retry's payload;
//	(b) a channel by which model-originated text, including any injected
//	    instruction the first candidate echoed, re-enters the next prompt.
//
// So feedback is CONSTRUCTED FROM TYPED FIELDS, never concatenated report
// prose, and hard-bounded. Its job is to name what to fix in Conduit's own
// vocabulary — error code, config path, valid alternatives — not to relay a
// model-influenced blob back into the model.
//
// These are a testable requirement, not a soft guideline.
const (
	// MaxFeedbackFindings caps how many findings are fed back per retry.
	MaxFeedbackFindings = 10
	// MaxFeedbackValueLen truncates any single echoed value.
	MaxFeedbackValueLen = 120
)

// RetryFeedback is the bounded, structured correction handed to the next
// attempt. It is deliberately a struct of typed fields rather than a string:
// building the prompt text is Render's job and cannot be bypassed by a caller
// concatenating something richer.
type RetryFeedback struct {
	// Problem names the gate that failed, in fixed vocabulary.
	Problem string
	// Items are the bounded, per-finding corrections.
	Items []FeedbackItem
	// Truncated is how many findings were dropped by the cap, so the prompt
	// can say so rather than silently implying the list is complete.
	Truncated int
}

// FeedbackItem is one correction, rendered only from typed fields.
type FeedbackItem struct {
	Code       string
	ConfigPath string
	Suggestion string
}

// clip truncates a model-influenced value to MaxFeedbackValueLen and strips
// newlines.
//
// Newlines matter as much as length: a value containing "\n\nIgnore previous
// instructions" would otherwise render as its own line in the retry prompt,
// visually indistinguishable from the instructions this package wrote.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= MaxFeedbackValueLen {
		return s
	}
	return s[:MaxFeedbackValueLen] + "…"
}

// FeedbackFromReport builds bounded feedback from a failed validate pass.
//
// Only error-severity findings are fed back, capped, and each rendered from
// Code/ConfigPath/Suggestion — never the finding's free-form message, which is
// where candidate-derived text lives.
func FeedbackFromReport(rep validate.Report) RetryFeedback {
	fb := RetryFeedback{Problem: "the previous candidate failed validation"}

	var errs []validate.Finding
	for _, f := range rep.Files {
		for _, fi := range f.Findings {
			if fi.Severity == validate.SeverityError {
				errs = append(errs, fi)
			}
		}
	}

	// Deterministic order: the same failure must produce the same prompt, or
	// retries are unreproducible and the eval harness cannot measure anything.
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].ConfigPath != errs[j].ConfigPath {
			return errs[i].ConfigPath < errs[j].ConfigPath
		}
		return errs[i].Code < errs[j].Code
	})

	if len(errs) > MaxFeedbackFindings {
		fb.Truncated = len(errs) - MaxFeedbackFindings
		errs = errs[:MaxFeedbackFindings]
	}

	for _, f := range errs {
		fb.Items = append(fb.Items, FeedbackItem{
			Code:       clip(f.Code),
			ConfigPath: clip(f.ConfigPath),
			Suggestion: clip(f.Suggestion),
		})
	}

	return fb
}

// FeedbackFromParseError builds feedback for a candidate that did not parse.
//
// The parser's error text quotes the candidate, so it is clipped like any
// other model-derived value.
func FeedbackFromParseError(err error) RetryFeedback {
	return RetryFeedback{
		Problem: "the previous candidate was not valid pipeline YAML",
		Items:   []FeedbackItem{{Code: "config.parse_error", Suggestion: clip(err.Error())}},
	}
}

// FeedbackFromSemantic builds feedback for a candidate that validated but did
// not match the request's intent.
func FeedbackFromSemantic(res SemanticResult) RetryFeedback {
	fb := RetryFeedback{Problem: "the previous candidate was valid but did not match the request"}

	issues := res.Issues
	if len(issues) > MaxFeedbackFindings {
		fb.Truncated = len(issues) - MaxFeedbackFindings
		issues = issues[:MaxFeedbackFindings]
	}
	for _, i := range issues {
		fb.Items = append(fb.Items, FeedbackItem{Code: "generate.semantic_mismatch", Suggestion: clip(i)})
	}
	return fb
}

// WithConnectorSuggestions adds did-you-mean corrections for plugin references
// that do not exist.
//
// This is the retry loop's consumer of fuzzymatch (design §7): turning
// "connector `postgre` does not exist; did you mean `postgres`?" into a
// self-correction INSIDE the retry budget, rather than a terminal failure.
// Suggestions come only from the known catalog, so nothing model-originated is
// echoed as if it were a valid name.
func (f RetryFeedback) WithConnectorSuggestions(unknown []string, catalog []string) RetryFeedback {
	for _, u := range unknown {
		item := FeedbackItem{Code: "connector.plugin_not_found", ConfigPath: clip(u)}
		if s := fuzzymatch.Suggest(u, catalog, 1); len(s) > 0 {
			item.Suggestion = fmt.Sprintf("did you mean %q? valid connectors: %s", s[0], strings.Join(catalog, ", "))
		} else {
			item.Suggestion = "valid connectors: " + strings.Join(catalog, ", ")
		}
		f.Items = append(f.Items, item)
	}
	return f
}

// Render produces the corrective text appended to the next attempt's prompt.
//
// Every line is built here from typed fields. There is no path by which raw
// report prose or raw candidate text reaches the model.
func (f RetryFeedback) Render() string {
	if len(f.Items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(f.Problem)
	b.WriteString(". Fix these and return only the corrected pipeline YAML:\n")

	for _, it := range f.Items {
		b.WriteString("- ")
		b.WriteString(it.Code)
		if it.ConfigPath != "" {
			b.WriteString(" at ")
			b.WriteString(it.ConfigPath)
		}
		if it.Suggestion != "" {
			b.WriteString(": ")
			b.WriteString(it.Suggestion)
		}
		b.WriteString("\n")
	}

	if f.Truncated > 0 {
		// Say so rather than implying the list is complete — a model told
		// "fix these 10" when there are 30 will confidently return something
		// still broken.
		fmt.Fprintf(&b, "(%d further problems omitted; fix the listed ones first)\n", f.Truncated)
	}

	return b.String()
}
