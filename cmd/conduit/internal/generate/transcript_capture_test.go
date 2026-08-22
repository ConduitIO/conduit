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

// TestCaptureTranscripts is WS1 A5a-3's capture tool (plan
// "ws1-a5-eval-plan.md" §4): it makes REAL, BILLED calls to a live Anthropic
// account and commits what comes back. It is a `//go:build generate_capture`
// test, not a `conduit` subcommand, so it never ships in the binary — the
// same convention templates_e2e/rag_template_e2e already use for
// maintenance-only, infra- or network-dependent tests
// (cmd/conduit/root/pipelines/template_gallery_e2e_integration_test.go).
//
// Invocation (mirrors plan §4 exactly):
//
//	CONDUIT_GENERATE_CAPTURE=1 ANTHROPIC_API_KEY=… \
//	  go test -tags=generate_capture -count=1 -timeout 30m \
//	  -run TestCaptureTranscripts ./cmd/conduit/internal/generate/...
//
// Re-capture one request only:
//
//	... -run TestCaptureTranscripts/<request-id> ...
//
// # Guards (all refusals, never defaults — plan §4, AC 1.22)
//
// decideCaptureGuard is the pure decision core, table-tested directly
// (TestDecideCaptureGuard) without spending anything: CONDUIT_GENERATE_CAPTURE
// unset or not exactly "1" -> skip (the key alone is not consent to spend
// money — an operator with ANTHROPIC_API_KEY set for an unrelated reason must
// never trigger a live run by accident); consent given but no
// ANTHROPIC_API_KEY -> fatal, naming the variable.
//
// # Ceilings (AC 1.22, "cannot run away")
//
// captureProvider wraps the live provider and refuses EVERY call once a hard
// ceiling trips — max provider calls, max cumulative tokens, or ctx's own
// wall-clock deadline (context.WithTimeout below, checked the same way
// provider.Replay already checks ctx.Err() before consuming a turn). This is
// structural: once tripped, the wrapped live provider is never called again,
// proven in TestCaptureProvider_Ceilings against a fake.
//
// # Redaction (plan §5)
//
// Every transcript is written to a scratch directory first. Only after
// ScanTranscriptForSecrets (redact.go) finds nothing across the WHOLE batch
// does scanAndPromoteScratch copy anything into testdata/transcripts — a
// single violating file aborts promoting ALL of them, so a violating
// transcript can never reach the working tree.
//
// manifest.yaml and every "<id>.missing.yaml" tombstone (below) get the SAME
// guarantee for a different reason: neither is a Transcript, so
// ScanTranscriptForSecrets' struct-shaped scan cannot see their own free-text
// fields at all (RequestOutcome.FailureReason, Tombstone.FailureCode). Those
// fields are held to a stricter rule instead — safeFailureReason NEVER
// returns a provider's own error text (only a conduiterr code plus an HTTP
// status, see its own doc comment) — and the result is still scanned
// (reasonFindings, below) before anything is promoted, and again by
// TestTranscripts_CarryNoSecretMaterial (secrets_scan_test.go) as raw text on
// every PR, independent of this test ever having run.
//
// # Partial results
//
// A per-request failure is one of two very different things, and this
// package never conflates them (see RequestOutcome, transcript.go):
//
//   - The model tried and never produced a passing candidate. That is DATA
//     — a real completion was recorded, buildTranscript captures it, and
//     Transcript.Outcome records the (failing) verdict. This has never
//     aborted anything and still doesn't.
//   - No completion was ever recorded for the request — a 429, a timeout, a
//     tripped ceiling, or a pre-call refusal. That is ABSENCE of data.
//     Before this package's partial-results follow-up, this case called
//     t.Errorf, which fails the whole `go test` process; combined with
//     generate-capture.yml's `set -euo pipefail`, ONE such request threw
//     away every other transcript the run had already paid for and
//     legitimately captured — the incident this section exists to prevent.
//     It is now logged (t.Logf), tracked per request, and only fails the
//     run outright once captureCompletenessVerdict's threshold — a STRICT
//     MAJORITY of the attempted requests missing — is crossed. Below that,
//     whatever was captured is promoted and manifest.yaml records exactly
//     which request(s) are missing and why (Manifest.RequestOutcomes),
//     rather than requiring a reader to infer it from an absent file.
//
// Absence of data itself splits further, via RequestOutcome.Unusable
// (provider.IsUnusableResponse): a transport-level miss (no response ever
// arrived — a 429, a timeout, a tripped ceiling) is reported separately from
// a response that DID arrive but was unusable (a decode failure, or a
// well-formed but empty completion — a refusal). The second case is a
// BILLED, attempted call — captureProvider.Complete counts its tokens toward
// Manifest.TotalTokensUsed even though it produced no transcript — and must
// never be reported the same way a rate limit is.
//
// A request that ends this run still missing (of either kind) gets a
// committed "<id>.missing.yaml" Tombstone (transcript.go) in place of a
// transcript — the artifact that lets LoadTranscripts' bijection check tell
// "this id was attempted and legitimately came back empty" apart from "this
// id was renamed out from under its transcript", the same distinction
// Manifest.RequestOutcomes makes for a human reading the diff, now made for
// the code that enforces the corpus's shape too.
//
// The redaction gate above is deliberately untouched by any of this: it
// remains all-or-nothing regardless of how many requests were captured or
// missing — a missing request never reaches scratchDir in the first place
// (runCapture only calls writeScratchTranscript for a request that DID
// produce a completion), so scanAndPromoteScratch's per-batch scan is
// unaffected by how many OTHER requests in the same run were missing. This
// holds by construction (see scanAndPromoteScratch's own doc comment), but
// as of this writing no test exercises a batch combining a missing request
// with a redaction violation among the captured ones directly;
// TestScanAndPromoteScratch_OneViolationBlocksTheWholeBatch proves the
// all-or-nothing guarantee itself (one violation blocks an otherwise-clean
// batch) but its fixture has no missing request at all.
package generate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/yaml/v3"
)

// --- Consent and key guards (AC 1.22) ---

const (
	// envCaptureConsent must be exactly "1" — the double-guard plan §4
	// requires. Any other value (including "true") is treated as absent:
	// refusals default to "no", never to "close enough".
	envCaptureConsent = "CONDUIT_GENERATE_CAPTURE"
	// envCapturePasses overrides the default pass count (item 5's "three
	// passes by default"). Optional.
	envCapturePasses = "CONDUIT_GENERATE_CAPTURE_PASSES"
	// envCaptureModel overrides DefaultAnthropicModel. Optional.
	envCaptureModel = "CONDUIT_GENERATE_CAPTURE_MODEL"

	defaultCapturePasses = 3
)

// captureGuardDecision is decideCaptureGuard's pure result — TestCaptureTranscripts
// turns it into a t.Skip/t.Fatal call; TestDecideCaptureGuard asserts on the
// value directly, without ever touching *testing.T's control flow.
type captureGuardDecision int

const (
	captureProceed captureGuardDecision = iota
	captureSkipNoConsent
	captureFatalNoKey
)

// decideCaptureGuard is AC 1.22's guard logic, pure and table-testable: env
// is looked up exactly twice, consent BEFORE the key, so that a key present
// for an unrelated reason (e.g. a developer's shell profile) can never by
// itself trigger a live run — see the package-level doc comment.
func decideCaptureGuard(env provider.Env) (captureGuardDecision, string) {
	if strings.TrimSpace(env(envCaptureConsent)) != "1" {
		return captureSkipNoConsent, fmt.Sprintf(
			"%s is not set to \"1\" — a live capture run is opt-in and spends real API budget; "+
				"the presence of %s alone is never treated as consent",
			envCaptureConsent, provider.EnvAnthropicKey,
		)
	}
	if strings.TrimSpace(env(provider.EnvAnthropicKey)) == "" {
		return captureFatalNoKey, fmt.Sprintf(
			"%s consented to a live capture run but %s is not set — refusing rather than silently skipping",
			envCaptureConsent, provider.EnvAnthropicKey,
		)
	}
	return captureProceed, ""
}

// capturePassCount reads envCapturePasses, defaulting to defaultCapturePasses
// and refusing (never silently clamping) a non-positive or unparsable value.
func capturePassCount(env provider.Env) (int, error) {
	v := strings.TrimSpace(env(envCapturePasses))
	if v == "" {
		return defaultCapturePasses, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q is not a positive integer", envCapturePasses, v)
	}
	return n, nil
}

func captureModel(env provider.Env) string {
	if v := strings.TrimSpace(env(envCaptureModel)); v != "" {
		return v
	}
	return provider.DefaultAnthropicModel
}

// --- Ceilings (AC 1.22, "cannot run away") ---

const (
	// captureAbsoluteMaxCalls is the hard backstop on provider calls,
	// independent of passes/requests — see captureCallCeiling. 1000 calls is
	// roughly 4x the worst case for the default 3 passes over the 28-request
	// corpus (28 * 3 passes * DefaultMaxAttempts = 252), so it engages only if
	// an operator drives this test with a drastically larger pass count or
	// corpus than exists today.
	captureAbsoluteMaxCalls = 1000
	// captureAbsoluteMaxTokens bounds cumulative TokensUsed across the WHOLE
	// run (every pass, every request). Plan §4 estimates ~586,000 tokens for
	// 3 passes over 28 requests; 2,000,000 is a >3x margin — generous enough
	// to absorb real variance, still small enough to cap worst-case cost in
	// the tens, not hundreds, of dollars even if every ceiling assumption
	// above is wrong at once.
	captureAbsoluteMaxTokens = 2_000_000
	// captureWallClockBudget bounds the ENTIRE test via context.WithTimeout,
	// inside the workflow's 30-minute job timeout (generate-capture.yml) —
	// belt-and-suspenders with `go test -timeout`, which an operator running
	// this by hand might forget to pass.
	captureWallClockBudget = 25 * time.Minute
)

// captureCallCeiling derives the per-run call ceiling from what THIS run
// actually asked for (passes * requestCount * DefaultMaxAttempts — Generate's
// own per-request retry budget, reused rather than re-guessed) and falls back
// to captureAbsoluteMaxCalls whenever that derived number is non-positive or
// exceeds it. The absolute backstop always wins when it is smaller: this
// function can only ever tighten the ceiling relative to the hard cap, never
// loosen it.
func captureCallCeiling(passes, requestCount int) int {
	n := passes * requestCount * DefaultMaxAttempts
	if n <= 0 || n > captureAbsoluteMaxCalls {
		return captureAbsoluteMaxCalls
	}
	return n
}

// captureProvider wraps a live provider.Provider for exactly one capture run.
// It has two jobs, both load-bearing for AC 1.22 and neither optional:
//
//  1. Enforce the three hard ceilings BEFORE a call ever reaches the wrapped
//     provider — calls, cumulative tokens, and ctx's own deadline (checked the
//     same way provider.Replay checks ctx.Err(), replay.go). Once ANY ceiling
//     trips, every subsequent call refuses immediately: a caller cannot "use
//     up" a ceiling once and keep going, and the wrapped provider is never
//     invoked again — proven in TestCaptureProvider_Ceilings by asserting the
//     wrapped fake's own call count stops advancing.
//  2. Record every raw provider.CompletionResult it sees, in order. Generate's
//     own Attempt type (generate.go) keeps only the EXTRACTED candidate —
//     never the verbatim completion text a committed Turn must carry
//     (transcript.go: "narration, fences, and all"). recorded is the only
//     place that text exists after a Generate call returns; startRequest
//     resets it, safe because Generate makes its calls for one request
//     strictly sequentially and this harness never runs requests concurrently
//     (no t.Parallel here).
type captureProvider struct {
	provider.Provider
	maxCalls  int
	maxTokens int

	mu       sync.Mutex
	calls    int
	tokens   int
	recorded []provider.CompletionResult
}

func (c *captureProvider) startRequest() {
	c.mu.Lock()
	c.recorded = nil
	c.mu.Unlock()
}

func (c *captureProvider) recordedTurns() []provider.CompletionResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.CompletionResult(nil), c.recorded...)
}

func (c *captureProvider) totalTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens
}

func (c *captureProvider) totalCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Complete enforces the ceilings and records the raw result — see the type
// doc comment. A tripped ceiling returns a plain error: Generate (generate.go)
// treats any Provider.Complete error as fatal to that call's request and
// returns immediately, which is exactly the "stop, don't retry into the
// ceiling" behavior a runaway-cost guard needs.
func (c *captureProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.CompletionResult{}, fmt.Errorf("capture ceiling: wall-clock deadline exceeded: %w", err)
	}

	c.mu.Lock()
	switch {
	case c.calls >= c.maxCalls:
		c.mu.Unlock()
		return provider.CompletionResult{}, fmt.Errorf(
			"capture ceiling: %d provider call(s) already made, max %d — refusing further calls", c.calls, c.maxCalls,
		)
	case c.tokens >= c.maxTokens:
		c.mu.Unlock()
		return provider.CompletionResult{}, fmt.Errorf(
			"capture ceiling: %d token(s) already used, max %d — refusing further calls", c.tokens, c.maxTokens,
		)
	}
	c.calls++
	c.mu.Unlock()

	res, err := c.Provider.Complete(ctx, req)
	if err != nil {
		// A call that reached the provider and got billed — a refusal
		// (provider.IsUnusableResponse) — still reports its token usage on
		// res even though it also returns an error (see anthropic.go's
		// "empty response" branch and its siblings): that usage counts
		// toward BOTH this run's cumulative token ceiling and
		// Manifest.TotalTokensUsed exactly like a successful call's does.
		// It is never appended to c.recorded, though — recorded backs
		// buildTranscript's Turns, and a refusal produced no completion
		// text worth committing as a turn.
		if res.TokensUsed > 0 {
			c.mu.Lock()
			c.tokens += res.TokensUsed
			c.mu.Unlock()
		}
		return res, err
	}

	c.mu.Lock()
	c.tokens += res.TokensUsed
	c.recorded = append(c.recorded, res)
	c.mu.Unlock()

	return res, nil
}

// --- Capture orchestration ---

// anthropicBlendedRatePerMTokUSD prices Manifest.EstimatedCostUSD. Anthropic's
// list rate is $3/MTok input, $15/MTok output (plan §4) — but
// provider.CompletionResult reports only a COMBINED total (see
// Manifest.TotalTokensUsed's doc comment), so an exact split-priced cost is
// not observable here. This blends the two list rates in the proportion plan
// §4's own cost table assumes for this corpus (~84% input / ~16% output
// tokens): 0.84*3 + 0.16*15 = 4.92. Accurate only to the extent real usage
// matches that split — TotalTokensUsed is exact, this multiplier is not, and
// EstimatedCostUSD's field name says so.
const anthropicBlendedRatePerMTokUSD = 4.92

func estimateCostUSD(tokens int) float64 {
	return float64(tokens) / 1_000_000 * anthropicBlendedRatePerMTokUSD
}

// corpusCommitSHAUnknown is corpusCommitSHA's fallback value — a named
// constant (rather than a repeated literal) so it reads as one deliberate
// sentinel, not a coincidence, everywhere it appears.
const corpusCommitSHAUnknown = "unknown"

// corpusCommitSHA returns the commit that last touched the corpus file, or
// corpusCommitSHAUnknown when git is unavailable or the lookup fails —
// capture must not fail (money already spent) just because provenance
// couldn't be recorded.
func corpusCommitSHA(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "log", "-1", "--format=%H", "--", "testdata/eval_requests.yaml").Output()
	if err != nil {
		return corpusCommitSHAUnknown
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return corpusCommitSHAUnknown
	}
	return sha
}

// buildTranscript assembles one request/pass's committed Transcript shape
// from raw (captureProvider's recorded completions for this Generate call, in
// order) and gen (Generate's own return value, which carries — 1:1 with raw,
// since every non-erroring Complete call always appends exactly one
// Attempt, generate.go — the extracted candidate, validate report, and
// semantic verdict raw alone cannot reconstruct).
//
// promptWithFeedback (generate.go) is reused rather than reimplemented, so
// UserPromptSHA256 is hashed from EXACTLY the text Generate sent — turn i's
// feedback is gen.Attempts[i-1].Feedback (the correction computed AFTER the
// previous attempt, which is what Generate threads into the next prompt);
// turn 0 gets the zero RetryFeedback, whose Render() is "" (feedback.go).
func buildTranscript(req Request, gen Generation, raw []provider.CompletionResult, providerName, model string, systemPromptSHA256, catalogFingerprint string, capturedAt time.Time) Transcript {
	turns := make([]Turn, len(raw))
	var feedback RetryFeedback
	for i, r := range raw {
		prompt := promptWithFeedback(req.Prompt, feedback)
		turns[i] = Turn{
			N:                i + 1,
			UserPromptSHA256: sha256Hex(prompt),
			TokensUsed:       r.TokensUsed,
			CompletionText:   r.Text,
		}
		if i < len(gen.Attempts) {
			feedback = gen.Attempts[i].Feedback
		}
	}

	// lastAttempt (generate.go) is reused rather than reimplemented: it is
	// already exactly "this Generation's own scored result", which is what
	// Outcome (transcript.go) documents itself as.
	var outcome Outcome
	if last := lastAttempt(gen); last != nil {
		outcome = Outcome{
			// last.Candidate == "" means extraction failed on this (the
			// LAST) attempt — every attempt failed to produce parseable
			// pipeline YAML, generate.go never calls validate.RunBytes, and
			// last.Report is left at its zero value. Report.OK() on a zero
			// Report reads true (Errors == 0: there was nothing to find a
			// problem WITH), which would otherwise commit this transcript's
			// own Outcome.ValidatePass as a PASS for a request that never
			// produced anything usable (B2, round-3 review of #2814) — the
			// same trap ScoreRun (score.go) guards against for the scoring
			// harness's own numbers. Guard on Candidate rather than trusting
			// Report.OK() alone; SemanticMatch needs no equivalent guard —
			// generate.go only ever sets att.Semantic after a candidate
			// clears validate, so its zero value (Match: false) is already
			// correct here.
			ValidatePass:   last.Candidate != "" && last.Report.OK(),
			SemanticMatch:  last.Semantic.Match,
			SemanticIssues: last.Semantic.Issues,
		}
	}

	return Transcript{
		SchemaVersion:      TranscriptSchemaVersion,
		RequestID:          req.ID,
		Provider:           providerName,
		Model:              model,
		CapturedAt:         capturedAt,
		CorpusPromptSHA256: sha256Hex(req.Prompt),
		SystemPromptSHA256: systemPromptSHA256,
		CatalogFingerprint: catalogFingerprint,
		Turns:              turns,
		Outcome:            outcome,
	}
}

// writeScratchTranscript writes tr FLAT into scratchDir (no provider/model
// nesting): scratchDir is a private t.TempDir() that exists only long enough
// for scanAndPromoteScratch to scan it, so it does not need to mirror
// destDir's testdata/transcripts/<provider>/<model>/ layout — and NOT nesting
// it means scanAndPromoteScratch's single os.ReadDir(scratchDir) sees every
// file without having to walk a variable-depth tree.
func writeScratchTranscript(t *testing.T, scratchDir string, tr Transcript) {
	t.Helper()
	data, err := yaml.Marshal(tr)
	if err != nil {
		t.Fatalf("marshaling transcript %q: %v", tr.RequestID, err)
	}
	if err := os.WriteFile(filepath.Join(scratchDir, tr.RequestID+".yaml"), data, 0o600); err != nil {
		t.Fatalf("writing scratch transcript %q: %v", tr.RequestID, err)
	}
}

// scanAndPromoteScratch runs ScanTranscriptForSecrets (redact.go) over every
// transcript file under scratchDir and, ONLY if every file is clean, copies
// all of them into destDir. A single violating file aborts promoting ALL of
// them — plan §5's "a violating transcript must never reach the working
// tree" is a run-level guarantee: partially promoting a batch would leave the
// working tree in a state no single commit represents.
//
// It returns findings when the redaction scan is not clean, and NEVER touches
// destDir in that case — the caller decides how to
// fail (runCapture calls t.Fatalf on a non-empty findings, per plan §5's "a
// violating transcript must never reach the working tree"). Detection is
// deliberately separate from failing the test: an I/O error reading or
// writing a file is still a t.Fatalf HERE (that is a broken test harness, not
// a redaction finding), but a secret finding is returned data, which is what
// lets TestScanAndPromoteScratch_OneViolationBlocksTheWholeBatch assert on it
// directly instead of needing a "this subtest is expected to fail" trick
// (which Go's testing package cannot express cleanly — a failed subtest
// always fails every ancestor test, unconditionally).
func scanAndPromoteScratch(ctx context.Context, t *testing.T, scratchDir, destDir string) (moved, findings []string) {
	t.Helper()

	entries, err := os.ReadDir(scratchDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing was successfully captured this run
		}
		t.Fatalf("reading scratch dir %q: %v", scratchDir, err)
	}

	type scratchFile struct {
		name string
		data []byte
	}
	var files []scratchFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || e.Name() == manifestFileName {
			continue
		}
		path := filepath.Join(scratchDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading scratch transcript %q: %v", path, err)
		}
		var tr Transcript
		if err := yaml.Unmarshal(data, &tr); err != nil {
			t.Fatalf("parsing scratch transcript %q: %v", path, err)
		}
		findings = append(findings, ScanTranscriptForSecrets(ctx, tr)...)
		files = append(files, scratchFile{name: e.Name(), data: data})
	}

	if len(findings) > 0 {
		return nil, findings
	}
	if len(files) == 0 {
		return nil, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("creating %q: %v", destDir, err)
	}
	moved = make([]string, 0, len(files))
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(destDir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("writing %q: %v", filepath.Join(destDir, f.name), err)
		}
		moved = append(moved, f.name)
	}
	return moved, nil
}

