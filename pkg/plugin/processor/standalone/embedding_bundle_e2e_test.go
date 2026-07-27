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

package standalone

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/tetratelabs/wazero"
)

// conduitProcessorAIDirEnv names the checkout of ConduitIO/conduit-processor-ai
// this test builds the real ai.embed standalone-WASM guest from. This is a
// cross-repo test by nature (the bundle's guest lives in a sibling repo, not
// this one) — see the TODO(bundle-merge) comments in both repos' go.mod
// files. It is set explicitly rather than assumed at a relative path because
// no merged/monorepo layout exists yet; a follow-up wires this into a bundle
// CI job that checks out both repos side by side and sets it there.
const conduitProcessorAIDirEnv = "CONDUIT_PROCESSOR_AI_DIR"

// buildEmbeddingWASM builds the REAL ai.embed processor binary
// (cmd/embedding in conduit-processor-ai) for wasip1/wasm from the checkout
// named by CONDUIT_PROCESSOR_AI_DIR, and returns the compiled bytes.
//
// This is not a fixture, a stub, or a purpose-built minimal guest: it is the
// literal artifact conduit-processor-ai's own build instructions produce
// (README.md: `GOOS=wasip1 GOARCH=wasm go build -tags wasm -o embedding.wasm
// ./cmd/embedding`), which in turn embeds the real embed.Processor —
// including the ollama Provider this task adds — running through the real
// SDK (github.com/conduitio/conduit-processor-sdk), including its real
// wasm-tagged egress.HTTPService wiring (sdk/wasm/util.go's InitUtils), not
// a test double.
//
// The test SKIPS (does not fail) when CONDUIT_PROCESSOR_AI_DIR is unset or
// doesn't look like a conduit-processor-ai checkout — see this package's
// e2e report for why: until the two repos are merged or a bundle CI job
// checks both out side by side, this test cannot run unconditionally in
// every CI context that runs this repo's own test suite alone.
func buildEmbeddingWASM(ctx context.Context, t *testing.T) []byte {
	t.Helper()

	dir := os.Getenv(conduitProcessorAIDirEnv)
	if dir == "" {
		t.Skipf("%s not set: skipping the real-embedding-guest bundle e2e test "+
			"(requires a sibling checkout of ConduitIO/conduit-processor-ai with cmd/embedding)",
			conduitProcessorAIDirEnv)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "embedding")); err != nil {
		t.Skipf("%s=%q does not look like a conduit-processor-ai checkout (no cmd/embedding): %v",
			conduitProcessorAIDirEnv, dir, err)
	}

	out := filepath.Join(t.TempDir(), "embedding.wasm")
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "wasm", "-o", out, "./cmd/embedding")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building real embedding.wasm from %s failed: %v\n%s", dir, err, stderr.String())
	}

	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading built embedding.wasm: %v", err)
	}
	return bin
}

// newEmbeddingProcessor instantiates the compiled ai.embed guest against
// this package's real host module (the same newWASMProcessor path every
// standalone WASM processor in production goes through — see
// processor.go), bound to policy.
func newEmbeddingProcessor(ctx context.Context, t *testing.T, mod wazero.CompiledModule, policy egress.Policy) *wasmProcessor {
	t.Helper()
	logger := log.Test(t)
	p, err := newWASMProcessor(ctx, TestRuntime, mod, CompiledHostModule, schema.NewInMemoryService(), policy, t.Name(), logger)
	if err != nil {
		t.Fatalf("instantiate embedding wasm processor: %v", err)
	}
	return p
}

