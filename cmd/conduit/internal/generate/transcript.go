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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// TranscriptSchemaVersion is the only schemaVersion LoadTranscripts accepts
// today. Bumping it is a format change: add a migration in LoadTranscripts
// (or a version-dispatch shim) in the same PR that bumps it, per CLAUDE.md's
// "serialized state ... must be readable by N+1 versions" — the future
// capture tool (A5a-3) always writes the current constant, never a caller-
// chosen value.
const TranscriptSchemaVersion = 1

// manifestFileName is the one file in a transcripts/<provider>/<model>/
// directory that is NOT a per-request transcript — LoadTranscripts and the
// redaction scanner both skip it by name when listing a directory.
const manifestFileName = "manifest.yaml"

// tombstoneFileSuffix marks a legitimately-missing transcript: a corpus
// request that was attempted but never produced a single completion on any
// pass (RequestOutcome.Captured == false, runCapture in
// transcript_capture_test.go) gets a "<id>.missing.yaml" file committed in
// place of "<id>.yaml". See the Tombstone type doc comment for why
// LoadTranscripts needs this rather than tolerating a missing file
// outright.
const tombstoneFileSuffix = ".missing.yaml"

// Transcript is one committed provider transcript: everything replay needs
// to answer Generate's calls for exactly one corpus Request, plus the
// metadata needed to decide whether it is still meaningful (design doc §10;
// plan doc "WS1 A5a/A5b" §3.1/§3.3).
//
// One Transcript is committed per corpus request, per provider, per model,
// as its own file at
// testdata/transcripts/<provider>/<model>/<requestID>.yaml — never a single
// file for the whole corpus, so that a diff reviewing one re-captured
// request never touches the other 27.
//
// Not captured, structurally: raw HTTP response bodies, headers, or any
// provider-side identifier (request-id, rate-limit counters, org id).
// CompletionResult (provider.CompletionResult) is the only thing an adapter
// ever hands back to Conduit's own code, and it carries none of those — see
// provider/provider.go's doc comment. A committed transcript can only ever
// carry what CompletionResult carries.
type Transcript struct {
	SchemaVersion int `yaml:"schemaVersion"`
	// RequestID must equal the corpus Request.ID this transcript was
	// captured against, AND the file's own basename (minus ".yaml") —
	// LoadTranscripts hard-errors if either disagrees with the other.
	RequestID string `yaml:"requestID"`
	// Provider is the adapter name that produced this transcript
	// (provider.NameAnthropic etc.), informational plus the value
	// ReplayProviderFor reports from Replay.Name().
	Provider string `yaml:"provider"`
	// Model is the provider-specific model identifier the capture run used.
	Model string `yaml:"model"`
	// CapturedAt is when the capture tool made the LAST call recorded in
	// Turns — never re-derived from a file's mtime, which a `git checkout`
	// or CI clone would silently reset.
	CapturedAt time.Time `yaml:"capturedAt"`
	// CorpusPromptSHA256 is sha256Hex(Request.Prompt) for RequestID, AT
	// CAPTURE TIME. LoadTranscripts recomputes it from the corpus passed in
	// and hard-errors on a mismatch — the check that catches a request
	// edited under an unchanged id (§3.3, AC 1.20).
	CorpusPromptSHA256 string `yaml:"corpusPromptSHA256"`
	// SystemPromptSHA256 is sha256Hex(BuildSystemPrompt(BuiltinCatalog()))
	// at capture time — the grounding prompt's own identity, distinct from
	// CorpusPromptSHA256 (the user request). LoadTranscripts compares this
	// against the CURRENT binary's system prompt hash to classify staleness
	// (classifyStaleness); a mismatch here is never an error, only data.
	SystemPromptSHA256 string `yaml:"systemPromptSHA256"`
	// CatalogFingerprint is CatalogFingerprint() at capture time — the
	// catalog's SEMANTIC identity (grounding.go), blind to descriptions and
	// defaults. Distinguishes cosmetic grounding drift (system prompt text
	// moved, e.g. a reworded description) from semantic drift (a
	// connector's actual parameter set moved) in classifyStaleness.
	CatalogFingerprint string `yaml:"catalogFingerprint"`
	// Turns is every provider call made for this request, in the order
	// Generate's retry loop made them — Turns[0] is attempt 1, Turns[1] the
	// first retry, and so on. ReplayProviderFor hands these back in this
	// same order, one per Complete call.
	Turns []Turn `yaml:"turns"`
	// Outcome is this transcript's own scored result at capture time — the
	// corpus verdict a re-capture is expected to keep clearing.
	Outcome Outcome `yaml:"outcome"`
}

