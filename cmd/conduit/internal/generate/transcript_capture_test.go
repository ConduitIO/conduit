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
package generate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
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
			ValidatePass:   last.Report.OK(),
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
// batch, and — only for a run that captured every given request in every
// pass — scores the run (ScoreMedian) and writes manifest.yaml.
//
// Split out from TestCaptureTranscripts so the FULL pipeline (transcript
// construction, the redaction-scan-then-promote gate, scoring, manifest
// provenance) is exercised by TestRunCapture_FullPipeline_FakeProvider
// against a fake provider and a scratch destDir — no live key, no network, and
// never touching the real
// committed testdata/transcripts.
func runCapture(ctx context.Context, t *testing.T, requests []Request, cp *captureProvider, providerName, model string, passes int, destDir string) (manifest Manifest, wrote bool) {
	t.Helper()

	runStart := time.Now().UTC()
	system := BuildSystemPrompt(BuiltinCatalog())
	systemSHA := sha256Hex(system)
	catalogFP := CatalogFingerprint()

	scratchDir := t.TempDir()

	var scoreCandidatesList []Candidates
	for pass := 1; pass <= passes; pass++ {
		passCandidates := make(Candidates, len(requests))
		for _, req := range requests {
			t.Run(req.ID, func(t *testing.T) {
				cp.startRequest()
				gen, genErr := Generate(ctx, Input{Prompt: req.Prompt, Provider: cp, Model: model})
				raw := cp.recordedTurns()
				passCandidates[req.ID] = gen.Candidate

				if len(raw) == 0 {
					t.Errorf("pass %d: no completion recorded for %q: %v", pass, req.ID, genErr)
					return
				}

				tr := buildTranscript(req, gen, raw, providerName, model, systemSHA, catalogFP, time.Now().UTC())
				writeScratchTranscript(t, scratchDir, tr)

				if genErr != nil {
					// Partial success: at least one completion was recorded
					// and promoted to scratch, but the request never cleared
					// the budget (or a later call in this same request tripped
					// a ceiling). Flag it loudly rather than silently
					// committing a transcript for a request that never
					// actually succeeded.
					t.Logf("pass %d: %q did not clear the generation budget: %v", pass, req.ID, genErr)
				}
			})
		}

		if len(passCandidates) == len(requests) {
			scoreCandidatesList = append(scoreCandidatesList, passCandidates)
		} else {
			t.Logf("pass %d: %d/%d requests ran (a -run filter is scoping this to a subset) — "+
				"excluded from corpus-level scoring and the manifest", pass, len(passCandidates), len(requests))
		}
	}

	moved, findings := scanAndPromoteScratch(ctx, t, scratchDir, destDir)
	if len(findings) > 0 {
		t.Fatalf("redaction scan found %d issue(s) — refusing to move ANY captured transcript into %q:\n%s",
			len(findings), destDir, strings.Join(findings, "\n"))
	}
	t.Logf("promoted %d transcript(s) into %q", len(moved), destDir)

	if len(scoreCandidatesList) != passes {
		t.Logf("only %d/%d pass(es) captured the full corpus — skipping manifest.yaml "+
			"(a scoped -run leaves the existing manifest untouched)", len(scoreCandidatesList), passes)
		return Manifest{}, false
	}

	ms := ScoreMedian(ctx, requests, scoreCandidatesList)
	passScores := make([]PassScore, len(ms.Runs))
	validateCounts := make([]int, len(ms.Runs))
	semanticCounts := make([]int, len(ms.Runs))
	for i, rs := range ms.Runs {
		passScores[i] = PassScore{
			Pass:               i + 1,
			Total:              rs.Total,
			ValidatePassCount:  rs.ValidatePassCount,
			ValidatePassRate:   rs.ValidatePassRate,
			SemanticMatchCount: rs.SemanticMatchCount,
			SemanticMatchRate:  rs.SemanticMatchRate,
		}
		validateCounts[i] = rs.ValidatePassCount
		semanticCounts[i] = rs.SemanticMatchCount
	}

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
		MedianValidatePassRate:   ms.ValidatePassRate,
		MedianSemanticMatchRate:  ms.SemanticMatchRate,
		MedianValidatePassCount:  medianInt(validateCounts),
		MedianSemanticMatchCount: medianInt(semanticCounts),
		PassScores:               passScores,
	}
	writeManifest(t, destDir, manifest)
	t.Logf("wrote manifest: %d calls, %d tokens, ~$%.2f estimated, median validate %.1f%%, median semantic %.1f%%",
		cp.totalCalls(), cp.totalTokens(), manifest.EstimatedCostUSD,
		ms.ValidatePassRate*100, ms.SemanticMatchRate*100)
	return manifest, true
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

	manifest, wrote := runCapture(context.Background(), t, requests, cp, "anthropic", "claude-sonnet-5-test", passes, destDir)

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
