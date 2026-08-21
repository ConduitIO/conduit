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

// validTombstone returns a well-formed Tombstone for request r, hashed
// against r.Prompt — a fixture LoadTranscripts accepts as satisfying
// bijection for r.ID, before any test perturbs it.
func validTombstone(r Request) Tombstone {
	return Tombstone{
		SchemaVersion:      TranscriptSchemaVersion,
		RequestID:          r.ID,
		CorpusPromptSHA256: sha256Hex(r.Prompt),
		FailureCode:        "generate.provider_error (HTTP 429)",
		CapturedAt:         time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
}

// writeTombstoneFixture marshals ts to dir/<id>.missing.yaml.
func writeTombstoneFixture(t *testing.T, dir, id string, ts Tombstone) {
	t.Helper()
	data, err := yaml.Marshal(ts)
	if err != nil {
		t.Fatalf("marshaling tombstone %q: %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+tombstoneFileSuffix), data, 0o600); err != nil {
		t.Fatalf("writing tombstone %q: %v", id, err)
	}
}

// TestLoadTranscripts_HappyPath pins that a well-formed, complete transcript
// directory loads cleanly and every entry classifies StalenessFresh — the
// baseline every perturbation test below is a deviation from.
func TestLoadTranscripts_HappyPath(t *testing.T) {
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

// TestLoadTranscripts_ManifestFileIsSkipped pins that manifest.yaml sitting
// alongside per-request transcripts is never treated as a malformed
// transcript (it has none of Transcript's required fields) or as an "extra"
// bijection violation.
func TestLoadTranscripts_ManifestFileIsSkipped(t *testing.T) {
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

// TestLoadTranscripts_MissingTranscript_ErrorsNamingTheID is the "removed
// file" half of AC 1.19: a corpus request with no transcript must hard-error,
// naming that request's id, rather than silently scoring it as a
// Result.Missing fail the way a live ScoreRun would (§3.3's whole point).
func TestLoadTranscripts_MissingTranscript_ErrorsNamingTheID(t *testing.T) {
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

// TestLoadTranscripts_ExtraTranscript_ErrorsNamingTheID is the "added file"
// half of AC 1.19: a transcript for an id the corpus doesn't have (e.g. a
// corpus entry renamed and the old transcript file left behind, or simply
// never removed) must hard-error naming that id.
func TestLoadTranscripts_ExtraTranscript_ErrorsNamingTheID(t *testing.T) {
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

// TestLoadTranscripts_RenamedCorpusID_ErrorsOnBothSides is the literal
// "corpus id renamed" scenario the plan calls out (§3.3): renaming req-a to
// req-a-v2 in the corpus, with the transcript directory untouched, must
// error naming req-a-v2 as missing AND req-a as extra — never absorbed as a
// passing 1/2 = 50% (the harness's analogue of "27/28 = 96%, still above the
// floor").
func TestLoadTranscripts_RenamedCorpusID_ErrorsOnBothSides(t *testing.T) {
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
	is.True(strings.Contains(err.Error(), "no transcript or tombstone: req-a-v2")) // corpus id with no transcript
	is.True(strings.Contains(err.Error(), "no corpus entry: req-a"))               // transcript id with no corpus entry (not the "req-a-v2" substring)
	is.True(!strings.Contains(err.Error(), "no corpus entry: req-a-v2"))           // the extra id is req-a, never req-a-v2
}

// --- Tombstones: a legitimately-missing transcript satisfies bijection,
// but a RENAMED corpus id must still hard-error even when tombstones exist
// elsewhere in the same directory. This is the load-bearing distinction the
// partial-results follow-up needs: a tombstone is an explicit, reviewable
// "this id was attempted and came back empty" fact, never a blanket
// tolerance for an absent file. ---

// TestLoadTranscripts_TombstonedID_LoadsCleanly proves the core of the
// fix: a corpus id with a committed "<id>.missing.yaml" instead of a
// transcript loads without error, is reported via LoadResult.Tombstoned,
// and is NOT present in ByID.
func TestLoadTranscripts_TombstonedID_LoadsCleanly(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	writeTombstoneFixture(t, dir, "req-b", validTombstone(reqs[1]))

	got, err := LoadTranscripts(dir, reqs)
	is.NoErr(err)
	is.Equal(len(got.ByID), 1)
	_, ok := got.ByID["req-a"]
	is.True(ok)
	_, ok = got.ByID["req-b"]
	is.True(!ok) // req-b has no transcript, only a tombstone

	is.Equal(len(got.Tombstoned), 1)
	ts, ok := got.Tombstoned["req-b"]
	is.True(ok)
	is.Equal(ts.FailureCode, "generate.provider_error (HTTP 429)")
}

// TestLoadTranscripts_RenamedCorpusID_StillErrors_EvenWithATombstonePresent
// is the test that distinguishes the two cases a tombstone must never
// blur together: renaming req-a to req-a-v2 in the corpus, with req-b
// legitimately tombstoned, must still hard-error naming req-a-v2 as
// missing — a tombstone existing for a DIFFERENT id must never be read as
// "some id in this directory accounts for the gap".
func TestLoadTranscripts_RenamedCorpusID_StillErrors_EvenWithATombstonePresent(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	writeTombstoneFixture(t, dir, "req-b", validTombstone(reqs[1]))

	renamed := []Request{
		{ID: "req-a-v2", Prompt: reqs[0].Prompt}, // req-a renamed, transcript left behind under "req-a"
		reqs[1],                                  // req-b unchanged, still tombstoned
	}

	_, err := LoadTranscripts(dir, renamed)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "no transcript or tombstone: req-a-v2"))
	is.True(strings.Contains(err.Error(), "no corpus entry: req-a"))
}

// TestLoadTranscripts_TombstonePromptEditedUnderSameID_Errors mirrors AC
// 1.20 for a tombstone: a request's prompt edited without a re-capture must
// still be caught even when the id in question has no transcript at all.
func TestLoadTranscripts_TombstonePromptEditedUnderSameID_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	writeTombstoneFixture(t, dir, "req-b", validTombstone(reqs[1]))

	edited := []Request{
		reqs[0],
		{ID: "req-b", Prompt: "a completely different prompt text, edited without a re-capture"},
	}

	_, err := LoadTranscripts(dir, edited)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-b"))
	is.True(strings.Contains(err.Error(), "corpusPromptSHA256"))
}

// TestLoadTranscripts_TombstoneAndTranscriptBothPresent_Errors proves an
// id can never carry both a transcript and a tombstone — a
// self-contradictory commit state that must never resolve by silently
// preferring one file over the other.
func TestLoadTranscripts_TombstoneAndTranscriptBothPresent_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	writeTranscript(t, dir, "req-b", validTranscript(reqs[1]))
	writeTombstoneFixture(t, dir, "req-b", validTombstone(reqs[1]))

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-b"))
	is.True(strings.Contains(err.Error(), "BOTH a transcript"))
}

// TestLoadTranscripts_MalformedTombstone_Errors mirrors
// TestLoadTranscripts_WrongSchemaVersion_Errors for a tombstone: never
// silently absorbed as "missing".
func TestLoadTranscripts_MalformedTombstone_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	bad := validTombstone(reqs[1])
	bad.FailureCode = ""
	writeTombstoneFixture(t, dir, "req-b", bad)

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "failureCode"))
}

