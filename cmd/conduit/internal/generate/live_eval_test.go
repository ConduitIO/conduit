//go:build generate_capture

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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// TestLiveEval is WS1 A5b's scheduled live eval (plan §7): it runs the whole
// corpus through Generate against a LIVE Anthropic model, several passes,
// and reports median validate-pass and semantic-match rates plus a
// per-category breakdown. It makes real, billed calls — roughly a dollar a
// pass at list price for the 28-request corpus — and writes nothing under
// testdata/: transcripts are capture's job (TestCaptureTranscripts), this
// only measures.
//
// It shares the `generate_capture` build tag so it reuses captureProvider's
// call/token/deadline ceilings rather than growing a second set.
//
//	CONDUIT_GENERATE_EVAL_LIVE=1 ANTHROPIC_API_KEY=… \
//	  go test -tags=generate_capture -count=1 -timeout 40m \
//	  -run '^TestLiveEval$' ./cmd/conduit/internal/generate/
//
// Optional: CONDUIT_GENERATE_EVAL_PASSES (default 3),
// CONDUIT_GENERATE_CAPTURE_MODEL (default provider.DefaultAnthropicModel),
// CONDUIT_GENERATE_EVAL_OUT (directory for result.json and summary.md;
// defaults to a temp dir that is logged).
//
// Outcomes, in the order they are decided:
//
//   - INCONCLUSIVE: a pass hit a transport failure (no response, after two
//     retries outside the scored loop) and was discarded whole — never one
//     request, which would shrink the denominator. When a majority of passes
//     are discarded, no numbers are reported and the test fails with
//     INCONCLUSIVE: "could not measure" is not "got worse" (plan §8.3).
//   - BELOW_FLOOR: a median is under its committed floor. The test fails.
//   - PASS: both medians clear their floors.
//
// The report is written before the verdict fails the test, so a red run
// still carries its numbers.
func TestLiveEval(t *testing.T) {
	if strings.TrimSpace(os.Getenv(envLiveEval)) != "1" {
		t.Skipf("%s is not set to \"1\" — the live eval spends real API budget and is opt-in; "+
			"%s alone is never treated as consent", envLiveEval, provider.EnvAnthropicKey)
	}
	apiKey := strings.TrimSpace(os.Getenv(provider.EnvAnthropicKey))
	if apiKey == "" {
		t.Fatalf("%s=1 but %s is not set", envLiveEval, provider.EnvAnthropicKey)
	}
	passes, err := livePassCount(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := captureModel(os.Getenv)

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatalf("loading corpus: %v", err)
	}

	outDir := os.Getenv(envLiveEvalOut)
	if outDir == "" {
		outDir = t.TempDir()
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveEvalWallClock)
	defer cancel()

	base := &provider.Anthropic{APIKey: apiKey, Model: model}
	cp := &captureProvider{
		Provider: base,
		// Transport retries re-run a request from attempt 1, so the ceiling
		// allows for them; the absolute backstop still wins when smaller.
		maxCalls:  min(2*captureCallCeiling(passes, len(requests)), captureAbsoluteMaxCalls),
		maxTokens: captureAbsoluteMaxTokens,
	}

	started := time.Now().UTC()
	report := runLiveEval(ctx, t, requests, cp, model, passes, liveRetryBackoff)
	report.Provider = base.Name()
	report.Model = model
	report.StartedAt = started
	report.FinishedAt = time.Now().UTC()
	report.TotalCalls = cp.totalCalls()
	report.TotalTokensUsed = cp.totalTokens()
	report.EstimatedCostUSD = estimateCostUSD(report.TotalTokensUsed)

	writeLiveReport(t, outDir, report)
	t.Logf("live eval report written to %s", outDir)

	switch report.Verdict {
	case liveVerdictInconclusive:
		t.Fatalf("INCONCLUSIVE: %d of %d pass(es) discarded for transport failures — no numbers reported",
			report.DiscardedPasses, passes)
	case liveVerdictBelowFloor:
		t.Errorf("BELOW_FLOOR: median validate pass %s (floor %.0f%%), median semantic match %s (floor %.0f%%)",
			fmtRate(report.MedianValidatePass), floorValidatePass*100,
			fmtRate(report.MedianSemanticMatch), floorSemanticMatch*100)
	case liveVerdictPass:
		t.Logf("PASS: median validate pass %s, median semantic match %s",
			fmtRate(report.MedianValidatePass), fmtRate(report.MedianSemanticMatch))
	}
}