// Turn is one provider call recorded within a Transcript.
type Turn struct {
	// N is the 1-based attempt number, matching Attempt.N (generate.go) —
	// carried for human readability in a diff; LoadTranscripts does not
	// require it to be contiguous or sorted, since a turn's POSITION in the
	// YAML list (not this field) is what ReplayProviderFor uses as replay
	// order.
	N int `yaml:"n"`
	// UserPromptSHA256 is sha256Hex of the exact prompt text sent for THIS
	// turn (the original request plus any prior turn's retry feedback,
	// promptWithFeedback in generate.go) — distinct from
	// Transcript.CorpusPromptSHA256, which hashes only the corpus's own
	// Request.Prompt and never the feedback-augmented text.
	UserPromptSHA256 string `yaml:"userPromptSHA256"`
	// TokensUsed is what the provider reported for this call, never
	// estimated — provider.CompletionResult's own invariant, carried
	// through unchanged.
	TokensUsed int `yaml:"tokensUsed"`
	// CompletionText is the model's reply, VERBATIM — narration, fences, and
	// all. extractCandidate's lenient reader is part of what replay
	// exercises (plan §3.1), so trimming or pre-extracting here would remove
	// exactly the thing a replay run is supposed to prove still works.
	CompletionText string `yaml:"completionText"`
}

// Outcome is a transcript's own scored result, recorded at capture time —
// what ScoreRun found for this request against Turns' LAST candidate, the
// day the transcript was captured.
type Outcome struct {
	ValidatePass  bool `yaml:"validatePass"`
	SemanticMatch bool `yaml:"semanticMatch"`
	// SemanticIssues carries scoreSemantic's Issues when SemanticMatch is
	// false — never populated when true, since a passing outcome has nothing
	// to explain.
	SemanticIssues []string `yaml:"semanticIssues,omitempty"`
}

