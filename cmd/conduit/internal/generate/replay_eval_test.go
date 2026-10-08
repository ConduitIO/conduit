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
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
)

// TestReplayEval replays every committed provider transcript through the
// REAL generation loop — Generate, which runs validate.RunBytes with the
// shipped Options and the prompt-derived intent judge — and scores each
// result against the corpus with ScoreRun. It is WS1 A5b's PR gate (plan
// §6): `.github/workflows/generate-eval.yml` runs it on every PR with
// outbound network blocked at the OS level.
//
// What it proves: given the model output recorded at capture time, today's
// Conduit code still turns it into the same verdicts. A change to
// extraction, validation, retry feedback, the intent judge, or the builtin
// connector/processor specs that moves any request's outcome shows up as a
// byte diff against testdata/replay_expected.json, naming the request.
//
// What it does not prove: anything about the model. The completions are
// frozen; only the scheduled live eval (TestLiveEval) measures the model.
//
// Determinism: requests are replayed in a seeded random order and reported
// in corpus order, so a result that depends on order (shared state leaking
// between Generate calls) breaks the golden. The workflow runs this three
// times and byte-compares all three outputs as well as the golden.
//
// To regenerate the golden after an intended change:
//
//	CONDUIT_GENERATE_REPLAY_UPDATE=1 go test -run '^TestReplayEval$' ./cmd/conduit/internal/generate/
//
// and commit testdata/replay_expected.json with the change that moved it.
// A golden diff that makes scores go up without a code change explaining it
// is the review signal (plan F6): replay can prove the code is stable, not
// that the transcripts are honest.
func TestReplayEval(t *testing.T) {
	if os.Getenv(envEvalOffline) == "1" {
		assertNoProviderEnv(t)
	}

	requests, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatalf("loading corpus: %v", err)
	}

	dirs, err := committedTranscriptDirs(transcriptsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		// Fail closed. An empty corpus must not read as a green replay: the
		// gate would then pass by measuring nothing.
		t.Fatalf("no committed transcript corpus under %s — capture one (generate-capture.yml or the "+
			"generate_capture tagged test) before this gate can mean anything", transcriptsRoot)
	}

	seed := uint64(time.Now().UnixNano())
	t.Logf("replay order seed: %d", seed)

	ctx := context.Background()
	result := replayResult{SchemaVersion: replayResultSchemaVersion}
	var stale []string
	for _, dir := range dirs {
		loaded, err := LoadTranscripts(dir, requests)
		if err != nil {
			t.Fatalf("loading transcripts from %s: %v", dir, err)
		}
		corpus := filepath.ToSlash(strings.TrimPrefix(dir, transcriptsRoot+string(filepath.Separator)))
		cr := replayCorpus(ctx, t, corpus, requests, loaded, seed)
		result.Corpora = append(result.Corpora, cr)

		for _, req := range requests {
			lt, ok := loaded.ByID[req.ID]
			if ok && lt.Staleness != StalenessFresh {
				stale = append(stale, fmt.Sprintf("%s/%s: %s", corpus, req.ID, lt.Staleness))
			}
		}
	}

	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshaling replay result: %v", err)
	}
	got = append(got, '\n')

	if out := os.Getenv(envReplayOut); out != "" {
		writeReplayArtifacts(t, out, got, result, stale)
	}
	for _, s := range stale {
		// Staleness is never a failure (plan §3.3): a connector bump must not
		// turn every PR red. It is surfaced, and the scheduled job is where a
		// re-capture is decided.
		t.Logf("STALE transcript: %s", s)
	}

	if os.Getenv(envReplayUpdate) == "1" {
		if err := os.WriteFile(replayGoldenPath, got, 0o600); err != nil {
			t.Fatalf("writing %s: %v", replayGoldenPath, err)
		}
		t.Logf("rewrote %s", replayGoldenPath)
		return
	}

	want, err := os.ReadFile(replayGoldenPath)
	if err != nil {
		t.Fatalf("reading golden %s: %v (regenerate with %s=1)", replayGoldenPath, err, envReplayUpdate)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replay result differs from %s — a code change moved at least one request's verdict.\n"+
			"requests whose record changed: %s\n%s\n"+
			"If the change is intended, regenerate with %s=1 and commit the golden with it.",
			replayGoldenPath, changedRecords(want, result), firstDiff(want, got), envReplayUpdate)
	}
}