const (
	// envLiveEval must be exactly "1" — consent, separate from the key and
	// from capture's own CONDUIT_GENERATE_CAPTURE.
	envLiveEval       = "CONDUIT_GENERATE_EVAL_LIVE"
	envLiveEvalOut    = "CONDUIT_GENERATE_EVAL_OUT"
	envLiveEvalPasses = "CONDUIT_GENERATE_EVAL_PASSES"

	defaultLivePasses = 3
	liveEvalWallClock = 35 * time.Minute

	liveVerdictPass         = "PASS"
	liveVerdictBelowFloor   = "BELOW_FLOOR"
	liveVerdictInconclusive = "INCONCLUSIVE"
)

// liveRetryBackoff is the fixed wait before each transport retry. Fixed, not
// Retry-After driven: checkStatus discards response headers (plan §8.3).
var liveRetryBackoff = []time.Duration{10 * time.Second, 30 * time.Second}

func livePassCount(env provider.Env) (int, error) {
	v := strings.TrimSpace(env(envLiveEvalPasses))
	if v == "" {
		return defaultLivePasses, nil
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 || fmt.Sprint(n) != v {
		return 0, fmt.Errorf("%s=%q is not a positive integer", envLiveEvalPasses, v)
	}
	return n, nil
}

// liveReport is result.json's shape.
type liveReport struct {
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	TotalCalls       int       `json:"totalCalls"`
	TotalTokensUsed  int       `json:"totalTokensUsed"`
	EstimatedCostUSD float64   `json:"estimatedCostUSD"`

	Verdict         string `json:"verdict"`
	Passes          int    `json:"passes"`
	DiscardedPasses int    `json:"discardedPasses"`

	// Medians across kept passes; omitted when INCONCLUSIVE.
	MedianValidatePass  *float64 `json:"medianValidatePass,omitempty"`
	MedianSemanticMatch *float64 `json:"medianSemanticMatch,omitempty"`
	MedianGenerateOK    *float64 `json:"medianGenerateOK,omitempty"`

	// Pooled is every kept pass's records summarized together — the
	// per-category breakdown. Omitted when INCONCLUSIVE.
	Pooled *evalSummary `json:"pooled,omitempty"`

	PassResults []livePass `json:"passResults"`
}

// livePass is one pass over the corpus.
type livePass struct {
	N             int          `json:"n"`
	Discarded     bool         `json:"discarded"`
	DiscardReason string       `json:"discardReason,omitempty"`
	Summary       *evalSummary `json:"summary,omitempty"`
	Records       []evalRecord `json:"records,omitempty"`
}

// runLiveEval is TestLiveEval's core, separated so a fake provider can drive
// it (live_eval_internal_test.go): every pass, transport retries, discard,
// medians and verdict.
func runLiveEval(ctx context.Context, t *testing.T, requests []Request, p provider.Provider, model string, passes int, backoff []time.Duration) liveReport {
	t.Helper()
	rep := liveReport{Passes: passes}

	var kept []livePass
	for n := 1; n <= passes; n++ {
		lp := livePass{N: n}
		records := make([]evalRecord, 0, len(requests))
		for _, req := range requests {
			gen, genErr, transportErr := generateWithTransportRetry(ctx, t, req, p, model, backoff)
			if transportErr != nil {
				lp.Discarded = true
				lp.DiscardReason = fmt.Sprintf("%s: %s", req.ID, safeFailureReason(transportErr))
				t.Logf("pass %d discarded: %s", n, lp.DiscardReason)
				break
			}
			records = append(records, newEvalRecord(ctx, req, gen, genErr))
		}
		if !lp.Discarded {
			s := summarize(records)
			lp.Summary = &s
			lp.Records = records
			kept = append(kept, lp)
			t.Logf("pass %d: validate %d/%d, semantic %d/%d, generate OK %d/%d",
				n, s.ValidatePass, s.Total, s.SemanticMatch, s.Total, s.GenerateOK, s.Total)
		} else {
			rep.DiscardedPasses++
		}
		rep.PassResults = append(rep.PassResults, lp)
	}

	// A majority of passes must survive: with 3 passes, one kept pass is a
	// single sample, not a median.
	if 2*len(kept) <= passes {
		rep.Verdict = liveVerdictInconclusive
		return rep
	}

	var vRates, sRates, gRates []float64
	var pooled []evalRecord
	for _, lp := range kept {
		s := lp.Summary
		vRates = append(vRates, float64(s.ValidatePass)/float64(s.Total))
		sRates = append(sRates, float64(s.SemanticMatch)/float64(s.Total))
		gRates = append(gRates, float64(s.GenerateOK)/float64(s.Total))
		pooled = append(pooled, lp.Records...)
	}
	mv, ms, mg := median(vRates), median(sRates), median(gRates)
	rep.MedianValidatePass, rep.MedianSemanticMatch, rep.MedianGenerateOK = &mv, &ms, &mg
	ps := summarize(pooled)
	rep.Pooled = &ps

	if mv < floorValidatePass || ms < floorSemanticMatch {
		rep.Verdict = liveVerdictBelowFloor
	} else {
		rep.Verdict = liveVerdictPass
	}
	return rep
}

// generateWithTransportRetry runs Generate for req, re-running it after a
// transport failure (no response at all: network error, timeout, non-2xx)
// up to len(backoff) more times. It returns transportErr non-nil only when
// every try failed that way; the caller then discards the whole pass.
//
// Anything else is data and returned as genErr: a model that answered badly,
// a pre-call refusal of the prompt, or a response that arrived but could not
// be used (a refusal or empty completion, provider.IsUnusableResponse) —
// that last one is a billed answer from the model, not an outage.
//
// A tripped captureProvider ceiling or an expired deadline is also a
// transport failure: the run could not measure, and retrying would only trip
// it again, so it is not retried.
func generateWithTransportRetry(ctx context.Context, t *testing.T, req Request, p provider.Provider, model string, backoff []time.Duration) (Generation, error, error) {
	t.Helper()
	for try := 0; ; try++ {
		gen, err := Generate(ctx, Input{Prompt: req.Prompt, Provider: p, Model: model})
		if !isTransportFailure(err) {
			return gen, err, nil
		}
		if ctx.Err() != nil || !isRetryableTransport(err) || try >= len(backoff) {
			return gen, nil, err
		}
		t.Logf("%s: transport failure (%s), retrying in %s", req.ID, safeFailureReason(err), backoff[try])
		select {
		case <-time.After(backoff[try]):
		case <-ctx.Done():
			return gen, nil, ctx.Err()
		}
	}
}

// isTransportFailure reports whether err means "no usable response ever
// arrived" rather than "the model answered".
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	if cerrors.Is(err, context.DeadlineExceeded) || cerrors.Is(err, context.Canceled) {
		return true
	}
	ce, ok := conduiterr.Get(err)
	if !ok {
		// captureProvider's ceiling refusals are plain errors; Generate's
		// own loop failures are always coded.
		return true
	}
	return ce.Code == provider.CodeProviderError && !provider.IsUnusableResponse(err)
}