func writeManifest(t *testing.T, destDir string, m Manifest) {
	t.Helper()
	data, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("marshaling manifest: %v", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("creating %q: %v", destDir, err)
	}
	if err := os.WriteFile(filepath.Join(destDir, manifestFileName), data, 0o600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
}

// safeFailureReason summarizes err for a value that ends up committed to
// manifest.yaml (RequestOutcome.FailureReason) or a tombstone
// (Tombstone.FailureCode) forever. It NEVER returns the provider's own
// error text: a provider error message may embed up to 512 bytes of raw
// response body (readErrorBody, provider/http.go) that can echo back
// caller-supplied or provider-controlled content — a 401 body quoting the
// rejected key, a *url.Error carrying credentials embedded in a base URL —
// and this package's manifest.yaml has no content-based redaction of its
// own before this fix (TestTranscripts_CarryNoSecretMaterial used to skip
// it by name).
//
// The only load-bearing information for a reviewer — enough to tell "this
// was a rate limit" from "this was a refusal" from "this was an auth
// failure" — is the conduiterr CODE (conduiterr.Get) plus the HTTP status
// when checkStatus recorded one (provider.HTTPStatus), or (nit from the
// round-2 review of #2814) whether the failure was specifically the
// calling context's deadline expiring (provider.IsTimeout). All three are
// safe by construction: a Code.Reason() is a registered, static string,
// never user- or provider-controlled text, the status is a bare integer,
// and IsTimeout is a bool. Every provider adapter wraps EVERY failure —
// auth rejection, rate limit, DNS miss, connection refused, our own
// timeout — under the SAME CodeProviderError (see that var's doc comment,
// provider/http.go), so without the HTTPStatus/IsTimeout discriminators
// this reason would read identically for all of them; HTTPStatus alone
// still can't tell "our own deadline expired" apart from every OTHER
// transport-level miss (a DNS failure, a connection refused) — none of
// those ever got far enough to have a status to report.
//
// Not every failure this package sees is a conduiterr — captureProvider's
// own ceiling and wall-clock-deadline errors (transcript_capture_test.go)
// are plain fmt.Errorf values built entirely from static text and integers
// this test computed itself, never provider-controlled content — so those
// fall back to their literal Error() text. Callers still run
// scanTextForSecrets over the RESULT before trusting it (see runCapture's
// reasonFindings check): not because this function is expected to leak
// (it never touches a conduiterr's Error()/message text, only its Code),
// but because that fallback text has never technically been proven safe
// the way the structured path has, and the same scan gates everything else
// destined for a committed file.
func safeFailureReason(err error) string {
	if err == nil {
		return "no completion recorded"
	}
	if ce, ok := conduiterr.Get(err); ok {
		reason := ce.Code.Reason()
		switch status, hasStatus := provider.HTTPStatus(err); {
		case hasStatus:
			reason = fmt.Sprintf("%s (HTTP %d)", reason, status)
		case provider.IsTimeout(err):
			reason = fmt.Sprintf("%s (timeout)", reason)
		}
		return reason
	}
	return err.Error()
}

// TestSafeFailureReason_Timeout is the regression test for N4 (round-3
// review of #2814): safeFailureReason's `(timeout)` suffix was never
// asserted anywhere on its own — only exercised transitively by tests that
// go through a real *testing.T end to end. provider.MarkIfTimeout is what a
// real adapter calls at its ctx.Err() == context.DeadlineExceeded branch
// (anthropic.go, ollama.go, openai.go), so this constructs the exact shape
// those call sites produce rather than a hand-rolled stand-in.
func TestSafeFailureReason_Timeout(t *testing.T) {
	wrapped := conduiterr.New(provider.CodeProviderError, "anthropic: context deadline exceeded")
	err := provider.MarkIfTimeout(wrapped, context.DeadlineExceeded)

	got := safeFailureReason(err)
	if !strings.HasSuffix(got, "(timeout)") {
		t.Fatalf("safeFailureReason(timeout error) = %q, want a string ending in %q", got, "(timeout)")
	}
	if strings.Contains(got, "context deadline exceeded") {
		t.Fatalf("safeFailureReason(timeout error) = %q, leaked the raw error text (must carry only the "+
			"registered Code.Reason() plus the (timeout) suffix)", got)
	}
}

// TestSafeFailureReason_HTTPStatus_NeverLeaksAndNil round out the axes
// TestSafeFailureReason_Timeout covers alone: nil (the one branch with no
// conduiterr at all), and a real HTTP-status-carrying error built the same
// way TestRunCapture_ProviderHTTPErrorNeverLeaksResponseBodyIntoManifest's
// httptest server produces one, asserting directly on safeFailureReason's
// own return value rather than only on the manifest field it ends up in.
func TestSafeFailureReason_HTTPStatus_NeverLeaksAndNil(t *testing.T) {
	if got := safeFailureReason(nil); got != "no completion recorded" {
		t.Fatalf("safeFailureReason(nil) = %q, want %q", got, "no completion recorded")
	}

	const leakedSecret = "sk-ant-1234567890abcdefghijklmnop"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"Incorrect API key provided: `+leakedSecret+`"}}`)
	}))
	t.Cleanup(srv.Close)

	base := &provider.Anthropic{BaseURL: srv.URL}
	_, err := base.Complete(context.Background(), provider.CompletionRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected the 401 response to produce an error")
	}

	got := safeFailureReason(err)
	if !strings.Contains(got, "HTTP 401") {
		t.Fatalf("safeFailureReason(401 error) = %q, want it to name HTTP 401", got)
	}
	if strings.Contains(got, leakedSecret) {
		t.Fatalf("safeFailureReason(401 error) = %q, leaked the response body's secret", got)
	}
}

// writeTombstone commits a Tombstone for a corpus request whose capture
// never produced a transcript on any pass — see the Tombstone type doc
// comment (transcript.go) for why LoadTranscripts' bijection check needs an
// explicit file here rather than tolerating a missing one.
func writeTombstone(t *testing.T, destDir string, ts Tombstone) {
	t.Helper()
	data, err := yaml.Marshal(ts)
	if err != nil {
		t.Fatalf("marshaling tombstone %q: %v", ts.RequestID, err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("creating %q: %v", destDir, err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ts.RequestID+tombstoneFileSuffix), data, 0o600); err != nil {
		t.Fatalf("writing tombstone %q: %v", ts.RequestID, err)
	}
}

// TestCaptureTranscripts is the entry point described in the file-level doc
// comment above. It is a thin shell around runCapture: guard-check, resolve
// config, build the live provider, then hand off — see runCapture's doc
// comment for why the handoff point is exactly here (it is what lets
// TestRunCapture_FullPipeline_FakeProvider exercise the whole pipeline
// against a fake provider instead).
func TestCaptureTranscripts(t *testing.T) {
	decision, msg := decideCaptureGuard(os.Getenv)
	switch decision {
	case captureSkipNoConsent:
		t.Skip(msg)
	case captureFatalNoKey:
		t.Fatal(msg)
	case captureProceed:
		// Fall through to the live capture run below.
	}

	apiKey := os.Getenv(provider.EnvAnthropicKey)
	model := captureModel(os.Getenv)
	passes, err := capturePassCount(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatalf("loading corpus: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), captureWallClockBudget)
	defer cancel()

	base := &provider.Anthropic{APIKey: apiKey, Model: model}
	cp := &captureProvider{
		Provider:  base,
		maxCalls:  captureCallCeiling(passes, len(requests)),
		maxTokens: captureAbsoluteMaxTokens,
	}
	destDir := filepath.Join("testdata", "transcripts", base.Name(), model)

	runCapture(ctx, t, requests, cp, base.Name(), model, passes, destDir)
}

// runCapture is TestCaptureTranscripts' testable core: given an
// already-ceiling-wrapped provider (real or fake — captureProvider does not
// care) and the directory to promote into, it runs every pass, builds and
// scratch-writes each request's transcript, redaction-scans and promotes the
// batch, and — for a run that attempted the FULL corpus (never scoped by a
// `-run` filter to a subset) — scores the run (ScoreMedian) and writes a
// FRESH manifest.yaml, INCLUDING for a run where some requests never
// produced a completion at all (see the file's "Partial results" doc
// comment): whatever a pass DID capture is promoted and recorded
// regardless, and captureCompletenessVerdict decides only how loud the
// `go test` signal is, never whether anything gets preserved. A scoped
// `-run` (a targeted re-capture of one id) never computes new
// medians/PassScores (there is no full-corpus data to compute them from),
// but it DOES patch the scoped id(s)' own RequestOutcomes/CapturedCount/
// MissingCount into whatever manifest.yaml the last full run left behind
// (patchManifestForScopedRun, M1 fix — round-3 review of #2814) — leaving
// it untouched entirely, as this used to, meant a re-capture that turned a
// previously-tombstoned id back into real data left the committed
// manifest.yaml contradicting the tree it describes.
//
// Split out from TestCaptureTranscripts so the FULL pipeline (transcript
// construction, the redaction-scan-then-promote gate, scoring, manifest
// provenance) is exercised by TestRunCapture_FullPipeline_FakeProvider and
// TestRunCapture_PartialFailure_PreservesGoodTranscripts against a fake
// provider and a scratch destDir — no live key, no network, and never
// touching the real committed testdata/transcripts.
// runCapture's third return value, passRuns, is every full-corpus pass's
// RunScore — including Result.Missing per request per pass — exactly as
// ScoreMedian computed it from the SAME Candidates maps runCapture itself
// built (the manifest's own PassScores is a summary derived from this same
// data, without RunScore.Results; see PassScore's doc comment for why the
// committed file doesn't carry that detail). It exists so a test can assert
// end-to-end on how runCapture's per-request closure populates Candidates —
// in particular, that a request with no completion gets NO entry at all
// (see the "absence of data" branch below) — rather than hand-building a
// Candidates map that only ASSERTS it mirrors that behavior. Nil for a
// scoped run that never reaches the full-corpus scoring step — that is
// independent of wrote (below), which a scoped run can still report true
// for if it patched an existing manifest.yaml (patchManifestForScopedRun).
//
// wrote is true whenever manifest.yaml was written OR patched on disk —
// not only for a fresh, full-corpus write. It is false only when there was
// truly nothing to do: a scoped run with no prior manifest.yaml to patch
// (patchManifestForScopedRun's own ok == false).
func runCapture(ctx context.Context, t *testing.T, requests []Request, cp *captureProvider, providerName, model string, passes int, destDir string) (manifest Manifest, passRuns []RunScore, wrote bool) {
	t.Helper()

	runStart := time.Now().UTC()
	system := BuildSystemPrompt(BuiltinCatalog())
	systemSHA := sha256Hex(system)
	catalogFP := CatalogFingerprint()

	scratchDir := t.TempDir()

	// attemptOrder/attempted track, in corpus order, exactly the requests
	// this run actually exercised — the full corpus for a normal run, or
	// the one id a scoped `-run TestCaptureTranscripts/<id>` re-capture
	// names (t.Run never invokes the closure below for a filtered-out id,
	// so both maps stay accurate for free). lastFailureErr holds, per
	// request id, the most recent error seen for a pass that recorded NO
	// completion at all — overwritten on every occurrence and deleted the
	// moment a pass DOES capture that id, so it reflects only the reason a
	// STILL-missing request is missing, never a transient blip a later pass
	// recovered from. It stores the raw error (not a string): the string
	// destined for a committed file is computed later, by safeFailureReason,
	// deliberately never from err.Error() directly — see that function's
	// doc comment.
	var attemptOrder []Request
	attempted := make(map[string]bool, len(requests))
	lastFailureErr := make(map[string]error)

	var scoreCandidatesList []Candidates
	// scoreMissingSets[i] is which of requests' ids contributed NO USABLE
	// candidate to scoreCandidatesList[i] — either no completion was ever
	// recorded on that pass (no map entry — see the inline comment below on
	// why passCandidates omits, rather than empty-strings, those entries)
	// OR a completion WAS recorded but never extracted into pipeline YAML
	// (an empty/whitespace-only entry: Generate exhausted every attempt
	// without ever producing a candidate — B2, round-3 review of #2814).
	// requestsMissingUsableCandidate treats both the same way ScoreRun
	// (score.go) itself does — a pass full of unparseable completions is
	// exactly as degraded as one the provider never answered at all. Kept
	// as a parallel slice, index-aligned with scoreCandidatesList (and
	// therefore with ms.Runs below), rather than added to RunScore/ScoreRun:
	// score.go's Result.Missing already carries the no-completion half of
	// this per-request, and re-deriving the per-pass set from
	// passCandidates here is cheaper than plumbing a new field through
	// ScoreRun for something runCapture already has for free. This is what
	// lets a degraded tail pass (a rate-limit storm, or
	// captureWallClockBudget expiring partway through) be excluded from the
	// median instead of silently dragging
	// MedianValidatePassRate/MedianSemanticMatchRate toward zero — see
	// PassScore.MissingCount and Manifest.DegradedPasses.
	//
	// H1 (round-4 review of #2814): this used to be scoreMissingCounts, a
	// parallel []int — a per-pass COUNT alone cannot tell summarizePasses
	// whether two degraded passes missed the same requests or different
	// ones, which is exactly the distinction between a stable partial
	// result and a misleading one (allMissingSetsEqual). The id set is the
	// smallest thing that preserves that distinction.
	var scoreMissingSets [][]string
	for pass := 1; pass <= passes; pass++ {
		passCandidates := make(Candidates, len(requests))
		for _, req := range requests {
			t.Run(req.ID, func(t *testing.T) {
				if !attempted[req.ID] {
					attempted[req.ID] = true
					attemptOrder = append(attemptOrder, req)
				}

				cp.startRequest()
				gen, genErr := Generate(ctx, Input{Prompt: req.Prompt, Provider: cp, Model: model})
				raw := cp.recordedTurns()

				if len(raw) == 0 {
					// Absence of data (see the file-level "Partial results"
					// doc comment): no completion was EVER recorded for this
					// attempt — a 429, a timeout, a tripped ceiling, or a
					// pre-call refusal, never a model that tried and produced
					// a bad answer (that case has raw != empty and falls
					// through to buildTranscript below, where it is captured
					// as ordinary, if failing, data). Logged, not
					// t.Errorf'd — a single infra blip on one pass must not
					// fail the whole go test run and discard every OTHER
					// transcript already captured; captureCompletenessVerdict
					// below is what decides when enough of these should.
					//
					// passCandidates deliberately gets NO entry for req.ID
					// here (never a key with an empty value) — ScoreRun
					// (score.go) reports a MISSING key as Result.Missing.
					// Storing an empty-string candidate instead would flip
					// Result.Missing to false — scoring this as DATA (a
					// request the provider answered, however badly) rather
					// than as absence of data, which is the wrong story for
					// a request whose provider call never even returned a
					// response. (B2, round-3 review of #2814, corrects this
					// comment's prior claim that an empty-string candidate
					// here "would score this exactly like a model that
					// produced a candidate and failed validation" — false as
					// written: before ScoreRun's own empty-candidate guard
					// existed, an empty string scored a validate PASS, not a
					// failed validation, which was the actual bug. ScoreRun
					// now hard-fails an empty candidate on both axes either
					// way — the choice to omit the key HERE is still right,
					// but for this reason: it is Result.Missing, not the
					// pass/fail verdict, that an empty string here would get
					// wrong.)
					lastFailureErr[req.ID] = genErr
					t.Logf("pass %d: %q produced no completion (absence of data): %v", pass, req.ID, genErr)
					return
				}
				delete(lastFailureErr, req.ID)
				passCandidates[req.ID] = gen.Candidate

				tr := buildTranscript(req, gen, raw, providerName, model, systemSHA, catalogFP, time.Now().UTC())
				writeScratchTranscript(t, scratchDir, tr)

				if genErr != nil {
					// Partial success: at least one completion was recorded
					// and promoted to scratch, but the request never cleared
					// the budget (or a later call in this same request tripped
					// a ceiling). This IS data — Transcript.Outcome records
					// the real (failing) verdict — never "missing", so it is
					// flagged for visibility only, not tracked as absent.
					t.Logf("pass %d: %q did not clear the generation budget: %v", pass, req.ID, genErr)
				}
			})
		}

		// attempted is checked AFTER every pass rather than passCandidates'
		// size: passCandidates now deliberately omits a key for any request
		// with no completion this pass (see above), so its size no longer
		// says anything about whether the FULL corpus was attempted — only
		// about how many of the attempted requests produced a candidate.
		// attempted is unconditional (set at the top of the t.Run closure
		// regardless of outcome) and, once a full-corpus run's first pass
		// completes, stays at len(requests) for every later pass too — a
		// `-run` filter's scope is fixed for the whole `go test` invocation,
		// never per-pass.
		if len(attempted) == len(requests) {
			scoreCandidatesList = append(scoreCandidatesList, passCandidates)
			scoreMissingSets = append(scoreMissingSets, requestsMissingUsableCandidate(requests, passCandidates))
		} else {
			t.Logf("pass %d: %d/%d requests ran (a -run filter is scoping this to a subset) — "+
				"excluded from corpus-level scoring and the manifest", pass, len(attempted), len(requests))
		}
	}

	// Every still-missing request's manifest-safe failure reason is
	// computed BEFORE anything is promoted, so a redaction-scan hit here
	// aborts the whole batch — nothing gets promoted, no manifest, no
	// tombstone — exactly like a violation found inside a transcript's own
	// content does (scanAndPromoteScratch below). safeFailureReason itself
	// never returns raw provider text, but the scan runs over its result
	// anyway as the same defence-in-depth every other field destined for a
	// committed file gets; see safeFailureReason's doc comment for why.
	safeReasons := make(map[string]string, len(lastFailureErr))
	unusable := make(map[string]bool, len(lastFailureErr))
	var reasonFindings []string
	for id, ferr := range lastFailureErr {
		reason := safeFailureReason(ferr)
		reasonFindings = append(reasonFindings, scanTextForSecrets(reason)...)
		safeReasons[id] = reason
		unusable[id] = provider.IsUnusableResponse(ferr)
	}
	if len(reasonFindings) > 0 {
		t.Fatalf("redaction scan found %d issue(s) in a request's failure reason — refusing to promote ANY captured "+
			"transcript into %q:\n%s", len(reasonFindings), destDir, strings.Join(reasonFindings, "\n"))
	}

	moved, findings := scanAndPromoteScratch(ctx, t, scratchDir, destDir)
	if len(findings) > 0 {
		t.Fatalf("redaction scan found %d issue(s) — refusing to move ANY captured transcript into %q:\n%s",
			len(findings), destDir, strings.Join(findings, "\n"))
	}
	t.Logf("promoted %d transcript(s) into %q", len(moved), destDir)

	// capturedIDs is derived straight from what scanAndPromoteScratch
	// actually promoted THIS run (in particular: if the redaction gate
	// above blocked the whole batch, moved is empty and every attempted
	// request has no entry here, correctly — nothing was promoted for any
	// of them). It is deliberately NOT the sole source of truth for
	// "captured" any more — see requestIsCaptured (H2 fix, round-4 review
	// of #2814): an earlier run may have already committed a real
	// transcript for an id THIS run's own attempt failed to reproduce
	// (provider down, rate-limited, a tripped ceiling, …), and that data is
	// still good. An earlier version of this code treated capturedIDs
	// alone as authoritative, recording Captured: false plus a
	// FailureReason for exactly that id, and only noticed the
	// already-on-disk case late enough to skip writing a (redundant)
	// tombstone over it — leaving RequestOutcomes, and therefore the
	// committed manifest.yaml, contradicting a tree that still had real
	// data for this id.
	capturedIDs := make(map[string]bool, len(moved))
	for _, name := range moved {
		capturedIDs[strings.TrimSuffix(name, ".yaml")] = true
	}

	outcomes := make([]RequestOutcome, len(attemptOrder))
	capturedCount, missingCount := 0, 0
	// carriedForwardIDs is every id THIS run counts as captured SOLELY
	// because requestIsCaptured found a valid transcript already on disk —
	// never because scanAndPromoteScratch promoted anything for it this
	// run. Fed into summarizePasses (H1 x H2 fix, round-5 review of #2814):
	// the equal-missing-sets exemption that lets a chronically-missing
	// request avoid tripping MediansUnreliable is only sound when that
	// request is ALSO visible as run-missing (MissingCount > 0) — which is
	// exactly what breaks when an id in every pass's missing set is only
	// "captured" via carry-forward. See summarizePasses' own doc comment.
	var carriedForwardIDs []string
	for i, req := range attemptOrder {
		outcome, carriedForward := classifyRequestOutcome(req, destDir, capturedIDs, lastFailureErr, safeReasons)
		outcomes[i] = outcome

		if outcome.Captured {
			capturedCount++
			if carriedForward {
				carriedForwardIDs = append(carriedForwardIDs, req.ID)
			}
			// A previous run's tombstone for this id, if any, is now
			// stale — this id has real data again (whether promoted THIS
			// run or already on disk from an earlier one), and
			// LoadTranscripts hard-errors on an id carrying both a
			// transcript and a tombstone (transcript.go).
			tombstonePath := filepath.Join(destDir, req.ID+tombstoneFileSuffix)
			if err := os.Remove(tombstonePath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("removing stale tombstone %q: %v", tombstonePath, err)
			}
			continue
		}

		missingCount++
		reason := safeReasons[req.ID]
		if reason == "" {
			reason = "no completion recorded"
		}
		outcomes[i].FailureReason = reason
		outcomes[i].Unusable = unusable[req.ID]

		writeTombstone(t, destDir, Tombstone{
			SchemaVersion:      TranscriptSchemaVersion,
			RequestID:          req.ID,
			CorpusPromptSHA256: sha256Hex(req.Prompt),
			FailureCode:        reason,
			CapturedAt:         runStart,
		})
	}

	reportCaptureCompleteness(t, len(attemptOrder), capturedCount, missingCount, outcomes)

	if len(scoreCandidatesList) != passes {
		// A scoped `-run TestCaptureTranscripts/<id>` re-capture — split
		// out into finishScopedRun to keep THIS function's own cyclomatic
		// complexity under gocyclo's threshold (the same reason
		// summarizePasses was split out for H2, round-2 review of #2814).
		return finishScopedRun(t, destDir, outcomes, len(scoreCandidatesList), passes)
	}

	ms := ScoreMedian(ctx, requests, scoreCandidatesList)
	passScores, degradedMedians := summarizePasses(ms.Runs, scoreMissingSets, carriedForwardIDs)

	// H2 fix (round-4 review of #2814): a run that promoted NOTHING new
	// learned nothing about the corpus's quality — passScores/
	// degradedMedians above, computed from zero completions, are pure,
	// uninformative zeros, not a real measurement, and must not overwrite
	// an existing manifest.yaml's real ones. See
	// preserveMediansIfNothingPromoted's own doc comment. Split out from
	// this function (kept runCapture's own cyclomatic complexity under
	// gocyclo's threshold, the same reason finishScopedRun and
	// summarizePasses were split out for earlier rounds of #2814's review).
	//
	// Moved BEFORE the switch below (round-5 review of #2814): the switch
	// decides whether to t.Errorf/t.Logf based on degradedMedians, and a
	// run that promoted nothing built degradedMedians from zero completions
	// — evaluating the switch on THAT synthetic data (before it gets
	// replaced by preserveMediansIfNothingPromoted, or correctly flagged
	// unreliable by it on a shape mismatch) risked t.Errorf-ing a `go test`
	// run whose real, preserved manifest was perfectly fine — exactly the
	// interaction TestRunCapture_SecondRunCapturesNothing_PreservesFirstRunsGoodManifest
	// guards against: reordering this after the switch makes that test
	// fail with a spurious t.Errorf even though its own assertions still
	// pass.
	passScores, degradedMedians = preserveMediansIfNothingPromoted(t, destDir, moved, requests, passes, passScores, degradedMedians)

	logDegradedPassSummary(t, degradedMedians, passes)

	manifest = Manifest{
		SchemaVersion:            TranscriptSchemaVersion,
		Provider:                 providerName,
		Model:                    model,
		CapturedAt:               runStart,
		RequestCount:             len(requests),
		TotalTokensUsed:          cp.totalTokens(),
		EstimatedCostUSD:         estimateCostUSD(cp.totalTokens()),
		CaptureCommand:           captureCommandString(passes, model),
		CorpusCommitSHA:          corpusCommitSHA(ctx),
		CatalogFingerprint:       catalogFP,
		SystemPromptSHA256:       systemSHA,
		Passes:                   passes,
		MedianValidatePassRate:   degradedMedians.validateRate,
		MedianSemanticMatchRate:  degradedMedians.semanticRate,
		MedianValidatePassCount:  degradedMedians.validateCount,
		MedianSemanticMatchCount: degradedMedians.semanticCount,
		MedianSampleSize:         degradedMedians.sampleSize,
		MediansUnreliable:        degradedMedians.allDegraded,
		PassScores:               passScores,
		DegradedPasses:           degradedMedians.degradedPasses,
		CapturedCount:            capturedCount,
		MissingCount:             missingCount,
		RequestOutcomes:          outcomes,
	}
	writeManifest(t, destDir, manifest)
	t.Logf("wrote manifest: %d/%d captured, %d calls, %d tokens, ~$%.2f estimated, median validate %.1f%%, median semantic %.1f%%",
		capturedCount, len(attemptOrder), cp.totalCalls(), cp.totalTokens(), manifest.EstimatedCostUSD,
		degradedMedians.validateRate*100, degradedMedians.semanticRate*100)
	return manifest, ms.Runs, true
}

// logDegradedPassSummary reports, via t, what (if anything) degradedMedians
// means for this run's own `go test` outcome — split out from runCapture
// (M1-3/H1 x H2, round-5 review of #2814: kept runCapture's own cyclomatic
// complexity under gocyclo's threshold, the same reason finishScopedRun,
// summarizePasses, and preserveMediansIfNothingPromoted were split out for
// earlier rounds).
func logDegradedPassSummary(t *testing.T, degradedMedians passesSummary, passes int) {
	t.Helper()
	switch {
	case degradedMedians.allDegraded:
		// B1 fix (round-3 review of #2814), generalized by H1 (round-4
		// review of #2814) and H1 x H2 (round-5 review of #2814): every one
		// of `passes` passes lost the full corpus AND missingSetsAreReliable
		// is false for those degraded passes — a rate-limit storm or
		// captureWallClockBudget expiring partway through and wiping every
		// pass from that point on is one way to get here, but so is a
		// provider that rotates which single request it answers pass to
		// pass, or a chronically-missing request that is ALSO only counted
		// captured via H2's on-disk carry-forward (see
		// missingSetsAreReliable's own doc comment). Either way
		// summarizePasses has no clean pass left to compute a median FROM,
		// so validateRate/semanticRate below are its all-passes fallback,
		// which scores every missing request as a hard fail with nothing to
		// dilute it. Treated as a hard failure of this `go test` run, the
		// same class of thing captureCompletenessVerdict already does for
		// attemptedCount == 0: t.Errorf (not t.Fatalf) so the manifest below
		// still gets written with whatever WAS captured (see
		// captureCompletenessVerdict's own doc comment on why a true
		// verdict there does not mean nothing is preserved — the same
		// reasoning applies here), but the `go test` exit code goes
		// nonzero, which generate-capture.yml's `capture` job turns into
		// CAPTURE_RESULT != "success" — the [!WARNING] PARTIAL-capture
		// banner and title, not the routine [!NOTE] one. Manifest.
		// MediansUnreliable (set below) carries the same fact into the
		// committed file for a reader who never sees the PR body. This is
		// also the branch preserveMediansIfNothingPromoted's own shape-
		// mismatch case (M4) reaches, above.
		t.Errorf("every one of %d capture pass(es) was degraded (see passScores[].missingCount) — no pass "+
			"contributed a usable candidate for the full corpus, so medianValidatePassRate (%.3f) and "+
			"medianSemanticMatchRate (%.3f) below are a fallback computed across ALL passes, with every "+
			"missing request scored as a hard fail and no clean pass to dilute that — NOT a reliable "+
			"median, and must not be used as this run's baseline. Whatever WAS captured is still promoted "+
			"and recorded below (capturedCount/missingCount/requestOutcomes).",
			passes, degradedMedians.validateRate, degradedMedians.semanticRate)
	case degradedMedians.sampleSize < passes:
		// A real, clean-pass-only median: degradedMedians.sampleSize (the
		// clean-pass count) is strictly less than passes, meaning the
		// degraded pass(es) named below were genuinely excluded from
		// medianValidatePassRate/medianSemanticMatchRate — the "excluded"
		// claim this message makes is actually true here. Deliberately a
		// separate Logf, not folded into reportCaptureCompleteness: that
		// function's fail/log threshold (captureCompletenessVerdict) is
		// scoped to REQUESTS missing from the whole run, and its
		// majority-threshold math would be wrong applied to PASSES instead
		// — a run can have every request captured (missingCount == 0, so
		// reportCaptureCompleteness has nothing to say) while still having
		// lost a tail pass to rate limiting, which is exactly the case this
		// exists to surface.
		t.Logf("pass(es) %v captured fewer than the full corpus (see passScores[].missingCount in the "+
			"manifest) — excluded from medianValidatePassRate/medianSemanticMatchRate so a wiped tail pass "+
			"does not silently drag those numbers toward zero; requests affected may still show as captured "+
			"overall (capturedCount/missingCount) if another pass produced them", degradedMedians.degradedPasses)
	case len(degradedMedians.degradedPasses) > 0:
		// H1 (round-5 review of #2814): degradedMedians.sampleSize == passes
		// here (the case above did NOT match), so — unlike the case above —
		// nothing was actually excluded from the median: summarizePasses'
		// all-passes fallback ran (n == 0, no clean pass existed) but
		// missingSetsAreReliable held, so the fallback's number equals what
		// a clean-pass median would have shown anyway. The previous version
		// of this message unconditionally claimed "excluded... so [it does]
		// not silently drag those numbers toward zero" even in this branch,
		// which is false when nothing was excluded — the identical false
		// claim generate-capture.yml's own DEGRADED banner made before its
		// own fix (see that workflow's "Compute the summary" step).
		t.Logf("pass(es) %v captured fewer than the full corpus (see passScores[].missingCount in the "+
			"manifest), but every degraded pass missed the SAME, reliably-attributed request(s) — "+
			"medianValidatePassRate/medianSemanticMatchRate below are the all-pass fallback median, which "+
			"equals what a clean-pass median would have shown too (nothing was excluded, and this is not "+
			"misleading); requests affected may still show as captured overall (capturedCount/missingCount) "+
			"if another pass, or an earlier run's transcript, produced them", degradedMedians.degradedPasses)
	}
}

// finishScopedRun is runCapture's return path for a scoped
// `-run TestCaptureTranscripts/<id>` re-capture — one that never attempted
// the full corpus (capturedPasses != passes), so there is no full-corpus
// data to compute new medians/PassScores from. Split out from runCapture
// (kept its own cyclomatic complexity under gocyclo's threshold, the same
// reason summarizePasses was split out for H2 — round-2 review of #2814).
//
// The tree itself was already mutated by the caller before this runs
// (promoted, and any stale tombstone removed, or a fresh tombstone
// written) — this only decides what happens to manifest.yaml. M1 fix
// (round-3 review of #2814): patch the EXISTING manifest.yaml's own
// RequestOutcomes/CapturedCount/MissingCount for just the scoped id(s)
// rather than leaving it untouched — before this fix, a re-capture that
// turned a previously-tombstoned id back into real data left the tree with
// "<id>.yaml" and no tombstone (correct) while manifest.yaml still said
// captured: false for that id, with a stale failureReason and stale counts
// nothing else ever caught (LoadTranscripts never reads the manifest).
// Medians and PassScores are left exactly as the last FULL run recorded
// them — see patchManifestForScopedRun's own doc comment for why.
func finishScopedRun(t *testing.T, destDir string, outcomes []RequestOutcome, capturedPasses, passes int) (Manifest, []RunScore, bool) {
	t.Helper()
	t.Logf("only %d/%d pass(es) captured the full corpus — a scoped -run limits scoring to a subset",
		capturedPasses, passes)

	patched, ok := patchManifestForScopedRun(t, destDir, outcomes)
	if !ok {
		t.Logf("no existing manifest.yaml in %q to patch — a scoped run before any full run has "+
			"nothing to patch and correctly leaves nothing behind", destDir)
		return Manifest{}, nil, false
	}
	t.Logf("patched manifest.yaml's requestOutcomes/capturedCount/missingCount for %d scoped request(s); "+
		"medians/passScores still describe the last full run", len(outcomes))
	return patched, nil, true
}

// requestIsCaptured reports whether req already has a VALID transcript on
// disk in destDir — the H2 fix's (round-4 review of #2814) disk-fallback
// half: an earlier run may have already committed a real transcript for an
// id THIS run's own attempt failed to reproduce (provider down,
// rate-limited, a tripped ceiling, …), and that data is still good.
// runCapture calls this only after checking capturedIDs (this run's own
// promotions) itself — see capturedIDs' own doc comment there for the
// contradiction the disk-fallback half replaces — so this function is never
// the sole source of "captured", only the fallback.
//
// M5 (round-5 review of #2814): a bare os.Stat used to be the entire check
// here — true for a directory, a zero-byte file, or (the actually dangerous
// case) a transcript captured for a DIFFERENT prompt under an id that was
// never renamed. Reproduced: edit a request's prompt without touching its
// id, run with the provider down, and the pre-fix version reported the
// stale, now-mismatched transcript as captured — a manifest.yaml claiming a
// fully-captured corpus for a tree LoadTranscripts (transcript.go) hard-
// rejects the moment anything actually loads it (corpusPromptSHA256
// mismatch). Now this reads and parses the file exactly like readTranscript
// does and requires RequestID and CorpusPromptSHA256 to still match req —
// the same two checks LoadTranscripts itself runs — so a stale or
// content-mismatched file is never silently counted as captured. When it
// returns false for an id that DOES have a file on disk, runCapture's
// caller falls through to the missing/tombstone branch, which writes
// "<id>.missing.yaml" alongside the (untouched, still mismatched) stale
// "<id>.yaml" — deliberately not resolved automatically: LoadTranscripts
// hard-errors on an id carrying both, which is the loud, specific failure
// this situation deserves rather than a silently wrong manifest.
//
// No TOCTOU concern reading then trusting this file: destDir is written
// only by this process for the duration of one runCapture call (doc.go's
// invariants), never concurrently by anything else.
func requestIsCaptured(req Request, destDir string) bool {
	data, err := os.ReadFile(filepath.Join(destDir, req.ID+".yaml"))
	if err != nil {
		return false
	}
	var t Transcript
	if err := yaml.Unmarshal(data, &t); err != nil {
		return false
	}
	return t.RequestID == req.ID && t.CorpusPromptSHA256 == sha256Hex(req.Prompt)
}

// classifyRequestOutcome builds one request's RequestOutcome for
// runCapture's outcomes loop, and separately reports whether a Captured
// == true outcome came ONLY from requestIsCaptured's on-disk carry-forward
// (carriedForward) rather than from anything promoted THIS run. Split out
// from runCapture (M2/M5, round-5 review of #2814 — the same reason
// finishScopedRun, summarizePasses, and preserveMediansIfNothingPromoted
// were split out for earlier rounds: keeps runCapture's own cyclomatic
// complexity under gocyclo's threshold) — there is no independent
// testability reason for the split here either, unlike those three.
//
// capturedIDs is what scanAndPromoteScratch promoted THIS run (runCapture's
// own single source of truth for "captured"); lastFailureErr and
// safeReasons are runCapture's own per-id tracking of the most recent
// pass this run attempted that produced no completion, and its
// manifest-safe rendering — see their declarations in runCapture for the
// full reasoning.
func classifyRequestOutcome(
	req Request, destDir string, capturedIDs map[string]bool, lastFailureErr map[string]error, safeReasons map[string]string,
) (outcome RequestOutcome, carriedForward bool) {
	promotedThisRun := capturedIDs[req.ID]
	captured := promotedThisRun
	if !captured {
		// M5 (round-5 review of #2814): requestIsCaptured now reads and
		// shape/hash-validates the on-disk file rather than trusting a
		// bare os.Stat — see that function's own doc comment.
		captured = requestIsCaptured(req, destDir)
	}
	outcome = RequestOutcome{RequestID: req.ID, Captured: captured}
	if !captured {
		return outcome, false
	}
	if promotedThisRun {
		return outcome, false
	}

	// M2 (round-5 review of #2814): H2 dropped this — a run carrying an id
	// forward from an earlier run's real transcript is still captured
	// overall, but if THIS run's own attempt(s) never produced a completion
	// for it either (lastFailureErr still has an entry — every pass this
	// run tried came back with no completion, none of them ever deleted
	// it), that fact must not vanish from the only committed record of why
	// a run could not reproduce a request. FailureReason is otherwise
	// "always empty when Captured is true" (see its own doc comment) —
	// this is the one deliberate exception, and it stays empty whenever
	// this run DID reproduce the request on at least one pass.
	if _, failedThisRun := lastFailureErr[req.ID]; failedThisRun {
		reason := safeReasons[req.ID]
		if reason == "" {
			reason = "no completion recorded"
		}
		outcome.FailureReason = reason
	}
	return outcome, true
}

// preserveMediansIfNothingPromoted is runCapture's H2 fix (round-4 review of
// #2814): moved is what scanAndPromoteScratch actually promoted THIS run
// (runCapture's own single source of truth for "captured"); when it is
// empty, every scoreCandidatesList entry this run built was empty too, so
// passScores/degradedMedians — computed by summarizePasses from zero
// completions — are pure, uninformative zeros, not a real measurement of
// anything. Writing them into manifest.yaml would silently replace an
// EXISTING file's real, previously-measured
// Median*/PassScores/DegradedPasses/MediansUnreliable fields with a
// fabricated 0.00 baseline from a run that never generated anything to
// score — the reviewer's "a run that promoted nothing learned nothing about
// the corpus" concern, and the second half of the same failure mode the
// disposition-carry-forward fix in runCapture's own outcomes loop addresses
// for CapturedCount/MissingCount/RequestOutcomes: a run with nothing new to
// report must not look like a run that MEASURED zero quality.
//
// Returns passScores/degradedMedians unchanged when moved is non-empty
// (the common case), or when there is no existing manifest.yaml to load
// (LoadManifest errors) — a first-ever run has no real baseline to
// preserve, so the fresh, if uninformative, zeros stand; that scenario is
// also independently caught by reportCaptureCompleteness's
// captureCompletenessVerdict, which fails a run that attempted requests and
// captured none of them regardless of what this function does.
//
// M4 (round-5 review of #2814): the prior manifest is only reused when its
// OWN shape still matches this run's — prior.RequestCount == len(requests)
// AND prior.Passes == passes. Without that check, a corpus that grew (or a
// passes flag that changed) between runs let a shape-mismatched prior
// manifest's medians publish unchanged: reproduced with a corpus growing
// 4 -> 6 requests between two runs, the second (1 pass, provider dead)
// published requestCount: 6 with a 1.000 median carried straight from the
// OLD 4-request run, three passScores[].total still reading 4, and
// capturedAt/corpusCommitSha/captureCommand all refreshed to the NEW run —
// nothing in the file marked the medians as describing a different,
// smaller corpus. The documented backstop (captureCompletenessVerdict
// failing a run that captured NONE of its requests) does not catch this:
// H2's carry-forward keeps capturedCount nonzero (the 4 old requests are
// still really on disk), so that verdict never fires. When the shape
// doesn't match, THIS run's own (zero, honestly-measured-from-nothing)
// passScores/degradedMedians are kept instead of the prior's, and
// degradedMedians.allDegraded is forced true — reusing
// Manifest.MediansUnreliable's existing "do not trust this as a baseline"
// signal (and the same t.Errorf/CAPTURE_RESULT!=success path runCapture's
// own switch already gives that field) rather than inventing a parallel
// staleness field nothing else in the pipeline knows to check.
func preserveMediansIfNothingPromoted(
	t *testing.T, destDir string, moved []string, requests []Request, passes int,
	passScores []PassScore, degradedMedians passesSummary,
) ([]PassScore, passesSummary) {
	t.Helper()
	if len(moved) > 0 {
		return passScores, degradedMedians
	}

	prior, err := LoadManifest(filepath.Join(destDir, manifestFileName))
	if err != nil {
		return passScores, degradedMedians
	}

	if prior.RequestCount != len(requests) || prior.Passes != passes {
		t.Logf("this run promoted no new transcripts, and the existing manifest describes a different-shaped "+
			"run (requestCount %d, passes %d) than this one (requestCount %d, passes %d) — not safe to reuse "+
			"as this run's median/pass-score fields; flagging this run's medians unreliable instead of "+
			"publishing a shape-mismatched baseline (M4, round-5 review of #2814)",
			prior.RequestCount, prior.Passes, len(requests), passes)
		degradedMedians.allDegraded = true
		return passScores, degradedMedians
	}

	t.Logf("this run promoted no new transcripts — preserving the existing manifest's median/pass-score "+
		"fields (from a run of %d passes) rather than overwriting them with zeros this run never measured",
		prior.Passes)
	return prior.PassScores, passesSummary{
		degradedPasses: prior.DegradedPasses,
		validateRate:   prior.MedianValidatePassRate,
		semanticRate:   prior.MedianSemanticMatchRate,
		validateCount:  prior.MedianValidatePassCount,
		semanticCount:  prior.MedianSemanticMatchCount,
		sampleSize:     prior.MedianSampleSize,
		allDegraded:    prior.MediansUnreliable,
	}
}

// patchManifestForScopedRun updates an EXISTING manifest.yaml in destDir
// with a scoped re-capture's own RequestOutcomes (M1, round-3 review of
// #2814). Before this fix, runCapture promoted/tombstoned files for a
// scoped `-run TestCaptureTranscripts/<id>` re-capture (mutating the tree)
// and then returned early WITHOUT ever touching manifest.yaml — so
// re-capturing a previously-tombstoned id back into real data left the
// committed tree with "<id>.yaml" and no tombstone (correct — H3's own
// fix), while manifest.yaml still said captured: false for that id, with a
// stale failureReason and stale CapturedCount/MissingCount that nothing
// else ever catches (LoadTranscripts never reads the manifest at all — see
// its own doc comment). The tree loaded fine; the committed manifest.yaml
// just quietly contradicted it.
//
// Only scopedOutcomes' own ids are patched into RequestOutcomes — every
// OTHER entry, and the medians/PassScores (which this scoped run has no
// full-corpus data to recompute — see runCapture's own check on
// len(scoreCandidatesList) != passes), are left exactly as the last FULL
// run recorded them: a single re-captured request says nothing new about
// what the other requests scored. CapturedCount/MissingCount ARE
// recomputed (from the patched RequestOutcomes list), since those two
// fields exist specifically to summarize it.
//
// Returns ok == false when there is no existing manifest.yaml to patch —
// LoadManifest's own not-exist error — which is correct, not a fallback:
// a scoped run before any full run has ever completed has nothing to
// patch, and correctly leaves nothing behind (unchanged from this
// function's absence).
func patchManifestForScopedRun(t *testing.T, destDir string, scopedOutcomes []RequestOutcome) (Manifest, bool) {
	t.Helper()

	path := filepath.Join(destDir, manifestFileName)
	existing, err := LoadManifest(path)
	if err != nil {
		if cerrors.Is(err, os.ErrNotExist) {
			return Manifest{}, false
		}
		t.Fatalf("loading existing manifest %q to patch: %v", path, err)
	}

	// Built append-only from a zero-length slice (never make'd with a
	// non-zero length and appended onto afterward — golangci's makezero
	// linter flags exactly that pattern), even though every existing
	// entry is expected to be replaced or carried through unchanged.
	patched := make([]RequestOutcome, 0, len(existing.RequestOutcomes)+len(scopedOutcomes))

	byID := make(map[string]RequestOutcome, len(scopedOutcomes))
	for _, o := range scopedOutcomes {
		byID[o.RequestID] = o
	}
	for _, o := range existing.RequestOutcomes {
		if updated, ok := byID[o.RequestID]; ok {
			patched = append(patched, updated)
			delete(byID, o.RequestID)
			continue
		}
		patched = append(patched, o)
	}
	// Any scoped id not found in the existing manifest at all (unusual —
	// every corpus request should already be present from the last full
	// run) is appended rather than silently dropped, in corpus order
	// relative to scopedOutcomes.
	for _, o := range scopedOutcomes {
		if _, stillPending := byID[o.RequestID]; stillPending {
			patched = append(patched, o)
		}
	}

	captured, missing := 0, 0
	for _, o := range patched {
		if o.Captured {
			captured++
		} else {
			missing++
		}
	}

	existing.RequestOutcomes = patched
	existing.CapturedCount = captured
	existing.MissingCount = missing
	writeManifest(t, destDir, existing)
	return existing, true
}

// TestPatchManifestForScopedRun_NoExistingManifest_ReturnsFalse covers the
// one patchManifestForScopedRun branch nothing else here exercises: no
// prior manifest.yaml at all (a scoped run before any full run ever
// completed). ok must come back false, and nothing should be written to
// destDir — asserted directly, since a bug that wrote an empty/partial
// manifest.yaml here would be exactly the "manifest contradicts the tree"
// class of defect M1 exists to prevent.
func TestPatchManifestForScopedRun_NoExistingManifest_ReturnsFalse(t *testing.T) {
	destDir := t.TempDir()

	_, ok := patchManifestForScopedRun(t, destDir, []RequestOutcome{{RequestID: "req-a", Captured: true}})
	if ok {
		t.Fatal("ok = true, want false — there was no existing manifest.yaml to patch")
	}
	if _, err := os.Stat(filepath.Join(destDir, manifestFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no manifest.yaml to have been written, stat err = %v", err)
	}
}

// TestPatchManifestForScopedRun_AppendsUnknownID covers the defensive
// "scoped id not already in the existing manifest" branch: unusual (every
// corpus request should already be present from the last full run), but
// must append rather than silently drop the id.
func TestPatchManifestForScopedRun_AppendsUnknownID(t *testing.T) {
	destDir := t.TempDir()
	writeManifest(t, destDir, Manifest{
		SchemaVersion: TranscriptSchemaVersion,
		RequestCount:  1,
		CapturedCount: 1,
		RequestOutcomes: []RequestOutcome{
			{RequestID: "req-a", Captured: true},
		},
	})

	patched, ok := patchManifestForScopedRun(t, destDir, []RequestOutcome{
		{RequestID: "req-new", Captured: false, FailureReason: "generate.provider_error (HTTP 429)"},
	})
	if !ok {
		t.Fatal("ok = false, want true — an existing manifest.yaml was there to patch")
	}
	if len(patched.RequestOutcomes) != 2 {
		t.Fatalf("len(patched.RequestOutcomes) = %d, want 2 (req-a carried through, req-new appended)",
			len(patched.RequestOutcomes))
	}
	if patched.CapturedCount != 1 || patched.MissingCount != 1 {
		t.Fatalf("patched CapturedCount/MissingCount = %d/%d, want 1/1", patched.CapturedCount, patched.MissingCount)
	}
}

// requestsMissingUsableCandidate returns, for one pass's passCandidates, the
// corpus request ids that pass contributed no USABLE candidate for — either
// no completion was ever recorded for that request this pass (no map entry:
// passCandidates[req.ID] then reads as Go's zero value for string, "",
// exactly like an explicit empty entry would) or a completion WAS recorded
// but every attempt failed to extract pipeline YAML from it (an explicit
// empty or whitespace-only entry — B2, round-3 review of #2814: see
// runCapture's own inline comment on why passCandidates gets that empty
// entry rather than omitting the key in this case). Both cases are scored
// identically by ScoreRun (score.go) — a hard fail on both axes — so they
// are counted identically here too: a pass where the model replied with
// nothing but unparseable garbage for every request is exactly as degraded,
// for Manifest.DegradedPasses/MedianValidatePassRate purposes, as one the
// provider never answered at all.
//
// The result is in requests order (the same []Request slice for every pass
// of a run), never re-sorted — that makes it directly, positionally
// comparable across passes (allMissingSetsEqual) without a separate sort
// step. H1 (round-4 review of #2814): this used to return only a count,
// which cannot distinguish "every pass missed the same requests" (a stable,
// non-misleading partial result) from "each pass missed a DIFFERENT subset"
// (the rotating-429s case that made a corpus scoring 1.00 on every
// individual request publish a 0.20 median) — summarizePasses needs the
// actual id set, not just how many.
func requestsMissingUsableCandidate(requests []Request, passCandidates Candidates) []string {
	var missing []string
	for _, req := range requests {
		if strings.TrimSpace(passCandidates[req.ID]) == "" {
			missing = append(missing, req.ID)
		}
	}
	return missing
}

// allMissingSetsEqual reports whether every pass in sets missed exactly the
// same corpus request ids, in the same order. Every element of sets was
// built by requestsMissingUsableCandidate from the SAME requests slice (one
// call per pass, within a single runCapture invocation), so a set that
// misses the same ids as another pass always lists them in the same
// position — a direct, positional comparison is exact set equality here,
// not merely same-length.
//
// len(sets) < 2 is vacuously true: there is nothing to disagree with.
func allMissingSetsEqual(sets [][]string) bool {
	if len(sets) < 2 {
		return true
	}
	first := sets[0]
	for _, s := range sets[1:] {
		if !idSetEqual(s, first) {
			return false
		}
	}
	return true
}

// idSetEqual reports whether a and b name exactly the same ids in the same
// order — the positional comparison allMissingSetsEqual documents is safe
// for, factored out so missingSetsAreReliable (below) can reuse it against a
// set built from a DIFFERENT source (RequestOutcomes) than the two
// missing-set slices it was originally written to compare.
func idSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, id := range a {
		if b[i] != id {
			return false
		}
	}
	return true
}

// missingSetsAreReliable is the equal-missing-sets exemption's full
// soundness check: every pass in sets must miss the same ids (
// allMissingSetsEqual) AND none of those ids may be captured overall SOLELY
// through H2's on-disk carry-forward (carriedForwardIDs — runCapture's
// requestIsCaptured disk fallback, never this run's own promotion).
//
// H1 x H2 interaction (HIGH-1, round-5 review of #2814): the equal-sets
// exemption (H1, round-4 review) was justified on the premise that "a
// chronically-missing request is ALSO visible as Manifest.MissingCount > 0"
// — a reader who distrusts a stable-looking degraded median still has
// MissingCount and RequestOutcomes to point at the culprit. H2 (same round)
// broke that premise: requestIsCaptured now also reports a request captured
// when an EARLIER run already committed a real transcript for it, even if
// every pass THIS run attempted came back with nothing. Reproduced: 3
// requests, 3 passes, run 2 on the same destDir, req-b 429s on every call —
// every pass's missing set is identically [req-b] (the exemption's ONLY
// check, pre-fix), so allDegraded stayed false and MediansUnreliable was
// never set, even though req-b is captured this run ONLY via carry-forward
// and the median silently dropped from 1.000 to 0.667 with nothing else in
// the file explaining why. Requiring the agreed-upon missing set to be
// disjoint from carriedForwardIDs restores the exemption's original
// premise: an id in the missing set that is captured only by carry-forward
// is NOT visible via MissingCount (MissingCount == 0 for it), so the
// exemption must not apply.
func missingSetsAreReliable(sets [][]string, carriedForwardIDs []string) bool {
	if !allMissingSetsEqual(sets) {
		return false
	}
	if len(sets) == 0 || len(carriedForwardIDs) == 0 {
		return true
	}
	carried := make(map[string]bool, len(carriedForwardIDs))
	for _, id := range carriedForwardIDs {
		carried[id] = true
	}
	for _, id := range sets[0] {
		if carried[id] {
			return false
		}
	}
	return true
}

// passesSummary is summarizePasses' reduction of a capture run's per-pass
// RunScores down to the median rates/counts Manifest reports, plus which
// passes were excluded from that median (see summarizePasses' own doc
// comment).
type passesSummary struct {
	degradedPasses []int
	validateRate   float64
	semanticRate   float64
	validateCount  float64
	semanticCount  float64
	// sampleSize is how many passes actually contributed to
	// validateRate/semanticRate/validateCount/semanticCount above — the
	// number of clean passes ordinarily, or every pass (len(runs)) whenever
	// NO pass was clean (n == 0, the all-passes fallback) — REGARDLESS of
	// whether allDegraded ends up true or false for that run (M1-3, round-5
	// review of #2814: a prior version of this comment tied the fallback to
	// "allDegraded's fallback", implying they always coincide; they do not
	// — the chronic-single-missing-request case has n == 0, and therefore
	// this same len(runs) fallback, while allDegraded correctly stays
	// false). N3 (round-3 review of #2814):
	// exposed via Manifest.MedianSampleSize so a reader isn't left
	// inferring "how many passes is this median actually over" from
	// Passes minus len(DegradedPasses) — a median over 1 pass and a median
	// over 3 both render as one number without this.
	sampleSize int
	// allDegraded is true when every pass in runs was degraded (no pass
	// contributed a usable candidate for the full corpus) AND
	// missingSetsAreReliable is false for those degraded passes' missing
	// sets — see summarizePasses' doc comment for why both conditions are
	// required, and for what the four fields above mean in that case.
	// runCapture treats this as a hard failure (B1, round-3 review of
	// #2814), the same class of thing captureCompletenessVerdict already
	// does for attemptedCount == 0. preserveMediansIfNothingPromoted (M4,
	// round-5 review of #2814) also sets this directly, independent of
	// runs/missingSets, when a preserved prior manifest's own shape
	// (RequestCount/Passes) doesn't match this run's — see that function's
	// doc comment.
	//
	// H1 (round-4 review of #2814): this used to require "at least one
	// pass was WIPED (missing every request)" instead of "the missing sets
	// disagree" — a proxy that missed the actual failure mode: 5 passes
	// each capturing exactly one different request (never a whole pass
	// wiped, so the old condition never fired) still leaves n == 0 with a
	// DIFFERENT set of missing ids every time, which is exactly as
	// misleading as a wipe. See allMissingSetsEqual's doc comment.
	//
	// H1 x H2 (round-5 review of #2814): agreeing missing sets alone is
	// not enough either — see missingSetsAreReliable's own doc comment for
	// the carry-forward interaction that broke the round-4 exemption.
	allDegraded bool
}

// summarizePasses builds Manifest.PassScores and reduces runs (ms.Runs from
// ScoreMedian) to the median rates/counts the manifest reports, given
// missingSets — index-aligned with runs, the corpus request ids that pass
// contributed no usable candidate for (runCapture's scoreMissingSets,
// requestsMissingUsableCandidate). Split out from runCapture as its own
// function (kept runCapture's own cyclomatic complexity under gocyclo's
// threshold, and this reduction is an independently-testable concern in its
// own right).
//
// A pass with len(missingSets[i]) > 0 is "degraded" (see PassScore.
// MissingCount and Manifest.DegradedPasses for what that means and why —
// H2, round-2 review of #2814): it is excluded from the returned median so
// a wiped tail pass (a rate-limit storm, or captureWallClockBudget
// expiring partway through) doesn't silently drag
// MedianValidatePassRate/MedianSemanticMatchRate toward zero under a
// manifest that otherwise reports MissingCount == 0 (a request captured on
// an earlier pass but absent from a later one is NOT run-missing — see
// Manifest.CapturedCount's doc comment — but ScoreRun DOES score the
// absence on every pass that lacks it).
//
// When every single pass is degraded, there is no clean pass left to
// exclude anything FROM — the returned validateRate/semanticRate/
// validateCount/semanticCount fall back to the median across ALL passes
// (identical to this function's pre-H2 behavior, and mathematically
// identical to ScoreMedian's own reduction), which is NOT "the median
// across every clean pass" those fields otherwise mean, because
// scoreMissingSets still scores every missing request as a hard fail on
// both axes (score.go's ScoreRun) with no clean pass to dilute that.
//
// That fallback is not automatically UNTRUSTWORTHY, though: a single
// request that is chronically missing across every pass (the same one
// request, every time — ordinary live-provider noise, already handled as a
// routine partial result by captureCompletenessVerdict below its majority
// threshold) leaves every pass's own missing SET identical, and therefore
// every pass's own rate identical and stable too, so the all-passes median
// equals what a "clean-pass" median would have shown too. The case that IS
// misleading is when the degraded passes disagree about WHICH requests they
// missed — B1 (round-3 review of #2814) found this via the special case of
// one or more passes WIPED entirely (missing every single corpus request,
// which trivially disagrees with a partially-degraded pass's smaller
// missing set): the signature of captureWallClockBudget expiring before a
// pass even started, or a rate-limit storm eating the whole pass.
// Reproduced: 3 passes, 2 wiped down to zero after a wall-clock budget
// expiry midway through pass 1, computes a median of 0.00 even though every
// request captured on pass 1 validated cleanly — the fallback IS what
// produces that misleadingly low number, not a defense against one.
//
// H1 (round-4 review of #2814): "at least one pass was WIPED" is only ONE
// way the missing sets can disagree, and checking for it specifically
// missed the more general failure mode — 5 passes, each capturing exactly
// one different request and missing the other four (rotating 429s: pass p
// captures only request p) has NO wiped pass at all (every pass captures
// something) yet still disagrees pass to pass about which four requests it
// missed, and is exactly as misleading as a wipe: every individual request
// actually validates 1/1 when captured, but the old WIPED-only condition
// read this as a stable partial result and published a 0.20 median.
// allMissingSetsEqual replaces the narrower wipe check with the general
// one: are the degraded passes missing the SAME requests, or different
// ones. allDegraded (below) is true only when BOTH conditions hold (no
// clean pass AND the missing sets disagree), so the caller can fail the
// run for the actual failure mode without also failing on ordinary
// single-request flakiness — see runCapture's own handling and
// Manifest.MediansUnreliable.
//
// H1 x H2 (HIGH-1, round-5 review of #2814): agreeing missing sets are
// necessary but no longer sufficient — carriedForwardIDs (runCapture's
// requestIsCaptured disk fallback: an id captured overall ONLY because an
// earlier run already committed a real transcript for it, never because
// THIS run reproduced it) is what the equal-sets exemption's premise
// ("chronically missing is also visible as MissingCount > 0") silently
// depends on. When a degraded pass's agreed-upon missing set contains an id
// that is only captured via carry-forward, that premise is false — the id
// reads as fully captured (MissingCount == 0) even though every pass this
// run attempted came back with nothing for it — so the exemption must not
// apply. See missingSetsAreReliable's own doc comment for the full
// reasoning and a worked repro.
func summarizePasses(runs []RunScore, missingSets [][]string, carriedForwardIDs []string) ([]PassScore, passesSummary) {
	passScores := make([]PassScore, len(runs))
	allValidateRates := make([]float64, len(runs))
	allSemanticRates := make([]float64, len(runs))
	allValidateCounts := make([]int, len(runs))
	allSemanticCounts := make([]int, len(runs))

	var degradedPasses []int
	var degradedMissingSets [][]string
	var cleanValidateRates, cleanSemanticRates []float64
	var cleanValidateCounts, cleanSemanticCounts []int

	for i, rs := range runs {
		passMissing := len(missingSets[i])
		passScores[i] = PassScore{
			Pass:               i + 1,
			Total:              rs.Total,
			ValidatePassCount:  rs.ValidatePassCount,
			ValidatePassRate:   rs.ValidatePassRate,
			SemanticMatchCount: rs.SemanticMatchCount,
			SemanticMatchRate:  rs.SemanticMatchRate,
			MissingCount:       passMissing,
		}
		allValidateRates[i] = rs.ValidatePassRate
		allSemanticRates[i] = rs.SemanticMatchRate
		allValidateCounts[i] = rs.ValidatePassCount
		allSemanticCounts[i] = rs.SemanticMatchCount

		if passMissing > 0 {
			degradedPasses = append(degradedPasses, i+1)
			degradedMissingSets = append(degradedMissingSets, missingSets[i])
			continue
		}
		cleanValidateRates = append(cleanValidateRates, rs.ValidatePassRate)
		cleanSemanticRates = append(cleanSemanticRates, rs.SemanticMatchRate)
		cleanValidateCounts = append(cleanValidateCounts, rs.ValidatePassCount)
		cleanSemanticCounts = append(cleanSemanticCounts, rs.SemanticMatchCount)
	}

	summary := passesSummary{
		degradedPasses: degradedPasses,
		validateRate:   median(allValidateRates),
		semanticRate:   median(allSemanticRates),
		validateCount:  medianInt(allValidateCounts),
		semanticCount:  medianInt(allSemanticCounts),
		sampleSize:     len(runs),
	}
	n := len(cleanValidateRates)
	if n > 0 && n < len(runs) {
		summary.validateRate = median(cleanValidateRates)
		summary.semanticRate = median(cleanSemanticRates)
		summary.validateCount = medianInt(cleanValidateCounts)
		summary.semanticCount = medianInt(cleanSemanticCounts)
		summary.sampleSize = n
	}
	// allDegraded requires BOTH no clean pass (n == 0) AND the degraded
	// passes' missing sets failing missingSetsAreReliable — n == 0 alone is
	// not enough: a single request that is chronically missing across every
	// pass (present in no pass's candidates at all, but the SAME one
	// request every time, so every degraded pass's missing set is
	// identical) also leaves n == 0, yet every pass's rate is identical and
	// stable — reporting the all-passes median in that case is not
	// misleading, it is the true answer, and this is exactly the "normal
	// partial result" scenario captureCompletenessVerdict already treats as
	// routine below its majority threshold, PROVIDED that request is not
	// ALSO being reported captured through disk carry-forward (H1 x H2,
	// round-5 review of #2814 — see missingSetsAreReliable's own doc
	// comment). Comparing the actual missing-id sets (H1, round-4 review of
	// #2814) narrows this to the real failure mode: passes that disagree
	// about which requests they captured, whether that disagreement takes
	// the form of one pass wiped entirely (B1, round-3 review of #2814) or
	// several passes each capturing a different rotating subset.
	summary.allDegraded = n == 0 && !missingSetsAreReliable(degradedMissingSets, carriedForwardIDs)
	return passScores, summary
}

// captureCompletenessThreshold is the fraction of ATTEMPTED requests
// (missingCount / attemptedCount) that must be missing before a capture run
// is failed outright rather than merely noted — see
// captureCompletenessVerdict for the full reasoning. A strict majority: the
// incident this exists to fix captured 27/28 (3.6% missing) and should have
// been treated as a normal, publishable partial result, not a reason to
// discard 27 already-paid-for transcripts.
const captureCompletenessThreshold = 0.5

// completenessSink is the minimal *testing.T surface
// reportCaptureCompleteness needs. It exists so a test can supply a fake
// implementation and observe whether the fail branch fired, without needing
// a real *testing.T — a hand-constructed &testing.T{} is not safe to use
// (its internal state is only valid when created by the testing package's
// own Run), so there is no other way to exercise this wiring's fail path
// without also failing whatever test triggers it.
type completenessSink interface {
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

// reportCaptureCompleteness wraps captureCompletenessVerdict's decision and
// reports it to sink — split out from runCapture so both the fail and
// no-fail branches are directly testable (TestReportCaptureCompleteness)
// without spending a real *testing.T's failure on a scenario that is
// SUPPOSED to fail.
//
// It calls captureCompletenessVerdict whenever there is anything to report
// on — a nonzero missingCount, OR attemptedCount itself being zero (see that
// function's doc comment for why zero attempted must never be silently
// treated as "nothing to report"). The only case that reports nothing at
// all is a clean, fully-attempted run.
func reportCaptureCompleteness(sink completenessSink, attemptedCount, capturedCount, missingCount int, outcomes []RequestOutcome) {
	if attemptedCount > 0 && missingCount == 0 {
		return
	}
	fail, msg := captureCompletenessVerdict(attemptedCount, capturedCount, missingCount, outcomes)
	if fail {
		sink.Errorf("%s", msg)
	} else {
		sink.Logf("%s", msg)
	}
}

// captureCompletenessVerdict is runCapture's threshold decision, pure and
// directly testable (TestCaptureCompletenessVerdict) without a fake
// provider or *testing.T: given the final captured/missing split for
// whatever requests this run actually attempted, it reports whether the run
// should be failed and the message that failure (or note) should carry —
// always naming both what WAS captured and what was NOT (missing id and
// reason), per the requirement that a reader never has to infer a failure
// from an absent file.
//
// attemptedCount == 0 is ALWAYS a hard failure, regardless of
// missingCount (which is also always 0 in that case — capturedCount +
// missingCount == attemptedCount is an invariant of how runCapture builds
// outcomes). A `-run` pattern or a `request_id` workflow input that matches
// nothing in the corpus (a typo, or an id no longer in
// testdata/eval_requests.yaml) attempts ZERO requests — the OLD unconditional
// `attemptedCount == 0 || missingCount == 0 -> return false, ""` guard let a
// run like that exit clean with an empty message, which reportCaptureCompleteness
// would then treat as "nothing to report" and generate-capture.yml's `capture`
// job would exit green having captured and published nothing at all.
//
// Below the threshold — including one flaky 429, one pass tripping a
// ceiling, or a single rate-limited request — this is ordinary live-provider
// noise, not a build-breaking event: the captured transcripts are exactly as
// valid as a clean run's, and the missing id is a normal, cheap follow-up
// (`-run TestCaptureTranscripts/<id>`, ~$0.03 per plan §4's per-request
// cost). fail is false, and the returned message is still non-empty so
// runCapture can log (not fail on) it.
//
// At or past a strict majority missing, something systemic broke — a
// revoked key, a provider outage, a ceiling misconfigured too low (plan §10
// failure modes #7-9), or systematic model refusal (RequestOutcome.Unusable
// — the provider responded on every attempt, but with a decode failure or a
// well-formed, empty completion; N8, round-3 review of #2814: named as its
// own cause now that Unusable exists to distinguish it from the other
// three, which are transport-level misses where no response arrived at
// all) — and the artifact is no longer representative of the corpus it
// claims to measure. fail is true. Note that a true verdict does NOT mean
// nothing gets preserved: runCapture promotes and writes the manifest for
// whatever WAS captured regardless of this verdict — this function
// controls only how loud the `go test` signal is.
func captureCompletenessVerdict(attemptedCount, capturedCount, missingCount int, outcomes []RequestOutcome) (fail bool, message string) {
	if attemptedCount == 0 {
		return true, fmt.Sprintf(
			"0/%d captured, %d/%d missing (zero requests were attempted) — the run captured nothing to report "+
				"on; check the -run pattern or request_id input (one that matches nothing in the corpus silently "+
				"attempts zero requests) and the corpus file itself",
			attemptedCount, missingCount, attemptedCount,
		)
	}
	if missingCount == 0 {
		return false, ""
	}

	missingIDs := make([]string, 0, missingCount)
	for _, o := range outcomes {
		if !o.Captured {
			missingIDs = append(missingIDs, fmt.Sprintf("%s (%s)", o.RequestID, o.FailureReason))
		}
	}
	missingFrac := float64(missingCount) / float64(attemptedCount)

	if missingFrac > captureCompletenessThreshold {
		return true, fmt.Sprintf(
			"most of this run failed to capture anything: %d/%d captured, %d/%d missing (%.0f%%) — "+
				"this is not a usable partial artifact; investigate before re-running (a revoked key, a "+
				"provider outage, a ceiling set too low, or systematic model refusal are the likely causes — "+
				"plan §10 failure modes #7-9, and check requestOutcomes[].unusable in the manifest below to "+
				"tell a refusal apart from the other three). Whatever WAS captured is still promoted and "+
				"recorded below. missing: %s",
			capturedCount, attemptedCount, missingCount, attemptedCount, missingFrac*100, strings.Join(missingIDs, ", "),
		)
	}

	return false, fmt.Sprintf(
		"%d/%d captured, %d/%d missing (%.1f%%, at or below the %.0f%% run-failing threshold) — "+
			"a normal partial result from live-provider noise; re-capture the missing id(s) with "+
			"-run TestCaptureTranscripts/<id> when convenient. missing: %s",
		capturedCount, attemptedCount, missingCount, attemptedCount, missingFrac*100, captureCompletenessThreshold*100,
		strings.Join(missingIDs, ", "),
	)
}

// captureCommandString renders the invocation for Manifest.CaptureCommand,
// with the API key always redacted — this string is committed to the repo.
func captureCommandString(passes int, model string) string {
	return fmt.Sprintf(
		"CONDUIT_GENERATE_CAPTURE=1 %s=*** go test -tags=generate_capture -count=1 -timeout 30m "+
			"-run TestCaptureTranscripts ./cmd/conduit/internal/generate/... "+
			"# %s=%d %s=%s",
		provider.EnvAnthropicKey, envCapturePasses, passes, envCaptureModel, model,
	)
}

// medianInt reuses score.go's unexported median() (same package) over int
// counts, so the manifest's median counts and ScoreMedian's median rates are
// computed by the exact same reduction — never two independently-written
// median implementations that could silently disagree.
func medianInt(counts []int) float64 {
	vals := make([]float64, len(counts))
	for i, c := range counts {
		vals[i] = float64(c)
	}
	return median(vals)
}

// --- Guard and ceiling proofs (AC 1.22) ---
//
// Every test below runs with no consent, no key, and no network — they prove
// the DECISION logic (guards, ceilings), never the live capture path itself.

// TestDecideCaptureGuard proves AC 1.22's refusal-not-default discipline:
// consent is checked BEFORE the key, an unset or non-"1" consent value always
// skips (even with a key present), and only "consent + key" proceeds.
func TestDecideCaptureGuard(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want captureGuardDecision
	}{
		{
			name: "no consent, no key -> skip",
			env:  map[string]string{},
			want: captureSkipNoConsent,
		},
		{
			name: "key present but no consent -> skip (the key alone is not consent)",
			env:  map[string]string{provider.EnvAnthropicKey: "sk-ant-test"},
			want: captureSkipNoConsent,
		},
		{
			name: "consent misspelled (\"true\" not \"1\") -> skip, refusal is not lenient",
			env:  map[string]string{envCaptureConsent: "true", provider.EnvAnthropicKey: "sk-ant-test"},
			want: captureSkipNoConsent,
		},
		{
			name: "consent given, no key -> fatal",
			env:  map[string]string{envCaptureConsent: "1"},
			want: captureFatalNoKey,
		},
		{
			name: "consent given, key present -> proceed",
			env:  map[string]string{envCaptureConsent: "1", provider.EnvAnthropicKey: "sk-ant-test"},
			want: captureProceed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := func(k string) string { return tt.env[k] }
			got, msg := decideCaptureGuard(env)
			if got != tt.want {
				t.Fatalf("decideCaptureGuard() = %v, want %v (msg: %q)", got, tt.want, msg)
			}
			if got != captureProceed && msg == "" {
				t.Fatal("a refusal must always carry an explanatory message")
			}
		})
	}
}

// TestCapturePassCount proves the pass-count guard refuses (never clamps) a
// bad value, and defaults correctly when unset.
func TestCapturePassCount(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    int
		wantErr bool
	}{
		{name: "unset -> default", env: map[string]string{}, want: defaultCapturePasses},
		{name: "valid override", env: map[string]string{envCapturePasses: "5"}, want: 5},
		{name: "zero -> error, never clamped to 1", env: map[string]string{envCapturePasses: "0"}, wantErr: true},
		{name: "negative -> error", env: map[string]string{envCapturePasses: "-1"}, wantErr: true},
		{name: "not a number -> error", env: map[string]string{envCapturePasses: "many"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := func(k string) string { return tt.env[k] }
			got, err := capturePassCount(env)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("capturePassCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCaptureCallCeiling proves the derived ceiling is bounded by
// captureAbsoluteMaxCalls in every direction — a huge pass/request count
// falls back to the absolute cap rather than growing past it.
func TestCaptureCallCeiling(t *testing.T) {
	tests := []struct {
		name         string
		passes, reqs int
		want         int
	}{
		{name: "default shape (3 passes, 28 requests)", passes: 3, reqs: 28, want: 3 * 28 * DefaultMaxAttempts},
		{name: "huge pass count falls back to the absolute cap", passes: 1000, reqs: 28, want: captureAbsoluteMaxCalls},
		{name: "zero requests falls back to the absolute cap", passes: 3, reqs: 0, want: captureAbsoluteMaxCalls},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureCallCeiling(tt.passes, tt.reqs)
			if got != tt.want {
				t.Fatalf("captureCallCeiling(%d, %d) = %d, want %d", tt.passes, tt.reqs, got, tt.want)
			}
			if got > captureAbsoluteMaxCalls {
				t.Fatalf("captureCallCeiling(%d, %d) = %d exceeds the absolute cap %d", tt.passes, tt.reqs, got, captureAbsoluteMaxCalls)
			}
		})
	}
}

// TestCaptureProvider_Ceilings proves each of the three ceilings is
// STRUCTURALLY enforced against a fake provider — no network, no key: once a
// ceiling trips, the wrapped fake's own call count stops advancing, meaning
// the ceiling refused the call BEFORE it ever reached the (in a real run,
// live and billed) wrapped provider.
func TestCaptureProvider_Ceilings(t *testing.T) {
	t.Run("max calls", func(t *testing.T) {
		fake := &fakeProvider{replies: []string{"reply"}, tokens: 1}
		cp := &captureProvider{Provider: fake, maxCalls: 2, maxTokens: 1_000_000}

		for i := 0; i < 2; i++ {
			if _, err := cp.Complete(context.Background(), provider.CompletionRequest{}); err != nil {
				t.Fatalf("call %d: unexpected error: %v", i+1, err)
			}
		}
		if _, err := cp.Complete(context.Background(), provider.CompletionRequest{}); err == nil {
			t.Fatal("expected the 3rd call to trip the max-calls ceiling")
		}
		if len(fake.requests) != 2 {
			t.Fatalf("wrapped provider must not be called once the ceiling trips: got %d call(s), want 2", len(fake.requests))
		}
	})

	t.Run("max tokens", func(t *testing.T) {
		fake := &fakeProvider{replies: []string{"reply"}, tokens: 100}
		cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 150}

		if _, err := cp.Complete(context.Background(), provider.CompletionRequest{}); err != nil {
			t.Fatalf("call 1: unexpected error: %v", err)
		}
		// 100 tokens used, ceiling is 150 — the pre-call check only refuses
		// once c.tokens >= maxTokens, so a 2nd 100-token call is still allowed
		// through (100 < 150) but pushes the running total to 200.
		if _, err := cp.Complete(context.Background(), provider.CompletionRequest{}); err != nil {
			t.Fatalf("call 2: unexpected error: %v", err)
		}
		if _, err := cp.Complete(context.Background(), provider.CompletionRequest{}); err == nil {
			t.Fatal("expected the 3rd call to trip the max-tokens ceiling (200 already used >= 150 max)")
		}
		if len(fake.requests) != 2 {
			t.Fatalf("wrapped provider must not be called once the ceiling trips: got %d call(s), want 2", len(fake.requests))
		}
	})

	t.Run("wall-clock deadline", func(t *testing.T) {
		fake := &fakeProvider{replies: []string{"reply"}, tokens: 1}
		cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 1_000_000}

		ctx, cancel := context.WithTimeout(context.Background(), 0) // already expired
		defer cancel()
		<-ctx.Done()

		if _, err := cp.Complete(ctx, provider.CompletionRequest{}); err == nil {
			t.Fatal("expected an expired context to trip the wall-clock ceiling")
		}
		if len(fake.requests) != 0 {
			t.Fatalf("wrapped provider must not be called once the deadline has passed: got %d call(s), want 0", len(fake.requests))
		}
	})

	t.Run("recorded turns and totals reflect only calls that reached the wrapped provider", func(t *testing.T) {
		fake := &fakeProvider{replies: []string{"first", "second", "third"}, tokens: 10}
		cp := &captureProvider{Provider: fake, maxCalls: 2, maxTokens: 1_000_000}

		cp.startRequest()
		for i := 0; i < 3; i++ {
			_, _ = cp.Complete(context.Background(), provider.CompletionRequest{})
		}
		turns := cp.recordedTurns()
		if len(turns) != 2 {
			t.Fatalf("recordedTurns() = %d entries, want 2 (the 3rd call was refused, not recorded)", len(turns))
		}
		if cp.totalCalls() != 2 {
			t.Fatalf("totalCalls() = %d, want 2", cp.totalCalls())
		}
		if cp.totalTokens() != 20 {
			t.Fatalf("totalTokens() = %d, want 20", cp.totalTokens())
		}
	})
}

// --- Full-pipeline proofs against a fake provider (no key, no network) ---
//
// The tests above prove the guards and ceilings in isolation. These two prove
// the pipeline end to end against fakeProvider (generate_test.go) and a
// scratch destDir, never testdata/transcripts: the first drives runCapture in
// full (transcript construction, scoring, manifest provenance); the second
// drives scanAndPromoteScratch directly to prove the redaction gate's
// all-or-nothing promise without needing a "this subtest is expected to fail"
// trick (see scanAndPromoteScratch's own doc comment for why).

// capturePipelineRequest builds a Request whose prompt and Expect exactly
// match what generate_test.go's own fakeProvider tests use with
// validCandidate (the generator -> log pipeline), so Generate resolves it in
// exactly ONE attempt — no retry, no risk of a shared fakeProvider's
// call-indexed reply script drifting out of sync across requests.
func capturePipelineRequest(id string) Request {
	return Request{
		ID:     id,
		Prompt: "read from the generator and write to the log",
		Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
	}
}

func TestRunCapture_FullPipeline_FakeProvider(t *testing.T) {
	requests := []Request{capturePipelineRequest("req-a"), capturePipelineRequest("req-b")}
	fake := &fakeProvider{replies: []string{"```yaml\n" + validCandidate + "```"}, tokens: 50}
	cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 1_000_000}

	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")
	const passes = 2

	manifest, _, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", passes, destDir)

	if !wrote {
		t.Fatal("expected a full-corpus run over both requests, across both passes, to write a manifest")
	}
	if manifest.RequestCount != len(requests) {
		t.Fatalf("manifest.RequestCount = %d, want %d", manifest.RequestCount, len(requests))
	}
	if manifest.Passes != passes {
		t.Fatalf("manifest.Passes = %d, want %d", manifest.Passes, passes)
	}
	if manifest.MedianValidatePassRate != 1 {
		t.Fatalf("manifest.MedianValidatePassRate = %v, want 1 (validCandidate always validates)", manifest.MedianValidatePassRate)
	}
	if manifest.TotalTokensUsed != fake.tokens*len(fake.requests) {
		t.Fatalf("manifest.TotalTokensUsed = %d, want %d (tokens-per-call * calls made)",
			manifest.TotalTokensUsed, fake.tokens*len(fake.requests))
	}
	if manifest.CaptureCommand == "" || strings.Contains(manifest.CaptureCommand, "sk-ant") {
		t.Fatalf("manifest.CaptureCommand must be set and never carry a real key: %q", manifest.CaptureCommand)
	}

	for _, req := range requests {
		path := filepath.Join(destDir, req.ID+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected %q to have been promoted: %v", path, err)
		}
		var tr Transcript
		if err := yaml.Unmarshal(data, &tr); err != nil {
			t.Fatalf("parsing promoted transcript %q: %v", path, err)
		}
		if tr.RequestID != req.ID {
			t.Fatalf("transcript %q: requestID = %q, want %q", path, tr.RequestID, req.ID)
		}
		if len(tr.Turns) != 1 {
			t.Fatalf("transcript %q: %d turn(s), want 1 (single-attempt happy path)", path, len(tr.Turns))
		}
		if tr.Turns[0].CompletionText != fake.replies[0] {
			t.Fatalf("transcript %q: CompletionText must be VERBATIM, fences and all", path)
		}
		if !tr.Outcome.ValidatePass {
			t.Fatalf("transcript %q: Outcome.ValidatePass = false, want true", path)
		}
	}

	if _, err := os.Stat(filepath.Join(destDir, manifestFileName)); err != nil {
		t.Fatalf("expected manifest.yaml to have been written to destDir: %v", err)
	}
}

// failOnPromptProvider wraps a fakeProvider but forces every call whose
// PROMPT contains marker to fail with err, regardless of pass or retry —
// simulating "this one corpus request never produces a completion, ever"
// (a 429 that never clears, a revoked scope, …) without needing a new field
// on fakeProvider itself (whose own `err` applies to every call
// unconditionally, which cannot express "only THIS request fails").
// Everything else is delegated to the wrapped fakeProvider unchanged.
type failOnPromptProvider struct {
	*fakeProvider
	marker string
	err    error
}

func (p *failOnPromptProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if strings.Contains(req.Prompt, p.marker) {
		return provider.CompletionResult{}, p.err
	}
	return p.fakeProvider.Complete(ctx, req)
}

// TestRunCapture_PartialFailure_PreservesGoodTranscripts is this package's
// regression test for the incident described in the file's "Partial
// results" doc comment: a 3-passes-over-3-requests run where ONE request
// (req-b) never produces a single completion on ANY pass — a permanent,
// simulated 429 — while req-a and req-c succeed on every pass. 1/3 missing
// (33%) is below captureCompletenessThreshold, so this proves the run
// SUCCEEDS (no t.Errorf — this test calls runCapture with its own real *t,
// so an unexpected Errorf here would fail this test too) while still
// promoting the two good transcripts and recording req-b's absence
// honestly, both in the promoted files and in the manifest.
// partialFailureFixture builds the shared 3-request, 1-permanent-failure
// scenario TestRunCapture_PartialFailure_PreservesGoodTranscripts and
// TestRunCapture_PartialFailure_TombstoneAndBijection both drive runCapture
// with — split into its own function (rather than duplicated) so the two
// tests stay independently under gocyclo's complexity limit while still
// exercising the identical setup.
func partialFailureFixture(t *testing.T) (requests []Request, cp *captureProvider, destDir string, infraErr error) {
	t.Helper()
	const failMarker = "TRIGGER_INFRA_FAILURE"
	requests = []Request{
		capturePipelineRequest("req-a"),
		{
			ID:     "req-b",
			Prompt: "read from the generator and write to the log " + failMarker,
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
		capturePipelineRequest("req-c"),
	}

	reply := "```yaml\n" + validCandidate + "```"
	base := &fakeProvider{replies: []string{reply}, tokens: 20}
	infraErr = fmt.Errorf("429: rate limited")
	fp := &failOnPromptProvider{fakeProvider: base, marker: failMarker, err: infraErr}
	cp = &captureProvider{Provider: fp, maxCalls: 1000, maxTokens: 1_000_000}

	destDir = filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")
	return requests, cp, destDir, infraErr
}

func TestRunCapture_PartialFailure_PreservesGoodTranscripts(t *testing.T) {
	requests, cp, destDir, infraErr := partialFailureFixture(t)
	const passes = 3

	manifest, _, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", passes, destDir)

	if !wrote {
		t.Fatal("expected the manifest to still be written: 1/3 missing is below the run-failing threshold")
	}
	if manifest.CapturedCount != 2 {
		t.Fatalf("manifest.CapturedCount = %d, want 2", manifest.CapturedCount)
	}
	if manifest.MissingCount != 1 {
		t.Fatalf("manifest.MissingCount = %d, want 1", manifest.MissingCount)
	}

	// The two good transcripts must be promoted — the whole point of this
	// fix is that req-b's failure never discards them.
	for _, id := range []string{"req-a", "req-c"} {
		if _, err := os.Stat(filepath.Join(destDir, id+".yaml")); err != nil {
			t.Fatalf("expected %q to have been promoted despite req-b's failure: %v", id, err)
		}
	}
	// req-b never produced a completion, so no transcript can exist for it —
	// promoting an empty/fabricated one would be worse than promoting
	// nothing.
	if _, err := os.Stat(filepath.Join(destDir, "req-b.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected req-b to have NO transcript, stat err = %v", err)
	}

	var gotReqB RequestOutcome
	found := false
	for _, o := range manifest.RequestOutcomes {
		if o.RequestID == "req-b" {
			gotReqB, found = o, true
		}
	}
	if !found {
		t.Fatal("expected manifest.RequestOutcomes to include an entry for req-b")
	}
	if gotReqB.Captured {
		t.Fatal("req-b outcome: Captured = true, want false")
	}
	if gotReqB.FailureReason == "" {
		t.Fatal("req-b outcome: FailureReason must explain why it is missing, got empty string")
	}
	if !strings.Contains(gotReqB.FailureReason, infraErr.Error()) {
		t.Fatalf("req-b outcome: FailureReason = %q, want it to name the underlying error %q", gotReqB.FailureReason, infraErr.Error())
	}

	for _, id := range []string{"req-a", "req-c"} {
		for _, o := range manifest.RequestOutcomes {
			if o.RequestID == id && (!o.Captured || o.FailureReason != "") {
				t.Fatalf("%s outcome = %+v, want Captured=true and no FailureReason", id, o)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(destDir, manifestFileName)); err != nil {
		t.Fatalf("expected manifest.yaml to have been written despite the partial failure: %v", err)
	}

	// The infra error is a plain fmt.Errorf, not a conduiterr — it takes
	// safeFailureReason's literal-text fallback path (see that function's
	// doc comment), so its text is preserved verbatim here. This is
	// distinct from TestRunCapture_ProviderHTTPErrorNeverLeaksResponseBodyIntoManifest,
	// which proves the structured (non-fallback) path for a REAL provider
	// error never carries response-body text at all.
	if gotReqB.Unusable {
		t.Fatal("req-b outcome: Unusable = true, want false — a 429 is absence of data, not a billed refusal")
	}
}

// flakyAfterFirstCallProvider succeeds the FIRST time it sees a request
// whose prompt contains marker, then fails every subsequent time — a
// request that captures cleanly on an early pass and then drops out of
// every later one, simulating a rate-limit storm or captureWallClockBudget
// expiring partway through a multi-pass run. Everything else is delegated
// to the wrapped fakeProvider unchanged.
type flakyAfterFirstCallProvider struct {
	*fakeProvider
	marker string
	err    error

	mu   sync.Mutex
	seen int
}

func (p *flakyAfterFirstCallProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if strings.Contains(req.Prompt, p.marker) {
		p.mu.Lock()
		p.seen++
		n := p.seen
		p.mu.Unlock()
		if n > 1 {
			return provider.CompletionResult{}, p.err
		}
	}
	return p.fakeProvider.Complete(ctx, req)
}

// TestRunCapture_DegradedTailPass_ExcludedFromMedian is the regression test
// for H2 (round-2 review of #2814): a request captured on pass 1 but absent
// from every later pass is NOT run-missing (runCapture's capturedIDs check
// only needs ONE pass to have produced it — manifest.MissingCount stays 0
// here) — but score.go's ScoreRun scores that request as a hard FAIL on
// every pass that lacks it, dragging medianValidatePassRate/
// medianSemanticMatchRate toward zero under a manifest that otherwise
// looks completely clean (MissingCount == 0 means
// reportCaptureCompleteness has nothing to say, and generate-capture.yml's
// banner is gated on MISSING, not on this).
//
// Unlike M1's weak regression test (transcript_capture_test.go's own review
// history), this one drives the real runCapture end to end rather than
// hand-building a Candidates map — reverting the clean-pass-median fix in
// runCapture (i.e. always using ms.ValidatePassRate/ms.SemanticMatchRate
// computed over ALL passes) makes this test fail: with req-b flaky on
// passes 2-3, the naive full-corpus median comes out to 2/3 (0.667), not 1.
func TestRunCapture_DegradedTailPass_ExcludedFromMedian(t *testing.T) {
	const flakyMarker = "TRIGGER_TAIL_FLAKE"
	requests := []Request{
		capturePipelineRequest("req-a"),
		{
			ID:     "req-b",
			Prompt: "read from the generator and write to the log " + flakyMarker,
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
		capturePipelineRequest("req-c"),
	}

	reply := "```yaml\n" + validCandidate + "```"
	base := &fakeProvider{replies: []string{reply}, tokens: 20}
	flakyErr := fmt.Errorf("429: rate limited")
	fp := &flakyAfterFirstCallProvider{fakeProvider: base, marker: flakyMarker, err: flakyErr}
	cp := &captureProvider{Provider: fp, maxCalls: 1000, maxTokens: 1_000_000}

	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")
	const passes = 3

	manifest, _, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", passes, destDir)

	if !wrote {
		t.Fatal("expected the manifest to still be written: req-b captured on pass 1, so nothing is run-missing")
	}
	if manifest.CapturedCount != 3 {
		t.Fatalf("manifest.CapturedCount = %d, want 3 (req-b captured on pass 1 counts as captured overall)", manifest.CapturedCount)
	}
	if manifest.MissingCount != 0 {
		t.Fatalf("manifest.MissingCount = %d, want 0 — this is exactly the H2 scenario: run-missing and "+
			"pass-missing are different things", manifest.MissingCount)
	}

	wantDegraded := []int{2, 3}
	if !reflect.DeepEqual(manifest.DegradedPasses, wantDegraded) {
		t.Fatalf("manifest.DegradedPasses = %v, want %v", manifest.DegradedPasses, wantDegraded)
	}
	if len(manifest.PassScores) != passes {
		t.Fatalf("len(manifest.PassScores) = %d, want %d", len(manifest.PassScores), passes)
	}
	wantPassMissing := []int{0, 1, 1}
	for i, ps := range manifest.PassScores {
		if ps.MissingCount != wantPassMissing[i] {
			t.Fatalf("PassScores[%d].MissingCount = %d, want %d", i, ps.MissingCount, wantPassMissing[i])
		}
	}

	// The regression this test guards against: only pass 1 is clean (all 3
	// requests validate), so the median MUST be computed over pass 1 alone
	// — 1.0 — never diluted by passes 2-3's req-b-scored-as-fail 2/3 rate.
	if manifest.MedianValidatePassRate != 1 {
		t.Fatalf("manifest.MedianValidatePassRate = %v, want 1 — a degraded tail pass must not drag this "+
			"toward zero when MissingCount is 0 (H2)", manifest.MedianValidatePassRate)
	}
	if manifest.MedianSemanticMatchRate != 1 {
		t.Fatalf("manifest.MedianSemanticMatchRate = %v, want 1 — same regression, semantic axis", manifest.MedianSemanticMatchRate)
	}
	// N3 (round-3 review of #2814): the median above is over exactly 1
	// clean pass, not all 3 — MedianSampleSize is what lets a reader see
	// that without computing Passes - len(DegradedPasses) themselves.
	if manifest.MedianSampleSize != 1 {
		t.Fatalf("manifest.MedianSampleSize = %d, want 1 (only pass 1 was clean)", manifest.MedianSampleSize)
	}
}

// TestRunCapture_SecondRunCapturesNothing_PreservesFirstRunsGoodManifest is
// the regression test for H2 (round-4 review of #2814): a run that promotes
// NOTHING new must not overwrite a manifest.yaml that already describes a
// fully-captured, well-scoring corpus with Captured: false dispositions and
// a 0.00 median, for requests that are still sitting right there on disk
// with real data.
//
// Reproduced against the pre-fix code before writing this test: run 1
// captures req-a and req-b cleanly (manifest: CapturedCount=2,
// MissingCount=0, MedianValidatePassRate=1). Run 2, same destDir, same
// requests, provider now permanently down from the very first call —
// nothing new is promoted, but req-a.yaml/req-b.yaml are still on disk from
// run 1 and LoadTranscripts would still load both fine. The pre-fix
// manifest.yaml then reported CapturedCount=0, MissingCount=2, both
// outcomes Captured: false with a FailureReason, and MedianValidatePassRate
// stamped back down to 0 — even though nothing about the actual corpus
// changed. That manifest diff alone is what generate-capture.yml's "Check
// whether any transcript changed" step sees, which (since the PR body's
// #2814 "partial results" fix stopped discarding a failed `go test` run's
// output outright) is what turns a run that captured nothing into a
// published PR whose entire diff is a regression, under a banner claiming
// it is "safe to review."
//
// Reverting either half of the H2 fix (the disposition carry-forward in
// runCapture's outcomes loop, or the median-preservation guarded on
// len(moved) == 0) makes this test fail.
//
// M2 (round-5 review of #2814): both req-a and req-b DO get a FailureReason
// now, even though Captured stays true — this run's own attempt genuinely
// failed to reproduce them (the dead provider), and that fact must not
// vanish just because an earlier run's real transcript is still being
// counted. See RequestOutcome.FailureReason's own doc comment for why this
// is the one case it is non-empty alongside Captured == true.
func TestRunCapture_SecondRunCapturesNothing_PreservesFirstRunsGoodManifest(t *testing.T) {
	requests := []Request{
		capturePipelineRequest("req-a"),
		capturePipelineRequest("req-b"),
	}
	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")
	const passes = 1

	// Run 1: provider succeeds on every call — both requests captured
	// cleanly, giving this destDir a real, good manifest.yaml to protect.
	goodProvider := &diesAfterNCallsProvider{n: 1000, reply: "```yaml\n" + validCandidate + "```"}
	cp1 := &captureProvider{Provider: goodProvider, maxCalls: 1000, maxTokens: 1_000_000}
	first, _, wrote1 := runCapture(context.Background(), t, requests, cp1, "anthropic", "claude-sonnet-5-test", passes, destDir)
	if !wrote1 {
		t.Fatal("expected the first run to write a manifest")
	}
	if first.CapturedCount != 2 || first.MissingCount != 0 {
		t.Fatalf("first run: CapturedCount/MissingCount = %d/%d, want 2/0", first.CapturedCount, first.MissingCount)
	}
	if first.MedianValidatePassRate != 1 {
		t.Fatalf("first run: MedianValidatePassRate = %v, want 1", first.MedianValidatePassRate)
	}

	// Run 2: SAME destDir, same requests — the provider is down from the
	// very first call, so nothing new is promoted this run (moved == 0
	// inside runCapture).
	deadProvider := &diesAfterNCallsProvider{n: 0, reply: "```yaml\n" + validCandidate + "```"}
	cp2 := &captureProvider{Provider: deadProvider, maxCalls: 1000, maxTokens: 1_000_000}
	second, _, wrote2 := runCapture(context.Background(), t, requests, cp2, "anthropic", "claude-sonnet-5-test", passes, destDir)
	if !wrote2 {
		t.Fatal("expected the second run to still write/patch a manifest")
	}

	// The two transcripts from run 1 must still be on disk, untouched.
	for _, id := range []string{"req-a", "req-b"} {
		if _, err := os.Stat(filepath.Join(destDir, id+".yaml")); err != nil {
			t.Fatalf("expected %q to still be on disk after the second (failed) run: %v", id, err)
		}
	}

	if second.CapturedCount != 2 {
		t.Fatalf("second run: manifest.CapturedCount = %d, want 2 — req-a/req-b are still real, committed "+
			"transcripts on disk; this run's failure to reproduce them does not make them missing", second.CapturedCount)
	}
	if second.MissingCount != 0 {
		t.Fatalf("second run: manifest.MissingCount = %d, want 0", second.MissingCount)
	}
	for _, o := range second.RequestOutcomes {
		// M2 (round-5 review of #2814): Captured stays true (carried
		// forward from run 1's real data) but FailureReason is now non-empty
		// — this run's own attempt for both ids genuinely produced nothing,
		// and that must still be visible in the committed manifest even
		// though it isn't what makes Captured false.
		if !o.Captured || o.FailureReason == "" || o.Unusable {
			t.Fatalf("second run: outcome for %q = %+v, want Captured=true, a non-empty FailureReason "+
				"(this run's own attempt failed even though carry-forward keeps it captured), not Unusable",
				o.RequestID, o)
		}
	}

	// The core H2 regression: a run that captured nothing new must not
	// stamp a fabricated 0.00 median over the real one run 1 measured.
	if second.MedianValidatePassRate != first.MedianValidatePassRate {
		t.Fatalf("second run: MedianValidatePassRate = %v, want %v (preserved from the first run, which is "+
			"the only run that ever measured anything)", second.MedianValidatePassRate, first.MedianValidatePassRate)
	}
	if second.MedianSemanticMatchRate != first.MedianSemanticMatchRate {
		t.Fatalf("second run: MedianSemanticMatchRate = %v, want %v (preserved)",
			second.MedianSemanticMatchRate, first.MedianSemanticMatchRate)
	}
	if !reflect.DeepEqual(second.PassScores, first.PassScores) {
		t.Fatalf("second run: PassScores = %+v, want the first run's preserved PassScores: %+v",
			second.PassScores, first.PassScores)
	}
}

// TestRunCapture_PartialFailure_TombstoneAndBijection is
// TestRunCapture_PartialFailure_PreservesGoodTranscripts's sibling, split
// out to keep both functions under gocyclo's complexity limit rather than
// packing every assertion for this one scenario into a single test: it
// proves the tombstone half of the SAME scenario — req-b (permanently
// failing) gets a committed "<id>.missing.yaml" and req-a/req-c (captured)
// get none — and that LoadTranscripts then loads the resulting directory
// cleanly, which is the integration proof that findings 1/2/4/6 actually
// compose into a corpus a consumer can load.
func TestRunCapture_PartialFailure_TombstoneAndBijection(t *testing.T) {
	requests, cp, destDir, _ := partialFailureFixture(t)
	const passes = 3

	_, _, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", passes, destDir)
	if !wrote {
		t.Fatal("expected the manifest to still be written: 1/3 missing is below the run-failing threshold")
	}

	if _, err := os.Stat(filepath.Join(destDir, "req-b"+tombstoneFileSuffix)); err != nil {
		t.Fatalf("expected a tombstone for req-b: %v", err)
	}
	for _, id := range []string{"req-a", "req-c"} {
		if _, err := os.Stat(filepath.Join(destDir, id+tombstoneFileSuffix)); !os.IsNotExist(err) {
			t.Fatalf("expected NO tombstone for captured id %q, stat err = %v", id, err)
		}
	}

	loaded, err := LoadTranscripts(destDir, requests)
	if err != nil {
		t.Fatalf("LoadTranscripts(%q) = %v, want no error", destDir, err)
	}
	if len(loaded.ByID) != 2 {
		t.Fatalf("LoadTranscripts: len(ByID) = %d, want 2", len(loaded.ByID))
	}
	if _, ok := loaded.Tombstoned["req-b"]; !ok {
		t.Fatal("LoadTranscripts: expected req-b in Tombstoned")
	}
}

// TestRunCapture_ProviderHTTPErrorNeverLeaksResponseBodyIntoManifest —
// renamed from Test_RunCapture_... (round-2 review of #2814): the leading
// underscore after "Test" broke substring matching against a `-run
// 'TestRunCapture'` filter (Go's `-run` is a regexp match against the test
// name; "Test_RunCapture" does not contain "TestRunCapture" as a substring
// because of the underscore), which would silently skip this test — the
// most important regression test in this file — from any invocation using
// that pattern. Every other TestRunCapture_* test in this file already used
// the no-underscore form; this was the one holdout.
//
// This is the regression test for the finding that a provider error's raw HTTP response
// body (readErrorBody, provider/http.go — up to 512 bytes, provider-
// controlled) could reach RequestOutcome.FailureReason and then
// manifest.yaml, a committed file, completely unscanned. It drives a REAL
// provider.Anthropic against an httptest server: one request always
// succeeds (so the run stays at 1/2 = 50% missing, AT the run-failing
// threshold but not past it — captureCompletenessVerdict's own "exactly
// half missing" boundary case — so runCapture never calls t.Errorf on this
// test's own *t), the other always gets a 401 whose body is deliberately
// shaped like a real leak (echoing back what looks like a rejected API
// key). That text must never appear anywhere in the manifest — only the
// structured, safe summary (safeFailureReason: a conduiterr code plus the
// HTTP status) does.
func TestRunCapture_ProviderHTTPErrorNeverLeaksResponseBodyIntoManifest(t *testing.T) {
	const failMarker = "TRIGGER_401"
	const leakedSecret = "sk-ant-1234567890abcdefghijklmnop"
	leakedBody := `{"error":{"type":"authentication_error","message":"Incorrect API key provided: ` + leakedSecret + `"}}`

	okBody, err := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": "```yaml\n" + validCandidate + "```"}},
		"usage":   map[string]int{"input_tokens": 5, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshaling ok response fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), failMarker) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, leakedBody)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(okBody)
	}))
	t.Cleanup(srv.Close)

	requests := []Request{
		capturePipelineRequest("req-ok"),
		{
			ID:     "req-401",
			Prompt: "read from the generator and write to the log " + failMarker,
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
	}
	base := &provider.Anthropic{BaseURL: srv.URL}
	cp := &captureProvider{Provider: base, maxCalls: 1000, maxTokens: 1_000_000}
	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")

	manifest, _, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", 1, destDir)
	if !wrote {
		t.Fatal("expected the manifest to be written: 1/2 missing is at, not past, the run-failing threshold")
	}

	data, err := os.ReadFile(filepath.Join(destDir, manifestFileName))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	if strings.Contains(string(data), leakedSecret) {
		t.Fatalf("manifest.yaml carries the leaked secret verbatim:\n%s", data)
	}
	if strings.Contains(string(data), "Incorrect API key") {
		t.Fatalf("manifest.yaml carries the raw provider response body:\n%s", data)
	}

	var gotReq401 RequestOutcome
	found := false
	for _, o := range manifest.RequestOutcomes {
		if o.RequestID == "req-401" {
			gotReq401, found = o, true
		}
	}
	if !found {
		t.Fatal("expected an outcome for req-401")
	}
	if gotReq401.Captured {
		t.Fatal("req-401: Captured = true, want false")
	}
	if strings.Contains(gotReq401.FailureReason, leakedSecret) {
		t.Fatalf("req-401: FailureReason carries the leaked secret: %q", gotReq401.FailureReason)
	}
	// The structured summary IS present and useful: the code plus the HTTP
	// status, which is exactly what a reviewer needs to triage a 401
	// without ever seeing the response body.
	if !strings.Contains(gotReq401.FailureReason, "HTTP 401") {
		t.Fatalf("req-401: FailureReason = %q, want it to name the HTTP status", gotReq401.FailureReason)
	}

	// req-401 also gets a tombstone, and the tombstone is subject to
	// exactly the same guarantee — it is the other place a manifest-safe
	// (never raw) reason gets committed.
	tsData, err := os.ReadFile(filepath.Join(destDir, "req-401"+tombstoneFileSuffix))
	if err != nil {
		t.Fatalf("reading tombstone for req-401: %v", err)
	}
	if strings.Contains(string(tsData), leakedSecret) {
		t.Fatalf("tombstone for req-401 carries the leaked secret verbatim:\n%s", tsData)
	}

	// And the redaction test itself (secrets_scan_test.go) must also catch
	// this if the structured path is ever bypassed — it scans manifest.yaml
	// and every tombstone as raw text, not as a parsed Transcript.
	if findings := scanTextForSecrets(string(data)); len(findings) > 0 {
		t.Fatalf("scanTextForSecrets found findings in a manifest that should already be clean: %v", findings)
	}
}