const (
	transcriptsRoot  = "testdata/transcripts"
	replayGoldenPath = "testdata/replay_expected.json"

	// envReplayUpdate=1 rewrites the golden instead of comparing against it.
	envReplayUpdate = "CONDUIT_GENERATE_REPLAY_UPDATE"
	// envReplayOut names a directory to write result.json, summary.md and
	// stale.txt into, for the workflow to diff, publish and annotate.
	envReplayOut = "CONDUIT_GENERATE_REPLAY_OUT"
	// envEvalOffline=1 is set by the PR workflow. It turns on the
	// environment check below: the job must not even have a provider
	// configured.
	envEvalOffline = "CONDUIT_GENERATE_EVAL_OFFLINE"

	replayResultSchemaVersion = 1
)

// replayResult is the golden's shape.
type replayResult struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Corpora       []replayCorpusResult `json:"corpora"`
}

// replayCorpusResult is one <provider>/<model> transcript set replayed.
type replayCorpusResult struct {
	Corpus  string         `json:"corpus"`
	Summary evalSummary    `json:"summary"`
	Records []replayRecord `json:"records"`
}

// replayRecord is an evalRecord plus what only replay can know: whether the
// replayed run took the same path the captured run did.
type replayRecord struct {
	evalRecord

	// TurnsRecorded is how many provider turns capture recorded.
	TurnsRecorded int `json:"turnsRecorded"`
	// ReplayExhausted is true when today's code asked for more turns than
	// were recorded — it retried where the captured run had stopped, so
	// at least one recorded candidate now fails a gate it used to clear.
	ReplayExhausted bool `json:"replayExhausted,omitempty"`
	// PromptsMatchCapture is false when any turn's user prompt (the request
	// plus retry feedback) differs from the one capture sent — retry
	// feedback wording or content moved.
	PromptsMatchCapture bool `json:"promptsMatchCapture"`
	// MatchesCapture is false when Generate's own verdict on the last
	// attempt (validate pass, intent-judge match) differs from the
	// transcript's recorded Outcome. Outcome holds Generate's verdict, not
	// the corpus verdict (see Outcome's doc comment), so that is what it is
	// compared against.
	MatchesCapture bool `json:"matchesCapture"`
}

// replayCorpus replays one loaded transcript set. Requests run in an order
// shuffled by seed; records come back in corpus order.
func replayCorpus(ctx context.Context, t *testing.T, corpus string, requests []Request, loaded LoadResult, seed uint64) replayCorpusResult {
	t.Helper()

	order := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)).Perm(len(requests))
	records := make([]replayRecord, len(requests))

	for _, i := range order {
		req := requests[i]
		if ts, ok := loaded.Tombstoned[req.ID]; ok {
			records[i] = replayRecord{evalRecord: tombstonedRecord(req, ts), PromptsMatchCapture: true, MatchesCapture: true}
			continue
		}
		lt, ok := loaded.ByID[req.ID]
		if !ok {
			// LoadTranscripts' bijection check makes this unreachable; fail
			// loudly rather than score a hole.
			t.Fatalf("%s: corpus id %q has neither a transcript nor a tombstone", corpus, req.ID)
		}
		tr := lt.Transcript

		p := newOfflineProvider(tr)
		gen, genErr := Generate(ctx, Input{Prompt: req.Prompt, Provider: p, Model: tr.Model})

		rec := replayRecord{
			evalRecord:          newEvalRecord(ctx, req, gen, genErr),
			TurnsRecorded:       len(tr.Turns),
			ReplayExhausted:     p.exhausted,
			PromptsMatchCapture: p.promptsMatch,
		}
		got := generateVerdict(gen)
		rec.MatchesCapture = got.ValidatePass == tr.Outcome.ValidatePass && got.SemanticMatch == tr.Outcome.SemanticMatch
		records[i] = rec
	}

	flat := make([]evalRecord, len(records))
	for i, r := range records {
		flat[i] = r.evalRecord
	}
	return replayCorpusResult{Corpus: corpus, Summary: summarize(flat), Records: records}
}

