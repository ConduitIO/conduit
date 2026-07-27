//go:build rag_template_e2e

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

// True engine-level end-to-end test for the postgres-pgvector-rag vendored
// template — the follow-up the template shipped without
// (templates/postgres-pgvector-rag/README.md's "tracked as follow-up work"
// note) and the §8 bar of docs/design-documents/20260724-ai-pipeline-components.md
// ("records are asserted to actually land in the vector table with the expected
// dimension — 'YAML parses' is explicitly not sufficient").
//
// # How this differs from the two existing e2es it borrows from
//
//   - template_gallery_e2e_integration_test.go (`templates_e2e`) proves the
//     BUILT-IN-only templates (postgres-s3, postgres-cdc-kafka) end to end
//     through the real engine, but never populates cfg.Processors.Path with a
//     WASM guest or cfg.Connectors.Path with an external connector binary — the
//     engine's standalone discovery paths are untouched.
//   - pkg/plugin/processor/standalone/rag_e2e_test.go (`rag_e2e`) proves the
//     chunk -> embed -> pgvector record-shape + delete contract, but deliberately
//     BYPASSES the engine: it hand-instantiates the WASM processors and drives
//     the pgvector sink by hand, never through NewRuntime discovery, the real
//     pipeline lifecycle, or the engine egress ceiling / per-processor
//     resolution.
//
// This test closes both gaps: it scaffolds the template with the real
// `conduit pipelines init`, makes ai.chunk / ai.embed (WASM) and pgvector
// (external binary) discoverable by the engine's OWN registries, boots the real
// conduit.Runtime, and asserts embedding rows actually land in pgvector (right
// dimension, right id, right source_key) then that a source-row delete removes
// every derived chunk row THROUGH the engine's tombstone fan-out.
//
// The only mocked boundary is the embedding provider's HTTP endpoint (an
// in-process httptest server standing in for Ollama), admitted through the
// engine's SSRF egress gate by an explicit loopback (IP,port) carve-out — the
// WASM boundary, the connector protocol, the egress gate, the pipeline
// lifecycle, and pgvector are all real.
//
// Skips cleanly (never fails) when CONDUIT_PROCESSOR_AI_DIR,
// CONDUIT_CONNECTOR_PGVECTOR_DIR, or the pgvector+logical-replication Postgres
// are unavailable, so a plain `go test` (without the sibling checkouts) is
// green-by-skip.
package pipelines_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	json "github.com/goccy/go-json"
	"github.com/jackc/pgx/v5"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// The two sibling-checkout env vars, duplicated from rag_e2e_test.go's
// (conduitProcessorAIDirEnv / conduitConnectorPgvectorDirEnv) — that file lives
// in package standalone under the `rag_e2e` tag, so its consts are not visible
// here. Same duplicate-small-glue precedent as template_gallery_e2e_test.go's
// own polling helpers vs. dev_integration_test.go.
const (
	ragTemplateProcessorAIDirEnv     = "CONDUIT_PROCESSOR_AI_DIR"
	ragTemplateConnectorPgvectorEnv  = "CONDUIT_CONNECTOR_PGVECTOR_DIR"
	ragTemplateProcessorAISkipReason = "requires a sibling checkout of ConduitIO/conduit-processor-ai (cmd/chunking + cmd/embedding)"
	ragTemplateConnectorSkipReason   = "requires a sibling checkout of ConduitIO/conduit-connector-pgvector (cmd/connector)"
)

// ragTemplatePGConnString is the connection string for the dedicated pgvector +
// logical-replication Postgres this suite expects (test/compose-rag-template.yaml)
// — a separate instance/port (:5445) from every other e2e's Postgres, see that
// compose file's header comment for why. The SAME instance is BOTH the CDC
// source AND the pgvector store (the template allows this — pipeline.yaml's
// destination url comment).
const ragTemplatePGConnString = "postgres://conduit:conduit@127.0.0.1:5445/conduit_rag_template_e2e?sslmode=disable"

