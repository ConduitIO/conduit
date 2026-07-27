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

//go:build rag_e2e

// This file is the RAG-pipeline delete-path e2e: chunk -> embed -> pgvector,
// proven against real infrastructure end to end, including the delete path
// that specifically exercises conduit-processor-ai's embed tombstone-
// passthrough fix (commit c54abb4: "pass delete tombstones through
// unembedded"). See the package's e2e report (PR description) for the full
// design-call writeup; the highlights are recorded here since they explain
// choices that would otherwise look arbitrary:
//
//  1. Placement: this lives in pkg/plugin/processor/standalone (not a new
//     package or a separate module) because the chunk/embed stages MUST run
//     as the real ai.chunk/ai.embed WASM guests through this package's real
//     host module (hostModuleInstance, TestRuntime, newWASMProcessor,
//     CompiledHostModule) — those are unexported, so only code inside this
//     package can drive them. See embedding_bundle_e2e_test.go, which this
//     file borrows its WASM-build/host-module harness from directly
//     (buildEmbeddingWASM, newEmbeddingProcessor, allowPolicy, hp).
//
//  2. Sink dependency direction: this test does NOT add
//     github.com/conduitio/conduit-connector-pgvector to this module's go.mod.
//     Two alternatives were considered:
//     a. `go get` the connector module directly (it IS a public, fetchable
//     repo) and call destination.Destination.Write in-process. Rejected:
//     empirically, doing this pulls conduit-connector-pgvector's entire
//     golangci-lint tool dependency tree (hundreds of indirect modules
//     it lists as direct requires) into this module's go.sum, on top of
//     a non-tagged pseudo-version pin — a real, concrete hygiene cost
//     for a test-only need, and it also imports a specific connector's
//     Go package into the core engine, which is not how Conduit loads
//     out-of-process connectors in production.
//     b. (chosen) Build the connector's real ./cmd/connector binary from a
//     sibling checkout (CONDUIT_CONNECTOR_PGVECTOR_DIR, mirroring
//     CONDUIT_PROCESSOR_AI_DIR's pattern exactly) and drive it as a real
//     external connector plugin over the REAL gRPC connector protocol,
//     using this repo's own pkg/plugin/connector/standalone.Dispenser —
//     the exact code path production Conduit uses for any out-of-process
//     connector. This is zero new go.mod dependencies AND more faithful
//     than an in-process Go call (it exercises Configure/Open/Run over
//     the wire, not just a bare function call). See pgvectorSink below.
//
//  3. Embed -> pgvector wire shape (FIXED — see conduit-processor-ai's
//     feat/embed-pgvector-shape): this test used to carry a
//     reshapeEmbeddingForSink bridge here because embed's attachEmbedding
//     json.Marshal'd the vector to a []byte before Set-ing it, which silently
//     corrupts once the record crosses ANY protobuf boundary (the WASM
//     guest<->host boundary here, or a destination gRPC boundary in a real
//     pipeline): structpb (google.golang.org/protobuf's Struct
//     representation, which opencdc.StructuredData.ToProto uses) has no
//     "bytes" leaf kind — pointed at a NESTED field it silently
//     base64-encodes the []byte into a STRING (which pgvector's
//     internal.ParseVector does not accept), and pointed at the ROOT field it
//     becomes opencdc.RawData round-tripping fine as bytes, but then the
//     whole payload is a bare JSON ARRAY, not the OBJECT pgvector's
//     structuredPayload requires. conduit-processor-ai's embed package now
//     builds a native []any of float64 (never json.Marshal'd) and writes it
//     to its own default outputField, ".Payload.After.vector" — a structpb
//     ListValue of NumberValues that round-trips losslessly and is exactly
//     the shape pgvector's internal.ParseVector documents accepting. Chunk's
//     own default outputField also changed, from raw bytes at the root to a
//     named ".Payload.After.text" field, which is what makes embed's default
//     inputField (also ".Payload.After.text") work with zero field
//     configuration between the two processors. This test now runs the real
//     embed.wasm output straight into pgvector's Destination.Write with no
//     bridge — see chunkAndEmbed below.
package standalone

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/client"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	pconnutilsserver "github.com/conduitio/conduit-connector-protocol/pconnutils/v1/server"
	connutilsv1 "github.com/conduitio/conduit-connector-protocol/proto/connutils/v1"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	connectorplugin "github.com/conduitio/conduit/pkg/plugin/connector"
	connectorstandalone "github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	json "github.com/goccy/go-json"
	"github.com/jackc/pgx/v5"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"github.com/tetratelabs/wazero"
	"google.golang.org/grpc"
)

