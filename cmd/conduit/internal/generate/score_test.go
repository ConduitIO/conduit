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
	"os"
	"testing"

	"github.com/matryer/is"
)

// This file proves the scorer is correct WITHOUT a live LLM: every case
// feeds a known-good or known-bad candidate pipeline YAML (committed under
// testdata/candidates) and asserts the exact score, never invoking a
// provider. See doc.go's invariants.

// mustReadCandidate loads a fixture candidate YAML by filename (relative to
// testdata/candidates/) as a string, failing the test on any read error.
func mustReadCandidate(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/candidates/" + name)
	if err != nil {
		t.Fatalf("reading fixture candidate %q: %v", name, err)
	}
	return string(data)
}

// findRequest returns the Request with the given id from requests, failing
// the test if it's not found — keeps every test below anchored to the real
// committed corpus (testdata/eval_requests.yaml) instead of a hand-rolled
// duplicate Expect that could silently drift from it.
func findRequest(t *testing.T, requests []Request, id string) Request {
	t.Helper()
	for _, r := range requests {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no fixture request with id %q in the committed corpus", id)
	return Request{}
}

func TestLoadRequests_CommittedCorpus(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)

	// Task brief AC: >= 25 canonical requests.
	is.True(len(requests) >= 25)

	seen := make(map[string]bool, len(requests))
	for _, r := range requests {
		is.True(r.ID != "")
		is.True(r.Prompt != "")
		is.True(r.Expect.SourceCategory != "")      // every committed fixture pins a source...
		is.True(r.Expect.DestinationCategory != "") // ...and a destination (task brief: "grounded in real built-in connectors")
		is.True(!seen[r.ID])                        // no duplicate ids
		seen[r.ID] = true

		// Every category used must be one of the six real built-in
		// connectors named in doc.go — never a hypothetical one.
		is.True(builtinConnectorCategories[r.Expect.SourceCategory])
		is.True(builtinConnectorCategories[r.Expect.DestinationCategory])
	}
}

func TestLoadRequests_RejectsDuplicateID(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	path := dir + "/dup.yaml"
	is.NoErr(os.WriteFile(path, []byte(`
- id: same-id
  prompt: "a"
  expect: {sourceCategory: file, destinationCategory: log}
- id: same-id
  prompt: "b"
  expect: {sourceCategory: file, destinationCategory: log}
`), 0o600))

	_, err := LoadRequests(path)
	is.True(err != nil)
}

func TestLoadRequests_RejectsMissingID(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	path := dir + "/missing-id.yaml"
	is.NoErr(os.WriteFile(path, []byte(`
- prompt: "no id here"
  expect: {sourceCategory: file, destinationCategory: log}
`), 0o600))

	_, err := LoadRequests(path)
	is.True(err != nil)
}

// builtinConnectorCategories is the test's own independent list of the six
// real built-in connectors (Context, task brief) — deliberately NOT
// imported from capability.go/semantic_score.go, so this test would catch
// the corpus drifting to reference a category neither this list nor the
// production code recognizes.
var builtinConnectorCategories = map[string]bool{
	"file": true, "generator": true, "kafka": true,
	"log": true, "postgres": true, "s3": true,
}

// --- validate-pass axis ---

func TestValidateCandidate_GoodCandidate_Passes(t *testing.T) {
	is := is.New(t)

	candidate := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-good.yaml")
	report, err := validateCandidate(context.Background(), candidate)
	is.NoErr(err)
	is.True(report.OK())
}

func TestValidateCandidate_BadSchema_Fails(t *testing.T) {
	is := is.New(t)

	candidate := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-bad-schema.yaml")
	report, err := validateCandidate(context.Background(), candidate)
	is.NoErr(err) // validateCandidate itself succeeds; the CANDIDATE fails validation
	is.True(!report.OK())
	is.True(report.Summary.Errors > 0)
}