// TestRunCapture_UnusableResponse_SetsRequestOutcomeUnusable is the other
// half of N4 (round-3 review of #2814): TestRunCapture_ProviderHTTPErrorNeverLeaksResponseBodyIntoManifest
// above proves the transport-miss (401) path leaves RequestOutcome.Unusable
// false, but nothing exercised the OTHER branch end to end — a response
// that DID arrive (a real 200) but decoded to no usable completion at all,
// which provider.IsUnusableResponse (and therefore captureProvider.Complete,
// which is what actually sets manifest-level Unusable) treats as a
// distinct, BILLED case from a 429/timeout/ceiling. Reuses the real
// Anthropic adapter against an httptest server, exactly like the 401 test,
// rather than hand-building a fake error that only ASSERTS it matches the
// real shape.
func TestRunCapture_UnusableResponse_SetsRequestOutcomeUnusable(t *testing.T) {
	const emptyMarker = "TRIGGER_EMPTY_RESPONSE"

	okBody, err := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": "```yaml\n" + validCandidate + "```"}},
		"usage":   map[string]int{"input_tokens": 5, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshaling ok response fixture: %v", err)
	}
	// A 200 with an EMPTY content array: a real, arrived response that
	// anthropic.go's adapter itself marks unusable ("empty response") -
	// never a transport-level miss.
	emptyBody, err := json.Marshal(map[string]any{
		"content": []map[string]string{},
		"usage":   map[string]int{"input_tokens": 5, "output_tokens": 0},
	})
	if err != nil {
		t.Fatalf("marshaling empty response fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		if strings.Contains(string(body), emptyMarker) {
			_, _ = w.Write(emptyBody)
			return
		}
		_, _ = w.Write(okBody)
	}))
	t.Cleanup(srv.Close)

	requests := []Request{
		capturePipelineRequest("req-ok"),
		{
			ID:     "req-empty",
			Prompt: "read from the generator and write to the log " + emptyMarker,
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
	}
	base := &provider.Anthropic{BaseURL: srv.URL}
	cp := &captureProvider{Provider: base, maxCalls: 1000, maxTokens: 1_000_000}
	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")

	manifest, _, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", 1, destDir)
	if !wrote {
		t.Fatal("expected the manifest to be written: 1/2 missing is at, not past, the run-failing threshold")
	}

	var gotReqEmpty RequestOutcome
	found := false
	for _, o := range manifest.RequestOutcomes {
		if o.RequestID == "req-empty" {
			gotReqEmpty, found = o, true
		}
	}
	if !found {
		t.Fatal("expected an outcome for req-empty")
	}
	if gotReqEmpty.Captured {
		t.Fatal("req-empty: Captured = true, want false — an empty completion is still absence of data")
	}
	// The core regression: a response that DID arrive but decoded to
	// nothing usable must be flagged Unusable, distinguishing it from a
	// transport-level miss (429/timeout/ceiling) that never got a response
	// at all — RequestOutcome's own doc comment on why the two must never
	// share a code path (a BILLED, attempted call vs. one that never
	// reached the provider).
	if !gotReqEmpty.Unusable {
		t.Fatal("req-empty: Unusable = false, want true — the provider responded (200) but decoded to no usable completion")
	}

	// The 401 case (this test's own sibling,
	// TestRunCapture_ProviderHTTPErrorNeverLeaksResponseBodyIntoManifest)
	// proves the negative for a transport miss; a healthy request in THIS
	// same run proves the positive is not just "always true".
	var gotReqOK RequestOutcome
	for _, o := range manifest.RequestOutcomes {
		if o.RequestID == "req-ok" {
			gotReqOK = o
		}
	}
	if !gotReqOK.Captured || gotReqOK.Unusable {
		t.Fatalf("req-ok outcome = %+v, want Captured=true and Unusable=false", gotReqOK)
	}
}