// Manifest is the per-<provider>/<model> capture-run summary committed
// alongside its transcripts (plan §3.1, AC 1.23's "manifest records measured
// tokens and cost"). Its schema is defined here because it is part of the
// committed layout; WRITING one is the capture tool's job (A5a-3:
// transcript_capture_test.go's TestCaptureTranscripts) — LoadTranscripts
// never reads or requires it, so a missing or malformed manifest.yaml can
// never block replay.
// Model, CaptureCommand, and CorpusCommitSHA below carry NO in-process
// scanTextForSecrets call of their own — the pre-promotion redaction gate
// (runCapture's reasonFindings check, transcript_capture_test.go) only
// scans RequestOutcome.FailureReason/Tombstone.FailureCode, the two
// free-text fields safeFailureReason's doc comment covers. This is
// deliberate, not an oversight (nit from the round-2 review of #2814,
// which asked this be made explicit rather than left implicit): all three
// are derived from inputs the OPERATOR running this capture already
// controls — Model and CaptureCommand from the CLI flags/env vars that
// operator set, CorpusCommitSHA from `git log` against this repo's own
// history — never from provider response text, which is the actual attack
// surface every OTHER redaction gate in this package exists for. The
// untagged TestTranscripts_CarryNoSecretMaterial (secrets_scan_test.go)
// still scans the fully-marshalled manifest.yaml as raw text on every PR
// as a backstop, independent of this reasoning.
type Manifest struct {
	SchemaVersion int    `yaml:"schemaVersion"`
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	// CapturedAt is the capture run's own start time, distinct from any one
	// Transcript.CapturedAt.
	CapturedAt   time.Time `yaml:"capturedAt"`
	RequestCount int       `yaml:"requestCount"`
	// TotalTokensUsed sums every Turn.TokensUsed the ENTIRE capture run
	// produced — every pass, every request, whether or not that pass ended up
	// promoted into testdata/transcripts — MEASURED, from providers' own
	// reported usage, per provider.CompletionResult's "never estimated"
	// invariant. This is deliberately the real total spend for the `go test`
	// invocation this manifest describes, not just the tokens behind the
	// (single, most-recent-successful-pass) transcripts actually committed,
	// because AC 1.23's "measured tokens and cost" is a cost-accounting
	// figure, not a per-file rollup.
	//
	// provider.CompletionResult carries only a COMBINED input+output count
	// (see its own doc comment and every adapter's Complete) — there is no
	// per-direction split anywhere in the Provider seam. A field here named
	// InputTokensUsed/OutputTokensUsed would have to be either always zero
	// (misleading — indistinguishable from "the provider reported zero") or
	// fabricated by assuming a split (exactly the "never estimated" rule this
	// field's doc exists to hold the line on). Splitting the count would
	// require a shipped-adapter change (each adapter's anthropicResponse-style
	// struct would need to keep Usage.InputTokens/OutputTokens separately
	// instead of summing them into CompletionResult.TokensUsed) — out of
	// scope for a maintenance-only capture tool; deferred alongside the
	// cache_control adapter change plan §4 already defers to v0.21.
	TotalTokensUsed int `yaml:"totalTokensUsed"`
	// EstimatedCostUSD prices TotalTokensUsed at anthropicBlendedRatePerMTokUSD
	// — an estimate (the field name says so, and unlike TotalTokensUsed this
	// field is allowed to be one): actual billed cost depends on rate-card
	// details (cached-prefix discounts, promotional pricing) this package has
	// no way to observe, AND on the real input/output split, which
	// TotalTokensUsed's own doc comment explains this package cannot measure.
	EstimatedCostUSD float64 `yaml:"estimatedCostUSD"`

	// --- A5a-3 additions: capture provenance and corpus-level scoring ---

	// CaptureCommand is the (secret-redacted) invocation that produced this
	// manifest — plan §4's methodology line, recorded rather than
	// re-documented, so a manifest a year from now is self-describing without
	// cross-referencing whatever the plan doc said at the time.
	CaptureCommand string `yaml:"captureCommand"`
	// CorpusCommitSHA is `git log -1 --format=%H` for
	// testdata/eval_requests.yaml at capture time — the commit that last
	// touched the corpus, not repo HEAD (which would drift on every unrelated
	// commit even when the corpus itself never changed). "unknown" when git
	// is unavailable or the lookup fails — capture must not fail (money
	// already spent) just because provenance couldn't be recorded.
	CorpusCommitSHA string `yaml:"corpusCommitSha"`
	// CatalogFingerprint is CatalogFingerprint() at capture time — the same
	// value every individual Transcript.CatalogFingerprint in this run
	// carries, recorded once more here so a reader can confirm the whole run
	// shared one grounding identity without opening every transcript file.
	CatalogFingerprint string `yaml:"catalogFingerprint"`
	// SystemPromptSHA256 is sha256Hex(BuildSystemPrompt(BuiltinCatalog())) at
	// capture time, matching every Transcript.SystemPromptSHA256 in this run.
	SystemPromptSHA256 string `yaml:"systemPromptSha256"`
	// Passes is how many full-corpus capture passes this run performed and
	// scored (plan's cost table assumes 3; TestCaptureTranscripts defaults to
	// 3, overridable via CONDUIT_GENERATE_CAPTURE_PASSES).
	Passes int `yaml:"passes"`
	// MedianValidatePassRate and MedianSemanticMatchRate are the median of
	// the two rates (fraction 0..1) across every CLEAN pass — a pass whose
	// PassScores[i].MissingCount is 0, i.e. it produced a completion for
	// every corpus request. A DEGRADED pass (PassScores[i].MissingCount >
	// 0 — a rate-limit storm or captureWallClockBudget expiring partway
	// through wipes every request from that pass onward) is excluded from
	// this median: ScoreRun scores a missing candidate as a hard fail on
	// both axes (score.go), so folding a degraded pass in here would drag
	// these two headline numbers toward zero even though the requests it
	// dropped are still recorded elsewhere (RequestOutcomes, and possibly
	// captured by another pass). Falls back to the median across ALL
	// passes only when every single pass is degraded (no clean pass
	// exists to prefer). DegradedPasses names which passes were excluded;
	// PassScores carries every pass's own rate, degraded or not, for the
	// detail this field summarizes away. Never best-of (CLAUDE.md's
	// benchmarking discipline).
	MedianValidatePassRate  float64 `yaml:"medianValidatePassRate"`
	MedianSemanticMatchRate float64 `yaml:"medianSemanticMatchRate"`
	// MedianValidatePassCount and MedianSemanticMatchCount are the median of
	// the two RAW COUNTS (not rates) across the same Passes runs — carried
	// alongside the rates so neither metric is ever collapsed into a single
	// percentage (task requirement: "as counts and percentages, never
	// collapsed into one number"). float64 because the median of an
	// even-length series can fall between two integers; Passes defaults to 3
	// (odd), where this is always a whole number in practice.
	MedianValidatePassCount  float64 `yaml:"medianValidatePassCount"`
	MedianSemanticMatchCount float64 `yaml:"medianSemanticMatchCount"`
	// PassScores is every pass's own RunScore rollup (counts AND rates, plan
	// §8.1's "three passes, median never best-of" made auditable) — the
	// median fields above are the headline, this is the detail a regression
	// investigation needs.
	PassScores []PassScore `yaml:"passScores"`

	// --- Partial-results follow-up (WS1 A5a-3, "make a partial capture
	// produce a usable, honest result instead of nothing") ---

	// CapturedCount and MissingCount split RequestCount into what this run
	// actually preserved versus what it could not, ACROSS ALL PASSES —
	// read together with RequestOutcomes for the per-request detail. A
	// request counts as captured the moment ANY pass produced a completion
	// for it (even one whose Transcript.Outcome later failed validate or
	// semantic scoring — that is DATA, not a missing request; see
	// RequestOutcome's own doc comment for the distinction this package
	// draws between the two). See transcript_capture_test.go's
	// captureCompletenessVerdict for the threshold that decides when a
	// nonzero MissingCount should fail the capture run outright rather
	// than merely be noted here.
	//
	// MissingCount is run-scoped, not pass-scoped: a request captured on
	// pass 1 but absent from passes 2-3 (a rate-limit storm or
	// captureWallClockBudget expiring partway through a later pass) counts
	// as captured here — CapturedCount/MissingCount say nothing about
	// which INDIVIDUAL passes went on to score it as present. That
	// per-pass picture, which DOES feed MedianValidatePassRate and
	// MedianSemanticMatchRate (a pass missing a request scores it as a
	// hard fail on both axes — score.go's ScoreRun), lives in
	// PassScores[i].MissingCount and DegradedPasses below. Do not read
	// MissingCount == 0 as "the scored rates saw a complete corpus on
	// every pass" — check DegradedPasses for that.
	CapturedCount int `yaml:"capturedCount"`
	MissingCount  int `yaml:"missingCount"`
	// DegradedPasses names (1-indexed) every pass excluded from
	// MedianValidatePassRate/MedianSemanticMatchRate/
	// MedianValidatePassCount/MedianSemanticMatchCount because it didn't
	// produce a completion for the full corpus (PassScores[i].MissingCount
	// > 0). Empty — and therefore absent from the committed YAML,
	// `omitempty` — when every pass captured the full corpus, which is the
	// common case and needs no reader attention. A nonempty list here is
	// exactly the CapturedCount/MissingCount blind spot documented above
	// made visible and machine-checkable: generate-capture.yml's "Open the
	// PR" step greps for this key too, not just MissingCount, so a run
	// that captured every request overall (MissingCount == 0) but lost a
	// tail pass to rate limiting or the wall-clock budget still gets the
	// PR banner instead of a routine title.
	DegradedPasses []int `yaml:"degradedPasses,omitempty"`
	// RequestOutcomes is this run's final disposition for every request it
	// attempted (the full corpus for a normal run, or the single id a
	// scoped `-run TestCaptureTranscripts/<id>` re-capture names) — in
	// corpus order. It is the field a PR reviewer reads to see "27
	// captured, 1 failed for reason X" without inferring it from a missing
	// file in the diff.
	RequestOutcomes []RequestOutcome `yaml:"requestOutcomes"`
}