// isRetryableTransport is false for the failures a retry cannot fix.
func isRetryableTransport(err error) bool {
	if _, ok := conduiterr.Get(err); !ok {
		return false // a tripped ceiling
	}
	return !cerrors.Is(err, context.DeadlineExceeded) && !cerrors.Is(err, context.Canceled)
}

func fmtRate(r *float64) string {
	if r == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *r*100)
}

func writeLiveReport(t *testing.T, dir string, rep liveReport) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatalf("marshaling live report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("writing result.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(renderLiveSummary(rep)), 0o600); err != nil {
		t.Fatalf("writing summary.md: %v", err)
	}
}

func renderLiveSummary(rep liveReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## generate-eval (live): %s\n\n", rep.Verdict)
	fmt.Fprintf(&b, "`%s/%s`, %d pass(es), %d discarded, %d provider calls, %d tokens (~$%.2f at blended list price), %s to %s.\n\n",
		rep.Provider, rep.Model, rep.Passes, rep.DiscardedPasses, rep.TotalCalls, rep.TotalTokensUsed,
		rep.EstimatedCostUSD, rep.StartedAt.Format(time.RFC3339), rep.FinishedAt.Format(time.RFC3339))

	if rep.Verdict == liveVerdictInconclusive {
		b.WriteString("No numbers: a majority of passes were discarded for transport failures.\n\n")
	} else {
		b.WriteString("| Metric | Median | Floor | Per pass |\n| --- | ---: | ---: | --- |\n")
		var vs, ss, gs []string
		for _, lp := range rep.PassResults {
			if lp.Summary == nil {
				vs, ss, gs = append(vs, "discarded"), append(ss, "discarded"), append(gs, "discarded")
				continue
			}
			s := lp.Summary
			vs = append(vs, fmt.Sprintf("%d/%d", s.ValidatePass, s.Total))
			ss = append(ss, fmt.Sprintf("%d/%d", s.SemanticMatch, s.Total))
			gs = append(gs, fmt.Sprintf("%d/%d", s.GenerateOK, s.Total))
		}
		fmt.Fprintf(&b, "| Validate pass | %s | %.0f%% | %s |\n", fmtRate(rep.MedianValidatePass), floorValidatePass*100, strings.Join(vs, ", "))
		fmt.Fprintf(&b, "| Semantic match (corpus) | %s | %.0f%% | %s |\n", fmtRate(rep.MedianSemanticMatch), floorSemanticMatch*100, strings.Join(ss, ", "))
		fmt.Fprintf(&b, "| Generate OK (own verdict) | %s | — | %s |\n\n", fmtRate(rep.MedianGenerateOK), strings.Join(gs, ", "))

		b.WriteString("### By category (pooled over kept passes)\n\n")
		b.WriteString(renderCategoryTable(*rep.Pooled))
		b.WriteString("\n### By request\n\n")
		b.WriteString(renderPerRequestTable(rep))
	}

	for _, lp := range rep.PassResults {
		if lp.Discarded {
			fmt.Fprintf(&b, "\nPass %d discarded: %s\n", lp.N, lp.DiscardReason)
		}
	}
	return b.String()
}