// TestRunCapture_MissingRequest_ScoredAsMissingNotFailedCandidate is the
// regression test for a request with no completion being scored as a
// failed model candidate rather than Missing (score.go's Result.Missing).
// Before the fix, runCapture set passCandidates[req.ID] = gen.Candidate
// (empty string on this path) BEFORE checking len(raw) == 0, so the map
// always carried a key for the id — ScoreRun then saw `ok == true` and
// scored a 429 as "the model produced a candidate that failed validation"
// instead of "no candidate was ever produced".
//
// M1 (round-2 review of #2814): the previous version of this test never
// called runCapture at all — it hand-built a Candidates map "mirroring
// exactly what runCapture's per-request closure does today" and asserted
// on ScoreRun directly, which is a property that was already true before
// the fix and stays true if the fix is reverted; it is not a regression
// test for runCapture's own map-building bug. This version drives the real
// runCapture end to end (reusing partialFailureFixture's permanently-failing
// req-b, shared with TestRunCapture_PartialFailure_PreservesGoodTranscripts)
// and inspects the per-pass RunScore.Results runCapture's own passRuns
// return value carries — so it fails if runCapture ever again inserts an
// empty-string entry instead of omitting the key.
func TestRunCapture_MissingRequest_ScoredAsMissingNotFailedCandidate(t *testing.T) {
	requests, cp, destDir, _ := partialFailureFixture(t)
	const passes = 3

	_, passRuns, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", passes, destDir)
	if !wrote {
		t.Fatal("expected the manifest to still be written: 1/3 missing is below the run-failing threshold")
	}
	if len(passRuns) != passes {
		t.Fatalf("len(passRuns) = %d, want %d", len(passRuns), passes)
	}

	for i, rs := range passRuns {
		var gotMissing, gotOK bool
		for _, res := range rs.Results {
			switch res.RequestID {
			case "req-b": // permanently fails every pass, per partialFailureFixture
				gotMissing = true
				if !res.Missing {
					t.Fatalf("pass %d: req-b: Missing = false, want true — a request runCapture never got a "+
						"completion for must never be scored as a failed candidate", i+1)
				}
				if res.ValidatePass {
					t.Fatalf("pass %d: req-b: ValidatePass = true, want false", i+1)
				}
				if res.SemanticMatch {
					t.Fatalf("pass %d: req-b: SemanticMatch = true, want false", i+1)
				}
			case "req-a", "req-c":
				gotOK = true
				if res.Missing {
					t.Fatalf("pass %d: %s: Missing = true, want false", i+1, res.RequestID)
				}
			}
		}
		if !gotMissing || !gotOK {
			t.Fatalf("pass %d: expected results for both req-b and the healthy requests, got %+v", i+1, rs.Results)
		}
	}
}