// RequestOutcome is one corpus request's final disposition within a capture
// run, distinguishing the two ways a request can end up with no promoted
// transcript from the one way it can fail and still be perfectly good data:
//
//   - Captured = true: at least one pass produced a completion for this
//     request, so a transcript exists in testdata/transcripts. Whether that
//     transcript's own Transcript.Outcome.ValidatePass/SemanticMatch is true
//     or false is irrelevant here — a request whose generation legitimately
//     failed (the model never produced a passing candidate) is still DATA,
//     arguably the most interesting data the benchmark has, and is recorded
//     as captured with its real (failing) Outcome, never as missing.
//   - Captured = false: NO pass ever produced a single completion for this
//     request — a transport error (429, timeout), a tripped ceiling, or a
//     pre-call refusal reached before the provider was ever called. This is
//     absence of data, not a disagreement about what the data says, and
//     FailureReason carries the last such error seen across every pass so a
//     reader knows why without re-running anything.
type RequestOutcome struct {
	RequestID string `yaml:"requestID"`
	Captured  bool   `yaml:"captured"`
	// FailureReason is set only when Captured is false — see the type doc
	// comment. Always empty when Captured is true.
	//
	// This is manifest-safe by construction (safeFailureReason,
	// transcript_capture_test.go): a conduiterr code plus an HTTP status
	// when known, NEVER the provider's own error text — a provider error
	// message may embed up to 512 bytes of raw response body
	// (readErrorBody, provider/http.go), which can echo back
	// caller-supplied or provider-controlled content (e.g. a 401 body
	// quoting the rejected key, a *url.Error carrying credentials embedded
	// in a base URL). manifest.yaml is a committed file with no
	// content-based redaction of its own — TestTranscripts_CarryNoSecretMaterial
	// (secrets_scan_test.go) scans it as raw text specifically because a
	// struct-shaped scan would never look at this field — so nothing that
	// reaches here may ever be raw, unscanned provider text.
	FailureReason string `yaml:"failureReason,omitempty"`
	// Unusable is true when Captured is false AND the underlying failure
	// was a response that DID arrive from the provider but could not be
	// turned into a completion — a decode failure, or a well-formed but
	// empty completion (a refusal) — as opposed to a transport-level
	// failure (network error, timeout, non-2xx status, a tripped ceiling)
	// where no response ever arrived at all (provider.IsUnusableResponse).
	// This is the distinction a 429 and a refusal must never share: both
	// leave Captured false, but only a refusal was a BILLED, attempted
	// call whose tokens are already counted in Manifest.TotalTokensUsed —
	// see captureProvider.Complete (transcript_capture_test.go). Always
	// false when Captured is true.
	Unusable bool `yaml:"unusable,omitempty"`
}

