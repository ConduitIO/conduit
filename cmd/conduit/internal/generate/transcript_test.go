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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/yaml/v3"
	"github.com/matryer/is"
)

// writeTranscript marshals t to dir/<id>.yaml, for tests that build a
// synthetic transcripts directory without depending on any real committed
// corpus (today's real testdata/transcripts is empty — A5a-3's job).
func writeTranscript(t *testing.T, dir, id string, tr Transcript) {
	t.Helper()
	data, err := yaml.Marshal(tr)
	if err != nil {
		t.Fatalf("marshaling transcript %q: %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), data, 0o600); err != nil {
		t.Fatalf("writing transcript %q: %v", id, err)
	}
}

// validTranscript returns a well-formed Transcript for request r, hashed
// against r.Prompt and the CURRENT binary's grounding — a fixture that
// LoadTranscripts accepts as-is, before any test perturbs it.
func validTranscript(r Request) Transcript {
	return Transcript{
		SchemaVersion:      TranscriptSchemaVersion,
		RequestID:          r.ID,
		Provider:           "anthropic",
		Model:              "claude-sonnet-5",
		CapturedAt:         time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		CorpusPromptSHA256: sha256Hex(r.Prompt),
		SystemPromptSHA256: sha256Hex(BuildSystemPrompt(BuiltinCatalog())),
		CatalogFingerprint: CatalogFingerprint(),
		Turns: []Turn{{
			N:                1,
			UserPromptSHA256: sha256Hex(r.Prompt),
			TokensUsed:       123,
			CompletionText:   "version: \"2.2\"\npipelines:\n  - id: p\n",
		}},
		Outcome: Outcome{ValidatePass: true, SemanticMatch: true},
	}
}

func twoRequestCorpus() []Request {
	return []Request{
		{ID: "req-a", Prompt: "prompt for request a"},
		{ID: "req-b", Prompt: "prompt for request b"},
	}
}

// Test_LoadTranscripts_HappyPath pins that a well-formed, complete transcript
// directory loads cleanly and every entry classifies StalenessFresh — the
// baseline every perturbation test below is a deviation from.
func Test_LoadTranscripts_HappyPath(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}

	got, err := LoadTranscripts(dir, reqs)
	is.NoErr(err)
	is.Equal(len(got.ByID), 2)
	for _, r := range reqs {
		lt, ok := got.ByID[r.ID]
		is.True(ok)
		is.Equal(lt.Staleness, StalenessFresh)
		is.Equal(lt.Transcript.RequestID, r.ID)
	}
}

// Test_LoadTranscripts_ManifestFileIsSkipped pins that manifest.yaml sitting
// alongside per-request transcripts is never treated as a malformed
// transcript (it has none of Transcript's required fields) or as an "extra"
// bijection violation.
func Test_LoadTranscripts_ManifestFileIsSkipped(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}
	is.NoErr(os.WriteFile(filepath.Join(dir, manifestFileName), []byte("schemaVersion: 1\n"), 0o600))

	got, err := LoadTranscripts(dir, reqs)
	is.NoErr(err)
	is.Equal(len(got.ByID), 2)
}

// --- Bijection (AC 1.19): a file added or removed on either side must
// hard-error naming the offending id. ---

// Test_LoadTranscripts_MissingTranscript_ErrorsNamingTheID is the "removed
// file" half of AC 1.19: a corpus request with no transcript must hard-error,
// naming that request's id, rather than silently scoring it as a
// Result.Missing fail the way a live ScoreRun would (§3.3's whole point).
func Test_LoadTranscripts_MissingTranscript_ErrorsNamingTheID(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	// Only req-a gets a transcript; req-b's is "removed".
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-b"))
	is.True(!strings.Contains(err.Error(), "req-a")) // req-a is fine; only the offending id is named as missing
}

// Test_LoadTranscripts_ExtraTranscript_ErrorsNamingTheID is the "added file"
// half of AC 1.19: a transcript for an id the corpus doesn't have (e.g. a
// corpus entry renamed and the old transcript file left behind, or simply
// never removed) must hard-error naming that id.
func Test_LoadTranscripts_ExtraTranscript_ErrorsNamingTheID(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}
	// An extra transcript with no corpus entry — as if req-a were renamed to
	// req-a-renamed in the corpus but its old transcript file was left in
	// place under the stale id.
	stray := validTranscript(Request{ID: "req-a-renamed", Prompt: "prompt for request a"})
	writeTranscript(t, dir, "req-a-renamed", stray)

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-a-renamed"))
}

// Test_LoadTranscripts_RenamedCorpusID_ErrorsOnBothSides is the literal
// "corpus id renamed" scenario the plan calls out (§3.3): renaming req-a to
// req-a-v2 in the corpus, with the transcript directory untouched, must
// error naming req-a-v2 as missing AND req-a as extra — never absorbed as a
// passing 1/2 = 50% (the harness's analogue of "27/28 = 96%, still above the
// floor").
func Test_LoadTranscripts_RenamedCorpusID_ErrorsOnBothSides(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}

	renamed := []Request{
		{ID: "req-a-v2", Prompt: reqs[0].Prompt}, // req-a renamed, transcript left behind under "req-a"
		reqs[1],
	}

	_, err := LoadTranscripts(dir, renamed)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "no transcript: req-a-v2"))    // corpus id with no transcript
	is.True(strings.Contains(err.Error(), "no corpus entry: req-a"))     // transcript id with no corpus entry (not the "req-a-v2" substring)
	is.True(!strings.Contains(err.Error(), "no corpus entry: req-a-v2")) // the extra id is req-a, never req-a-v2
}