// refusesMarkedRequestProvider replies with well-formed, non-empty text
// that never extracts into pipeline YAML ("I'm sorry, I can't help with
// that.") for any request whose prompt contains marker, and a normal
// fenced, extractable reply for every other request — modeling systematic
// model refusal on ONE request alongside an otherwise-healthy pass (B2,
// round-3 review of #2814), as distinct from a transport-level miss where
// no response arrives at all (flakyAfterFirstCallProvider, above).
type refusesMarkedRequestProvider struct {
	marker     string
	validReply string
}

func (p refusesMarkedRequestProvider) Name() string { return "refuses-marked" }

func (p refusesMarkedRequestProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if strings.Contains(req.Prompt, p.marker) {
		return provider.CompletionResult{Text: "I'm sorry, I can't help with that.", TokensUsed: 5}, nil
	}
	return provider.CompletionResult{Text: p.validReply, TokensUsed: 20}, nil
}

// TestRunCapture_SystematicRefusal_ScoresValidateFailNotPass is the
// end-to-end regression test for B2 (round-3 review of #2814): a provider
// that replies with well-formed but unparseable text for every attempt on
// one request (systematic refusal) produces a completion recorded as raw
// data — never Missing, since runCapture's "absence of data" branch never
// fires when a real completion WAS recorded — but Generate never extracts
// a candidate from it. Before this fix, ScoreRun scored the resulting
// empty candidate a validate PASS (validate.RunBytes on zero bytes finds
// nothing wrong), buildTranscript committed the same false PASS into the
// refused request's own Transcript.Outcome, and the pass was never marked
// degraded — inflating the headline numbers with exactly the failure this
// corpus exists to measure. A second, healthy request is included so the
// pass is merely degraded (not wiped), keeping this test isolated from B1's
// separate allDegraded failure path (TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable).
func TestRunCapture_SystematicRefusal_ScoresValidateFailNotPass(t *testing.T) {
	const marker = "TRIGGER_REFUSAL"
	requests := []Request{
		{
			ID:     "req-refused",
			Prompt: "read from the generator and write to the log " + marker,
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
		capturePipelineRequest("req-ok"),
	}
	fake := refusesMarkedRequestProvider{marker: marker, validReply: "```yaml\n" + validCandidate + "```"}
	cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 1_000_000}
	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")

	manifest, passRuns, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", 1, destDir)
	if !wrote {
		t.Fatal("expected the manifest to be written: both requests were attempted and a completion was recorded for each")
	}

	// Both requests must be reported as CAPTURED (a completion was recorded
	// for each — this is data, not absence of data), never Missing.
	if manifest.CapturedCount != 2 || manifest.MissingCount != 0 {
		t.Fatalf("manifest CapturedCount/MissingCount = %d/%d, want 2/0 — a refused-but-recorded completion is "+
			"captured DATA, not a missing request", manifest.CapturedCount, manifest.MissingCount)
	}

	if len(passRuns) != 1 || len(passRuns[0].Results) != 2 {
		t.Fatalf("unexpected passRuns shape: %+v", passRuns)
	}
	var gotRefused bool
	for _, res := range passRuns[0].Results {
		if res.RequestID != "req-refused" {
			continue
		}
		gotRefused = true
		if res.Missing {
			t.Fatal("res.Missing = true, want false — a completion WAS recorded, this is not absence of data")
		}
		// The core regression: the empty candidate must score a hard fail
		// on BOTH axes, not a validate PASS.
		if res.ValidatePass {
			t.Fatal("res.ValidatePass = true, want false — an empty/unparseable candidate must never score a validate PASS")
		}
		if res.SemanticMatch {
			t.Fatal("res.SemanticMatch = true, want false")
		}
	}
	if !gotRefused {
		t.Fatalf("expected a result for req-refused, got %+v", passRuns[0].Results)
	}

	// The pass must be marked degraded — a request whose only completions
	// never extracted a candidate is exactly as uninformative, for this
	// purpose, as one the provider never answered at all.
	if len(manifest.PassScores) != 1 || manifest.PassScores[0].MissingCount == 0 {
		t.Fatalf("manifest.PassScores = %+v, want PassScores[0].MissingCount > 0", manifest.PassScores)
	}
	if len(manifest.DegradedPasses) == 0 {
		t.Fatal("manifest.DegradedPasses is empty, want the one pass named")
	}
	if manifest.MediansUnreliable {
		t.Fatal("manifest.MediansUnreliable = true, want false — req-ok keeps this pass merely degraded, not wiped")
	}

	// The committed transcript's own Outcome must not claim a pass either
	// (B2's buildTranscript half of the fix).
	data, err := os.ReadFile(filepath.Join(destDir, "req-refused.yaml"))
	if err != nil {
		t.Fatalf("reading committed transcript: %v", err)
	}
	var tr Transcript
	if err := yaml.Unmarshal(data, &tr); err != nil {
		t.Fatalf("unmarshaling transcript: %v", err)
	}
	if tr.Outcome.ValidatePass {
		t.Fatal("committed Transcript.Outcome.ValidatePass = true, want false — the model never produced a usable candidate")
	}
}