// ollamaEmbeddingsHandlerRequest mirrors conduit-processor-ai's
// ollamaEmbeddingRequest wire shape (model, prompt) — duplicated here
// deliberately rather than imported, since this test only needs to decode
// the request the real guest sends, not share code with it.
type ollamaEmbeddingsHandlerRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// TestEmbeddingWASM_HostEgressBundle_EndToEnd is the host-egress bundle's
// acceptance test: config-enabled egress -> the REAL ai.embed WASM guest
// (built from conduit-processor-ai, configured for the ollama provider) ->
// egress.Do -> this package's host module -> the host-side SSRF gate
// (pkg/plugin/processor/egress) -> a local httptest server standing in for
// Ollama.
//
// What this proves, precisely: every hop in that chain is the real,
// production code path — the real compiled embed.Processor (not a fake or
// a purpose-built stub guest), the real host module (hostModuleInstance,
// the same type host_module_egress_test.go exercises), and the real
// per-processor egress.Service gate (pkg/plugin/processor/egress). The only
// non-production stand-in is the destination server itself (a local
// httptest.Server playing Ollama's /api/embeddings, not a real Ollama
// install) — acceptable because the property under test is the host-side
// gate and the guest's own request/response handling, neither of which
// cares whether the JSON on the other end came from Ollama or a fixture
// that speaks its wire shape.
func TestEmbeddingWASM_HostEgressBundle_EndToEnd(t *testing.T) {
	ctx := context.Background()
	wasmBytes := buildEmbeddingWASM(ctx, t)

	mod, err := TestRuntime.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("compile real embedding.wasm: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	t.Run("HappyPath_RealGuestThroughHostToLocalServer", func(t *testing.T) {
		testEmbeddingHappyPath(t, ctx, mod)
	})
	t.Run("SSRF_HostNotAllowlisted_BlockedEndToEndThroughRealGuest", func(t *testing.T) {
		testEmbeddingSSRFBlocked(t, ctx, mod)
	})
	t.Run("ProviderError_Invariant1And3_NoPlaceholderEmbedding", func(t *testing.T) {
		testEmbeddingProviderErrorInvariant(t, ctx, mod)
	})
}

// testEmbeddingHappyPath proves the full chain works for real: config ->
// guest -> egress.Do -> host gate -> local server -> guest -> two output
// records each carrying the real embedding vector from the response the
// local server sent back for THAT record's own prompt (proving the guest's
// one-egress-call-per-record sub-batching, MaxBatchSize()==1 for ollama,
// keeps the record<->response mapping correct rather than scrambling or
// reusing the first call's result).
func testEmbeddingHappyPath(t *testing.T, ctx context.Context, mod wazero.CompiledModule) {
	is := is.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		is.NoErr(err)
		var req ollamaEmbeddingsHandlerRequest
		is.NoErr(json.Unmarshal(body, &req))
		is.Equal(req.Model, "test-embed-model")

		// Deterministic, per-prompt vector so the test can assert each
		// output record got back the vector for ITS OWN input, not a
		// neighbor's.
		vec := []float32{float32(len(req.Prompt)), 1, 2}
		w.Header().Set("Content-Type", "application/json")
		is.NoErr(json.NewEncoder(w).Encode(map[string]any{"embedding": vec}))
	}))
	defer srv.Close()

	policy := allowPolicy(t, "http://"+hp(t, srv))
	underTest := newEmbeddingProcessor(ctx, t, mod, policy)

	is.NoErr(underTest.Configure(ctx, config.Config{
		"provider":       "ollama",
		"model":          "test-embed-model",
		"ollama.baseURL": srv.URL,
		"requestTimeout": "5s",
		"maxRetries":     "0",
	}))
	is.NoErr(underTest.Open(ctx))

	// embed's default inputField is .Payload.After.text (the composable RAG
	// record shape the chunking processor emits), so the text must be a named
	// StructuredData field, not raw bytes at the payload root.
	records := []opencdc.Record{
		{Operation: opencdc.OperationCreate, Payload: opencdc.Change{After: opencdc.StructuredData{"text": "short"}}},
		{Operation: opencdc.OperationCreate, Payload: opencdc.Change{After: opencdc.StructuredData{"text": "a much longer chunk of text"}}},
	}
	processed := underTest.Process(ctx, records)
	is.Equal(len(processed), 2)

	wantTexts := []string{"short", "a much longer chunk of text"}
	wantLens := []int{len("short"), len("a much longer chunk of text")}
	for i, pr := range processed {
		single, ok := pr.(sdk.SingleRecord)
		if !ok {
			t.Fatalf("record %d: want sdk.SingleRecord, got %T (%+v)", i, pr, pr)
		}
		rec := opencdc.Record(single)

		is.Equal(rec.Metadata["ai.embedding.provider"], "ollama")
		is.Equal(rec.Metadata["ai.embedding.model"], "test-embed-model")
		is.Equal(rec.Metadata["ai.embedding.dimension"], "3")
		// Ollama reports no token usage — the tokensUsed/tokensUsedScope
		// metadata must be ABSENT, never a fabricated "0" (design doc §2
		// "never estimated"; embed/ollama.go's parseOllamaEmbeddingResponse).
		_, hasTokens := rec.Metadata["ai.embedding.tokensUsed"]
		is.True(!hasTokens)
		_, hasScope := rec.Metadata["ai.embedding.tokensUsedScope"]
		is.True(!hasScope)

		// embed writes the vector as a native []any of float64 to its default
		// outputField (.Payload.After.vector), preserving the input "text" field
		// alongside — the composable {text, vector} shape a vector sink consumes.
		sd, ok := rec.Payload.After.(opencdc.StructuredData)
		if !ok {
			t.Fatalf("record %d: want opencdc.StructuredData output payload, got %T", i, rec.Payload.After)
		}
		is.Equal(sd["text"], wantTexts[i]) // input text preserved
		vec, ok := sd["vector"].([]any)
		if !ok {
			t.Fatalf("record %d: want []any vector at .Payload.After.vector, got %T", i, sd["vector"])
		}
		is.Equal(len(vec), 3)
		is.Equal(int(vec[0].(float64)), wantLens[i])
	}

	is.NoErr(underTest.Teardown(ctx))
}