// A schema-valid-but-semantically-wrong candidate still passes validate —
// this is exactly the DX-review failure mode (design doc: "a pipeline can
// validate and confidently do the wrong thing") the semantic axis below
// exists to catch.
func TestValidateCandidate_WrongConnectorButValid_Passes(t *testing.T) {
	is := is.New(t)

	candidate := mustReadCandidate(t, "kafka-to-postgres-sync-bad-wrong-connector.yaml")
	report, err := validateCandidate(context.Background(), candidate)
	is.NoErr(err)
	is.True(report.OK())
}

// --- semantic-intent axis ---

func TestScoreSemantic_GoodCandidate_Matches(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")

	candidate := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-good.yaml")
	res := scoreSemantic(context.Background(), req.Expect, candidate)
	is.True(res.Match)
	is.Equal(len(res.Issues), 0)
}

func TestScoreSemantic_NoCapabilitiesNeeded_Matches(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "generator-to-log-smoketest")

	candidate := mustReadCandidate(t, "generator-to-log-smoketest-good.yaml")
	res := scoreSemantic(context.Background(), req.Expect, candidate)
	is.True(res.Match)
}

func TestScoreSemantic_WrongConnector_FailsEvenThoughValid(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "kafka-to-postgres-sync")

	candidate := mustReadCandidate(t, "kafka-to-postgres-sync-bad-wrong-connector.yaml")

	// Confirm the premise: this candidate DOES pass validate.
	report, verr := validateCandidate(context.Background(), candidate)
	is.NoErr(verr)
	is.True(report.OK())

	// But it must fail the semantic check: wrong source AND destination.
	res := scoreSemantic(context.Background(), req.Expect, candidate)
	is.True(!res.Match)
	is.True(len(res.Issues) >= 2)
}

// A candidate with two connectors in the same role is malformed input the
// checker must never resolve by guessing which one "the" source was — it
// should report an unresolvable (empty) category for that role, which
// mismatches any non-empty Expect.
func TestScoreSemantic_MultipleSourceConnectors_NeverGuessesACategory(t *testing.T) {
	is := is.New(t)

	candidate := `
version: "2.2"
pipelines:
  - id: two-sources
    status: running
    connectors:
      - id: src1
        type: source
        plugin: builtin:postgres
      - id: src2
        type: source
        plugin: builtin:kafka
      - id: dst
        type: destination
        plugin: builtin:s3
`
	res := scoreSemantic(context.Background(), Expect{SourceCategory: "postgres", DestinationCategory: "s3"}, candidate)
	is.True(!res.Match)
}

func TestScoreSemantic_DroppedFilter_Fails(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")

	candidate := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-bad-dropped-filter.yaml")

	report, verr := validateCandidate(context.Background(), candidate)
	is.NoErr(verr)
	is.True(report.OK()) // still schema-valid

	res := scoreSemantic(context.Background(), req.Expect, candidate)
	is.True(!res.Match)
	is.Equal(len(res.Issues), 1)
}

func TestScoreSemantic_SwappedDirection_Fails(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")

	candidate := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-bad-swapped-direction.yaml")

	report, verr := validateCandidate(context.Background(), candidate)
	is.NoErr(verr)
	is.True(report.OK())

	res := scoreSemantic(context.Background(), req.Expect, candidate)
	is.True(!res.Match) // source/destination categories are swapped relative to Expect
}

func TestScoreSemantic_UnknownPlugin_NeverFabricatesAMatch(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")

	candidate := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-bad-unknown-plugin.yaml")

	// A non-builtin plugin reference is schema-valid (validate doesn't
	// resolve plugin registries without ResolvePlugins).
	report, verr := validateCandidate(context.Background(), candidate)
	is.NoErr(verr)
	is.True(report.OK())

	res := scoreSemantic(context.Background(), req.Expect, candidate)
	is.True(!res.Match) // an unresolvable source category never counts as a match
}

func TestScoreSemantic_MalformedYAML_NeverPanicsNeverMatches(t *testing.T) {
	is := is.New(t)

	res := scoreSemantic(context.Background(), Expect{SourceCategory: "postgres", DestinationCategory: "kafka"}, "not: [valid: yaml: at all")
	is.True(!res.Match)
	is.True(len(res.Issues) >= 1)
}

// --- capability.go ---