// ragTemplateVectorDim is the single dimension shared by three places that MUST
// agree or the pgvector destination refuses to open (it validates dim at
// startup): the ollama mock's returned vector length, the template's
// `dimension` setting (left at its default 768), and the vector(N) column of
// the target table. Kept at the template default 768 to minimize deviation from
// the shipped YAML (§3.5 of the plan).
const ragTemplateVectorDim = 768

// The exact placeholder strings the template scaffolds (verified against
// templates/postgres-pgvector-rag/pipeline.yaml). Anchored patches key off
// these; a template shape change makes the anchored replace fail loudly rather
// than silently patch the wrong (or no) line.
const (
	ragTemplateSourceURLPlaceholder = "postgres://user:password@localhost:5432/dbname?sslmode=disable"
	ragTemplateDestURLPlaceholder   = "postgres://user:password@localhost:5432/vectordb?sslmode=disable"
	ragTemplateDestTablePlaceholder = "document_chunks"
	ragTemplateOllamaBaseURLDefault = "http://localhost:11434"
)

// buildAIProcessorWASM builds the REAL ai.chunk and ai.embed standalone-WASM
// guests (cmd/chunking, cmd/embedding in conduit-processor-ai) from
// CONDUIT_PROCESSOR_AI_DIR into procDir as ai-chunk.wasm / ai-embed.wasm —
// mirroring rag_e2e_test.go's buildChunkingWASM / buildEmbeddingWASM, but
// placing the artifacts where the engine's processor registry scans
// (cfg.Processors.Path) rather than returning the bytes for hand-instantiation.
// Skips (never fails) when the checkout is absent.
func buildAIProcessorWASM(ctx context.Context, t *testing.T, procDir string) {
	t.Helper()

	dir := os.Getenv(ragTemplateProcessorAIDirEnv)
	if dir == "" {
		t.Skipf("%s not set: skipping the RAG template gallery e2e (%s)", ragTemplateProcessorAIDirEnv, ragTemplateProcessorAISkipReason)
	}
	for _, sub := range []string{"chunking", "embedding"} {
		if _, err := os.Stat(filepath.Join(dir, "cmd", sub)); err != nil {
			t.Skipf("%s=%q does not look like a conduit-processor-ai checkout (no cmd/%s): %v", ragTemplateProcessorAIDirEnv, dir, sub, err)
		}
	}

	build := func(cmdPkg, outName string) {
		out := filepath.Join(procDir, outName)
		cmd := exec.CommandContext(ctx, "go", "build", "-tags", "wasm", "-o", out, "./cmd/"+cmdPkg)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("building real %s from %s failed: %v\n%s", outName, dir, err, stderr.String())
		}
		requireNonEmptyFile(t, out) // a failed cross-repo build can leave a zero-byte file the registry logs-and-skips
	}
	build("chunking", "ai-chunk.wasm")
	build("embedding", "ai-embed.wasm")
}

// buildPgvectorConnectorBinary builds the REAL conduit-connector-pgvector
// ./cmd/connector native binary from CONDUIT_CONNECTOR_PGVECTOR_DIR into connDir
// (where the engine's standalone connector registry scans, cfg.Connectors.Path)
// — mirroring rag_e2e_test.go's buildPgvectorConnectorBinary. Skips (never
// fails) when the checkout is absent.
func buildPgvectorConnectorBinary(ctx context.Context, t *testing.T, connDir string) {
	t.Helper()

	dir := os.Getenv(ragTemplateConnectorPgvectorEnv)
	if dir == "" {
		t.Skipf("%s not set: skipping the RAG template gallery e2e (%s)", ragTemplateConnectorPgvectorEnv, ragTemplateConnectorSkipReason)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "connector")); err != nil {
		t.Skipf("%s=%q does not look like a conduit-connector-pgvector checkout (no cmd/connector): %v", ragTemplateConnectorPgvectorEnv, dir, err)
	}

	out := filepath.Join(connDir, "conduit-connector-pgvector")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/connector")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building real conduit-connector-pgvector binary from %s failed: %v\n%s", dir, err, stderr.String())
	}
	requireNonEmptyFile(t, out)
}

