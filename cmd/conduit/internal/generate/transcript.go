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
// committed layout; WRITING one is the capture tool's job (A5a-3, not this
// slice) — LoadTranscripts never reads or requires it, so a missing or
// malformed manifest.yaml can never block replay.
type Manifest struct {
	SchemaVersion int    `yaml:"schemaVersion"`
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	// CapturedAt is the capture run's own start time, distinct from any one
	// Transcript.CapturedAt.
	CapturedAt   time.Time `yaml:"capturedAt"`
	RequestCount int       `yaml:"requestCount"`
	// TotalTokensUsed sums every Turn.TokensUsed across every transcript this
	// run produced — MEASURED, from providers' own reported usage, per
	// provider.CompletionResult's "never estimated" invariant.
	TotalTokensUsed int `yaml:"totalTokensUsed"`
	// EstimatedCostUSD is TotalTokensUsed priced at the provider's list rate
	// at capture time — an estimate (the field name says so), because actual
	// billed cost depends on rate-card details (cached-prefix discounts,
	// promotional pricing) this package has no way to observe.
	EstimatedCostUSD float64 `yaml:"estimatedCostUSD"`
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
}

// LoadTranscripts reads every committed transcript in dir (one file per
// corpus request, "<requestID>.yaml"; manifestFileName is skipped, never
// treated as a malformed transcript) and validates it against requests
// BEFORE returning anything a caller could score.
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
//  1. Bijection — every requests[i].ID has exactly one transcript file in
//     dir, and every transcript file's id has a matching entry in requests.
//     Both directions are checked and the error names every offending id, not
//     just the first (AC 1.19).
//  2. corpusPromptSHA256 equality — a transcript's recorded hash of the
//     corpus prompt it was captured against must equal
//     sha256Hex(requests[i].Prompt) for that SAME id today. This is what
//     catches a request's prompt text edited under an id nobody renamed (AC
//     1.20) — bijection alone would pass that case, since the id itself
//     never moved.
//
// Grounding staleness (Staleness) is computed and returned on every result
// but is never a reason to fail this call — see Staleness's doc comment.
//
// Malformed transcripts (missing required fields, a requestID that disagrees
// with its own filename, an unrecognized schemaVersion) are also hard
// errors, matching LoadRequests' "never silently drops a malformed entry"
// discipline (fixture.go).
func LoadTranscripts(dir string, requests []Request) (LoadResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return LoadResult{}, cerrors.Errorf("reading transcripts directory %q: %w", dir, err)
	}

	byID := make(map[string]Transcript, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || e.Name() == manifestFileName {
			continue
		}

		id := strings.TrimSuffix(e.Name(), ".yaml")
		path := filepath.Join(dir, e.Name())

		data, err := os.ReadFile(path)
		if err != nil {
			return LoadResult{}, cerrors.Errorf("reading transcript %q: %w", path, err)
		}
		var t Transcript
		if err := yaml.Unmarshal(data, &t); err != nil {
			return LoadResult{}, cerrors.Errorf("parsing transcript %q: %w", path, err)
		}
		if err := validateTranscriptShape(t, path, id); err != nil {
			return LoadResult{}, err
		}
		byID[id] = t
	}

	corpusIDs := make(map[string]bool, len(requests))
	promptByID := make(map[string]string, len(requests))
	for _, r := range requests {
		corpusIDs[r.ID] = true
		promptByID[r.ID] = r.Prompt
	}

	if err := checkBijection(dir, corpusIDs, byID); err != nil {
		return LoadResult{}, err
	}

	currentSystemPromptSHA256 := sha256Hex(BuildSystemPrompt(BuiltinCatalog()))
	currentCatalogFingerprint := CatalogFingerprint()

	out := make(map[string]LoadedTranscript, len(byID))
	for id, t := range byID {
		wantPromptSHA256 := sha256Hex(promptByID[id])
		if t.CorpusPromptSHA256 != wantPromptSHA256 {
			return LoadResult{}, cerrors.Errorf(
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

	return LoadResult{ByID: out}, nil
}

// checkBijection reports every corpus id with no transcript AND every
// transcript id with no corpus entry, in one error naming all of them — AC
// 1.19 requires each direction to name the offending id, not just detect that
// SOME mismatch exists.
func checkBijection(dir string, corpusIDs map[string]bool, byID map[string]Transcript) error {
	var missing, extra []string
	for id := range corpusIDs {
		if _, ok := byID[id]; !ok {
			missing = append(missing, id)
		}
	}
	for id := range byID {
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
		fmt.Fprintf(&msg, "; corpus id(s) with no transcript: %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		fmt.Fprintf(&msg, "; transcript id(s) with no corpus entry: %s", strings.Join(extra, ", "))
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