// generateVerdict is Generate's own verdict on its last attempt, in the
// exact shape capture records it (buildTranscript): validate pass only for a
// candidate that was actually extracted, and the intent judge's match.
// Issues are left out; the comparison is on the two booleans.
func generateVerdict(gen Generation) Outcome {
	last := lastAttempt(gen)
	if last == nil {
		return Outcome{}
	}
	return Outcome{
		ValidatePass:  last.Candidate != "" && last.Report.OK(),
		SemanticMatch: last.Semantic.Match,
	}
}

// offlineProvider is the ONLY provider TestReplayEval hands to Generate. Its
// sole field is a *provider.Replay, so no live adapter is reachable from it
// by construction — the structural half of "PR CI never calls a provider"
// (plan §6 layer 1; the workflow adds the environment and OS-level egress
// layers). It also checks each turn's prompt against the hash capture
// recorded for it.
type offlineProvider struct {
	replay  *provider.Replay
	prompts []string // Turn.UserPromptSHA256, in turn order

	calls        int
	exhausted    bool
	promptsMatch bool
}

func newOfflineProvider(tr Transcript) *offlineProvider {
	prompts := make([]string, len(tr.Turns))
	for i, turn := range tr.Turns {
		prompts[i] = turn.UserPromptSHA256
	}
	return &offlineProvider{replay: ReplayProviderFor(tr), prompts: prompts, promptsMatch: true}
}

func (o *offlineProvider) Name() string { return o.replay.Name() }

func (o *offlineProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if o.calls < len(o.prompts) {
		if sha256Hex(req.Prompt) != o.prompts[o.calls] {
			o.promptsMatch = false
		}
	} else {
		o.exhausted = true
	}
	o.calls++
	return o.replay.Complete(ctx, req)
}

// assertNoProviderEnv is the environment half of the no-network proof: in
// the PR job, no provider may even be configured. On its own this would be
// weak (with nothing set, resolution refuses before an adapter exists — plan
// F8), which is why offlineProvider and the workflow's egress block exist
// too.
func assertNoProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{provider.EnvProvider, provider.EnvAnthropicKey, provider.EnvOpenAIKey, provider.EnvOllamaHost} {
		if os.Getenv(k) != "" {
			t.Fatalf("%s is set in an offline eval run — the replay job must carry no provider configuration", k)
		}
	}
	if _, err := provider.Resolve(provider.ResolveInput{Env: os.Getenv}); err == nil {
		t.Fatal("provider.Resolve found a provider in an offline eval run — the replay job must not be able to build one")
	}
}