// testEmbeddingSSRFBlocked is the bundle's highest-value assertion: the
// SSRF gate proven end-to-end through the REAL compiled guest, not a host-
// module unit test in isolation. The pipeline is configured to reach a real
// local server, but that server's (IP,port) was never granted by the
// egress policy (only an unrelated decoy server's was) — the same
// (IP,port)-exact-match invariant policy.go's matchesCarveOut/MatchHostPort
// enforce (allowlisting 127.0.0.1:11434 must not admit 127.0.0.1:6379).
//
// Expected outcome: the guest's egress.Do call is refused by the host gate
// (ErrForbidden), embed/ollama.go's classifyOllamaEgressError wraps it as a
// coded ai.embedding_provider_error, and the record comes back as an
// sdk.ErrorRecord — never a passthrough, never a placeholder vector, and
// the decoy target is never contacted (proving no confusion between the
// two policy entries).
func testEmbeddingSSRFBlocked(t *testing.T, ctx context.Context, mod wazero.CompiledModule) {
	is := is.New(t)

	reachedTarget := false
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reachedTarget = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"embedding":[9,9,9]}`))
	}))
	defer targetSrv.Close()

	decoySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"embedding":[0,0,0]}`))
	}))
	defer decoySrv.Close()

	// Policy grants ONLY the decoy's (IP,port) — the pipeline config below
	// points at targetSrv instead, an unauthorized host from the policy's
	// point of view (an operator misconfiguration, or a compromised config
	// value; either way the gate must hold).
	policy := allowPolicy(t, "http://"+hp(t, decoySrv))
	underTest := newEmbeddingProcessor(ctx, t, mod, policy)

	is.NoErr(underTest.Configure(ctx, config.Config{
		"provider":       "ollama",
		"model":          "test-embed-model",
		"ollama.baseURL": targetSrv.URL,
		"requestTimeout": "5s",
		"maxRetries":     "0",
	}))
	is.NoErr(underTest.Open(ctx))

	processed := underTest.Process(ctx, []opencdc.Record{
		{Operation: opencdc.OperationCreate, Payload: opencdc.Change{After: opencdc.StructuredData{"text": "ssrf-attempt"}}},
	})
	is.Equal(len(processed), 1)

	errRecord, ok := processed[0].(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("want sdk.ErrorRecord for a non-allowlisted target, got %T (%+v)", processed[0], processed[0])
	}
	is.True(errRecord.Error != nil)
	msg := errRecord.Error.Error()
	// The coded error and the host gate's own reason both survive the
	// guest -> host proto boundary as message text (see
	// conduit-processor-sdk's run.go protoConverter.error: Message =
	// err.Error(), which for *embed.Error is "<code>: <message> ...").
	is.True(strings.Contains(msg, "ai.embedding_provider_error"))
	is.True(strings.Contains(msg, "not in the pipeline's egress allowlist") ||
		strings.Contains(msg, "forbidden"))
	is.True(!reachedTarget) // the gate refused the call before it ever reached the target server

	is.NoErr(underTest.Teardown(ctx))
}

