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
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// This file holds the record and report shapes shared by the two eval runs:
// TestReplayEval (replay_eval_test.go, untagged, every PR, no network) and
// TestLiveEval (live_eval_test.go, `generate_capture` tag, scheduled, live
// provider). Both drive the same Generate loop and score with the same
// ScoreRun, so they must also describe a request's outcome the same way —
// one record type keeps a replay golden and a live report comparable field
// for field.

// The committed quality floors (design doc §10, docs/generate-benchmark.md).
// Medians at or above both are a pass. They are fixed here rather than read
// from config so that moving one is a reviewed diff, never a workflow input.
const (
	floorValidatePass  = 0.90
	floorSemanticMatch = 0.70
)

// evalRecord is one corpus request's outcome in one eval pass.
//
// Two verdicts are carried and never merged (plan F7): GenerateOK is
// Generate's OWN end-to-end verdict (it returned a candidate, which means it
// passed validate and Generate's prompt-derived intent judge), while
// ValidatePass/SemanticMatch are the CORPUS verdict — ScoreRun against the
// request's committed Expect. They can disagree: the intent judge reads only
// the prompt, the corpus reads ground truth.
type evalRecord struct {
	ID                  string   `json:"id"`
	SourceCategory      string   `json:"sourceCategory"`
	DestinationCategory string   `json:"destinationCategory"`
	Capabilities        []string `json:"capabilities,omitempty"`

	// Tombstoned is true when capture committed "<id>.missing.yaml" for this
	// id: no completion was ever recorded. It scores as a fail on every axis,
	// matching ScoreRun's treatment of a missing candidate — the denominator
	// never shrinks.
	Tombstoned bool `json:"tombstoned,omitempty"`

	Attempts     int    `json:"attempts"`
	TokensUsed   int    `json:"tokensUsed"`
	GenerateOK   bool   `json:"generateOK"`
	GenerateCode string `json:"generateCode,omitempty"`

	ValidatePass     bool     `json:"validatePass"`
	SemanticMatch    bool     `json:"semanticMatch"`
	SemanticIssues   []string `json:"semanticIssues,omitempty"`
	ValidateFindings []string `json:"validateFindings,omitempty"`
}

// newEvalRecord builds req's record from one Generate call and scores the
// candidate it left behind (the validated one on success, the last one on
// failure — Generation's own contract) through ScoreRun, so the corpus
// verdict is computed exactly the way capture and every other caller compute
// it. An empty candidate is scored by ScoreRun as a fail on both axes.
func newEvalRecord(ctx context.Context, req Request, gen Generation, genErr error) evalRecord {
	rec := evalRecord{
		ID:                  req.ID,
		SourceCategory:      req.Expect.SourceCategory,
		DestinationCategory: req.Expect.DestinationCategory,
		Capabilities:        req.Expect.RequiredCapabilities,
		Attempts:            len(gen.Attempts),
		TokensUsed:          gen.TokensUsed,
		GenerateOK:          genErr == nil,
	}
	if genErr != nil {
		rec.GenerateCode = errorCode(genErr)
	}

	rs := ScoreRun(ctx, []Request{req}, Candidates{req.ID: gen.Candidate})
	res := rs.Results[0]
	rec.ValidatePass = res.ValidatePass
	rec.SemanticMatch = res.SemanticMatch
	if !res.SemanticMatch {
		rec.SemanticIssues = res.SemanticIssues
	}
	rec.ValidateFindings = errorFindings(res.ValidateReport)
	return rec
}

// tombstonedRecord is the record for an id capture could not record at all.
func tombstonedRecord(req Request, ts Tombstone) evalRecord {
	return evalRecord{
		ID:                  req.ID,
		SourceCategory:      req.Expect.SourceCategory,
		DestinationCategory: req.Expect.DestinationCategory,
		Capabilities:        req.Expect.RequiredCapabilities,
		Tombstoned:          true,
		GenerateCode:        ts.FailureCode,
		SemanticIssues:      []string{"no completion was recorded at capture time"},
	}
}

// errorCode is the stable conduiterr reason for err, or "uncoded" for an
// error that carries none. Never err.Error(): a message can carry provider
// text, and the code is what agents and this report branch on.
func errorCode(err error) string {
	if ce, ok := conduiterr.Get(err); ok {
		return ce.Code.Reason()
	}
	return "uncoded"
}