// TestLoadTranscripts_EmptyTombstoneFile_Errors is a nit from the round-2
// review of #2814: TestLoadTranscripts_MalformedTombstone_Errors above only
// pins an otherwise-well-formed Tombstone with FailureCode cleared, never a
// genuinely empty (0-byte) file — e.g. a truncated write, or `touch`'d by a
// human mid-fix. yaml.Unmarshal on an empty document succeeds (returns a
// zero-value Tombstone) rather than erroring, so this exercises
// validateTombstoneShape's FIRST check (schemaVersion) rather than the last
// (failureCode) — a different code path than the existing test covers.
func TestLoadTranscripts_EmptyTombstoneFile_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	is.NoErr(os.WriteFile(filepath.Join(dir, "req-b"+tombstoneFileSuffix), nil, 0o600))

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-b"+tombstoneFileSuffix))
	is.True(strings.Contains(err.Error(), "schemaVersion"))
}

// TestLoadTranscripts_UnparseableTombstoneFile_Errors is the other half of
// the same nit: content that isn't valid YAML AT ALL (as opposed to valid
// YAML missing a required field) must fail at the yaml.Unmarshal step
// itself (readTombstone's "parsing tombstone" error), never reach
// validateTombstoneShape.
func TestLoadTranscripts_UnparseableTombstoneFile_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[0]))
	is.NoErr(os.WriteFile(filepath.Join(dir, "req-b"+tombstoneFileSuffix), []byte(":::not valid yaml:::["), 0o600))

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "parsing tombstone"))
}