func TestHasCapability_UnknownTag_NeverMatches(t *testing.T) {
	is := is.New(t)
	is.True(!hasCapability(nil, "this-tag-does-not-exist"))
}

// --- ScoreRun / ScoreMedian ---

func TestScoreRun_MissingCandidate_FailsBothMetrics(t *testing.T) {
	is := is.New(t)

	requests := []Request{
		{ID: "a", Prompt: "x", Expect: Expect{SourceCategory: "file", DestinationCategory: "log"}},
	}
	rs := ScoreRun(context.Background(), requests, Candidates{}) // no entry for "a"

	is.Equal(rs.Total, 1)
	is.Equal(rs.ValidatePassCount, 0)
	is.Equal(rs.SemanticMatchCount, 0)
	is.True(rs.Results[0].Missing)
	is.Equal(rs.ValidatePassRate, 0.0)
	is.Equal(rs.SemanticMatchRate, 0.0)
}

func TestScoreRun_MixedResults_ComputesRatesOverFullDenominator(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	good := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")
	other := findRequest(t, requests, "kafka-to-postgres-sync")

	candidates := Candidates{
		good.ID: mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-good.yaml"),
		// other.ID deliberately has no candidate: Missing, counts against
		// the denominator instead of shrinking it.
	}
	rs := ScoreRun(context.Background(), []Request{good, other}, candidates)

	is.Equal(rs.Total, 2)
	is.Equal(rs.ValidatePassCount, 1)
	is.Equal(rs.SemanticMatchCount, 1)
	is.Equal(rs.ValidatePassRate, 0.5)
	is.Equal(rs.SemanticMatchRate, 0.5)
}

func TestMedian_OddAndEvenLengths(t *testing.T) {
	is := is.New(t)

	// Values chosen to be exact in binary floating point (eighths), so the
	// assertion isn't sensitive to floating-point rounding.
	is.Equal(median([]float64{0.5, 1.0, 0.25}), 0.5)         // odd: sorted middle
	is.Equal(median([]float64{0.5, 1.0, 0.25, 0.75}), 0.625) // even: average of the two middles
	is.Equal(median(nil), 0.0)
}

// ScoreMedian must report the MEDIAN across runs, never the best run — a
// harness that could be gamed by cherry-picking its best attempt would not
// be a trustworthy release gate (CLAUDE.md's benchi discipline, applied to
// model-output scoring).
func TestScoreMedian_ReportsMedianNotBestOf(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")

	good := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-good.yaml")
	badSchema := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-bad-schema.yaml")

	oneReq := []Request{req}
	runs := []Candidates{
		{req.ID: good},      // run 1: pass (rate 1.0)
		{req.ID: good},      // run 2: pass (rate 1.0)
		{req.ID: badSchema}, // run 3: fail (rate 0.0)
	}

	ms := ScoreMedian(context.Background(), oneReq, runs)
	// Sorted rates: [0.0, 1.0, 1.0] -> median 1.0. The single failing run
	// must not drag the reported number below what the median actually is,
	// and a best-of reduction would trivially also read 1.0 here — the real
	// assertion is in the NEXT test, which forces median != best-of.
	is.Equal(ms.ValidatePassRate, 1.0)
	is.Equal(len(ms.Runs), 3)
}

func TestScoreMedian_DiffersFromBestOfWhenMajorityFails(t *testing.T) {
	is := is.New(t)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	is.NoErr(err)
	req := findRequest(t, requests, "postgres-cdc-to-kafka-filtered")

	good := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-good.yaml")
	badSchema := mustReadCandidate(t, "postgres-cdc-to-kafka-filtered-bad-schema.yaml")

	oneReq := []Request{req}
	runs := []Candidates{
		{req.ID: badSchema}, // fail (0.0)
		{req.ID: badSchema}, // fail (0.0)
		{req.ID: good},      // pass (1.0) -- the best run
	}

	ms := ScoreMedian(context.Background(), oneReq, runs)
	// Best-of would report 1.0; the median of [0.0, 0.0, 1.0] is 0.0.
	is.Equal(ms.ValidatePassRate, 0.0)
}