// PassScore is one capture pass's corpus-level score, mirroring RunScore
// (score.go) but without RunScore.Results — the manifest is a summary, not a
// second copy of every per-request finding (those live in each committed
// Transcript.Outcome).
type PassScore struct {
	Pass               int     `yaml:"pass"`
	Total              int     `yaml:"total"`
	ValidatePassCount  int     `yaml:"validatePassCount"`
	ValidatePassRate   float64 `yaml:"validatePassRate"`
	SemanticMatchCount int     `yaml:"semanticMatchCount"`
	SemanticMatchRate  float64 `yaml:"semanticMatchRate"`
	// MissingCount is how many corpus requests produced NO completion on
	// THIS pass specifically (runCapture omits a request from that pass's
	// Candidates rather than scoring an empty-string stand-in — see
	// runCapture's inline comment on why — so ValidatePassRate/
	// SemanticMatchRate above already score every one of these as a fail;
	// this field is what makes that visible instead of silently baked into
	// the rate). A request missing here may still be Manifest.Captured (a
	// different, later pass produced it) — this is per-pass, Manifest.
	// MissingCount is per-run; do not conflate the two. Nonzero marks this
	// pass as "degraded": excluded from Manifest.MedianValidatePassRate/
	// MedianSemanticMatchRate and named in Manifest.DegradedPasses.
	MissingCount int `yaml:"missingCount"`
}

// Tombstone is committed as "<id>.missing.yaml" in place of a Transcript for
// a corpus request that was attempted but never produced a single
// completion on any capture pass (RequestOutcome.Captured == false; see
// runCapture's "Partial results" doc comment, transcript_capture_test.go).
//
// It exists so LoadTranscripts' bijection check (checkBijection) can tell
// "this id was legitimately attempted and came back with nothing" apart
// from "this id was renamed out from under its transcript" WITHOUT
// weakening bijection into blanket tolerance for a missing file —
// LoadTranscripts' own doc comment explains why silently tolerating an
// absent transcript would let a renamed corpus id quietly turn a
// 28-request corpus into a passing 27/28. A tombstone is the explicit,
// reviewable, committed artifact that makes "this id has no data, and
// here's why" a fact checkBijection can see, instead of an absence
// checkBijection has to guess about.
//
// It also survives a lost or stale manifest.yaml (LoadTranscripts never
// reads the manifest at all): bijection stays correct even if
// manifest.yaml is deleted, corrupted, or never committed for some reason,
// because the tombstone — not the manifest — is what checkBijection
// consults.
type Tombstone struct {
	SchemaVersion int `yaml:"schemaVersion"`
	// RequestID must equal the corpus Request.ID this tombstone was written
	// for, AND the file's own basename (minus ".missing.yaml") —
	// LoadTranscripts hard-errors if either disagrees with the other, the
	// same discipline Transcript.RequestID follows.
	RequestID string `yaml:"requestID"`
	// CorpusPromptSHA256 is checked the exact same way a real Transcript's
	// is (LoadTranscripts): a request edited under an unchanged id must
	// still be caught even when that id has no data at all.
	CorpusPromptSHA256 string `yaml:"corpusPromptSHA256"`
	// FailureCode is the manifest-safe reason this id has no transcript —
	// see RequestOutcome.FailureReason's doc comment for what "safe" means
	// here (a conduiterr code plus an HTTP status when known, never the
	// provider's own error text). Required: a tombstone with no reason is
	// as useless as an absent file was.
	FailureCode string `yaml:"failureCode"`
	// CapturedAt is when the capture run that produced this tombstone
	// ran — same meaning as Transcript.CapturedAt.
	CapturedAt time.Time `yaml:"capturedAt"`
}

// LoadManifest reads the manifest.yaml committed alongside one capture run's
// transcripts. It is independent of LoadTranscripts — nothing in bijection or
// prompt-hash validation depends on a manifest existing or parsing.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, cerrors.Errorf("reading manifest %q: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, cerrors.Errorf("parsing manifest %q: %w", path, err)
	}
	return m, nil
}

// Staleness classifies a loaded Transcript against the CURRENT binary's
// grounding, per plan §3.3. It is data, never an error: LoadTranscripts
// returns it on every LoadedTranscript, and it is the CALLER's decision what
// a non-fresh transcript means (a PR-CI warning, a scheduled re-capture, or
// nothing at all). Making drift an error here would be the "red on every
// connector bump" flaky-gate failure CLAUDE.md's process-maturity history
// already names — this package refuses to repeat it.
type Staleness int

const (
	// StalenessFresh: the current binary's system prompt hashes the same as
	// it did at capture time. The transcript is answering exactly the
	// question the binary asks today.
	StalenessFresh Staleness = iota
	// StalenessCosmeticDrift: the system prompt text has changed (a
	// dependabot bump reworded a connector description, say) but
	// CatalogFingerprint — the SEMANTIC grounding identity — has not. The
	// transcript's answer is still valid; only its literal prompt text is
	// out of date.
	StalenessCosmeticDrift
	// StalenessSemanticDrift: CatalogFingerprint itself has changed — a
	// connector's required/optional parameter set moved since capture. The
	// transcript may be answering a question the binary no longer asks the
	// same way.
	StalenessSemanticDrift
)

// String renders s for a report line or a warning annotation.
func (s Staleness) String() string {
	switch s {
	case StalenessFresh:
		return "fresh"
	case StalenessCosmeticDrift:
		return "cosmetic"
	case StalenessSemanticDrift:
		return "semantic"
	default:
		return "unknown"
	}
}