// --- corpusPromptSHA256 (AC 1.20): a prompt edited under an unchanged id. ---

// Test_LoadTranscripts_PromptEditedUnderSameID_Errors is AC 1.20: bijection
// alone passes here (the id didn't move), so this is a DIFFERENT check —
// corpusPromptSHA256 must equal sha256Hex of the CURRENT corpus prompt for
// that id, and a hand-edited prompt with no re-capture must hard-error.
func Test_LoadTranscripts_PromptEditedUnderSameID_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}

	edited := []Request{
		{ID: "req-a", Prompt: "a completely different prompt text, edited without a re-capture"},
		reqs[1],
	}

	_, err := LoadTranscripts(dir, edited)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-a"))
	is.True(strings.Contains(err.Error(), "corpusPromptSHA256"))
}

// --- Malformed transcripts: never silently absorbed as "missing". ---

func Test_LoadTranscripts_WrongSchemaVersion_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}
	bad := validTranscript(reqs[0])
	bad.SchemaVersion = 999
	writeTranscript(t, dir, "req-a", bad)

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "schemaVersion"))
}

func Test_LoadTranscripts_RequestIDDisagreesWithFilename_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[1])) // wrong content under req-a's filename
	writeTranscript(t, dir, "req-b", validTranscript(reqs[1]))

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "does not match its filename"))
}

func Test_LoadTranscripts_NoTurns_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}
	empty := validTranscript(reqs[0])
	empty.Turns = nil
	writeTranscript(t, dir, "req-a", empty)

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "no turns"))
}

// --- Staleness classification (§3.3): data, never an error. ---

func Test_ClassifyStaleness(t *testing.T) {
	is := is.New(t)

	fresh := Transcript{SystemPromptSHA256: "sys-a", CatalogFingerprint: "cat-a"}
	is.Equal(classifyStaleness(fresh, "sys-a", "cat-a"), StalenessFresh)

	cosmetic := Transcript{SystemPromptSHA256: "sys-old", CatalogFingerprint: "cat-a"}
	is.Equal(classifyStaleness(cosmetic, "sys-new", "cat-a"), StalenessCosmeticDrift)

	semantic := Transcript{SystemPromptSHA256: "sys-old", CatalogFingerprint: "cat-old"}
	is.Equal(classifyStaleness(semantic, "sys-new", "cat-new"), StalenessSemanticDrift)
}

// Test_LoadTranscripts_StaleTranscript_LoadsWithoutError pins that a
// transcript whose recorded grounding no longer matches the current binary
// is NOT a load error — only its Staleness field reflects the drift. A
// red-on-every-connector-bump loader would be exactly the flaky-gate failure
// §3.3 refuses to reintroduce.
func Test_LoadTranscripts_StaleTranscript_LoadsWithoutError(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	stale := validTranscript(reqs[0])
	stale.SystemPromptSHA256 = "no-longer-the-current-system-prompt-hash"
	stale.CatalogFingerprint = "no-longer-the-current-catalog-fingerprint"
	writeTranscript(t, dir, "req-a", stale)
	writeTranscript(t, dir, "req-b", validTranscript(reqs[1]))

	got, err := LoadTranscripts(dir, reqs)
	is.NoErr(err)
	is.Equal(got.ByID["req-a"].Staleness, StalenessSemanticDrift)
	is.Equal(got.ByID["req-b"].Staleness, StalenessFresh)
}

// --- ReplayProviderFor ---

func Test_ReplayProviderFor_CarriesTurnsAndIdentity(t *testing.T) {
	is := is.New(t)
	tr := Transcript{
		Provider: "anthropic",
		Model:    "claude-sonnet-5",
		Turns: []Turn{
			{CompletionText: "first", TokensUsed: 10},
			{CompletionText: "second", TokensUsed: 20},
		},
	}

	p := ReplayProviderFor(tr)
	is.Equal(p.Name(), "anthropic")
	is.Equal(len(p.Turns), 2)
	is.Equal(p.Turns[0].Text, "first")
	is.Equal(p.Turns[0].TokensUsed, 10)
	is.Equal(p.Turns[1].Text, "second")
}

// --- Manifest ---

func Test_LoadManifest_ReadsCommittedShape(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, manifestFileName)
	is.NoErr(os.WriteFile(path, []byte(`
schemaVersion: 1
provider: anthropic
model: claude-sonnet-5
capturedAt: 2026-08-01T00:00:00Z
requestCount: 28
totalTokensUsed: 94000
estimatedCostUSD: 2.9
`), 0o600))

	m, err := LoadManifest(path)
	is.NoErr(err)
	is.Equal(m.Provider, "anthropic")
	is.Equal(m.RequestCount, 28)
	is.Equal(m.TotalTokensUsed, 94000)
}

func Test_LoadManifest_MissingFile_Errors(t *testing.T) {
	is := is.New(t)
	_, err := LoadManifest(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	is.True(err != nil)
}