// errorFindings renders a report's error-severity findings as
// "code @ configPath" — enough to see WHAT failed in a golden diff without
// pinning message wording, which validate is free to improve.
func errorFindings(r validate.Report) []string {
	var out []string
	for _, f := range r.Files {
		for _, fd := range f.Findings {
			if fd.Severity != validate.SeverityError {
				continue
			}
			s := fd.Code
			if fd.ConfigPath != "" {
				s += " @ " + fd.ConfigPath
			}
			out = append(out, s)
		}
	}
	return out
}

// evalSummary is the count-only rollup of one pass's records. Counts, never
// rates: a 28-request corpus makes every percentage a cliff (plan F11), and
// integers make a golden byte-stable with no float formatting in the loop.
type evalSummary struct {
	Total         int             `json:"total"`
	ValidatePass  int             `json:"validatePass"`
	SemanticMatch int             `json:"semanticMatch"`
	GenerateOK    int             `json:"generateOK"`
	Tombstoned    int             `json:"tombstoned"`
	ByCategory    []categoryScore `json:"byCategory"`
}

// categoryScore is one slice of the corpus: every request whose source,
// destination, or required capability is Value.
type categoryScore struct {
	Dimension     string `json:"dimension"`
	Value         string `json:"value"`
	Total         int    `json:"total"`
	ValidatePass  int    `json:"validatePass"`
	SemanticMatch int    `json:"semanticMatch"`
	GenerateOK    int    `json:"generateOK"`
}

// categoryDimensions is the fixed order slices are reported in. A request
// with no required capability lands under capability "none", so every
// dimension's totals sum to the corpus size.
var categoryDimensions = []string{roleSource, roleDestination, "capability"}

func recordCategories(r evalRecord) map[string][]string {
	caps := r.Capabilities
	if len(caps) == 0 {
		caps = []string{"none"}
	}
	return map[string][]string{
		roleSource:      {r.SourceCategory},
		roleDestination: {r.DestinationCategory},
		"capability":    caps,
	}
}

// summarize rolls records up. Pooling across several passes is just calling
// it on their concatenated records.
func summarize(records []evalRecord) evalSummary {
	s := evalSummary{Total: len(records)}
	type key struct{ dim, val string }
	slices := map[key]*categoryScore{}

	for _, r := range records {
		if r.ValidatePass {
			s.ValidatePass++
		}
		if r.SemanticMatch {
			s.SemanticMatch++
		}
		if r.GenerateOK {
			s.GenerateOK++
		}
		if r.Tombstoned {
			s.Tombstoned++
		}
		for dim, vals := range recordCategories(r) {
			for _, v := range vals {
				k := key{dim, v}
				cs := slices[k]
				if cs == nil {
					cs = &categoryScore{Dimension: dim, Value: v}
					slices[k] = cs
				}
				cs.Total++
				if r.ValidatePass {
					cs.ValidatePass++
				}
				if r.SemanticMatch {
					cs.SemanticMatch++
				}
				if r.GenerateOK {
					cs.GenerateOK++
				}
			}
		}
	}

	rank := map[string]int{}
	for i, d := range categoryDimensions {
		rank[d] = i
	}
	s.ByCategory = make([]categoryScore, 0, len(slices))
	for _, cs := range slices {
		s.ByCategory = append(s.ByCategory, *cs)
	}
	sort.Slice(s.ByCategory, func(i, j int) bool {
		a, b := s.ByCategory[i], s.ByCategory[j]
		if a.Dimension != b.Dimension {
			return rank[a.Dimension] < rank[b.Dimension]
		}
		return a.Value < b.Value
	})
	return s
}

// pct renders n/d as a percentage with one decimal, and "n/a" for d == 0.
func pct(n, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}

// renderCategoryTable renders s.ByCategory as a markdown table, counts and
// percentages side by side.
func renderCategoryTable(s evalSummary) string {
	var b strings.Builder
	b.WriteString("| Dimension | Value | Requests | Validate pass | Semantic match | Generate OK |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: |\n")
	for _, c := range s.ByCategory {
		fmt.Fprintf(&b, "| %s | %s | %d | %d (%s) | %d (%s) | %d (%s) |\n",
			c.Dimension, c.Value, c.Total,
			c.ValidatePass, pct(c.ValidatePass, c.Total),
			c.SemanticMatch, pct(c.SemanticMatch, c.Total),
			c.GenerateOK, pct(c.GenerateOK, c.Total))
	}
	return b.String()
}
