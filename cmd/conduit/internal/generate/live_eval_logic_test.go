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
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
)

// These tests drive runLiveEval through the REAL Anthropic adapter with a
// scripted HTTP Doer, so the transport-vs-data classification is tested
// against the errors the adapter actually produces (a 429, an empty
// completion), not hand-built ones. No network: the Doer never dials.

// scriptedDoer answers the Nth HTTP call with script(N) (1-based).
type scriptedDoer struct {
	mu     sync.Mutex
	n      int
	script func(n int) (status int, text string)
}

func (d *scriptedDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.n++
	n := d.n
	d.mu.Unlock()

	status, text := d.script(n)
	var body string
	if status == http.StatusOK {
		content := []map[string]string{}
		if text != "" {
			content = append(content, map[string]string{"type": "text", "text": text})
		}
		b, _ := json.Marshal(map[string]any{
			"content": content,
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
		body = string(b)
	} else {
		body = `{"type":"error"}`
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (d *scriptedDoer) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

func smokeRequest(t *testing.T) []Request {
	t.Helper()
	reqs, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if r.ID == "generator-to-log-smoketest" {
			return []Request{r}
		}
	}
	t.Fatal("corpus has no generator-to-log-smoketest request")
	return nil
}

func goodSmokeCandidate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/candidates/generator-to-log-smoketest-good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return "```yaml\n" + string(b) + "```\n"
}

func liveFor(d *scriptedDoer) provider.Provider {
	return &provider.Anthropic{APIKey: "test-key-not-real", Model: "fake", BaseURL: "http://fake.invalid", HTTP: d}
}

var noBackoff = []time.Duration{0, 0}

func TestRunLiveEval_AllGood_Pass(t *testing.T) {
	good := goodSmokeCandidate(t)
	d := &scriptedDoer{script: func(int) (int, string) { return http.StatusOK, good }}

	rep := runLiveEval(context.Background(), t, smokeRequest(t), liveFor(d), "fake", 3, noBackoff)

	if rep.Verdict != liveVerdictPass {
		t.Fatalf("verdict = %s, want PASS", rep.Verdict)
	}
	if *rep.MedianValidatePass != 1 || *rep.MedianSemanticMatch != 1 || *rep.MedianGenerateOK != 1 {
		t.Fatalf("medians = %v/%v/%v, want 1/1/1", *rep.MedianValidatePass, *rep.MedianSemanticMatch, *rep.MedianGenerateOK)
	}
	if rep.Pooled.Total != 3 {
		t.Fatalf("pooled total = %d, want 3 (one request x three passes)", rep.Pooled.Total)
	}
	if d.calls() != 3 {
		t.Fatalf("calls = %d, want 3", d.calls())
	}
}

// A pass whose request never gets a response, even after retries, is
// discarded whole. Two of three surviving is still a median.
func TestRunLiveEval_TransportFailure_DiscardsWholePass(t *testing.T) {
	good := goodSmokeCandidate(t)
	// Call 1: pass 1 ok. Calls 2-4: pass 2's first try and both retries
	// rate-limited. Call 5: pass 3 ok.
	d := &scriptedDoer{script: func(n int) (int, string) {
		if n >= 2 && n <= 4 {
			return http.StatusTooManyRequests, ""
		}
		return http.StatusOK, good
	}}

	rep := runLiveEval(context.Background(), t, smokeRequest(t), liveFor(d), "fake", 3, noBackoff)

	if rep.DiscardedPasses != 1 || !rep.PassResults[1].Discarded {
		t.Fatalf("discarded = %d (pass 2 discarded: %v), want exactly pass 2", rep.DiscardedPasses, rep.PassResults[1].Discarded)
	}
	if rep.Verdict != liveVerdictPass {
		t.Fatalf("verdict = %s, want PASS from the two kept passes", rep.Verdict)
	}
	if rep.Pooled.Total != 2 {
		t.Fatalf("pooled total = %d, want 2 — a discarded pass contributes nothing", rep.Pooled.Total)
	}
	// The reason names the request and uses safeFailureReason, which never
	// carries the provider's response body.
	reason := rep.PassResults[1].DiscardReason
	if !strings.HasPrefix(reason, "generator-to-log-smoketest: ") || strings.Contains(reason, `"type"`) {
		t.Fatalf("discard reason = %q, want the request id and no raw provider body", reason)
	}
}

func TestRunLiveEval_RetryRecovers_PassKept(t *testing.T) {
	good := goodSmokeCandidate(t)
	d := &scriptedDoer{script: func(n int) (int, string) {
		if n == 2 {
			return http.StatusServiceUnavailable, ""
		}
		return http.StatusOK, good
	}}

	rep := runLiveEval(context.Background(), t, smokeRequest(t), liveFor(d), "fake", 3, noBackoff)

	if rep.DiscardedPasses != 0 || rep.Verdict != liveVerdictPass {
		t.Fatalf("discarded = %d, verdict = %s; want 0 and PASS — one retried 503 is not a lost pass", rep.DiscardedPasses, rep.Verdict)
	}
}

func TestRunLiveEval_MajorityDiscarded_InconclusiveWithNoNumbers(t *testing.T) {
	d := &scriptedDoer{script: func(int) (int, string) { return http.StatusTooManyRequests, "" }}

	rep := runLiveEval(context.Background(), t, smokeRequest(t), liveFor(d), "fake", 3, noBackoff)

	if rep.Verdict != liveVerdictInconclusive {
		t.Fatalf("verdict = %s, want INCONCLUSIVE", rep.Verdict)
	}
	if rep.MedianValidatePass != nil || rep.MedianSemanticMatch != nil || rep.Pooled != nil {
		t.Fatal("an INCONCLUSIVE run must report no numbers")
	}
	if want := 3 * (1 + len(noBackoff)); d.calls() != want {
		t.Fatalf("calls = %d, want %d (first try + %d retries per pass)", d.calls(), want, len(noBackoff))
	}
}

// A response that arrived but carried no completion is the model refusing:
// billed, attempted, and data — never a transport failure that would hide
// it by discarding the pass.
func TestRunLiveEval_EmptyCompletionIsData_NotDiscarded(t *testing.T) {
	d := &scriptedDoer{script: func(int) (int, string) { return http.StatusOK, "" }}

	rep := runLiveEval(context.Background(), t, smokeRequest(t), liveFor(d), "fake", 3, noBackoff)

	if rep.DiscardedPasses != 0 {
		t.Fatalf("discarded = %d, want 0 — a refusal is a scored failure", rep.DiscardedPasses)
	}
	if rep.Verdict != liveVerdictBelowFloor {
		t.Fatalf("verdict = %s, want BELOW_FLOOR", rep.Verdict)
	}
	rec := rep.PassResults[0].Records[0]
	if rec.GenerateOK || rec.ValidatePass || rec.SemanticMatch {
		t.Fatalf("record = %+v, want a fail on every axis", rec)
	}
	if rec.GenerateCode != provider.CodeProviderError.Reason() {
		t.Fatalf("generateCode = %q, want %q", rec.GenerateCode, provider.CodeProviderError.Reason())
	}
}

func TestRunLiveEval_UnparseableOutput_BelowFloor(t *testing.T) {
	d := &scriptedDoer{script: func(int) (int, string) { return http.StatusOK, "I would rather not write YAML today." }}

	rep := runLiveEval(context.Background(), t, smokeRequest(t), liveFor(d), "fake", 3, noBackoff)

	if rep.Verdict != liveVerdictBelowFloor || *rep.MedianValidatePass != 0 {
		t.Fatalf("verdict = %s, median validate = %v; want BELOW_FLOOR at 0", rep.Verdict, *rep.MedianValidatePass)
	}
	if got := rep.PassResults[0].Records[0].Attempts; got != DefaultMaxAttempts {
		t.Fatalf("attempts = %d, want the full budget %d", got, DefaultMaxAttempts)
	}
}

func TestLivePassCount(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", defaultLivePasses, false},
		{"5", 5, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"3x", 0, true},
		{"abc", 0, true},
	} {
		got, err := livePassCount(func(string) string { return tc.in })
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("livePassCount(%q) = %d, %v; want %d, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