// conduitConnectorPgvectorDirEnv names the checkout of
// ConduitIO/conduit-connector-pgvector this test builds the real ./cmd/connector
// binary from — see the design-call note at the top of this file for why this
// is an env-var-driven external build rather than a go.mod dependency.
const conduitConnectorPgvectorDirEnv = "CONDUIT_CONNECTOR_PGVECTOR_DIR"

// ragE2EPGConnString is the connection string for the dedicated pgvector
// Postgres this suite expects (testdata/rag_e2e/docker-compose.yml) — a
// separate instance/port from conduit-connector-pgvector's own test database,
// see that compose file's header comment for why.
const ragE2EPGConnString = "postgres://conduit:conduit@127.0.0.1:5435/conduit_rag_e2e?sslmode=disable"

// buildChunkingWASM builds the REAL ai.chunk standalone-WASM guest
// (cmd/chunking in conduit-processor-ai) from CONDUIT_PROCESSOR_AI_DIR, the
// same checkout and skip semantics buildEmbeddingWASM (in
// embedding_bundle_e2e_test.go) uses for cmd/embedding. Chunking needs no
// egress capability at all (design doc: it performs no I/O), so unlike embed
// it never touches the host module's http_request export — see doc.go in
// conduit-processor-ai's chunk package.
func buildChunkingWASM(ctx context.Context, t *testing.T) []byte {
	t.Helper()

	dir := os.Getenv(conduitProcessorAIDirEnv)
	if dir == "" {
		t.Skipf("%s not set: skipping the RAG pipeline e2e (requires a sibling checkout of "+
			"ConduitIO/conduit-processor-ai with cmd/chunking)", conduitProcessorAIDirEnv)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "chunking")); err != nil {
		t.Skipf("%s=%q does not look like a conduit-processor-ai checkout (no cmd/chunking): %v",
			conduitProcessorAIDirEnv, dir, err)
	}

	out := filepath.Join(t.TempDir(), "chunking.wasm")
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "wasm", "-o", out, "./cmd/chunking")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building real chunking.wasm from %s failed: %v\n%s", dir, err, stderr.String())
	}

	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading built chunking.wasm: %v", err)
	}
	return bin
}

// buildPgvectorConnectorBinary builds the REAL conduit-connector-pgvector
// ./cmd/connector binary (a native OS/arch build — this is an out-of-process
// connector plugin, not a WASM guest) from CONDUIT_CONNECTOR_PGVECTOR_DIR. See
// the design-call note at the top of this file for why this is a subprocess
// build rather than a go.mod import of the connector's Go package.
func buildPgvectorConnectorBinary(ctx context.Context, t *testing.T) string {
	t.Helper()

	dir := os.Getenv(conduitConnectorPgvectorDirEnv)
	if dir == "" {
		t.Skipf("%s not set: skipping the RAG pipeline e2e (requires a sibling checkout of "+
			"ConduitIO/conduit-connector-pgvector with cmd/connector)", conduitConnectorPgvectorDirEnv)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "connector")); err != nil {
		t.Skipf("%s=%q does not look like a conduit-connector-pgvector checkout (no cmd/connector): %v",
			conduitConnectorPgvectorDirEnv, dir, err)
	}

	out := filepath.Join(t.TempDir(), "conduit-connector-pgvector")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/connector")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building real conduit-connector-pgvector binary from %s failed: %v\n%s", dir, err, stderr.String())
	}
	return out
}

