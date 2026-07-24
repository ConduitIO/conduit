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

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
)

// Candidates maps a Request.ID to the full pipeline-config YAML text
// produced for it — by a live provider call once `generate` is built
// (v0.20), or by a fixture in this package's own tests today. This harness
// never produces a Candidates value itself; it only scores one.
type Candidates map[string]string

// Result is one Request's outcome within a single ScoreRun pass.
type Result struct {
	RequestID string

	// Missing is true when the Candidates map this Result came from had no
	// entry for RequestID. A missing candidate counts as a FAIL on both
	// metrics (see ScoreRun) — it is never excluded from the denominator.
	Missing bool

	ValidatePass   bool
	ValidateReport validate.Report
	// ValidateErr is non-nil only when validateCandidate itself could not
	// run (e.g. a temp-file I/O failure) — distinct from the candidate
	// simply failing validation, which shows up as findings inside
	// ValidateReport with ValidatePass false and ValidateErr nil.
	ValidateErr error

	SemanticMatch  bool
	SemanticIssues []string
}

// RunScore is the result of scoring every Request in a corpus against one
// Candidates mapping ("one run" — see ScoreMedian for repeats).
type RunScore struct {
	Total              int
	ValidatePassCount  int
	SemanticMatchCount int
	ValidatePassRate   float64
	SemanticMatchRate  float64
	Results            []Result
}

// ScoreRun scores one full pass: every request in requests is looked up in
// candidates and scored on both axes independently. A request.ID absent
// from candidates is recorded as Result.Missing and counts as a fail on
// BOTH metrics — never silently dropped from the denominator (see doc.go's
// invariants). requests order is preserved in RunScore.Results.
func ScoreRun(ctx context.Context, requests []Request, candidates Candidates) RunScore {
	rs := RunScore{Total: len(requests), Results: make([]Result, 0, len(requests))}

	for _, req := range requests {
		res := Result{RequestID: req.ID}

		candidate, ok := candidates[req.ID]
		if !ok {
			res.Missing = true
			res.SemanticIssues = []string{"no candidate provided for this request"}
		} else {
			report, err := validateCandidate(ctx, candidate)
			res.ValidateReport = report
			res.ValidateErr = err
			res.ValidatePass = err == nil && report.OK()

			sem := scoreSemantic(ctx, req.Expect, candidate)
			res.SemanticMatch = sem.Match
			res.SemanticIssues = sem.Issues
		}

		if res.ValidatePass {
			rs.ValidatePassCount++
		}
		if res.SemanticMatch {
			rs.SemanticMatchCount++
		}
		rs.Results = append(rs.Results, res)
	}

	if rs.Total > 0 {
		rs.ValidatePassRate = float64(rs.ValidatePassCount) / float64(rs.Total)
		rs.SemanticMatchRate = float64(rs.SemanticMatchCount) / float64(rs.Total)
	}
	return rs
}

// MedianScore is the result of scoring N repeated runs over the same
// requests (e.g. N separate generations against a live provider, per
// design doc §10's "re-run >= 3x each") and reporting the MEDIAN — never
// the best-of, per CLAUDE.md's benchmarking discipline ("report medians
// with variance, not best runs") applied here to model-output scoring
// instead of throughput numbers.
type MedianScore struct {
	Runs              []RunScore
	ValidatePassRate  float64
	SemanticMatchRate float64
}

// ScoreMedian calls ScoreRun once per element of runs and reduces the two
// rates to their median across runs. Every individual RunScore is kept in
// MedianScore.Runs for detail (e.g. rendering into docs/generate-benchmark.md
// or a scheduled-CI artifact) — the median alone is not enough context to
// debug a regression.
func ScoreMedian(ctx context.Context, requests []Request, runs []Candidates) MedianScore {
	ms := MedianScore{Runs: make([]RunScore, len(runs))}
	validateRates := make([]float64, len(runs))
	semanticRates := make([]float64, len(runs))

	for i, c := range runs {
		rs := ScoreRun(ctx, requests, c)
		ms.Runs[i] = rs
		validateRates[i] = rs.ValidatePassRate
		semanticRates[i] = rs.SemanticMatchRate
	}

	ms.ValidatePassRate = median(validateRates)
	ms.SemanticMatchRate = median(semanticRates)
	return ms
}

// median returns the middle value of vals (average of the two middle
// values for an even-length input), without mutating the caller's slice.
// median(nil) is 0 — ScoreMedian never calls it with an empty runs slice
// from its own tests, but a defensive zero is safer than a panic for a
// function that will eventually be fed live, possibly-empty data.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