// TestRunCapture_ScopedRecapture_PatchesExistingManifest is the end-to-end
// regression test for M1 (round-3 review of #2814): a scoped
// `-run TestCaptureTranscripts/<id>` re-capture that turns a
// previously-tombstoned id back into real data must patch that id's own
// RequestOutcome (and CapturedCount/MissingCount) into the manifest.yaml
// the last full run left behind — before this fix, runCapture mutated the
// TREE (promoted the new transcript, removed the stale tombstone — H3's own
// fix) but returned early without ever touching manifest.yaml, leaving a
// committed file that still said captured: false for an id the tree now
// has real data for.
//
// Scoping only happens via the real `go test -run` mechanism (Go's testing
// package decides which t.Run(req.ID, ...) closures execute BEFORE
// runCapture's own code ever sees it — there is no in-process way to
// simulate "only 1 of 2 requests attempted" against a real *testing.T), so
// this re-execs the already-built test binary the same way
// TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable does,
// filtered to just the req-b subtest.
func TestRunCapture_ScopedRecapture_PatchesExistingManifest(t *testing.T) {
	const helperEnv = "CONDUIT_TEST_SCOPEDPATCH_HELPER"
	const destDirEnv = "CONDUIT_TEST_SCOPEDPATCH_DESTDIR"

	if os.Getenv(helperEnv) == "1" {
		runScopedRecaptureHelper(t, os.Getenv(destDirEnv))
		return
	}

	destDir := t.TempDir()
	seedManifestForScopedRecapture(t, destDir)

	cmd := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=^TestRunCapture_ScopedRecapture_PatchesExistingManifest$/^req-b$",
		"-test.v",
	)
	cmd.Env = append(os.Environ(), helperEnv+"=1", destDirEnv+"="+destDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess (scoped re-capture of req-b) failed: %v\noutput:\n%s", err, out)
	}

	// req-b.yaml must be promoted and its tombstone gone (H3's own fix,
	// unaffected by M1 — asserted here too since M1's fix sits right next
	// to that code).
	if _, err := os.Stat(filepath.Join(destDir, "req-b.yaml")); err != nil {
		t.Fatalf("expected req-b.yaml to be promoted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "req-b"+tombstoneFileSuffix)); !os.IsNotExist(err) {
		t.Fatalf("expected req-b's tombstone to be removed, stat err = %v", err)
	}

	data, rerr := os.ReadFile(filepath.Join(destDir, manifestFileName))
	if rerr != nil {
		t.Fatalf("reading patched manifest: %v", rerr)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshaling patched manifest: %v", err)
	}

	// The core regression: req-b's OWN outcome must now say captured, with
	// no stale failure reason, and the summary counts must reflect it.
	var gotReqB, gotReqA bool
	for _, o := range manifest.RequestOutcomes {
		switch o.RequestID {
		case "req-b":
			gotReqB = true
			if !o.Captured {
				t.Fatal("patched req-b RequestOutcome.Captured = false, want true")
			}
			if o.FailureReason != "" {
				t.Fatalf("patched req-b RequestOutcome.FailureReason = %q, want empty (stale reason must be cleared)",
					o.FailureReason)
			}
		case "req-a":
			gotReqA = true
			if !o.Captured {
				t.Fatal("req-a RequestOutcome.Captured = false, want true — untouched by this scoped run")
			}
		}
	}
	if !gotReqB || !gotReqA {
		t.Fatalf("expected RequestOutcomes for both req-a and req-b, got %+v", manifest.RequestOutcomes)
	}
	if manifest.CapturedCount != 2 || manifest.MissingCount != 0 {
		t.Fatalf("manifest CapturedCount/MissingCount = %d/%d, want 2/0 after the scoped re-capture",
			manifest.CapturedCount, manifest.MissingCount)
	}
	// Medians/PassScores describe the LAST FULL run and must be untouched
	// by a scoped run that has no full-corpus data to recompute them from.
	if manifest.MedianValidatePassRate != 0.5 {
		t.Fatalf("manifest.MedianValidatePassRate = %v, want 0.5 (untouched from the seeded full run)",
			manifest.MedianValidatePassRate)
	}
}