// stubSchemaService satisfies pconnutils.SchemaService with unimplemented
// methods. Nothing in this suite's records ever carries schema
// subject/version metadata (see this file's header comment §3 on what's real
// vs. bridged), and conduit-connector-sdk's DestinationWithSchemaExtraction
// middleware (enabled by default) skips decoding entirely whenever that
// metadata is absent from a record — so these methods are never actually
// invoked. They exist purely so a real (if inert) connUtils gRPC server can
// be reachable at CONDUIT_CONNECTOR_UTILITIES_GRPC_TARGET, which the
// connector SDK requires to be configured at process startup regardless of
// whether a pipeline ever calls it (see startRAGConnUtilsServer).
type stubSchemaService struct{}

func (stubSchemaService) CreateSchema(context.Context, pconnutils.CreateSchemaRequest) (pconnutils.CreateSchemaResponse, error) {
	return pconnutils.CreateSchemaResponse{}, cerrors.New("rag_e2e stub: schema service is not implemented (no test record carries schema metadata)")
}

func (stubSchemaService) GetSchema(context.Context, pconnutils.GetSchemaRequest) (pconnutils.GetSchemaResponse, error) {
	return pconnutils.GetSchemaResponse{}, cerrors.New("rag_e2e stub: schema service is not implemented (no test record carries schema metadata)")
}

// startRAGConnUtilsServer stands up the minimal real connUtils gRPC server
// (schema service) that any conduit-connector-sdk-based connector requires to
// be reachable at startup, mirroring pkg/conduit/runtime.go's
// startConnectorUtils — a real grpc.Server, a real
// connutilsv1.SchemaServiceServer registration, listening on a real loopback
// port, torn down via t.Cleanup. Only the schema-registry BACKING is a stub
// (stubSchemaService) since none of this suite's records ever exercise it.
func startRAGConnUtilsServer(t *testing.T) string {
	t.Helper()
	is := is.New(t)

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)

	grpcServer := grpc.NewServer()
	connutilsv1.RegisterSchemaServiceServer(grpcServer, pconnutilsserver.NewSchemaServiceServer(stubSchemaService{}))

	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	return lis.Addr().String()
}

// pgvectorSink drives a REAL, out-of-process conduit-connector-pgvector
// Destination plugin over the real gRPC connector protocol
// (pconnector.DestinationPlugin), using this repo's own
// pkg/plugin/connector/standalone.Dispenser — the same dispenser production
// Conduit uses for any standalone (external) connector plugin (see
// pkg/plugin/connector/standalone/registry.go's Registry.NewDispenser). This
// is a deliberately minimal re-implementation of pkg/connector/destination.go's
// configure/open/run/Write/Ack sequence, without that type's Instance/
// persister/inspector machinery, which this standalone test has no use for and
// would otherwise have to fake.
type pgvectorSink struct {
	dispenser *connectorstandalone.Dispenser
	plugin    connectorplugin.DestinationPlugin
	stream    pconnector.DestinationRunStreamClient
	stopRun   context.CancelFunc
}