// committedTranscriptDirs lists every <provider>/<model> directory under
// root, sorted so the golden's corpus order is stable.
func committedTranscriptDirs(root string) ([]string, error) {
	providerDirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", root, err)
	}
	var dirs []string
	for _, pd := range providerDirs {
		if !pd.IsDir() {
			continue
		}
		modelDirs, err := os.ReadDir(filepath.Join(root, pd.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %q: %w", filepath.Join(root, pd.Name()), err)
		}
		for _, md := range modelDirs {
			if md.IsDir() {
				dirs = append(dirs, filepath.Join(root, pd.Name(), md.Name()))
			}
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// writeReplayArtifacts writes what the workflow publishes: the raw result,
// a markdown summary for the job page, and the staleness list it turns into
// warning annotations.
func writeReplayArtifacts(t *testing.T, dir string, raw []byte, result replayResult, stale []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	files := map[string][]byte{
		"result.json": raw,
		"summary.md":  []byte(renderReplaySummary(result)),
		"stale.txt":   []byte(strings.Join(stale, "\n")),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

func renderReplaySummary(result replayResult) string {
	var b strings.Builder
	b.WriteString("## generate-eval (replay)\n\n")
	b.WriteString("Committed transcripts replayed through `Generate` (validate gate + intent judge) and scored " +
		"against the corpus. Recorded model output, no provider call: this measures Conduit's code, not the model.\n\n")
	for _, c := range result.Corpora {
		s := c.Summary
		fmt.Fprintf(&b, "### %s\n\n", c.Corpus)
		b.WriteString("| Metric | Count | Rate |\n| --- | ---: | ---: |\n")
		fmt.Fprintf(&b, "| Validate pass (floor %.0f%%) | %d/%d | %s |\n", floorValidatePass*100, s.ValidatePass, s.Total, pct(s.ValidatePass, s.Total))
		fmt.Fprintf(&b, "| Semantic match (floor %.0f%%) | %d/%d | %s |\n", floorSemanticMatch*100, s.SemanticMatch, s.Total, pct(s.SemanticMatch, s.Total))
		fmt.Fprintf(&b, "| Generate OK (own verdict) | %d/%d | %s |\n", s.GenerateOK, s.Total, pct(s.GenerateOK, s.Total))
		fmt.Fprintf(&b, "| Tombstoned (never captured) | %d/%d | |\n\n", s.Tombstoned, s.Total)

		var diverged []string
		for _, r := range c.Records {
			if !r.MatchesCapture || r.ReplayExhausted || !r.PromptsMatchCapture {
				diverged = append(diverged, r.ID)
			}
		}
		if len(diverged) > 0 {
			fmt.Fprintf(&b, "Replay took a different path than capture for: %s\n\n", strings.Join(diverged, ", "))
		}
		b.WriteString(renderCategoryTable(s))
		b.WriteString("\n")
	}
	return b.String()
}

// changedRecords names every "<corpus>/<id>" whose record differs between
// the golden and got, so a failure points at requests rather than at the
// summary counts that moved with them.
func changedRecords(want []byte, got replayResult) string {
	var old replayResult
	if err := json.Unmarshal(want, &old); err != nil {
		return fmt.Sprintf("(golden does not parse: %v)", err)
	}
	prev := map[string][]byte{}
	for _, c := range old.Corpora {
		for _, r := range c.Records {
			b, _ := json.Marshal(r)
			prev[c.Corpus+"/"+r.ID] = b
		}
	}
	var changed []string
	for _, c := range got.Corpora {
		for _, r := range c.Records {
			b, _ := json.Marshal(r)
			if k := c.Corpus + "/" + r.ID; !bytes.Equal(prev[k], b) {
				changed = append(changed, k)
			}
		}
	}
	if len(changed) == 0 {
		return "(none — only corpus-level fields moved)"
	}
	return strings.Join(changed, ", ")
}

// firstDiff names the first line where want and got disagree, with a little
// context — enough to find the moved request in a CI log.
func firstDiff(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			where := ""
			for j := min(i, len(gl)-1); j >= 0; j-- {
				if s := strings.TrimSpace(gl[j]); strings.HasPrefix(s, `"id":`) {
					where = " (request " + strings.TrimSuffix(strings.TrimPrefix(s, `"id": `), ",") + ")"
					break
				}
			}
			return fmt.Sprintf("first difference at line %d%s:\n  want: %s\n  got:  %s", i+1, where, w, g)
		}
	}
	return "outputs differ only in trailing bytes"
}