// classifyStaleness is Staleness's pure core, taking the current binary's
// system-prompt hash and catalog fingerprint directly rather than computing
// them itself — exercised in tests against synthetic values, the same
// grounding.go pattern fingerprintCatalog uses for testability without
// depending on the real builtin connector registry.
func classifyStaleness(t Transcript, currentSystemPromptSHA256, currentCatalogFingerprint string) Staleness {
	if t.SystemPromptSHA256 == currentSystemPromptSHA256 {
		return StalenessFresh
	}
	if t.CatalogFingerprint == currentCatalogFingerprint {
		return StalenessCosmeticDrift
	}
	return StalenessSemanticDrift
}

// LoadedTranscript pairs a validated Transcript with its Staleness against
// the CURRENT binary, as computed at LoadTranscripts time.
type LoadedTranscript struct {
	Transcript Transcript
	Staleness  Staleness
}

// LoadResult is LoadTranscripts' return value: every transcript found, keyed
// by request id, already validated against requests.
type LoadResult struct {
	ByID map[string]LoadedTranscript
	// Tombstoned holds every corpus id whose capture legitimately produced
	// no transcript (a committed "<id>.missing.yaml", Tombstone), keyed by
	// request id. checkBijection already proved every corpus id is
	// accounted for by exactly one of ByID or Tombstoned — a caller (the
	// replay/eval consumer) skips a tombstoned id rather than treating its
	// absence from ByID as an error.
	Tombstoned map[string]Tombstone
}

// LoadTranscripts reads every committed transcript in dir (one file per
// corpus request, "<requestID>.yaml"; manifestFileName is skipped, never
// treated as a malformed transcript) and validates it against requests
// BEFORE returning anything a caller could score.
//
// A corpus id may also be satisfied by a committed "<requestID>.missing.yaml"
// Tombstone instead of a transcript — see the Tombstone type doc comment for
// why that is a distinct, explicit case, never absorbed the way a plain
// missing file would be.
//
// ScoreRun counts a request with no candidate as a fail on BOTH metrics —
// correct for a live run, where "no candidate" really does mean the model
// never produced anything usable. For replay that same rule is silently
// wrong: a corpus id renamed out from under its transcript would be absorbed
// as one more "missing candidate", quietly turning a 28-request corpus into
// a 27/28 = 96% pass rate — still above both floors, still green, measuring
// a smaller and differently-shaped corpus than the one actually committed.
// So LoadTranscripts hard-errors, never skips, on:
//
//  1. Bijection — every requests[i].ID has exactly one transcript OR
//     tombstone file in dir, and every transcript/tombstone file's id has a
//     matching entry in requests. Both directions are checked and the error
//     names every offending id, not just the first (AC 1.19). A corpus id
//     with NEITHER a transcript nor a tombstone is still a hard "missing"
//     error — a renamed id is never silently tolerated just because
//     tombstones exist.
//  2. corpusPromptSHA256 equality — a transcript's (or tombstone's) recorded
//     hash of the corpus prompt it was captured against must equal
//     sha256Hex(requests[i].Prompt) for that SAME id today. This is what
//     catches a request's prompt text edited under an id nobody renamed (AC
//     1.20) — bijection alone would pass that case, since the id itself
//     never moved.
//  3. An id carrying BOTH a transcript and a tombstone — self-contradictory
//     (RequestOutcome can only ever report one disposition per id per
//     capture run) and always a hard error rather than a guess about which
//     file to trust.
//
// Grounding staleness (Staleness) is computed and returned on every
// transcript result but is never a reason to fail this call — see
// Staleness's doc comment.
//
// Malformed transcripts (missing required fields, a requestID that disagrees
// with its own filename, an unrecognized schemaVersion) are also hard
// errors, matching LoadRequests' "never silently drops a malformed entry"
// discipline (fixture.go). The same applies to a malformed tombstone.
func LoadTranscripts(dir string, requests []Request) (LoadResult, error) {
	byID, tombstones, err := scanTranscriptsDir(dir)
	if err != nil {
		return LoadResult{}, err
	}

	// An id can never legitimately carry both — that would mean a capture
	// run somehow recorded two different final dispositions for the same
	// request. Caught here, before bijection even runs, rather than
	// silently preferring one file over the other.
	for id := range tombstones {
		if _, ok := byID[id]; ok {
			return LoadResult{}, cerrors.Errorf(
				"transcripts directory %q: %q has BOTH a transcript (%s.yaml) and a tombstone (%s%s) — "+
					"exactly one may exist per id; delete the stale one",
				dir, id, id, id, tombstoneFileSuffix,
			)
		}
	}

	corpusIDs := make(map[string]bool, len(requests))
	promptByID := make(map[string]string, len(requests))
	for _, r := range requests {
		corpusIDs[r.ID] = true
		promptByID[r.ID] = r.Prompt
	}

	if err := checkBijection(dir, corpusIDs, byID, tombstones); err != nil {
		return LoadResult{}, err
	}
	if err := checkTombstonePrompts(tombstones, promptByID); err != nil {
		return LoadResult{}, err
	}

	out, err := loadedTranscriptsFor(byID, promptByID)
	if err != nil {
		return LoadResult{}, err
	}
	return LoadResult{ByID: out, Tombstoned: tombstones}, nil
}