// openPgvectorSink dispenses, configures, and opens the real pgvector
// Destination plugin (binPath, built by buildPgvectorConnectorBinary) and
// starts its Run stream — mirroring pkg/connector/destination.go's Open,
// configure, open, and run methods. connUtilsAddr must point at a running
// connUtils gRPC server (startRAGConnUtilsServer) — the connector SDK refuses
// to start without one configured, even though this suite never actually
// calls it (see startRAGConnUtilsServer's doc comment).
func openPgvectorSink(ctx context.Context, t *testing.T, binPath, connUtilsAddr string, settings config.Config) *pgvectorSink {
	t.Helper()
	is := is.New(t)

	dispenser, err := connectorstandalone.NewDispenser(
		zerolog.Nop(),
		binPath,
		client.WithEnvVar(pconnector.EnvConduitConnectorID, "rag-e2e-pgvector-sink"),
		client.WithEnvVar(pconnector.EnvConduitConnectorMaxReceiveRecordSize, strconv.Itoa(server.DefaultMaxReceiveRecordSize)),
		// The connector refuses to start without a token env var (its SDK
		// treats an unset one as talking to a pre-v0.11.0 host and aborts).
		// This suite never exercises anything the token would gate (no
		// schema-registry or secret-resolver calls — see this file's header
		// comment §3 on what's real vs. bridged), so a placeholder value is
		// enough, mirroring pkg/plugin/connector/standalone/registry.go's own
		// "irrelevant-token" used for specifier-only dispenses.
		client.WithEnvVar(pconnutils.EnvConduitConnectorToken, "rag-e2e-irrelevant-token"),
		client.WithEnvVar(pconnutils.EnvConduitConnectorUtilitiesGRPCTarget, connUtilsAddr),
	)
	is.NoErr(err)

	plugin, err := dispenser.DispenseDestination()
	is.NoErr(err)

	_, err = plugin.Configure(ctx, pconnector.DestinationConfigureRequest{Config: settings})
	is.NoErr(err)
	_, err = plugin.Open(ctx, pconnector.DestinationOpenRequest{})
	is.NoErr(err)

	runCtx, stopRun := context.WithCancel(ctx)
	stream := plugin.NewStream()
	if err := plugin.Run(runCtx, stream); err != nil {
		stopRun()
		t.Fatalf("run pgvector destination plugin: %v", err)
	}

	return &pgvectorSink{
		dispenser: dispenser,
		plugin:    plugin,
		stream:    stream.Client(),
		stopRun:   stopRun,
	}
}

// write sends recs to the real pgvector Destination.Write (via the real gRPC
// stream) and waits for its ack response, failing the test on any nacked
// record. This is a synchronous send-then-await-ack — deliberately simpler
// than the engine's overlapped Write/Ack pipelining (pkg/connector/destination.go),
// which this single-threaded test doesn't need.
func (s *pgvectorSink) write(t *testing.T, recs []opencdc.Record) {
	t.Helper()
	is := is.New(t)

	is.NoErr(s.stream.Send(pconnector.DestinationRunRequest{Records: recs}))
	resp, err := s.stream.Recv()
	is.NoErr(err)
	if len(resp.Acks) != len(recs) {
		t.Fatalf("expected %d acks for %d written records, got %d", len(recs), len(recs), len(resp.Acks))
	}
	for i, ack := range resp.Acks {
		if ack.Error != "" {
			t.Fatalf("record %d write nacked by pgvector destination: %s", i, ack.Error)
		}
	}
}

func (s *pgvectorSink) teardown(t *testing.T, ctx context.Context) {
	t.Helper()
	s.stopRun()
	if _, err := s.plugin.Teardown(ctx, pconnector.DestinationTeardownRequest{}); err != nil {
		t.Logf("tearing down pgvector destination plugin: %v", err)
	}
}

// connectRAGPostgres opens a connection to the dedicated rag_e2e pgvector
// database (testdata/rag_e2e/docker-compose.yml), skipping the test (never
// failing it) if the database is unreachable — mirroring
// conduit-connector-pgvector's own test.Connect skip semantics, duplicated
// locally rather than imported (see the design-call note at the top of this
// file for why this suite does not depend on that module at all).
func connectRAGPostgres(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, ragE2EPGConnString)
	if err != nil {
		t.Skipf("skipping RAG pipeline e2e: rag_e2e pgvector database not reachable (%v); "+
			"run `docker compose -f testdata/rag_e2e/docker-compose.yml up -d`", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func ensureVectorExtension(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	is := is.New(t)
	_, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`)
	is.NoErr(err)
}

// setupRAGVectorTable creates the table shape pgvector's default config
// expects: id text PK (the chunk_id upsert key), a vector(dim) column, a
// metadata jsonb column, and an indexed source_key column (the delete
// fan-out's match column — design doc §5).
func setupRAGVectorTable(ctx context.Context, t *testing.T, conn *pgx.Conn, table string, dim int) {
	t.Helper()
	is := is.New(t)
	_, err := conn.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %q (id text PRIMARY KEY, embedding vector(%d), metadata jsonb, source_key text)`, table, dim,
	))
	is.NoErr(err)
	_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE INDEX ON %q (source_key)`, table))
	is.NoErr(err)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %q`, table))
	})
}