// testEmbeddingProviderErrorInvariant proves invariants 1/3
// (CLAUDE.md: never acknowledge/deliver a record as successfully processed
// without a durable, real result) end to end for this provider: a local
// server that fails for one record's prompt and succeeds for another's
// must produce exactly one ErrorRecord and one embedded SingleRecord — the
// failure must never widen into losing the good record, and the good
// record must never regress into a placeholder/error, and the bad record
// must never come back embedded with a fabricated vector.
func testEmbeddingProviderErrorInvariant(t *testing.T, ctx context.Context, mod wazero.CompiledModule) {
	is := is.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		is.NoErr(err)
		var req ollamaEmbeddingsHandlerRequest
		is.NoErr(json.Unmarshal(body, &req))

		if req.Prompt == "trigger-error" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"synthetic provider failure"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		is.NoErr(json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 2, 3}}))
	}))
	defer srv.Close()

	policy := allowPolicy(t, "http://"+hp(t, srv))
	underTest := newEmbeddingProcessor(ctx, t, mod, policy)

	is.NoErr(underTest.Configure(ctx, config.Config{
		"provider":       "ollama",
		"model":          "test-embed-model",
		"ollama.baseURL": srv.URL,
		"requestTimeout": "5s",
		"maxRetries":     "0", // fail fast: this test asserts failure shape, not retry behavior
	}))
	is.NoErr(underTest.Open(ctx))

	processed := underTest.Process(ctx, []opencdc.Record{
		{Operation: opencdc.OperationCreate, Payload: opencdc.Change{After: opencdc.StructuredData{"text": "trigger-error"}}},
		{Operation: opencdc.OperationCreate, Payload: opencdc.Change{After: opencdc.StructuredData{"text": "this one succeeds"}}},
	})
	is.Equal(len(processed), 2)

	errRecord, ok := processed[0].(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("record 0: want sdk.ErrorRecord (provider failure), got %T (%+v)", processed[0], processed[0])
	}
	is.True(errRecord.Error != nil)
	is.True(strings.Contains(errRecord.Error.Error(), "ai.embedding_provider_error"))
	// The exact vendor status/message must survive too, proving this isn't
	// a generic catch-all: the guest actually parsed the local server's
	// response.
	is.True(strings.Contains(errRecord.Error.Error(), "500") ||
		strings.Contains(errRecord.Error.Error(), "synthetic provider failure"))

	single, ok := processed[1].(sdk.SingleRecord)
	if !ok {
		t.Fatalf("record 1: want sdk.SingleRecord (independent success), got %T (%+v)", processed[1], processed[1])
	}
	rec := opencdc.Record(single)
	sd, ok := rec.Payload.After.(opencdc.StructuredData)
	if !ok {
		t.Fatalf("record 1: want opencdc.StructuredData output payload, got %T", rec.Payload.After)
	}
	is.Equal(sd["text"], "this one succeeds") // input text preserved alongside the vector
	vec, ok := sd["vector"].([]any)
	if !ok {
		t.Fatalf("record 1: want []any vector at .Payload.After.vector, got %T", sd["vector"])
	}
	is.Equal(len(vec), 3)
	is.Equal([]float64{vec[0].(float64), vec[1].(float64), vec[2].(float64)}, []float64{1, 2, 3})

	is.NoErr(underTest.Teardown(ctx))
}