// seedManifestForScopedRecapture writes destDir into the state a prior FULL
// run (req-a captured, req-b permanently missing) would have left it in:
// req-a's committed transcript, req-b's tombstone, and a manifest.yaml
// whose RequestOutcomes/counts/medians describe exactly that.
func seedManifestForScopedRecapture(t *testing.T, destDir string) {
	t.Helper()

	reqA := capturePipelineRequest("req-a")
	tr := Transcript{
		SchemaVersion:      TranscriptSchemaVersion,
		RequestID:          reqA.ID,
		Provider:           "anthropic",
		Model:              "claude-sonnet-5-test",
		CapturedAt:         time.Now().UTC(),
		CorpusPromptSHA256: sha256Hex(reqA.Prompt),
		SystemPromptSHA256: sha256Hex(BuildSystemPrompt(BuiltinCatalog())),
		CatalogFingerprint: CatalogFingerprint(),
		Turns: []Turn{{
			N: 1, UserPromptSHA256: sha256Hex(reqA.Prompt), TokensUsed: 20,
			CompletionText: "```yaml\n" + validCandidate + "```",
		}},
		Outcome: Outcome{ValidatePass: true, SemanticMatch: true},
	}
	data, err := yaml.Marshal(tr)
	if err != nil {
		t.Fatalf("marshaling seed transcript: %v", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("creating %q: %v", destDir, err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "req-a.yaml"), data, 0o600); err != nil {
		t.Fatalf("writing seed transcript: %v", err)
	}

	writeTombstone(t, destDir, Tombstone{
		SchemaVersion:      TranscriptSchemaVersion,
		RequestID:          "req-b",
		CorpusPromptSHA256: sha256Hex("read from the generator and write to the log TRIGGER_SCOPED_RECAPTURE"),
		FailureCode:        "generate.provider_error (HTTP 429)",
		CapturedAt:         time.Now().UTC(),
	})

	writeManifest(t, destDir, Manifest{
		SchemaVersion:           TranscriptSchemaVersion,
		Provider:                "anthropic",
		Model:                   "claude-sonnet-5-test",
		CapturedAt:              time.Now().UTC(),
		RequestCount:            2,
		Passes:                  1,
		MedianValidatePassRate:  0.5,
		MedianSemanticMatchRate: 0.5,
		PassScores:              []PassScore{{Pass: 1, Total: 2, ValidatePassCount: 1, ValidatePassRate: 0.5, SemanticMatchCount: 1, SemanticMatchRate: 0.5}},
		CapturedCount:           1,
		MissingCount:            1,
		RequestOutcomes: []RequestOutcome{
			{RequestID: "req-a", Captured: true},
			{RequestID: "req-b", Captured: false, FailureReason: "generate.provider_error (HTTP 429)"},
		},
	})
}

// runScopedRecaptureHelper is the real scenario
// TestRunCapture_ScopedRecapture_PatchesExistingManifest runs inside its
// re-exec'd subprocess, filtered to ONLY req-b's subtest (the requests
// slice still names both — Go's own `-run` filtering is what makes this a
// genuinely scoped run, exactly as a real `-run TestCaptureTranscripts/<id>`
// invocation would): req-b now succeeds, simulating a clean re-capture of a
// previously-failing request.
func runScopedRecaptureHelper(t *testing.T, destDir string) {
	t.Helper()
	requests := []Request{
		capturePipelineRequest("req-a"),
		{
			ID:     "req-b",
			Prompt: "read from the generator and write to the log TRIGGER_SCOPED_RECAPTURE",
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
	}
	fake := &fakeProvider{replies: []string{"```yaml\n" + validCandidate + "```"}, tokens: 20}
	cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 1_000_000}

	runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", 1, destDir)
}

// TestCaptureCompletenessVerdict proves the threshold decision directly and
// without a *testing.T in the loop (calling runCapture with a scenario that
// crosses the failure threshold would fail THIS test too — see
// TestRunCapture_PartialFailure_PreservesGoodTranscripts's doc comment for
// why that test stays below the threshold): the incident's own 1/28 (3.6%)
// must not fail; a strict majority missing must; the boundary (exactly half)
// falls on the "not yet most" side; a fully-failed scoped single-request
// re-capture (1/1 missing) must fail loudly, not print an empty message.
func TestCaptureCompletenessVerdict(t *testing.T) {
	outcomesFor := func(missingIDs ...string) []RequestOutcome {
		out := make([]RequestOutcome, len(missingIDs))
		for i, id := range missingIDs {
			out[i] = RequestOutcome{RequestID: id, Captured: false, FailureReason: "429: rate limited"}
		}
		return out
	}

	tests := []struct {
		name                         string
		attempted, captured, missing int
		outcomes                     []RequestOutcome
		wantFail                     bool
		wantEmptyMessage             bool
	}{
		{
			name:      "nothing missing -> success, no message",
			attempted: 28, captured: 28, missing: 0,
			wantFail:         false,
			wantEmptyMessage: true,
		},
		{
			name:      "the incident itself: 1/28 missing -> below threshold, not a failure",
			attempted: 28, captured: 27, missing: 1,
			outcomes: outcomesFor("kafka-connect-unwrap-to-postgres"),
			wantFail: false,
		},
		{
			name:      "exactly half missing -> still not 'most', not a failure",
			attempted: 28, captured: 14, missing: 14,
			outcomes: outcomesFor(idRange(14, "req")...),
			wantFail: false,
		},
		{
			name:      "just past half missing -> most of the corpus, fails",
			attempted: 28, captured: 13, missing: 15,
			outcomes: outcomesFor(idRange(15, "req")...),
			wantFail: true,
		},
		{
			name:      "a fully-failed scoped single-request recapture -> 100% missing, fails",
			attempted: 1, captured: 0, missing: 1,
			outcomes: outcomesFor("only-request"),
			wantFail: true,
		},
		{
			// The regression case: a typo'd -run pattern or request_id
			// input matches nothing in the corpus, so NOTHING is attempted
			// at all — the old `attemptedCount == 0 || missingCount == 0`
			// guard treated this identically to a clean, fully-successful
			// run (false, ""), which is how a capture job could exit green
			// having captured nothing.
			name:      "zero attempted -> explicit failure, never a silent green",
			attempted: 0, captured: 0, missing: 0,
			wantFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fail, msg := captureCompletenessVerdict(tt.attempted, tt.captured, tt.missing, tt.outcomes)
			if fail != tt.wantFail {
				t.Fatalf("captureCompletenessVerdict(...) fail = %v, want %v (msg: %q)", fail, tt.wantFail, msg)
			}
			if tt.wantEmptyMessage {
				if msg != "" {
					t.Fatalf("expected an empty message when nothing is missing, got %q", msg)
				}
				return
			}
			if msg == "" {
				t.Fatal("expected a non-empty message naming what was and wasn't captured")
			}
			capturedStr := fmt.Sprintf("%d/%d captured", tt.captured, tt.attempted)
			if !strings.Contains(msg, capturedStr) {
				t.Fatalf("message %q does not name what was captured (%q)", msg, capturedStr)
			}
			for _, o := range tt.outcomes {
				if !strings.Contains(msg, o.RequestID) {
					t.Fatalf("message %q does not name missing id %q", msg, o.RequestID)
				}
			}
		})
	}
}

// idRange returns n synthetic ids "<prefix>-0".."<prefix>-<n-1>", used by
// TestCaptureCompletenessVerdict's boundary cases where the exact ids don't
// matter, only the count.
func idRange(n int, prefix string) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return ids
}

// fakeCompletenessSink implements completenessSink without a *testing.T, so
// TestReportCaptureCompleteness can exercise BOTH of
// reportCaptureCompleteness's branches — including the one that calls
// Errorf — without failing the test doing the exercising.
type fakeCompletenessSink struct {
	errorfCalled bool
	logfCalled   bool
	lastMsg      string
}

func (f *fakeCompletenessSink) Errorf(format string, args ...any) {
	f.errorfCalled = true
	f.lastMsg = fmt.Sprintf(format, args...)
}

func (f *fakeCompletenessSink) Logf(format string, args ...any) {
	f.logfCalled = true
	f.lastMsg = fmt.Sprintf(format, args...)
}

// TestReportCaptureCompleteness is the regression test for the previously
// untested "if fail { t.Errorf } else { t.Logf }" wiring in runCapture: both
// halves are exercised directly here via fakeCompletenessSink, including the
// fail branch, which could never be exercised through runCapture itself
// without also failing whichever test called it.
func TestReportCaptureCompleteness(t *testing.T) {
	t.Run("below threshold logs, never errors", func(t *testing.T) {
		sink := &fakeCompletenessSink{}
		reportCaptureCompleteness(sink, 28, 27, 1, []RequestOutcome{
			{RequestID: "kafka-connect-unwrap-to-postgres", FailureReason: "generate.provider_error (HTTP 429)"},
		})
		if sink.errorfCalled {
			t.Fatalf("Errorf called for a below-threshold miss: %q", sink.lastMsg)
		}
		if !sink.logfCalled {
			t.Fatal("expected Logf to be called")
		}
	})

	t.Run("past threshold errors", func(t *testing.T) {
		sink := &fakeCompletenessSink{}
		outcomes := make([]RequestOutcome, 20)
		for i := range outcomes {
			outcomes[i] = RequestOutcome{RequestID: fmt.Sprintf("req-%d", i), FailureReason: "generate.provider_error"}
		}
		reportCaptureCompleteness(sink, 28, 8, 20, outcomes)
		if !sink.errorfCalled {
			t.Fatal("expected Errorf to be called for a past-threshold miss")
		}
	})

	t.Run("zero attempted errors, never silently reports nothing", func(t *testing.T) {
		sink := &fakeCompletenessSink{}
		reportCaptureCompleteness(sink, 0, 0, 0, nil)
		if !sink.errorfCalled {
			t.Fatal("expected Errorf to be called when zero requests were attempted")
		}
	})

	t.Run("nothing missing calls neither", func(t *testing.T) {
		sink := &fakeCompletenessSink{}
		reportCaptureCompleteness(sink, 28, 28, 0, nil)
		if sink.errorfCalled || sink.logfCalled {
			t.Fatal("expected neither Errorf nor Logf for a clean, fully-attempted run")
		}
	})
}

// TestSummarizePasses_AllDegraded_FlagsUnreliableFallback is the regression
// test for B1 (round-3 review of #2814), reproducing the review's own
// scenario directly against summarizePasses (no *testing.T-failing side
// effects, so this test can safely assert the bad behavior is gone without
// itself needing to fail): 5 requests, 3 capture passes, a provider that
// dies after 3 successful calls (captureWallClockBudget expiring 60% into
// pass 1, in the language of runCapture's own doc comment) — pass 1 captures
// req-1..req-3 (which validate cleanly) and is missing req-4/req-5; passes 2
// and 3 capture nothing at all. Every pass is therefore degraded
// (missingCounts all > 0), which is exactly the case the pre-fix code
// treated as "fall back to the median across every pass" — computing 0.00
// even though the requests that WERE captured validated 3/3.
//
// Reverting the allDegraded field (and the fallback-is-unreliable framing in
// summarizePasses' doc comment) does not change what this test asserts —
// what it guards is the CALLER'S ability to tell the two cases apart, so the
// meaningful regression coverage is that allDegraded comes back true here;
// see TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable below
// for the end-to-end proof that runCapture actually acts on it.
func TestSummarizePasses_AllDegraded_FlagsUnreliableFallback(t *testing.T) {
	requests := []Request{
		capturePipelineRequest("req-1"),
		capturePipelineRequest("req-2"),
		capturePipelineRequest("req-3"),
		capturePipelineRequest("req-4"),
		capturePipelineRequest("req-5"),
	}
	// Candidates map values here are ScoreRun/validateCandidate input
	// directly (validate.RunBytes), never the fenced markdown a live
	// provider reply would carry — that fence-stripping is extractCandidate's
	// job inside Generate, a layer these summarizePasses unit tests bypass
	// entirely (c.f. diesAfterNCallsProvider below, which DOES go through
	// Generate and so DOES need the fence).

	// Mirrors runCapture's own candidate map construction: a pass that never
	// produced a completion for a request omits that key entirely (score.go's
	// Result.Missing), never an empty-string stand-in.
	pass1 := Candidates{"req-1": validCandidate, "req-2": validCandidate, "req-3": validCandidate}
	pass2 := Candidates{}
	pass3 := Candidates{}
	// req-4/req-5 missing pass 1; everything missing passes 2-3 — built via
	// requestsMissingUsableCandidate itself (runCapture's own derivation),
	// not hand-typed, so this test can never silently drift from what
	// production code actually computes.
	missingSets := [][]string{
		requestsMissingUsableCandidate(requests, pass1),
		requestsMissingUsableCandidate(requests, pass2),
		requestsMissingUsableCandidate(requests, pass3),
	}

	ctx := context.Background()
	ms := ScoreMedian(ctx, requests, []Candidates{pass1, pass2, pass3})
	// carriedForwardIDs is nil: these unit tests exercise summarizePasses
	// directly, with no destDir/disk state at all, so there is nothing to
	// carry forward — see missingSetsAreReliable's own doc comment for what
	// this argument guards against (HIGH-1, round-5 review of #2814).
	_, summary := summarizePasses(ms.Runs, missingSets, nil)

	if !summary.allDegraded {
		t.Fatal("summary.allDegraded = false, want true — every one of 3 passes had a nonzero missingCount")
	}
	if len(summary.degradedPasses) != 3 {
		t.Fatalf("summary.degradedPasses = %v, want all 3 passes named", summary.degradedPasses)
	}
	// The regression this test exists to catch: the requests that WERE
	// captured (pass 1's req-1..req-3) validate 3/3, but with no clean pass
	// to compute a median from, the all-passes fallback still comes out to
	// 0 (median of [0.6, 0, 0] == 0) — this is summarizePasses' documented
	// fallback behavior, not a bug in this test; what matters is that
	// allDegraded is true so the caller knows not to trust it.
	if summary.validateRate != 0 {
		t.Fatalf("summary.validateRate = %v, want 0 (median of [0.6, 0, 0]) — if this changed, update the "+
			"scenario or the assertion, but confirm allDegraded is still what the caller relies on", summary.validateRate)
	}
}

// TestSummarizePasses_ChronicSingleMissingRequest_NotAllDegraded is the
// negative counterpart to TestSummarizePasses_AllDegraded_FlagsUnreliableFallback:
// a single request that is chronically missing on EVERY pass (never any
// OTHER request, and never a whole pass wiped) must NOT set allDegraded,
// even though — same as the all-degraded case — no individual pass is
// "clean" (missingCount == 0 for every one of them). This is exactly
// TestRunCapture_PartialFailure_PreservesGoodTranscripts's own scenario
// (req-b permanently rate-limited, req-a/req-c always captured) reduced to
// summarizePasses directly: an over-broad allDegraded condition (n == 0
// alone, without also requiring a wiped pass) would flip this ordinary,
// below-threshold partial result — the exact case captureCompletenessVerdict
// exists to treat as routine — into a hard failure of the whole capture run,
// which is the regression this test guards against.
func TestSummarizePasses_ChronicSingleMissingRequest_NotAllDegraded(t *testing.T) {
	requests := []Request{
		capturePipelineRequest("req-a"),
		capturePipelineRequest("req-b"),
		capturePipelineRequest("req-c"),
	}
	// req-b is missing from every pass; req-a/req-c are captured and valid
	// on every pass — identical, stable degradation, not a wipeout.
	pass := Candidates{"req-a": validCandidate, "req-c": validCandidate}
	missingSet := requestsMissingUsableCandidate(requests, pass) // ["req-b"] every time
	missingSets := [][]string{missingSet, missingSet, missingSet}

	ctx := context.Background()
	ms := ScoreMedian(ctx, requests, []Candidates{pass, pass, pass})
	// carriedForwardIDs is nil: these unit tests exercise summarizePasses
	// directly, with no destDir/disk state at all, so there is nothing to
	// carry forward — see missingSetsAreReliable's own doc comment for what
	// this argument guards against (HIGH-1, round-5 review of #2814).
	_, summary := summarizePasses(ms.Runs, missingSets, nil)

	if summary.allDegraded {
		t.Fatal("summary.allDegraded = true, want false — one chronically-missing request among three is " +
			"ordinary partial-result noise (captureCompletenessVerdict's territory), not a wiped pass")
	}
	if len(summary.degradedPasses) != 3 {
		t.Fatalf("summary.degradedPasses = %v, want all 3 passes named (missingCount 1 > 0 on every one)",
			summary.degradedPasses)
	}
	// The fallback (median across all passes) is still what gets published
	// here — but it is the SAME number a clean-pass median would have shown
	// (every pass identically 2/3), which is why allDegraded correctly
	// stays false: nothing about this number is misleading.
	if want := 2.0 / 3.0; summary.validateRate != want {
		t.Fatalf("summary.validateRate = %v, want %v", summary.validateRate, want)
	}
}

// TestSummarizePasses_RotatingSubset_FlagsAllDegraded is the regression test
// for H1 (round-4 review of #2814): the WIPED-pass-only proxy that used to
// gate allDegraded missed this exact scenario. 5 requests, 5 passes, pass p
// captures ONLY request p (a rotating 429: every OTHER request fails on
// that pass) — no pass is ever wiped (every pass captures exactly one
// request, so passMissing == 4 < 5 == Total on every pass, and the old
// `passMissing == rs.Total` wipe check never fires), yet the passes
// thoroughly disagree about WHICH request they captured: pass 1's missing
// set is {req-2,req-3,req-4,req-5}, pass 2's is {req-1,req-3,req-4,req-5},
// and so on — five different sets, never equal to each other.
//
// Reproduced against the pre-fix code before writing this test: every
// individual request validates 1/1 whenever it IS captured (true corpus
// quality 1.00), but summarizePasses' all-passes fallback computes a median
// of 0.20 (each pass scores exactly 1/5 = 0.20, so the median of five
// identical 0.20s is 0.20) — and the old condition
// (n == 0 && anyPassWiped) read this as a stable, non-misleading partial
// result and left allDegraded false, publishing 0.20 as this run's
// baseline. Reverting the allMissingSetsEqual-based fix (restoring the
// wipe-only check) makes this test fail: allDegraded comes back false.
func TestSummarizePasses_RotatingSubset_FlagsAllDegraded(t *testing.T) {
	requests := []Request{
		capturePipelineRequest("req-1"),
		capturePipelineRequest("req-2"),
		capturePipelineRequest("req-3"),
		capturePipelineRequest("req-4"),
		capturePipelineRequest("req-5"),
	}

	passes := make([]Candidates, len(requests))
	missingSets := make([][]string, len(requests))
	for p := range requests {
		// Pass p (0-indexed here, request p+1 in id terms) captures ONLY
		// requests[p] — every other request is missing this pass, exactly
		// the "rotating 429" scenario from the round-4 review.
		passes[p] = Candidates{requests[p].ID: validCandidate}
		missingSets[p] = requestsMissingUsableCandidate(requests, passes[p])
	}

	ctx := context.Background()
	ms := ScoreMedian(ctx, requests, passes)
	// carriedForwardIDs is nil: these unit tests exercise summarizePasses
	// directly, with no destDir/disk state at all, so there is nothing to
	// carry forward — see missingSetsAreReliable's own doc comment for what
	// this argument guards against (HIGH-1, round-5 review of #2814).
	_, summary := summarizePasses(ms.Runs, missingSets, nil)

	if !summary.allDegraded {
		t.Fatal("summary.allDegraded = false, want true — every pass captured a DIFFERENT single request, " +
			"so the passes disagree about which requests they missed even though no pass was ever wiped " +
			"(this is exactly the H1 regression: a wipe-only check misses this case)")
	}
	if len(summary.degradedPasses) != len(requests) {
		t.Fatalf("summary.degradedPasses = %v, want all %d passes named", summary.degradedPasses, len(requests))
	}
	if want := 0.2; summary.validateRate != want {
		t.Fatalf("summary.validateRate = %v, want %v (median of five identical 0.2 passes) — if this changed, "+
			"update the scenario, but confirm allDegraded is still what the caller relies on not to be misled "+
			"by it", summary.validateRate, want)
	}
}

// TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable is the
// end-to-end regression test for B1: runCapture itself, given the review's
// exact "provider dies partway through pass 1" scenario, must (a) fail its
// own `go test` run (t.Errorf — the same class of failure
// captureCompletenessVerdict already uses for attemptedCount == 0) rather
// than exit clean under a misleading 0.00 baseline, and (b) still write a
// manifest.yaml — with MediansUnreliable: true — for whatever WAS captured
// (req-1..req-3, which validate 3/3).
//
// Asserting "this call makes *testing.T fail" cannot be done in-process:
// once ANY subtest calls t.Errorf, Go's testing package propagates that
// failure to every ancestor including the top-level test binary (verified
// empirically — there is no way to catch and discard it, which is exactly
// why reportCaptureCompleteness above is tested through the fakeCompletenessSink
// indirection instead of a real *testing.T). runCapture cannot take that same
// indirection: its per-request t.Run(req.ID, ...) subtests are what let a
// scoped `-run TestCaptureTranscripts/<id>` re-capture work at all (file
// header), which requires a concrete *testing.T. So this test re-execs the
// already-built test binary (os.Args[0], the standard os/exec-style pattern
// for asserting a process exits nonzero) as a subprocess, lets THAT process's
// real *testing.T take the real Errorf, and asserts on the parent side that
// the subprocess failed and left the expected manifest on disk.
func TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable(t *testing.T) {
	const helperEnv = "CONDUIT_TEST_ALLDEGRADED_HELPER"
	const destDirEnv = "CONDUIT_TEST_ALLDEGRADED_DESTDIR"

	if os.Getenv(helperEnv) == "1" {
		runAllPassesDegradedHelper(t, os.Getenv(destDirEnv))
		return
	}

	destDir := t.TempDir()
	cmd := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=^TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable$",
		"-test.v",
	)
	cmd.Env = append(os.Environ(), helperEnv+"=1", destDirEnv+"="+destDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the subprocess (runCapture with every pass degraded) to exit nonzero via t.Errorf; "+
			"output:\n%s", out)
	}
	if !strings.Contains(string(out), "every one of 3 capture pass(es) was degraded") {
		t.Fatalf("subprocess failed as expected, but not for the reason this test checks — output:\n%s", out)
	}

	data, rerr := os.ReadFile(filepath.Join(destDir, manifestFileName))
	if rerr != nil {
		t.Fatalf("expected manifest.yaml to still be written despite the failure: %v\nsubprocess output:\n%s", rerr, out)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}
	if !manifest.MediansUnreliable {
		t.Fatal("manifest.MediansUnreliable = false, want true")
	}
	if manifest.CapturedCount != 3 {
		t.Fatalf("manifest.CapturedCount = %d, want 3 (req-1..req-3 captured on pass 1)", manifest.CapturedCount)
	}
	if manifest.MissingCount != 2 {
		t.Fatalf("manifest.MissingCount = %d, want 2 (req-4/req-5 never captured on any pass)", manifest.MissingCount)
	}
	if len(manifest.DegradedPasses) != 3 {
		t.Fatalf("manifest.DegradedPasses = %v, want all 3 passes named", manifest.DegradedPasses)
	}
}