func requireNonEmptyFile(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat built artifact %q: %v", path, err)
	}
	if fi.Size() == 0 {
		t.Fatalf("built artifact %q is zero bytes — the cross-repo build produced nothing usable", path)
	}
}

// ragTemplateMockHostPort is hp(t, srv) from rag_e2e_test.go / the
// host_module_egress_test.go host:port helper: httptest.NewServer binds a
// loopback ephemeral port; return its "host:port" so the SAME string feeds the
// ollama.baseURL, the ceiling allowlist, and the per-processor sdk.egress.allow.
func ragTemplateMockHostPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse mock server URL %q: %v", srv.URL, err)
	}
	return u.Host
}

// ollamaMockRequest mirrors conduit-processor-ai embed's ollama request wire
// shape (model, prompt), duplicated deliberately (same rationale as
// rag_e2e_test.go's ollamaMockHandlerRequest) — this test only decodes the
// guest's request, it does not share code with it.
type ollamaMockRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// newOllamaMockServer stands in for Ollama's POST /api/embeddings, returning a
// fixed 768-dimension vector (ragTemplateVectorDim) — the ONLY mocked boundary
// in this suite. Binds loopback via httptest, so its (IP,port) is admissible
// through the egress gate only by an explicit carve-out (see the test body).
func newOllamaMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	is := is.New(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaMockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vec := make([]float32, ragTemplateVectorDim)
		vec[0] = float32(len(req.Prompt)) // salt slot 0 so a record<->response mixup would be visible
		for i := 1; i < ragTemplateVectorDim; i++ {
			vec[i] = float32(i%7) + 0.5
		}
		w.Header().Set("Content-Type", "application/json")
		is.NoErr(json.NewEncoder(w).Encode(map[string]any{"embedding": vec}))
	}))
}

// patchExactlyOnce replaces old with new in s, asserting old occurs EXACTLY
// once (anchored/defensive, mirroring templates_e2e's patchPostgresSourceURL:
// a bare count-1 replace that matched a comment occurrence, not the real
// setting, was the concrete bug that helper was written to prevent). It returns
// the patched string and fails the test if old is absent, duplicated, or new is
// somehow not present afterward.
func patchExactlyOnce(t *testing.T, s, oldStr, newStr string) string {
	t.Helper()
	if n := strings.Count(s, oldStr); n != 1 {
		t.Fatalf("expected exactly one %q in the scaffolded template, found %d — "+
			"template YAML shape changed, update this test's patching logic", oldStr, n)
	}
	s = strings.Replace(s, oldStr, newStr, 1)
	if !strings.Contains(s, newStr) {
		t.Fatalf("patch to %q did not take", newStr)
	}
	return s
}