// scanTranscriptsDir reads dir and classifies every entry: manifestFileName
// is skipped, an "<id>.missing.yaml" file is parsed and validated as a
// Tombstone, and every other "<id>.yaml" file is parsed and validated as a
// Transcript. Split out from LoadTranscripts to keep that function's own
// cyclomatic complexity down — this is the one part of loading that is pure
// I/O and per-file shape validation, with no corpus-comparison logic at all.
func scanTranscriptsDir(dir string) (byID map[string]Transcript, tombstones map[string]Tombstone, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, cerrors.Errorf("reading transcripts directory %q: %w", dir, err)
	}

	byID = make(map[string]Transcript, len(entries))
	tombstones = make(map[string]Tombstone, len(entries))
	for _, e := range entries {
		if e.IsDir() || e.Name() == manifestFileName {
			continue
		}

		switch {
		case strings.HasSuffix(e.Name(), tombstoneFileSuffix):
			id := strings.TrimSuffix(e.Name(), tombstoneFileSuffix)
			ts, err := readTombstone(filepath.Join(dir, e.Name()), id)
			if err != nil {
				return nil, nil, err
			}
			tombstones[id] = ts

		case strings.HasSuffix(e.Name(), ".yaml"):
			id := strings.TrimSuffix(e.Name(), ".yaml")
			t, err := readTranscript(filepath.Join(dir, e.Name()), id)
			if err != nil {
				return nil, nil, err
			}
			byID[id] = t
		}
	}
	return byID, tombstones, nil
}

// readTranscript reads, parses, and shape-validates one "<id>.yaml" file.
func readTranscript(path, id string) (Transcript, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Transcript{}, cerrors.Errorf("reading transcript %q: %w", path, err)
	}
	var t Transcript
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Transcript{}, cerrors.Errorf("parsing transcript %q: %w", path, err)
	}
	if err := validateTranscriptShape(t, path, id); err != nil {
		return Transcript{}, err
	}
	return t, nil
}

// readTombstone reads, parses, and shape-validates one "<id>.missing.yaml"
// file.
func readTombstone(path, id string) (Tombstone, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Tombstone{}, cerrors.Errorf("reading tombstone %q: %w", path, err)
	}
	var ts Tombstone
	if err := yaml.Unmarshal(data, &ts); err != nil {
		return Tombstone{}, cerrors.Errorf("parsing tombstone %q: %w", path, err)
	}
	if err := validateTombstoneShape(ts, path, id); err != nil {
		return Tombstone{}, err
	}
	return ts, nil
}

// checkTombstonePrompts is the tombstone half of AC 1.20: a tombstone's
// recorded corpusPromptSHA256 must match the corpus prompt for that SAME id
// today, exactly the check loadedTranscriptsFor runs for a real Transcript —
// a request edited under an unchanged id must still be caught even when
// that id has no data at all.
func checkTombstonePrompts(tombstones map[string]Tombstone, promptByID map[string]string) error {
	for id, ts := range tombstones {
		wantPromptSHA256 := sha256Hex(promptByID[id])
		if ts.CorpusPromptSHA256 != wantPromptSHA256 {
			return cerrors.Errorf(
				"tombstone %q: corpusPromptSHA256 %q does not match the corpus prompt for id %q today (%q) — "+
					"the request was edited under an unchanged id; re-capture this transcript",
				id, ts.CorpusPromptSHA256, id, wantPromptSHA256,
			)
		}
	}
	return nil
}

// loadedTranscriptsFor checks AC 1.20 for every real Transcript (mirroring
// checkTombstonePrompts for a Tombstone) and pairs each with its Staleness
// against the CURRENT binary's grounding.
func loadedTranscriptsFor(byID map[string]Transcript, promptByID map[string]string) (map[string]LoadedTranscript, error) {
	currentSystemPromptSHA256 := sha256Hex(BuildSystemPrompt(BuiltinCatalog()))
	currentCatalogFingerprint := CatalogFingerprint()

	out := make(map[string]LoadedTranscript, len(byID))
	for id, t := range byID {
		wantPromptSHA256 := sha256Hex(promptByID[id])
		if t.CorpusPromptSHA256 != wantPromptSHA256 {
			return nil, cerrors.Errorf(
				"transcript %q: corpusPromptSHA256 %q does not match the corpus prompt for id %q today (%q) — "+
					"the request was edited under an unchanged id; re-capture this transcript",
				id, t.CorpusPromptSHA256, id, wantPromptSHA256,
			)
		}

		out[id] = LoadedTranscript{
			Transcript: t,
			Staleness:  classifyStaleness(t, currentSystemPromptSHA256, currentCatalogFingerprint),
		}
	}
	return out, nil
}