// diesAfterNCallsProvider returns a valid, passing completion for its first
// n calls and errors on every call after that — modeling
// captureWallClockBudget expiring (or a rate-limit storm starting) partway
// through a multi-pass run, across every request rather than just one (c.f.
// flakyAfterFirstCallProvider above, which flakes a single marked request).
type diesAfterNCallsProvider struct {
	n     int
	reply string

	mu    sync.Mutex
	calls int
}

func (p *diesAfterNCallsProvider) Name() string { return "dies-after-n-calls" }

func (p *diesAfterNCallsProvider) Complete(_ context.Context, _ provider.CompletionRequest) (provider.CompletionResult, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n > p.n {
		return provider.CompletionResult{}, fmt.Errorf("capture ceiling: wall-clock deadline exceeded (simulated)")
	}
	return provider.CompletionResult{Text: p.reply, TokensUsed: 20}, nil
}

// runAllPassesDegradedHelper is the real scenario
// TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable runs inside
// its re-exec'd subprocess: 5 requests, 3 passes, a provider that dies after
// 3 successful calls — so pass 1 captures req-1..req-3 and is missing
// req-4/req-5, and passes 2-3 capture nothing (the provider is already dead
// by the time they start). This calls runCapture on the subprocess's OWN
// real *testing.T, so the t.Errorf inside it (B1's fix) really does fail
// this subprocess — that is the behavior under test.
func runAllPassesDegradedHelper(t *testing.T, destDir string) {
	t.Helper()
	requests := []Request{
		capturePipelineRequest("req-1"),
		capturePipelineRequest("req-2"),
		capturePipelineRequest("req-3"),
		capturePipelineRequest("req-4"),
		capturePipelineRequest("req-5"),
	}
	fake := &diesAfterNCallsProvider{n: 3, reply: "```yaml\n" + validCandidate + "```"}
	cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 1_000_000}

	runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", 3, destDir)
}

// TestRunCapture_ChronicPerPassMiss_CapturedViaDiskCarryForward_FlagsUnreliable
// is the regression test for HIGH-1, the round-5 review of #2814's blocker:
// H1's (round-4) equal-missing-sets exemption for MediansUnreliable and H2's
// (round-4) on-disk carry-forward combine to publish a fabricated quality
// regression with NO evidence anywhere in the committed manifest.
//
// The exemption's premise was "a chronically-missing request is ALSO
// visible as Manifest.MissingCount > 0" — a reader who distrusts a stable-
// looking degraded median still has MissingCount/RequestOutcomes to point
// at the culprit. H2 breaks that premise for exactly this scenario: run 1
// captures req-b (among others) cleanly, giving it a real committed
// transcript; run 2, same destDir, req-b 429s on EVERY call of EVERY pass —
// req-b's missing set is identical pass to pass (the exemption's only
// check, pre-fix), but requestIsCaptured's disk fallback still reports it
// captured overall, so MissingCount stays 0 and RequestOutcomes says
// Captured: true. Before this fix, MediansUnreliable never fired and the
// manifest published medianValidatePassRate dropping from run 1's 1.000 to
// run 2's 0.667 with nothing else in the file explaining why.
//
// Because this scenario makes runCapture's own t.Errorf fire (same as
// TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable, whose own
// doc comment explains why that cannot be asserted in-process), this
// re-execs the built test binary the same way.
//
// Reverting missingSetsAreReliable back to a bare allMissingSetsEqual call
// (dropping the carriedForwardIDs check) makes this test fail:
// MediansUnreliable comes back false.
func TestRunCapture_ChronicPerPassMiss_CapturedViaDiskCarryForward_FlagsUnreliable(t *testing.T) {
	const helperEnv = "CONDUIT_TEST_CHRONICCARRYFORWARD_HELPER"
	const destDirEnv = "CONDUIT_TEST_CHRONICCARRYFORWARD_DESTDIR"

	if os.Getenv(helperEnv) == "1" {
		runChronicPerPassMissCarryForwardHelper(t, os.Getenv(destDirEnv))
		return
	}

	destDir := t.TempDir()

	// Run 1 (parent process): a provider that succeeds unconditionally,
	// including for req-b's prompt — gives req-b a real, valid, committed
	// transcript to carry forward. partialFailureFixture's own cp/destDir
	// are unused here (both processes independently rebuild the same
	// deterministic `requests` slice — no cross-process state to share).
	requests, _, _, _ := partialFailureFixture(t)
	cleanProvider := &diesAfterNCallsProvider{n: 1000, reply: "```yaml\n" + validCandidate + "```"}
	cpClean := &captureProvider{Provider: cleanProvider, maxCalls: 1000, maxTokens: 1_000_000}
	first, _, wrote1 := runCapture(context.Background(), t, requests, cpClean, "anthropic", "claude-sonnet-5-test", 3, destDir)
	if !wrote1 {
		t.Fatal("expected the first run to write a manifest")
	}
	if first.MedianValidatePassRate != 1 || first.MediansUnreliable {
		t.Fatalf("first run: MedianValidatePassRate/MediansUnreliable = %v/%v, want 1/false",
			first.MedianValidatePassRate, first.MediansUnreliable)
	}

	// Run 2 (the subprocess): req-b 429s on every call, every pass — the
	// real regression scenario.
	cmd := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=^TestRunCapture_ChronicPerPassMiss_CapturedViaDiskCarryForward_FlagsUnreliable$",
		"-test.v",
	)
	cmd.Env = append(os.Environ(), helperEnv+"=1", destDirEnv+"="+destDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the subprocess (run 2, req-b chronically missing but carried forward from run 1) "+
			"to exit nonzero via t.Errorf; output:\n%s", out)
	}
	if !strings.Contains(string(out), "every one of 3 capture pass(es) was degraded") {
		t.Fatalf("subprocess failed as expected, but not for the reason this test checks — output:\n%s", out)
	}

	data, rerr := os.ReadFile(filepath.Join(destDir, manifestFileName))
	if rerr != nil {
		t.Fatalf("expected manifest.yaml to still be written despite the failure: %v\nsubprocess output:\n%s", rerr, out)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}

	// The core HIGH-1 regression: MediansUnreliable must be true, even
	// though CapturedCount/MissingCount show a fully-captured corpus.
	if !manifest.MediansUnreliable {
		t.Fatal("manifest.MediansUnreliable = false, want true — req-b is captured every pass ONLY via disk " +
			"carry-forward from run 1, so the equal-missing-sets exemption must not apply")
	}
	if manifest.CapturedCount != 3 || manifest.MissingCount != 0 {
		t.Fatalf("manifest.CapturedCount/MissingCount = %d/%d, want 3/0 — this IS the fabrication HIGH-1 "+
			"describes: MissingCount alone gives no evidence req-b ever failed this run, which is exactly "+
			"why MediansUnreliable must carry that signal instead", manifest.CapturedCount, manifest.MissingCount)
	}
	wantDegraded := []int{1, 2, 3}
	if !reflect.DeepEqual(manifest.DegradedPasses, wantDegraded) {
		t.Fatalf("manifest.DegradedPasses = %v, want %v", manifest.DegradedPasses, wantDegraded)
	}
	if want := 2.0 / 3.0; manifest.MedianValidatePassRate != want {
		t.Fatalf("manifest.MedianValidatePassRate = %v, want %v (the reviewer's own repro: 1.000 -> 0.667)",
			manifest.MedianValidatePassRate, want)
	}

	// M2 (round-5 review of #2814), verified in the same scenario: req-b's
	// own outcome, though Captured, must still explain that THIS run's own
	// attempt failed — otherwise MediansUnreliable would be the ONLY
	// evidence left in the file.
	var gotReqB RequestOutcome
	found := false
	for _, o := range manifest.RequestOutcomes {
		if o.RequestID == "req-b" {
			gotReqB, found = o, true
		}
	}
	if !found || !gotReqB.Captured || gotReqB.FailureReason == "" {
		t.Fatalf("req-b outcome = %+v (found=%v), want Captured=true with a non-empty FailureReason (M2)",
			gotReqB, found)
	}
}

// runChronicPerPassMissCarryForwardHelper is run 2 of
// TestRunCapture_ChronicPerPassMiss_CapturedViaDiskCarryForward_FlagsUnreliable's
// scenario, inside its re-exec'd subprocess — mirrors
// runAllPassesDegradedHelper's own reasoning for why this needs a real
// *testing.T in a subprocess rather than an in-process call.
func runChronicPerPassMissCarryForwardHelper(t *testing.T, destDir string) {
	t.Helper()
	requests, cp, _, _ := partialFailureFixture(t)
	runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", 3, destDir)
}

// TestPreserveMediansIfNothingPromoted_ShapeMismatch_FlagsUnreliableNotStale
// is the regression test for MEDIUM-4 (round-5 review of #2814):
// preserveMediansIfNothingPromoted used to reuse a prior manifest's
// medians/PassScores with no check that the prior run even describes the
// SAME corpus. Reproduced: run 1 captures a 4-request corpus cleanly over 3
// passes (median 1.000); the corpus then grows to 6 requests, and run 2 (1
// pass, provider fully dead) promotes nothing new. Pre-fix, the published
// manifest.yaml said requestCount: 6 with medianValidatePassRate: 1.000 and
// three passScores[].total entries still reading 4 — a corpus a third of
// which was never captured, reporting a perfect score, with
// capturedAt/corpusCommitSha/captureCommand all refreshed to run 2 so
// nothing marked the medians as foreign. The documented backstop
// (captureCompletenessVerdict failing a run that captured NONE of its
// requests) does not catch this: H2's carry-forward keeps capturedCount at
// 4 (the old requests are still really on disk), so that verdict never
// fires.
//
// This scenario also makes runCapture's own t.Errorf fire (the shape
// mismatch forces degradedMedians.allDegraded, same signal
// TestRunCapture_AllPassesDegraded_FailsTheRunAndFlagsUnreliable uses), so
// it re-execs the built test binary the same way.
//
// Reverting JUST the RequestCount/Passes shape guard in
// preserveMediansIfNothingPromoted (while keeping HIGH-1's carriedForwardIDs
// fix) still makes this test fail: MediansUnreliable comes back false and
// PassScores[].Total comes back 4, both wrongly carried from run 1's
// shape — because preserveMediansIfNothingPromoted runs unconditionally
// whenever moved is empty and unconditionally overwrites whatever
// summarizePasses computed for THIS run, including a correctly-computed
// allDegraded=true.
func TestPreserveMediansIfNothingPromoted_ShapeMismatch_FlagsUnreliableNotStale(t *testing.T) {
	const helperEnv = "CONDUIT_TEST_SHAPEMISMATCH_HELPER"
	const destDirEnv = "CONDUIT_TEST_SHAPEMISMATCH_DESTDIR"

	if os.Getenv(helperEnv) == "1" {
		runShapeMismatchHelper(t, os.Getenv(destDirEnv))
		return
	}

	destDir := t.TempDir()

	// Run 1 (parent process): 4 requests, 3 passes, clean — a real baseline
	// to protect (or, pre-fix, to wrongly leak into a differently-shaped
	// run).
	oldRequests := []Request{
		capturePipelineRequest("req-1"),
		capturePipelineRequest("req-2"),
		capturePipelineRequest("req-3"),
		capturePipelineRequest("req-4"),
	}
	cleanProvider := &diesAfterNCallsProvider{n: 1000, reply: "```yaml\n" + validCandidate + "```"}
	cpClean := &captureProvider{Provider: cleanProvider, maxCalls: 1000, maxTokens: 1_000_000}
	first, _, wrote1 := runCapture(context.Background(), t, oldRequests, cpClean, "anthropic", "claude-sonnet-5-test", 3, destDir)
	if !wrote1 {
		t.Fatal("expected the first run to write a manifest")
	}
	if first.MedianValidatePassRate != 1 || first.RequestCount != 4 {
		t.Fatalf("first run: MedianValidatePassRate/RequestCount = %v/%d, want 1/4",
			first.MedianValidatePassRate, first.RequestCount)
	}

	// Run 2 (the subprocess): the corpus grows to 6 requests, 1 pass, the
	// provider is fully dead — nothing new is promoted.
	cmd := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=^TestPreserveMediansIfNothingPromoted_ShapeMismatch_FlagsUnreliableNotStale$",
		"-test.v",
	)
	cmd.Env = append(os.Environ(), helperEnv+"=1", destDirEnv+"="+destDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the subprocess (run 2, shape-mismatched, nothing promoted) to exit nonzero via "+
			"t.Errorf; output:\n%s", out)
	}

	data, rerr := os.ReadFile(filepath.Join(destDir, manifestFileName))
	if rerr != nil {
		t.Fatalf("expected manifest.yaml to still be written despite the failure: %v\nsubprocess output:\n%s", rerr, out)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}

	if manifest.RequestCount != 6 {
		t.Fatalf("manifest.RequestCount = %d, want 6", manifest.RequestCount)
	}
	// The core MEDIUM-4 regression: MediansUnreliable must be true, and
	// PassScores must describe THIS run's own (zero) shape — never the
	// prior run's stale 4-request one.
	if !manifest.MediansUnreliable {
		t.Fatal("manifest.MediansUnreliable = false, want true — the prior manifest describes a " +
			"different-shaped corpus (4 requests, 3 passes) than this run (6 requests, 1 pass) and must not " +
			"be published as this run's baseline")
	}
	for i, ps := range manifest.PassScores {
		if ps.Total != 6 {
			t.Fatalf("manifest.PassScores[%d].Total = %d, want 6 (this run's own corpus size) — a value of "+
				"4 here would mean the prior run's shape leaked through despite the mismatch", i, ps.Total)
		}
	}
	// The old 4 requests are still real, valid, on-disk transcripts (H2
	// carry-forward) — only the 2 new ones were never captured by anything.
	if manifest.CapturedCount != 4 || manifest.MissingCount != 2 {
		t.Fatalf("manifest.CapturedCount/MissingCount = %d/%d, want 4/2", manifest.CapturedCount, manifest.MissingCount)
	}
}

// runShapeMismatchHelper is run 2 of
// TestPreserveMediansIfNothingPromoted_ShapeMismatch_FlagsUnreliableNotStale's
// scenario, inside its re-exec'd subprocess.
func runShapeMismatchHelper(t *testing.T, destDir string) {
	t.Helper()
	newRequests := []Request{
		capturePipelineRequest("req-1"),
		capturePipelineRequest("req-2"),
		capturePipelineRequest("req-3"),
		capturePipelineRequest("req-4"),
		capturePipelineRequest("req-5"),
		capturePipelineRequest("req-6"),
	}
	deadProvider := &diesAfterNCallsProvider{n: 0, reply: "```yaml\n" + validCandidate + "```"}
	cpDead := &captureProvider{Provider: deadProvider, maxCalls: 1000, maxTokens: 1_000_000}
	runCapture(context.Background(), t, newRequests, cpDead, "anthropic", "claude-sonnet-5-test", 1, destDir)
}

// TestRequestIsCaptured_RejectsContentMismatch is the focused unit
// regression test for MEDIUM-5 (round-5 review of #2814): a bare os.Stat
// used to be the entire check requestIsCaptured ran, which is true for a
// directory, a zero-byte file, or (the dangerous case here) a transcript
// captured for a DIFFERENT prompt under an id that was never renamed.
// Reverting requestIsCaptured back to a bare os.Stat makes this test fail:
// a mismatched-content file would be reported captured.
func TestRequestIsCaptured_RejectsContentMismatch(t *testing.T) {
	destDir := t.TempDir()
	original := Request{ID: "req-a", Prompt: "read from the generator and write to the log"}
	edited := Request{ID: "req-a", Prompt: "read from the generator and write to the log (edited)"}

	// No file at all: never captured.
	if requestIsCaptured(original, destDir) {
		t.Fatal("requestIsCaptured() = true with no file on disk, want false")
	}

	writeScratchTranscript(t, destDir, Transcript{
		SchemaVersion:      TranscriptSchemaVersion,
		RequestID:          original.ID,
		Provider:           "anthropic",
		Model:              "claude-sonnet-5-test",
		CorpusPromptSHA256: sha256Hex(original.Prompt),
		SystemPromptSHA256: "deadbeef",
		CatalogFingerprint: "deadbeef",
		Turns:              []Turn{{N: 1, UserPromptSHA256: "deadbeef", TokensUsed: 1, CompletionText: "x"}},
	})

	// Same prompt as when captured: valid, counts as captured.
	if !requestIsCaptured(original, destDir) {
		t.Fatal("requestIsCaptured() = false for a valid, matching transcript, want true")
	}
	// Edited prompt under the SAME id: the file on disk answers a
	// DIFFERENT question now — must not count as captured.
	if requestIsCaptured(edited, destDir) {
		t.Fatal("requestIsCaptured() = true for a transcript captured against a DIFFERENT prompt under the " +
			"same id, want false — this is exactly the edited-prompt-unchanged-id case AC 1.20 exists to catch")
	}

	// Corrupt/non-YAML content: never captured, never a panic.
	if err := os.WriteFile(filepath.Join(destDir, "req-corrupt.yaml"), []byte("not: valid: yaml: :::"), 0o600); err != nil {
		t.Fatalf("writing corrupt fixture: %v", err)
	}
	if requestIsCaptured(Request{ID: "req-corrupt", Prompt: "x"}, destDir) {
		t.Fatal("requestIsCaptured() = true for unparseable YAML, want false")
	}
}

// TestRunCapture_EditedPromptUnderUnchangedID_NotCountedCaptured is the
// end-to-end sibling of TestRequestIsCaptured_RejectsContentMismatch (M5,
// round-5 review of #2814): drives runCapture itself rather than calling
// requestIsCaptured directly, and proves the fallback this situation lands
// in — a tombstone written ALONGSIDE the stale, mismatched transcript —
// is exactly what makes LoadTranscripts (transcript.go) hard-error on load,
// per requestIsCaptured's own doc comment ("the loud, specific failure this
// situation deserves rather than a silently wrong manifest").
func TestRunCapture_EditedPromptUnderUnchangedID_NotCountedCaptured(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")
	original := []Request{capturePipelineRequest("req-a"), capturePipelineRequest("req-b")}

	cleanProvider := &diesAfterNCallsProvider{n: 1000, reply: "```yaml\n" + validCandidate + "```"}
	cpClean := &captureProvider{Provider: cleanProvider, maxCalls: 1000, maxTokens: 1_000_000}
	first, _, wrote1 := runCapture(context.Background(), t, original, cpClean, "anthropic", "claude-sonnet-5-test", 1, destDir)
	if !wrote1 || first.CapturedCount != 2 {
		t.Fatalf("expected the first run to capture both requests cleanly, got %+v", first)
	}

	// req-a's prompt is edited under its UNCHANGED id; req-b is untouched.
	// The provider is fully dead this run, so nothing new is promoted.
	edited := []Request{
		{
			ID: "req-a", Prompt: "read from the generator and write to the log (edited)",
			Expect: Expect{SourceCategory: "generator", DestinationCategory: "log"},
		},
		capturePipelineRequest("req-b"),
	}
	deadProvider := &diesAfterNCallsProvider{n: 0, reply: "```yaml\n" + validCandidate + "```"}
	cpDead := &captureProvider{Provider: deadProvider, maxCalls: 1000, maxTokens: 1_000_000}
	second, _, wrote2 := runCapture(context.Background(), t, edited, cpDead, "anthropic", "claude-sonnet-5-test", 1, destDir)
	if !wrote2 {
		t.Fatal("expected the second run to still write a manifest")
	}

	// The core MEDIUM-5 regression: req-a must NOT count as captured — the
	// on-disk file answers a prompt this run no longer asks.
	if second.CapturedCount != 1 || second.MissingCount != 1 {
		t.Fatalf("second.CapturedCount/MissingCount = %d/%d, want 1/1 — req-a's stale, mismatched transcript "+
			"must not be counted captured", second.CapturedCount, second.MissingCount)
	}
	var gotReqA RequestOutcome
	for _, o := range second.RequestOutcomes {
		if o.RequestID == "req-a" {
			gotReqA = o
		}
	}
	if gotReqA.Captured {
		t.Fatalf("req-a outcome = %+v, want Captured=false", gotReqA)
	}

	// Both the stale transcript AND the new tombstone now exist for req-a —
	// deliberately not auto-resolved (see requestIsCaptured's doc comment).
	if _, err := os.Stat(filepath.Join(destDir, "req-a.yaml")); err != nil {
		t.Fatalf("expected the stale req-a.yaml to still be on disk, untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "req-a"+tombstoneFileSuffix)); err != nil {
		t.Fatalf("expected a NEW tombstone for req-a: %v", err)
	}

	// The safety net: LoadTranscripts must hard-error on the coexistence,
	// naming req-a specifically — never silently pick one file over the
	// other.
	_, loadErr := LoadTranscripts(destDir, edited)
	if loadErr == nil {
		t.Fatal("LoadTranscripts(destDir, edited) = nil error, want a hard error — req-a carries BOTH a " +
			"transcript and a tombstone")
	}
	if !strings.Contains(loadErr.Error(), "req-a") || !strings.Contains(loadErr.Error(), "BOTH") {
		t.Fatalf("LoadTranscripts error = %q, want it to name req-a and explain the transcript+tombstone conflict", loadErr.Error())
	}
}

// TestScanAndPromoteScratch_OneViolationBlocksTheWholeBatch proves plan §5 /
// task item 1's all-or-nothing guarantee directly against the promotion gate:
// two real, Generate-produced transcripts are written to scratch (one clean,
// one with a completion carrying a deny-listed secret pattern), and a SINGLE
// violation must block BOTH from being promoted — including the entirely
// clean one. A partial promotion would leave the working tree in a state no
// single commit represents.
func TestScanAndPromoteScratch_OneViolationBlocksTheWholeBatch(t *testing.T) {
	cleanReply := "```yaml\n" + validCandidate + "```"
	secretReply := "```yaml\n" + validCandidate + "```\nnote: sk-ant-1234567890abcdefghijklmnop"

	requests := []Request{capturePipelineRequest("req-clean"), capturePipelineRequest("req-secret")}
	fake := &fakeProvider{replies: []string{cleanReply, secretReply}, tokens: 10}
	cp := &captureProvider{Provider: fake, maxCalls: 1000, maxTokens: 1_000_000}

	system := BuildSystemPrompt(BuiltinCatalog())
	systemSHA := sha256Hex(system)
	catalogFP := CatalogFingerprint()

	scratchDir := t.TempDir()
	for _, req := range requests {
		cp.startRequest()
		gen, genErr := Generate(context.Background(), Input{Prompt: req.Prompt, Provider: cp, Model: "claude-sonnet-5-test"})
		raw := cp.recordedTurns()
		if len(raw) == 0 {
			t.Fatalf("setup: no completion recorded for %q: %v", req.ID, genErr)
		}
		tr := buildTranscript(req, gen, raw, "anthropic", "claude-sonnet-5-test", systemSHA, catalogFP, time.Now().UTC())
		writeScratchTranscript(t, scratchDir, tr)
	}

	destDir := filepath.Join(t.TempDir(), "anthropic", "claude-sonnet-5-test")
	moved, findings := scanAndPromoteScratch(context.Background(), t, scratchDir, destDir)

	if len(findings) == 0 {
		t.Fatal("expected the secret-carrying transcript to produce at least one finding")
	}
	if len(moved) != 0 {
		t.Fatalf("expected NOTHING promoted when any finding exists, got %v", moved)
	}
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Fatalf("expected destDir %q to never have been created (nothing was ever promoted into it), stat err = %v", destDir, err)
	}
}