// TestLoadTranscripts_OrphanedTombstone_ErrorsNamingTheID is the tombstone
// analogue of TestLoadTranscripts_ExtraTranscript_ErrorsNamingTheID above,
// pinned separately per a nit from the round-2 review of #2814: a
// "<id>.missing.yaml" for an id absent from the corpus (e.g. a corpus entry
// renamed, or a request removed outright, with its old tombstone left
// behind) is exactly as much a bijection violation as an orphaned real
// transcript — checkBijection's own doc comment says so explicitly ("an
// ORPHANED tombstone ... is exactly as much a bijection problem as an
// orphaned transcript"), but no test exercised a tombstone taking this path
// specifically until now.
func TestLoadTranscripts_OrphanedTombstone_ErrorsNamingTheID(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	for _, r := range reqs {
		writeTranscript(t, dir, r.ID, validTranscript(r))
	}
	// An orphaned tombstone — as if req-a-orphan were removed from the
	// corpus (or renamed) but its tombstone file was left in place.
	stray := validTombstone(Request{ID: "req-a-orphan", Prompt: "prompt for an orphaned request"})
	writeTombstoneFixture(t, dir, "req-a-orphan", stray)

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "req-a-orphan"))
}

// --- corpusPromptSHA256 (AC 1.20): a prompt edited under an unchanged id. ---

// TestLoadTranscripts_PromptEditedUnderSameID_Errors is AC 1.20: bijection
// alone passes here (the id didn't move), so this is a DIFFERENT check —
// corpusPromptSHA256 must equal sha256Hex of the CURRENT corpus prompt for
// that id, and a hand-edited prompt with no re-capture must hard-error.
func TestLoadTranscripts_PromptEditedUnderSameID_Errors(t *testing.T) {
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

func TestLoadTranscripts_WrongSchemaVersion_Errors(t *testing.T) {
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

func TestLoadTranscripts_RequestIDDisagreesWithFilename_Errors(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	reqs := twoRequestCorpus()
	writeTranscript(t, dir, "req-a", validTranscript(reqs[1])) // wrong content under req-a's filename
	writeTranscript(t, dir, "req-b", validTranscript(reqs[1]))

	_, err := LoadTranscripts(dir, reqs)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "does not match its filename"))
}

func TestLoadTranscripts_NoTurns_Errors(t *testing.T) {
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

// TestLoadTranscripts_StaleTranscript_LoadsWithoutError pins that a
// transcript whose recorded grounding no longer matches the current binary
// is NOT a load error — only its Staleness field reflects the drift. A
// red-on-every-connector-bump loader would be exactly the flaky-gate failure
// §3.3 refuses to reintroduce.
func TestLoadTranscripts_StaleTranscript_LoadsWithoutError(t *testing.T) {
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

// --- Committed tree ---

// TestLoadTranscripts_CommittedTreeLoadsCleanly is the regression test for
// B3 (round-3 review of #2814): every OTHER LoadTranscripts test above
// exercises it against a synthetic t.TempDir() fixture this file builds
// itself — nothing in CI ever calls it against the REAL committed
// testdata/transcripts tree, so a tree LoadTranscripts would reject (H3's
// both-files case if a future `git rm` step in generate-capture.yml misses
// one, a corpus id renamed inside the same PR window, a partially-promoted
// tree left by a `go test` panic or -timeout kill between promotion and the
// tombstone loop) merges green with nothing to catch it — the PR body's
// "Manifest numbers look sane" checkbox is a human where a test belongs.
//
// This walks every real testdata/transcripts/<provider>/<model> leaf
// directory and requires LoadRequests + LoadTranscripts to succeed against
// it, skipping cleanly — never failing — whenever nothing is committed yet,
// exactly like TestTranscripts_CarryNoSecretMaterial (secrets_scan_test.go)
// does for the same reason: an empty corpus is not a broken one. Every
// currently-committed subtree is captured against testdata/eval_requests.yaml
// (doc.go, transcript.go's CorpusCommitSHA doc comment) — if a future corpus
// other than that one ever gets its own transcripts directory, this test
// needs to learn which corpus backs which subtree rather than assuming one
// for all of them.
func TestLoadTranscripts_CommittedTreeLoadsCleanly(t *testing.T) {
	root := "testdata/transcripts"

	providerDirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		t.Skip("no transcripts directory yet (testdata/transcripts) — nothing to load")
	}
	if err != nil {
		t.Fatalf("reading %q: %v", root, err)
	}

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatalf("loading corpus: %v", err)
	}

	var checked int
	for _, pd := range providerDirs {
		if !pd.IsDir() {
			continue
		}
		providerDir := filepath.Join(root, pd.Name())
		modelDirs, err := os.ReadDir(providerDir)
		if err != nil {
			t.Fatalf("reading %q: %v", providerDir, err)
		}
		for _, md := range modelDirs {
			if !md.IsDir() {
				continue
			}
			dir := filepath.Join(providerDir, md.Name())
			if _, err := LoadTranscripts(dir, requests); err != nil {
				t.Errorf("LoadTranscripts(%q) against the real committed tree: %v", dir, err)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Skip("no provider/model transcript subtree committed yet — nothing to load")
	}
	t.Logf("LoadTranscripts succeeded against %d committed provider/model subtree(s) under %s", checked, root)
}