var ragIdentifierCounter atomic.Uint64

func randomRAGIdentifier(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("rag_e2e_%d_%d", time.Now().UnixNano(), ragIdentifierCounter.Add(1))
}

// sourceKeyRowCount returns the number of rows in table whose source_key
// column equals sourceKey — the delete fan-out's own match column (design doc
// §5), so this is the assertion the tombstone-passthrough fix is proven
// against: it must be N right after the create-path write, and exactly 0
// after the delete tombstone reaches the sink.
func sourceKeyRowCount(ctx context.Context, t *testing.T, conn *pgx.Conn, table, sourceKey string) int {
	t.Helper()
	is := is.New(t)
	var count int
	is.NoErr(conn.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %q WHERE source_key = $1`, table), sourceKey).Scan(&count))
	return count
}

// newRAGProcessor instantiates a compiled WASM guest against this package's
// real host module, exactly like embedding_bundle_e2e_test.go's
// newEmbeddingProcessor — except it takes an explicit id instead of deriving
// one from t.Name(). This test instantiates TWO wasmProcessors (chunk and
// embed) from the SAME outer *testing.T, and newWASMProcessor's wazero module
// instantiation keys on that id: reusing newEmbeddingProcessor for both would
// give them the identical id (t.Name()) and the second instantiation fails
// with "module has already been instantiated".
func newRAGProcessor(ctx context.Context, t *testing.T, mod wazero.CompiledModule, policy egress.Policy, id string) *wasmProcessor {
	t.Helper()
	logger := log.Test(t)
	p, err := newWASMProcessor(ctx, TestRuntime, mod, CompiledHostModule, schema.NewInMemoryService(), policy, id, logger)
	if err != nil {
		t.Fatalf("instantiate %q wasm processor: %v", id, err)
	}
	return p
}

// chunkAndEmbed drives one source record through the REAL ai.chunk and
// ai.embed WASM guests in sequence, returning the records exactly as embed
// produced them — ready for the pgvector sink with NO reshaping bridge (see
// this file's header comment §3: conduit-processor-ai's embed now writes a
// native []any vector to its own default outputField, which is wire-
// compatible with pgvector's default vectorField as-is). A create/update
// record fans out to N chunk records then N embedded records; a delete
// tombstone (Operation == OperationDelete) fans out to exactly one
// delete-intent record that both stages pass through unembedded — the exact
// path the embed tombstone-passthrough fix (c54abb4) added.
func chunkAndEmbed(t *testing.T, ctx context.Context, chunkProc, embedProc *wasmProcessor, rec opencdc.Record) []opencdc.Record {
	t.Helper()

	chunked := chunkProc.Process(ctx, []opencdc.Record{rec})
	var toEmbed []opencdc.Record
	for _, cr := range chunked {
		switch v := cr.(type) {
		case sdk.SingleRecord:
			toEmbed = append(toEmbed, opencdc.Record(v))
		case sdk.MultiRecord:
			toEmbed = append(toEmbed, []opencdc.Record(v)...)
		default:
			t.Fatalf("chunk stage: unexpected processed record type %T (%+v)", cr, cr)
		}
	}

	embedded := embedProc.Process(ctx, toEmbed)
	out := make([]opencdc.Record, 0, len(embedded))
	for _, er := range embedded {
		single, ok := er.(sdk.SingleRecord)
		if !ok {
			t.Fatalf("embed stage: unexpected processed record type %T (%+v) — want sdk.SingleRecord", er, er)
		}
		out = append(out, opencdc.Record(single))
	}
	return out
}

// ollamaMockHandlerRequest mirrors conduit-processor-ai embed's
// ollamaEmbeddingRequest wire shape, duplicated deliberately (see
// embedding_bundle_e2e_test.go's identical comment on
// ollamaEmbeddingsHandlerRequest — this test only needs to decode the guest's
// request, not share code with it).
type ollamaMockHandlerRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// ragE2EVectorDim is the fixed embedding dimension the ollama mock server
// always returns, and the dimension pgvector's target table and config are
// set up with.
const ragE2EVectorDim = 8

// newOllamaMockServer returns an httptest server standing in for Ollama's
// POST /api/embeddings — the ONLY mocked boundary in this suite (see the
// package doc comment on faithfulness). It returns a fixed-dimension,
// per-prompt-length-salted vector so a wrong-record/wrong-vector mixup would
// be visible if it occurred, though this suite's assertions check row
// presence/absence and dimension, not exact vector equality.
func newOllamaMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	is := is.New(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaMockHandlerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vec := make([]float32, ragE2EVectorDim)
		vec[0] = float32(len(req.Prompt))
		for i := 1; i < ragE2EVectorDim; i++ {
			vec[i] = float32(i)
		}
		w.Header().Set("Content-Type", "application/json")
		is.NoErr(json.NewEncoder(w).Encode(map[string]any{"embedding": vec}))
	}))
}

// TestRAGPipeline_ChunkEmbedPgvector is the RAG-pipeline validation payoff:
// chunk -> embed -> pgvector proven end to end against real WASM guests, a
// real host-egress gate, and a real out-of-process pgvector connector talking
// to a real Postgres. See this file's header comment for the full set of
// design calls (placement, dependency direction, the embed/pgvector wire-shape
// bridge).
//
// Skips cleanly (never fails) when CONDUIT_PROCESSOR_AI_DIR,
// CONDUIT_CONNECTOR_PGVECTOR_DIR, or the rag_e2e Postgres are unavailable.
func TestRAGPipeline_ChunkEmbedPgvector(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)

	chunkWASM := buildChunkingWASM(ctx, t)
	embedWASM := buildEmbeddingWASM(ctx, t) // reused verbatim from embedding_bundle_e2e_test.go
	sinkBinPath := buildPgvectorConnectorBinary(ctx, t)

	pg := connectRAGPostgres(ctx, t)
	ensureVectorExtension(ctx, t, pg)
	table := randomRAGIdentifier(t)
	setupRAGVectorTable(ctx, t, pg, table, ragE2EVectorDim)

	srv := newOllamaMockServer(t)
	defer srv.Close()

	chunkMod, err := TestRuntime.CompileModule(ctx, chunkWASM)
	is.NoErr(err)
	defer func() { _ = chunkMod.Close(ctx) }()
	embedMod, err := TestRuntime.CompileModule(ctx, embedWASM)
	is.NoErr(err)
	defer func() { _ = embedMod.Close(ctx) }()

	// Chunk needs no egress at all (design doc: it performs no I/O) — a
	// deny-all zero-value policy proves that (any accidental egress attempt
	// would fail loudly rather than silently succeeding).
	chunkProc := newRAGProcessor(ctx, t, chunkMod, egress.Policy{}, "rag-e2e-chunk")
	is.NoErr(chunkProc.Configure(ctx, config.Config{
		"strategy":  "fixed_size",
		"chunkSize": "20",
		"overlap":   "0",
		// inputField reads this suite's synthetic source row's "content"
		// column (a stand-in for a real Postgres CDC column) — it isn't part
		// of the composable contract, unlike outputField below.
		"inputField": ".Payload.After.content",
		// outputField is left at its default (".Payload.After.text") — this
		// is the real contract under test: chunk's default output composes
		// with embed's default input with zero configuration (see this
		// file's header comment §3 and chunkAndEmbed).
	}))
	is.NoErr(chunkProc.Open(ctx))
	defer func() { is.NoErr(chunkProc.Teardown(ctx)) }()

	policy := allowPolicy(t, "http://"+hp(t, srv))
	embedProc := newRAGProcessor(ctx, t, embedMod, policy, "rag-e2e-embed")
	is.NoErr(embedProc.Configure(ctx, config.Config{
		"provider":       "ollama",
		"model":          "rag-e2e-model",
		"ollama.baseURL": srv.URL,
		"requestTimeout": "5s",
		"maxRetries":     "0",
		// inputField/outputField are left at their defaults
		// (".Payload.After.text" / ".Payload.After.vector") — matching
		// chunk's default output and pgvector's default vectorField
		// respectively, with zero field configuration between the three
		// components. This is the composable contract this e2e proves.
	}))
	is.NoErr(embedProc.Open(ctx))
	defer func() { is.NoErr(embedProc.Teardown(ctx)) }()

	connUtilsAddr := startRAGConnUtilsServer(t)
	sink := openPgvectorSink(ctx, t, sinkBinPath, connUtilsAddr, config.Config{
		"url":       ragE2EPGConnString,
		"table":     table,
		"dimension": strconv.Itoa(ragE2EVectorDim),
	})
	defer sink.teardown(t, ctx)

	t.Run("CreateThenDelete_AllVectorsLandThenAllVectorsRemoved", func(t *testing.T) {
		testRAGCreateThenDelete(t, ctx, pg, table, chunkProc, embedProc, sink)
	})
	t.Run("ReEmbedFewerChunksThenDelete_NoOrphans", func(t *testing.T) {
		testRAGReEmbedFewerChunksThenDelete(t, ctx, pg, table, chunkProc, embedProc, sink)
	})
}

// testRAGCreateThenDelete is the core payoff assertion: a source record
// chunks into 5 pieces, each gets a real embedding from the real (mocked
// transport, real code path) ollama provider, and all 5 land as rows in
// pgvector keyed by chunk_id and tagged with source_key "doc-1". Deleting the
// source record must then remove ALL 5 rows via pgvector's delete-by-
// source_key fan-out — and reaching the sink at all depends on the embed
// tombstone-passthrough fix: before that fix, the delete tombstone would
// resolve its (nonexistent) input field, become an sdk.ErrorRecord, and never
// reach pgvector — the assertion at the end of this test is exactly the one
// that fix made pass.
func testRAGCreateThenDelete(t *testing.T, ctx context.Context, pg *pgx.Conn, table string, chunkProc, embedProc *wasmProcessor, sink *pgvectorSink) {
	is := is.New(t)

	source := opencdc.Record{
		Operation: opencdc.OperationCreate,
		Key:       opencdc.RawData("doc-1"),
		Payload:   opencdc.Change{After: opencdc.StructuredData{"content": strings.Repeat("a", 100)}},
	}

	created := chunkAndEmbed(t, ctx, chunkProc, embedProc, source)
	is.Equal(len(created), 5) // 100 runes / chunkSize 20, no overlap => exactly 5 chunks
	sink.write(t, created)

	is.Equal(sourceKeyRowCount(ctx, t, pg, table, "doc-1"), 5)

	var ids []string
	rows, err := pg.Query(ctx, fmt.Sprintf(`SELECT id FROM %q WHERE source_key = $1 ORDER BY id`, table), "doc-1")
	is.NoErr(err)
	for rows.Next() {
		var id string
		is.NoErr(rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	is.NoErr(rows.Err())
	is.Equal(ids, []string{"doc-1:0", "doc-1:1", "doc-1:2", "doc-1:3", "doc-1:4"})

	// Every row's vector must actually be populated at the configured
	// dimension — not just present, but real.
	var nonNullVectors int
	is.NoErr(pg.QueryRow(ctx, fmt.Sprintf(
		`SELECT count(*) FROM %q WHERE source_key = $1 AND vector_dims(embedding) = $2`, table,
	),
		"doc-1", ragE2EVectorDim).Scan(&nonNullVectors))
	is.Equal(nonNullVectors, 5)

	// THE delete-path assertion: a tombstone for the source record must
	// remove every chunk row derived from it. Before the embed
	// tombstone-passthrough fix, this tombstone would have died as an
	// ErrorRecord at the embed stage and never reached this Write call at
	// all, leaving all 5 rows above orphaned forever.
	tombstone := opencdc.Record{Operation: opencdc.OperationDelete, Key: opencdc.RawData("doc-1")}
	deleteRecs := chunkAndEmbed(t, ctx, chunkProc, embedProc, tombstone)
	is.Equal(len(deleteRecs), 1)
	is.Equal(deleteRecs[0].Operation, opencdc.OperationDelete)
	is.Equal(deleteRecs[0].Metadata["ai.chunk.source_key"], "doc-1")
	sink.write(t, deleteRecs)

	is.Equal(sourceKeyRowCount(ctx, t, pg, table, "doc-1"), 0)
}

// testRAGReEmbedFewerChunksThenDelete is the design doc §5 property (the
// pgvector connector's own TestDestination_DeleteBySourceKey_RemovesAllChunksAcrossChunkCountChange)
// driven through the real chunk/embed/pgvector pipeline instead of a direct
// Destination.Write call: a source record first chunks into 5 pieces: an edit
// then shrinks it to 2 chunks (an update, upserting doc-2:0 and doc-2:1 in
// place — doc-2:2..4 are never re-emitted and would orphan without
// source_key-based delete fan-out). A final delete tombstone must remove
// every row for source_key "doc-2", including the 3 stale ones from the
// larger chunk count, proving no orphans survive the delete even when the
// chunk count changed since the rows were written.
func testRAGReEmbedFewerChunksThenDelete(t *testing.T, ctx context.Context, pg *pgx.Conn, table string, chunkProc, embedProc *wasmProcessor, sink *pgvectorSink) {
	is := is.New(t)

	first := opencdc.Record{
		Operation: opencdc.OperationCreate,
		Key:       opencdc.RawData("doc-2"),
		Payload:   opencdc.Change{After: opencdc.StructuredData{"content": strings.Repeat("a", 100)}},
	}
	firstRecs := chunkAndEmbed(t, ctx, chunkProc, embedProc, first)
	is.Equal(len(firstRecs), 5)
	sink.write(t, firstRecs)
	is.Equal(sourceKeyRowCount(ctx, t, pg, table, "doc-2"), 5)

	second := opencdc.Record{
		Operation: opencdc.OperationUpdate,
		Key:       opencdc.RawData("doc-2"),
		Payload:   opencdc.Change{After: opencdc.StructuredData{"content": strings.Repeat("b", 40)}},
	}
	secondRecs := chunkAndEmbed(t, ctx, chunkProc, embedProc, second)
	is.Equal(len(secondRecs), 2) // 40 runes / chunkSize 20 => exactly 2 chunks
	sink.write(t, secondRecs)

	// doc-2:0 and doc-2:1 were just updated in place; doc-2:2..4 are stale
	// survivors from the first (larger) chunk count — still present, not yet
	// orphan-swept (pgvector's upsert never deletes rows on its own).
	is.Equal(sourceKeyRowCount(ctx, t, pg, table, "doc-2"), 5)

	tombstone := opencdc.Record{Operation: opencdc.OperationDelete, Key: opencdc.RawData("doc-2")}
	deleteRecs := chunkAndEmbed(t, ctx, chunkProc, embedProc, tombstone)
	is.Equal(len(deleteRecs), 1)
	sink.write(t, deleteRecs)

	// Every row for source_key "doc-2" is gone, including the 3 stale rows
	// from the chunk count that has since changed — the property a
	// chunk_id-guessing delete could not provide.
	is.Equal(sourceKeyRowCount(ctx, t, pg, table, "doc-2"), 0)
}