// connectRAGTemplatePostgres opens the pgvector+logrepl Postgres, skipping
// (never failing) if unreachable — mirroring rag_e2e_test.go's
// connectRAGPostgres skip semantics.
func connectRAGTemplatePostgres(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, ragTemplatePGConnString)
	if err != nil {
		t.Skipf("skipping RAG template gallery e2e: pgvector+logrepl Postgres not reachable (%v); "+
			"run `docker compose -f test/compose-rag-template.yaml up --wait`", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// waitForQueryCount polls a scalar-count query until it returns want or the
// deadline elapses — poll, never sleep-and-check-once (the CDC-lag lesson from
// templates_e2e and rag_e2e). Returns the last observed count on timeout for a
// legible failure.
func waitForQueryCount(ctx context.Context, t *testing.T, conn *pgx.Conn, query string, args []any, want int, timeout time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int
	for {
		var count int
		if err := conn.QueryRow(ctx, query, args...).Scan(&count); err != nil {
			t.Fatalf("count query failed: %v (query=%q)", err, query)
		}
		last = count
		if count == want {
			return count, true
		}
		if time.Now().After(deadline) {
			return last, false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestTemplateGalleryRAG_Integration is the postgres-pgvector-rag template's
// full-engine end-to-end test. See this file's header comment for the design
// calls; the acceptance criteria are the plan's §5.
func TestTemplateGalleryRAG_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-engine RAG template gallery e2e in -short mode")
	}
	ctx := context.Background()
	is := is.New(t)

	// --- build + place BOTH plugin kinds BEFORE NewRuntime (registries scan at
	// construction). Skips cleanly if either sibling checkout is absent. ---
	tmp := t.TempDir()
	procDir := filepath.Join(tmp, "processors")
	connDir := filepath.Join(tmp, "connectors")
	is.NoErr(os.MkdirAll(procDir, 0o755))
	is.NoErr(os.MkdirAll(connDir, 0o755))
	buildAIProcessorWASM(ctx, t, procDir)
	buildPgvectorConnectorBinary(ctx, t, connDir)

	// --- infra: one pgvector+logrepl Postgres as BOTH source and vector store.
	// Skips cleanly if unreachable. ---
	pg := connectRAGTemplatePostgres(ctx, t)
	_, err := pg.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`)
	is.NoErr(err)

	// Source table the template's `tables: my_table` reads, with the text
	// column its chunk inputField (.Payload.After.content) points at.
	_, err = pg.Exec(ctx, `DROP TABLE IF EXISTS my_table`)
	is.NoErr(err)
	_, err = pg.Exec(ctx, `CREATE TABLE my_table (id INT PRIMARY KEY, content TEXT NOT NULL)`)
	is.NoErr(err)
	t.Cleanup(func() { _, _ = pg.Exec(context.Background(), `DROP TABLE IF EXISTS my_table`) })

	// Vector table exactly per the template README's CREATE TABLE. A unique
	// name keeps this isolated from any leftover state; it is patched into the
	// scaffolded YAML below.
	vectorTable := fmt.Sprintf("rag_template_e2e_%d", time.Now().UnixNano())
	_, err = pg.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %q (id text PRIMARY KEY, embedding vector(%d), metadata jsonb, source_key text)`,
		vectorTable, ragTemplateVectorDim))
	is.NoErr(err)
	_, err = pg.Exec(ctx, fmt.Sprintf(`CREATE INDEX ON %q (source_key)`, vectorTable))
	is.NoErr(err)
	t.Cleanup(func() { _, _ = pg.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %q`, vectorTable)) })

	// --- seed ONE row BEFORE boot so snapshotMode:initial captures it.
	// Content stays well under the template's recursive chunkSize (1000 runes),
	// so it deterministically chunks into exactly ONE chunk — the delete-path
	// payoff (tombstone removes every derived row) holds for one chunk exactly
	// as for many, and one chunk needs no assumptions about the recursive
	// splitter's multi-chunk boundaries (which live in the sibling repo). ---
	const seedRowID = 1
	const seedContent = "Conduit RAG template gallery engine-level end-to-end test content."
	const wantChunkRows = 1
	_, err = pg.Exec(ctx, `INSERT INTO my_table (id, content) VALUES ($1, $2)`, seedRowID, seedContent)
	is.NoErr(err)

	// --- mock embedding provider on loopback. Its (IP,port) is what both
	// egress sides must admit by exact carve-out. ---
	srv := newOllamaMockServer(t)
	defer srv.Close()
	mockHostPort := ragTemplateMockHostPort(t, srv) // e.g. 127.0.0.1:54321
	// The allowlist entry MUST carry the http:// scheme and be the literal
	// resolved loopback (IP,port), not "localhost": ParseAllowEntry defaults a
	// bare host:port to the https scheme, which would never match the guest's
	// http egress call; and the dial-time gate refuses loopback except by an
	// exact IP-literal (IP,port) carve-out (egress/ipguard.go, policy.go's
	// matchesCarveOut). Deriving from srv.URL keeps it correct whether httptest
	// bound 127.0.0.1 or [::1].
	allowSpec := "http://" + mockHostPort
	ollamaBaseURL := "http://" + mockHostPort

	// --- pre-flight egress-intersection sanity: fail fast with a clear message
	// if the ceiling ∩ per-processor request does not admit the mock, rather
	// than a mysterious embed timeout at run time. ---
	preflightEgressAdmitsMock(t, allowSpec)

	// --- scaffold via the REAL `conduit pipelines init`. ---
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))
	pipelinePath := scaffoldTemplate(t, pipelinesDir, "postgres-pgvector-rag")

	original, err := os.ReadFile(pipelinePath)
	is.NoErr(err)
	yaml := string(original)

	// Assert all four plugin refs are present BEFORE patching (guards against a
	// template shape change silently making the test vacuous).
	for _, ref := range []string{"builtin:postgres", "standalone:ai.chunk", "standalone:ai.embed", "standalone:pgvector"} {
		if !strings.Contains(yaml, ref) {
			t.Fatalf("scaffolded template missing expected plugin ref %q — template shape changed", ref)
		}
	}

	// --- anchored patches ONLY: source url, destination url + table, embed's
	// ollama.baseURL, and an added per-processor sdk.egress.allow. No structural
	// rewrite; dimension left at the template default 768. ---
	yaml = patchExactlyOnce(t, yaml, "url: "+ragTemplateSourceURLPlaceholder, "url: "+ragTemplatePGConnString)
	yaml = patchExactlyOnce(t, yaml, "url: "+ragTemplateDestURLPlaceholder, "url: "+ragTemplatePGConnString)
	yaml = patchExactlyOnce(t, yaml, "table: "+ragTemplateDestTablePlaceholder, "table: "+vectorTable)
	// Replace the embed processor's ollama.baseURL line and, on the same
	// anchored replace, append the per-processor egress opt-in at the same
	// 10-space settings indentation. Both are required: the ceiling alone
	// resolves to deny-all without the per-processor sdk.egress.allow
	// (PolicyFromSettings returns DenyAll when the key is absent), and vice
	// versa (ResolvePolicy intersects the two).
	embedAnchor := "ollama.baseURL: " + ragTemplateOllamaBaseURLDefault
	embedReplacement := "ollama.baseURL: " + ollamaBaseURL + "\n          sdk.egress.allow: " + allowSpec
	yaml = patchExactlyOnce(t, yaml, embedAnchor, embedReplacement)

	is.NoErr(os.WriteFile(pipelinePath, []byte(yaml), 0o600))

	// --- boot the real engine. Capture logs to a sink for diagnosis on
	// timeout. Egress ceiling opened AND scoped to exactly the mock's (IP,port)
	// (the per-processor opt-in is patched above). ---
	logs := &safeBuffer{}
	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = filepath.Join(tmp, "conduit.db")
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir
	cfg.Processors.Path = procDir
	cfg.Connectors.Path = connDir
	cfg.Processors.Egress.Enabled = true
	cfg.Processors.Egress.Allow = []string{allowSpec}
	cfg.Log.Level = "info"
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		zl := zerolog.New(logs).With().Timestamp().Logger().Level(l)
		return log.New(zl)
	}

	r, err := conduit.NewRuntime(cfg)
	is.NoErr(err)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(runCtx) }()

	// Raised Ready timeout: cold CI + two wasm compiles at construction + a
	// connector subprocess spawn + logical-replication slot setup take well over
	// the 20s the built-in-only template e2es use.
	readyStart := time.Now()
	select {
	case <-r.Ready:
	case err := <-runDone:
		t.Fatalf("runtime exited before becoming ready: %v\n\n=== LOGS ===\n%s", err, logs.String())
	case <-time.After(60 * time.Second):
		t.Fatalf("runtime did not become ready within 60s (elapsed %s)\n\n=== LOGS ===\n%s", time.Since(readyStart), logs.String())
	}

	// --- create path: the seeded row flows source->chunk->embed(mock)->pgvector.
	// Poll until the derived chunk row lands, then assert its shape. ---
	countQuery := fmt.Sprintf(`SELECT count(*) FROM %q`, vectorTable)
	if got, ok := waitForQueryCount(ctx, t, pg, countQuery, nil, wantChunkRows, 60*time.Second); !ok {
		t.Fatalf("create path: expected %d chunk row(s) in %q, last observed %d\n\n=== LOGS ===\n%s",
			wantChunkRows, vectorTable, got, logs.String())
	}

	// Read the landed row back rather than predicting its source_key: the value
	// is derived by the chunk processor from the Postgres record key, whose exact
	// stringification is a cross-repo detail. Assert the shape invariants that
	// DO hold regardless: a real 768-dim vector, a non-empty source_key, and the
	// deterministic id == "<source_key>:<chunk_index>" the template documents.
	var gotID, gotSourceKey string
	var gotDims int
	is.NoErr(pg.QueryRow(ctx, fmt.Sprintf(
		`SELECT id, source_key, vector_dims(embedding) FROM %q`, vectorTable)).Scan(&gotID, &gotSourceKey, &gotDims))
	is.Equal(gotDims, ragTemplateVectorDim) // real vector at the configured dimension, not null
	is.True(gotSourceKey != "")             // source_key populated (the delete fan-out's match column)
	is.Equal(gotID, gotSourceKey+":0")      // deterministic id == "<source_key>:<chunk_index>"

	// --- delete path THROUGH THE ENGINE: delete the source row post-Ready so it
	// arrives via CDC (not snapshot); the tombstone must fan out
	// source->chunk->embed(passthrough)->pgvector and remove every row for this
	// source_key. Short settle to let logical replication catch up past the
	// snapshot, then poll to zero. ---
	time.Sleep(2 * time.Second)
	_, err = pg.Exec(ctx, `DELETE FROM my_table WHERE id = $1`, seedRowID)
	is.NoErr(err)

	deleteCountQuery := fmt.Sprintf(`SELECT count(*) FROM %q WHERE source_key = $1`, vectorTable)
	if got, ok := waitForQueryCount(ctx, t, pg, deleteCountQuery, []any{gotSourceKey}, 0, 60*time.Second); !ok {
		t.Fatalf("delete path: expected 0 rows for source_key %q in %q, last observed %d\n\n=== LOGS ===\n%s",
			gotSourceKey, vectorTable, got, logs.String())
	}

	// --- graceful shutdown: cancel and assert the runtime returns in time (no
	// goroutine/connector leak). ---
	cancel()
	select {
	case <-runDone:
	case <-time.After(30 * time.Second):
		t.Fatalf("runtime did not shut down after context cancellation\n\n=== LOGS ===\n%s", logs.String())
	}
}

// preflightEgressAdmitsMock reconstructs the engine's egress resolution
// (per-processor request ∩ engine ceiling) from the exported egress primitives
// and asserts the mock's (scheme,host,port) survives with nothing dropped — the
// same computation resolveEgressPolicy performs at processor open, run here so a
// misconfigured allowlist fails with a clear message rather than an opaque embed
// timeout during the run.
func preflightEgressAdmitsMock(t *testing.T, allowSpec string) {
	t.Helper()
	is := is.New(t)

	ceilingAllow, err := egress.ParseAllowlist(allowSpec)
	is.NoErr(err)
	ceiling := egress.Policy{Enabled: true, Allowlist: ceilingAllow}

	requested, err := egress.PolicyFromSettings(map[string]string{egress.ConfigKeyAllow: allowSpec})
	is.NoErr(err)

	effective, dropped := egress.ResolvePolicy(requested, ceiling)
	is.Equal(len(dropped), 0) // nothing the pipeline requested falls outside the ceiling
	is.True(effective.Enabled)

	u, err := url.Parse(allowSpec)
	is.NoErr(err)
	port := u.Port()
	if port == "" {
		port = "80" // http default; ParseAllowEntry does the same
	}
	is.True(effective.MatchHostPort(u.Scheme, u.Hostname(), port)) // the mock target is admitted
}