// checkBijection reports every corpus id with neither a transcript nor a
// tombstone, AND every transcript/tombstone id with no corpus entry, in one
// error naming all of them — AC 1.19 requires each direction to name the
// offending id, not just detect that SOME mismatch exists. A tombstoned id
// satisfies the corpus side exactly like a transcript does; an ORPHANED
// tombstone (no corpus entry) is exactly as much a bijection problem as an
// orphaned transcript.
func checkBijection(dir string, corpusIDs map[string]bool, byID map[string]Transcript, tombstones map[string]Tombstone) error {
	var missing, extra []string
	for id := range corpusIDs {
		_, hasTranscript := byID[id]
		_, hasTombstone := tombstones[id]
		if !hasTranscript && !hasTombstone {
			missing = append(missing, id)
		}
	}
	for id := range byID {
		if !corpusIDs[id] {
			extra = append(extra, id)
		}
	}
	for id := range tombstones {
		if !corpusIDs[id] {
			extra = append(extra, id)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}

	sort.Strings(missing)
	sort.Strings(extra)

	var msg strings.Builder
	fmt.Fprintf(&msg, "transcripts directory %q is not a bijection with the corpus", dir)
	if len(missing) > 0 {
		fmt.Fprintf(&msg, "; corpus id(s) with no transcript or tombstone: %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		fmt.Fprintf(&msg, "; transcript/tombstone id(s) with no corpus entry: %s", strings.Join(extra, ", "))
	}
	return cerrors.New(msg.String())
}

// validateTranscriptShape hard-errors on a malformed transcript before it
// ever reaches the bijection or prompt-hash checks — an empty or
// wrong-schema file must never be silently treated as "no transcript for
// this id" (which would misreport as a missing-id bijection failure instead
// of the actual malformed-file problem).
func validateTranscriptShape(t Transcript, path, expectedID string) error {
	if t.SchemaVersion != TranscriptSchemaVersion {
		return cerrors.Errorf("transcript %q: schemaVersion %d, want %d", path, t.SchemaVersion, TranscriptSchemaVersion)
	}
	if t.RequestID == "" {
		return cerrors.Errorf("transcript %q: no requestID", path)
	}
	if t.RequestID != expectedID {
		return cerrors.Errorf("transcript %q: requestID %q does not match its filename id %q", path, t.RequestID, expectedID)
	}
	if t.Provider == "" {
		return cerrors.Errorf("transcript %q: no provider", path)
	}
	if t.Model == "" {
		return cerrors.Errorf("transcript %q: no model", path)
	}
	if t.CorpusPromptSHA256 == "" {
		return cerrors.Errorf("transcript %q: no corpusPromptSHA256", path)
	}
	if t.SystemPromptSHA256 == "" {
		return cerrors.Errorf("transcript %q: no systemPromptSHA256", path)
	}
	if t.CatalogFingerprint == "" {
		return cerrors.Errorf("transcript %q: no catalogFingerprint", path)
	}
	if len(t.Turns) == 0 {
		return cerrors.Errorf("transcript %q: no turns", path)
	}
	for _, turn := range t.Turns {
		if turn.CompletionText == "" {
			return cerrors.Errorf("transcript %q: turn %d has no completion text", path, turn.N)
		}
	}
	return nil
}

// validateTombstoneShape hard-errors on a malformed tombstone before it
// ever reaches the bijection or prompt-hash checks — mirrors
// validateTranscriptShape's discipline for Transcript.
func validateTombstoneShape(ts Tombstone, path, expectedID string) error {
	if ts.SchemaVersion != TranscriptSchemaVersion {
		return cerrors.Errorf("tombstone %q: schemaVersion %d, want %d", path, ts.SchemaVersion, TranscriptSchemaVersion)
	}
	if ts.RequestID == "" {
		return cerrors.Errorf("tombstone %q: no requestID", path)
	}
	if ts.RequestID != expectedID {
		return cerrors.Errorf("tombstone %q: requestID %q does not match its filename id %q", path, ts.RequestID, expectedID)
	}
	if ts.CorpusPromptSHA256 == "" {
		return cerrors.Errorf("tombstone %q: no corpusPromptSHA256", path)
	}
	if ts.FailureCode == "" {
		return cerrors.Errorf("tombstone %q: no failureCode", path)
	}
	return nil
}

// sha256Hex hashes s and returns the lowercase hex digest — the same
// crypto/sha256 + encoding/hex pairing fingerprintCatalog (grounding.go)
// already uses, applied here to a single string instead of a stream of
// tuples.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ReplayProviderFor builds a provider.Replay that answers Generate's calls
// with t's recorded Turns, in file order — turn 0 for attempt 1, turn 1 for
// the first retry, and so on (provider.Replay's own "keyed by attempt index"
// contract). No network call is ever made; this is what a PR-CI replay job
// drives Generate with instead of a live provider.Provider.
func ReplayProviderFor(t Transcript) *provider.Replay {
	turns := make([]provider.ReplayTurn, len(t.Turns))
	for i, turn := range t.Turns {
		turns[i] = provider.ReplayTurn{Text: turn.CompletionText, TokensUsed: turn.TokensUsed}
	}
	return &provider.Replay{
		ProviderName: t.Provider,
		Model:        t.Model,
		Turns:        turns,
	}
}