// renderPerRequestTable shows, per request, how many kept passes cleared
// each axis, and the most common reason it did not.
func renderPerRequestTable(rep liveReport) string {
	type agg struct {
		n, v, s, g int
		issues     map[string]int
	}
	byID := map[string]*agg{}
	var order []string
	for _, lp := range rep.PassResults {
		for _, r := range lp.Records {
			a := byID[r.ID]
			if a == nil {
				a = &agg{issues: map[string]int{}}
				byID[r.ID] = a
				order = append(order, r.ID)
			}
			a.n++
			if r.ValidatePass {
				a.v++
			}
			if r.SemanticMatch {
				a.s++
			}
			if r.GenerateOK {
				a.g++
			}
			for _, is := range r.SemanticIssues {
				a.issues[is]++
			}
			for _, f := range r.ValidateFindings {
				a.issues["validate: "+f]++
			}
		}
	}

	var b strings.Builder
	b.WriteString("| Request | Validate | Semantic | Generate OK | Most common issue |\n| --- | ---: | ---: | ---: | --- |\n")
	for _, id := range order {
		a := byID[id]
		top := ""
		if len(a.issues) > 0 {
			keys := make([]string, 0, len(a.issues))
			for k := range a.issues {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				if a.issues[keys[i]] != a.issues[keys[j]] {
					return a.issues[keys[i]] > a.issues[keys[j]]
				}
				return keys[i] < keys[j]
			})
			top = strings.ReplaceAll(keys[0], "|", "\\|")
		}
		fmt.Fprintf(&b, "| %s | %d/%d | %d/%d | %d/%d | %s |\n", id, a.v, a.n, a.s, a.n, a.g, a.n, top)
	}
	return b.String()
}
