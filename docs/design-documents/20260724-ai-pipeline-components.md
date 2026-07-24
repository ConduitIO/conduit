# AI-pipeline components: chunking, embedding, and vector destinations

## Summary

This is the gating design doc for v0.20 Workstream 8 (`v020-execution-plan.md`, "AI-pipeline
components"): a **new subsystem**, in **new dedicated repos**, that turns Conduit into the data
layer for RAG applications. The canonical pipeline is **CDC → chunk → embed → vector store** —
already-shipped Postgres CDC feeding two new processor types (chunking, embedding) and a new
family of vector-store destinations (pgvector first; Qdrant, Pinecone, Turbopuffer fast-follow per
`ROADMAP.md`).

**No code merges until this doc has DeVaris's sign-off** (v0.20 plan, WS8 acceptance criterion 1).
Embedding processors and vector destinations sit **on the record path** — every record that is
chunked, embedded, or upserted is subject to invariants 1–7 — so this is **Tier-1-adjacent**
despite living outside the core engine, and it lands **behind WS0 (chaos-CI gate) and WS6 (DBZ-2
correctness suite)** per the v0.20 phased sequence.

**New repos this doc names and justifies:**

| Repo | Contents | SDK / transport | Why a new repo |
| --- | --- | --- | --- |
| `conduit-processor-ai` | Chunking processor + embedding processor | `conduit-processor-sdk` (WASM/wazero, WASI Preview 1), plus one small new host capability (§1) | `conduit-processor-sdk` is Go/WASM-only and these are the first processors needing outbound network access — they don't belong compiled into the core engine (`pkg/plugin/processor/builtin`), and the SDK repo itself ships no processor implementations today |
| `conduit-connector-pgvector` | pgvector destination (v0.20) | `conduit-connector-sdk` (Go, gRPC-standalone) | Same convention as every other target-system connector (`conduit-connector-postgres`, `-s3`, `-kafka`) — one repo per destination system, not one repo per "vector store" concept |
| `conduit-connector-qdrant` | Qdrant destination (fast-follow, contingency valve) | `conduit-connector-sdk` | Same convention; explicitly the item that slips to v0.21 if Phase C compresses (v0.20 plan, contingency drop order #2) |

Pinecone and Turbopuffer (`ROADMAP.md` line 207) are named in the roadmap but **out of scope for
this doc and for v0.20** — no design work is done on them here; a future connector repo follows the
same pattern once pgvector and Qdrant have proven it out.

**Embedding-provider decision (the `generate`-level-rigor item):** three pluggable adapters —
**OpenAI**, **Voyage AI**, and **local via Ollama** — resolved the same deterministic way
`conduit generate`'s provider resolution works (explicit flag/config → env → auto-detect exactly
one candidate → refuse on zero or ambiguous), with the same zero-new-dependency discipline. Detail
in Decision §2.

**The load-bearing engineering call this doc makes, stated up front:** embedding calls need
outbound HTTP, but the only processor runtime `conduit-processor-sdk` ships today is WASI Preview 1
over wazero, which has **no socket API**. Rather than inventing a second processor transport (a
gRPC-standalone processor plugin — real, but out of scope for one release) or compiling these into
the core engine as built-ins (ruled out — DeVaris's "new dedicated repos" call), this doc extends the
**existing, precedented, host-mediated capability pattern** processors already use for schema
lookups (`pprocutils.SchemaService`) with one new capability: a bounded, allowlisted, host-executed
HTTP call. See Decision §1 — this is the single core-engine-adjacent change WS8 requires, and it
is called out as its own review item in Open Questions.

## Context

### What already exists and what this doc reuses (verified against the tree)

- **`conduit-processor-sdk` is WASM/WASI-P1-only for standalone processors, and built-in-only for
  anything needing full Go stdlib.** `pkg/plugin/processor/standalone/registry.go` runs guest
  modules under `wazero` + `wasi_snapshot_preview1` — WASI Preview 1 has no sockets API. The two
  processors that need outbound HTTP today (`pkg/plugin/processor/builtin/impl/openai`,
  `.../ollama`) are **built-in** — compiled directly into the Conduit binary, with full `net/http`
  access — precisely because WASM standalone gives them no path to the network. That is the same
  wall an embedding processor hits, and CLAUDE.md's "new dedicated repos" call (v0.20 plan, WS8)
  rules out the built-in escape hatch for this subsystem.
- **A host-mediated capability channel already exists and is precedent, not a new mechanism.**
  `pprocutils.SchemaService` (`conduit-processor-sdk/pprocutils`, wired through
  `pkg/plugin/processor/standalone/host_module.go`) lets a WASM guest processor issue a
  command-request the **host** executes (a schema-registry lookup) and returns a response over the
  existing command-request/response channel — the guest never gets raw sockets, the host performs
  the actual I/O and hands back a typed result. This is exactly the shape needed for outbound HTTP:
  extend the same channel with one more host-executed capability instead of granting WASI-P1
  sockets or inventing a second plugin transport.
- **gRPC standalone (HashiCorp go-plugin) is the primary any-language plugin architecture going
  forward** per the (pending-ratification) ADR `20260722-wasm-component-model-deferred.md`, but
  **only for connectors today** — no gRPC-standalone processor runtime is shipped or scaffolded
  anywhere in the tree (verified: `pkg/plugin/processor` has exactly two subtrees, `standalone`
  [WASM] and `builtin` [compiled-in]; no third transport). Building one would be new core-engine
  engine capability, is not named as v0.20 scope anywhere in the execution plan, and is
  out of scope for this doc — see Alternatives §1.
- **The `generate` design doc already set the provider-decision bar this doc must match**
  (`20260722-conduit-generate.md`, Decision §1): a small `Provider` interface, deterministic
  resolution order (explicit → env → auto-detect-exactly-one → refuse-on-ambiguity), zero-new-
  dependency adapters (reuse the vendored OpenAI client; hand-roll thin `net/http` JSON clients for
  everything without an official/needed Go SDK), and an explicit "no cost/latency numbers without a
  benchi-class benchmark" non-goal. This doc's embedding-provider decision (§2) follows the same
  shape, in a different repo.
- **The templates gallery's built-in-only constraint does not cover the RAG template.**
  `20260723-templates-gallery.md` §2 restricts its vendored, `go:embed`-based MVP to
  `builtin.DefaultBuiltinConnectors` only, and §7 explicitly names "a future template needing a
  non-built-in connector" as a case requiring **registry-backed** template distribution — flagged in
  that doc as "gated on the registry MVP... an additive Phase-2 breadth layer." The RAG-sync
  template (chunk/embed processors + pgvector destination, none built-in) is the **first** template
  to actually need that breadth layer. This doc's §8 designs it as a registry-install-backed
  template, not a vendored one, and flags the templates-gallery CI rule that needs an explicit,
  scoped exception (Open Questions).
- **The connector registry's install path already exists** (`20260713-connector-registry-mvp.md`,
  shipped v0.18): `conduit connectors install <name>@<version>` resolves and installs a signed
  connector from the index. This is the mechanism the RAG template's preflight step uses to fetch
  `conduit-connector-pgvector` and the `conduit-processor-ai` processors — no new install machinery
  is designed here.
- **A processor is a synchronous batch call, `Process(records) -> records`**
  (`20260722-wasm-component-model-deferred.md`, Context) — this is exactly why processors run on
  WASM cleanly today. It matters directly for ack correctness (Decision §7): a processor that
  blocks or retries internally already gets "no early ack" for free, because the engine does not
  advance past a `Process` call that has not returned.
- **`conduit-connector-postgres`, `-s3`, `-kafka` are separate repos per target system**, each with
  its own acceptance-test suite against `conduit-connector-sdk`'s `AcceptanceTest` harness
  (referenced by the Rust SDK design doc, `20260722-rust-connector-sdk.md`, Context). The
  per-system-repo convention this doc follows for `conduit-connector-pgvector`/`-qdrant` is not new;
  it is the existing pattern applied to a new system family.

### Why this needs a design doc before code

New subsystem (CLAUDE.md: "adds a subsystem" is non-trivial by definition), touching a public
contract in three places — a new processor-to-host capability surface (§1), a new provider-config
shape mirroring `generate`'s (§2), and a new connector-family convention for vector stores (§5) —
plus it sits on the record path, so invariants 1–7 apply throughout. Per the v0.20 plan, this is
one of five Tier-1/Tier-1-adjacent sign-offs and is explicitly sequenced in Phase A (design-doc
sign-off) before any Phase C implementation.

## Goals / Non-goals

### Goals

- Ship a **chunking processor** (`conduit-processor-ai`) implementing document-splitting
  strategies for RAG: fixed-size, sentence-boundary, and recursive/structural splitting.
- Ship an **embedding processor** (`conduit-processor-ai`) with a **pluggable provider** (OpenAI,
  Voyage, local-via-Ollama), batching, rate-limit handling, and per-record token-cost metadata.
- Ship a **pgvector destination** (`conduit-connector-pgvector`) with upsert semantics, metadata
  mapping, and dimension validation at pipeline start.
- Name and scope (not build) the Qdrant fast-follow (`conduit-connector-qdrant`).
- Ship a working **"keep your RAG index fresh from Postgres" quickstart + template**: Postgres CDC
  → chunk → embed → pgvector, CI-tested end to end with records asserted at the vector store.
- Hold every record-path decision to invariants 1–7: no early ack, no silent drop, idempotent
  upsert under retry, bounded buffering under backpressure, and no silent schema/dimension
  mismatch.
- Match the `generate` design doc's rigor on the provider decision: pluggable interface,
  deterministic resolution, zero-new-dependency justification per adapter, cost/latency **shape**
  documented (not benchmarked numbers, per CLAUDE.md's no-unbenchmarked-performance-claims rule).

### Non-goals

- **Not a general-purpose vector-store abstraction layer.** Each destination is its own connector
  against its own repo, following the existing per-system-connector convention — no shared
  "VectorStore" Go interface spanning repos (see Alternatives §2 for why a single multi-backend
  connector was rejected).
- **Not fine-tuning or training infrastructure.** Embedding providers are used as hosted/local
  inference only, exactly like `generate`'s LLM providers.
- **Not a re-ranking, retrieval, or query-time RAG layer.** This subsystem is the **write path**
  into a vector store — keeping an index fresh. Query-time retrieval is the RAG application's own
  concern, entirely outside Conduit.
- **Not a generic outbound-HTTP capability for arbitrary WASM processors.** The host capability
  added in §1 is scoped, allowlisted per pipeline config, and justified specifically by the
  embedding use case — it is not offered as a general escape hatch from the WASM sandbox.
- **No performance/cost numbers without a benchi-class benchmark**, mirroring `generate`'s
  Non-goals verbatim: token-cost-per-record and provider latency **shape** (what drives it, how to
  bound it) are documented; specific millisecond/dollar figures are not asserted here.
- **Qdrant, Pinecone, Turbopuffer implementations are out of scope for this doc.** Qdrant is named
  as the fast-follow connector repo; Pinecone/Turbopuffer are named in `ROADMAP.md` only.
- **No changes to `conduit-connector-protocol` or `conduit-processor-protocol`.** The host-capability
  addition in §1 is additive to the existing WASM host-module surface, not a protocol-version
  change.

## Decision

### 1. New host capability: bounded, allowlisted outbound HTTP for WASM processors

**The problem, precisely stated.** `conduit-processor-sdk` standalone processors run under wazero +
WASI Preview 1, which has no socket API. An embedding processor must call an HTTP API (OpenAI,
Voyage) or a local Ollama server. Chunking needs no network at all and is unaffected.

**The decision:** extend the existing host-mediated capability channel
(`pprocutils.SchemaService`'s command-request/response pattern) with one new capability,
`pprocutils.HTTPService` (naming illustrative — the exact interface is a build-time decision, not
frozen by this doc):

```go
// Illustrative shape — the exact signature is an implementation-time decision.
// The load-bearing properties this doc DOES fix: the host performs the I/O, the
// guest never gets a socket, and every call is bound by an explicit allowlist +
// timeout + response-size cap the *pipeline config* sets, never the guest.
type HTTPService interface {
    Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

type HTTPRequest struct {
    Method  string
    URL     string // validated against the pipeline's configured allowlist before dispatch
    Headers map[string]string
    Body    []byte
}
```

**Why this is the right shape, not a workaround:**

- **The guest never gets a raw socket.** The host (full Go, `net/http`) performs the actual
  request; the guest only ever sees a request/response pair over the same command channel that
  already carries schema-lookup calls. This is a strictly _smaller_ new surface than granting WASI
  sockets, and it keeps every other processor's sandbox boundary completely unchanged.
- **The allowlist is pipeline config, not processor config** — the host validates the destination
  host against a list the pipeline author (not the processor, not the model, not any
  provider-returned redirect) set at deploy time. A processor cannot request a URL outside the
  allowlist; the call fails closed (`ai.embedding_host_not_allowed`, §9) rather than silently
  following a redirect or a provider-returned URL. This is the SSRF mitigation — see Failure Modes
  §5.
- **Bounded by construction:** a per-call timeout and a maximum response-body size are host-enforced
  parameters (not guest-requestable), so a slow or malicious response cannot hang a `Process` call
  indefinitely or exhaust host memory reading an unbounded body.
- **This is core-engine-adjacent, not core-engine-owned.** The new capability lives in
  `conduit-processor-sdk`'s `pprocutils` package and its host-side wiring in
  `pkg/plugin/processor/standalone/host_module.go` — a small, additive change to an existing,
  already-reviewed capability-broker pattern, not a new plugin transport or protocol rev. It still
  touches the core engine, so it needs its own scoped review before `conduit-processor-ai`'s embedding
  processor can be built against it — flagged explicitly in Open Questions, not glossed over as
  "just a new repo's problem."

### 2. Embedding-provider decision — held to `generate`'s bar

**Providers:** `openai`, `voyage`, `ollama` (local). Resolution mirrors `generate`'s exactly:
explicit (`--provider` / pipeline config) → env (`CONDUIT_EMBED_PROVIDER`) → auto-detect exactly
one resolvable candidate (checked in the same fixed order: `ANTHROPIC`-equivalent hosted keys
first for reporting purposes only, then Ollama reachability) → refuse on zero or ambiguous
candidates with a coded, actionable error. No hardcoded default vendor, for the same
broker-neutrality reason `generate` gives.

| Provider | Implementation | Dependency | No-new-dep justification |
| --- | --- | --- | --- |
| `openai` | Host-side call to OpenAI's Embeddings API (`POST /v1/embeddings`), reusing the **same already-vendored** `github.com/sashabaranov/go-openai` client the `openai` built-in processor and `generate`'s OpenAI adapter already use. | Zero new (in this new repo's own module — but the same dependency already proven elsewhere in the org). | Reuses a maintained client already trusted for the equivalent completions API; adding a second OpenAI HTTP client would be the YAGNI violation. |
| `voyage` | Host-side, hand-rolled `net/http` JSON client against Voyage's Embeddings API (`POST /v1/embeddings`, `Authorization: Bearer` header) — no official Go SDK exists, and none is needed for one JSON-in/JSON-out endpoint. | Zero new beyond stdlib. | Mirrors `generate`'s Anthropic adapter precedent exactly: a simple single-endpoint JSON API doesn't justify a dependency. |
| `ollama` (local) | Host-side, hand-rolled `net/http` JSON client against a local Ollama server's `/api/embeddings` endpoint (e.g. `nomic-embed-text`), **the same shape as the existing `ollama` built-in processor's hand-rolled client** — copy the precedent, don't fork a second one. | Zero new beyond stdlib. | Identical justification to `generate`'s Ollama adapter: this is the zero-API-key, 5-minute-wow local path, and the tree already has a working precedent for exactly this shape. |

**Why these three, and why no in-WASM local model runtime.** A fourth option — running a local
embedding model directly inside the WASM guest (an ONNX/GGML runtime compiled to WASI) — is
rejected: it would pull a heavy runtime dependency into `conduit-processor-ai`'s own module, is
unproven under wazero's WASI-P1-only support, and duplicates work Ollama already does well. Ollama
already is Conduit's established "local, zero-key" precedent (`generate`'s Decision §1); reusing it
here is the same choice generate made, made consistently.

**Batching.** Each provider's embeddings endpoint accepts a batch of input texts in one call
(OpenAI, Voyage) or one call per text (Ollama's `/api/embeddings` today takes a single input) — the
processor batches N chunked-text records into as few host-side HTTP calls as the provider's batch
API supports, reducing per-record HTTP round-trip overhead and, for token-priced providers,
per-request overhead. Batch size is a config knob (default proposed: 96, OpenAI's practical batch
ceiling for typical embedding models — an implementation-time constant, not frozen here) bounded so
a single batch's request body stays under the host's response-size/time budget (§1).

**Rate-limit handling.** A 429 (or provider-specific rate-limit signal) triggers host-side
exponential backoff honoring a `Retry-After` header when present, bounded by a max-retry count
per batch. Exhausting retries surfaces `ai.embedding_provider_error` (§9) for that batch — the
batch's records are **not** silently dropped or acked; see Decision §7 and Failure Modes §1.

**Token-cost-per-record docs.** Every provider adapter attaches `tokens_used` (when the provider
reports it — OpenAI and Voyage both return usage in the response; never estimated when not
reported) to each embedded record's metadata, and the processor's cookbook doc ships a worked
example: "N chunked records of ~500 characters each ≈ M tokens per record at this provider's
published per-token/per-million-token rate" — citing the provider's own published pricing at time
of writing, explicitly caveated as "verify current pricing against the provider" rather than a
Conduit-benchmarked number. This is the same "bounded, not quantified" stance `generate` took on
cost (Non-goals) — a Conduit-asserted latency/throughput number requires benchi; a provider's own
published price list does not, and citing it plainly is exactly the "tokens-per-record cost docs"
CLAUDE.md's AI-pipeline workflow section requires.

**Per-call timeout.** Each host-executed HTTP call runs under a default per-attempt deadline
(proposed 30s, overridable), the same shape as `generate`'s per-attempt provider timeout — a hung
connection fails deterministically to a coded error rather than wedging the pipeline.

### 3. Chunking processor

Standalone WASM processor in `conduit-processor-ai`, using `conduit-processor-sdk`'s existing
`Process(records) -> records` interface (no new host capability needed — pure in-memory text
processing).

- **Strategies (config-selected):** `fixed_size` (character/token count with configurable overlap),
  `sentence` (split on sentence boundaries via a simple, dependency-free heuristic — not a full NLP
  library, consistent with the no-new-deps discipline), `recursive` (try progressively finer
  separators — paragraph → sentence → word — until each chunk fits a target size, the common RAG
  "recursive character splitter" pattern).
- **Fan-out shape:** one input record can produce **N output records** (one per chunk). Each output
  record carries: a stable `chunk_id` (`{source_record_key}:{chunk_index}`), the chunk text as
  payload, and metadata linking back to the source record's key/position and the chunk's
  offset/length in the original document — this linkage is what makes the embedding processor's
  and the vector destination's idempotency keys derivable and stable across retries (§6).
- **Tombstones pass through unchanged** — a delete of the source record does not need chunking; it
  fans out to a deletion signal per previously-chunked ID (§7, the vector-destination delete path)
  rather than being chunked itself. The chunking processor is responsible for recognizing a
  tombstone and re-emitting the delete intent tagged with enough information (the source key) for
  the embedding processor to pass through and the vector destination to resolve into per-chunk
  deletes — see Decision §5's delete-fan-out note, since a single source row may have produced
  multiple chunk rows that all need deleting.

### 4. Embedding processor

Also in `conduit-processor-ai`, standalone WASM, using the new host capability (§1) for the actual
provider call.

- **Input:** chunked records (from the chunking processor, or any upstream processor emitting
  compatible chunk records — the two are pipeline-composed, not hard-wired to each other).
- **Batching:** accumulates records up to the configured batch size or a max-wait duration
  (bounded — never unbounded, §7), then issues one host-mediated HTTP call per batch per §2.
- **Partial-batch failure handling (the invariant-1/3 case):** if a batch call fails entirely, no
  record in that batch is embedded or acked — the whole `Process` call returns an error for that
  batch, which (per the synchronous `Process(records) -> records` semantics) means the engine does
  not advance those records past this processor; they are retried or routed to the pipeline's DLQ
  per its configured processor-error policy, **never** partially embedded with a placeholder vector
  and passed through. If the provider's response indicates some records in a batch succeeded and
  others failed (a real API shape for e.g. one malformed input in an otherwise-good batch), the
  processor must **not** widen success to the whole batch or silently drop the failed ones — the
  per-record result is honored 1:1, succeeded records proceed with their embedding, failed ones are
  reported as `Process`-level per-record errors so the engine's existing per-record
  error-routing (DLQ) applies, exactly as it already does for any other processor error.
- **Output:** the embedding vector (as the record payload's structured field), the model/provider
  name, dimension, and `tokens_used` attached as metadata — the vector destination validates
  dimension against this (§5).

### 5. Vector destination: pgvector (first), Qdrant (fast-follow)

`conduit-connector-pgvector`, a standard Destination connector against `conduit-connector-sdk`
(Go, gRPC-standalone) — no new SDK, no new transport, the existing connector pattern.

- **Upsert semantics.** `INSERT ... ON CONFLICT (id) DO UPDATE SET embedding = ..., metadata = ...`
  keyed by the chunk's stable `chunk_id` (§3) — never a bare `INSERT`, so a re-delivered record
  (at-least-once redelivery, invariant 3) converges to the same row rather than duplicating it. See
  §6 for the idempotency argument in full.
- **Metadata mapping.** Record metadata (source table/key, chunk offset, embedding model/provider,
  arbitrary user-configured passthrough fields) maps to a `JSONB` column alongside the `vector`
  column — configurable field selection, documented in the connector's README per the existing
  connector-doc convention (config reference, delivery-semantics notes, runnable example).
- **Dimension validation at pipeline start — fail fast, actionable error.** On `Configure`/`Open`,
  the connector reads the target table's `vector` column dimension (pgvector exposes this via
  `atttypmod` on the column) and compares it against the configured/discovered embedding dimension
  (from the upstream embedding processor's declared model, or an explicit config override). A
  mismatch is a **coded, actionable startup error** (`ai.vector_dimension_mismatch`, §9) naming both
  the table's dimension and the configured embedding model's dimension — never a silent runtime
  failure on the first upsert, and never a silent truncation/padding of the vector to fit.
- **Deletes / tombstones.** A tombstone (or the chunking processor's re-emitted delete intent, §3)
  resolves to `DELETE FROM ... WHERE id = ...` for **every** `chunk_id` previously derived from that
  source record. Since chunk count can vary per source record (a document that shrinks on update
  produces fewer chunks than before), the destination cannot assume a 1:1 chunk-to-source mapping at
  delete time — it deletes by matching a `source_key` metadata column (not just literal `chunk_id`
  guesses), so a source-record delete removes **all** chunks ever derived from it, including ones
  from a chunk count that has since changed. This is stated explicitly because "delete the chunk
  with today's expected ID" would silently leave orphaned rows from a prior chunking of the same
  source record — a real correctness gap this design closes by matching on `source_key`, not
  `chunk_id` alone.
- **Qdrant fast-follow** follows the identical shape (upsert-by-ID via Qdrant's native upsert API,
  dimension validated against the target collection's configured vector size at connector startup,
  metadata as Qdrant's payload) — named here as the contingency-valve item, not designed in depth in
  this doc; its own README/acceptance suite lands with its own PR when built.

### 6. Upsert idempotency under retry — the invariant-1/3 story for the sink

**The property this doc requires, stated as a test:** embedding the same source chunk twice (a
retry after a crash, a re-delivered record after an ack was lost upstream, or a duplicate from an
at-least-once redelivery) and upserting both results into pgvector must leave the table in **the
same state** as if it had been upserted once — same row, same vector, same metadata, no duplicate
row, no half-written column set.

This holds because:

1. **The chunk_id is derived deterministically from source data**, not generated fresh per attempt
   (`{source_record_key}:{chunk_index}`, §3) — two attempts at chunking the same source record
   produce the same IDs, not two different random IDs.
2. **The write is a single-statement upsert** (`ON CONFLICT ... DO UPDATE`), not a
   read-then-write — there is no window where a retry's write could race a concurrent write to the
   same row and leave a partially-applied column set; Postgres's own `ON CONFLICT` atomicity is the
   enforcement point, not application-level locking.
3. **A retried batch re-embeds and re-upserts the full record**, never a partial column update — if
   the embedding call is retried, the retry produces a complete new vector + metadata pair and the
   upsert replaces the whole row's `embedding`/`metadata` columns together, so there's no scenario
   where an old vector persists alongside new metadata (or vice versa) from two different attempts.

The required test (Testing section) is: SIGKILL the pipeline mid-upsert (after the embedding call
succeeded, mid-write to pgvector), restart, and verify the resumed pipeline's re-processing of the
same source record converges to one correct row, not two or a corrupted one.

### 7. Token-cost backpressure — the invariant-1/3 story for a slow/expensive embedder

**The failure this must not produce:** a source (CDC) that produces records faster than the
embedding provider can process them must never cause Conduit to (a) ack source records before they
are durably embedded and upserted, or (b) buffer unboundedly in memory, or (c) silently drop
records to keep up.

This holds by construction, not by a new mechanism this doc invents:

- **The processor's `Process` call is synchronous.** The embedding processor's batching logic
  accumulates up to a **bounded** number of records (the configured batch size) before issuing a
  call; it does not accept records 1002 while still holding 1000 unembedded ones in an unbounded
  internal queue. Once a batch is full (or a bounded max-wait elapses), `Process` is called, and the
  call blocks the pipeline's forward progress until it returns — this is the engine's existing
  backpressure mechanism, inherited for free, not a new one built here.
- **No early ack.** Because acking is the engine's job after the full pipeline stage completes for a
  record, and the embedding processor's `Process` call does not return until the batch is embedded
  (or definitively failed, §4), a slow provider means the **source** slows down (backpressure
  propagates upstream through the existing synchronous pipeline), not that records get acked while
  still waiting to be embedded.
- **The cost signal is surfaced, not silently accrued.** Every embedded record's `tokens_used`
  metadata (§2) is available to `conduit pipeline inspect` and the pipeline's metrics endpoint (§9),
  so a pipeline that is accumulating cost faster than expected is observable in near-real-time, not
  discovered at the end of a billing cycle.
- **Bounded batch size is a hard config ceiling**, not an adaptive/unbounded buffer — this is the
  concrete mechanism that keeps "backpressure" from silently becoming "unbounded memory growth" if a
  provider goes slow rather than fully down. A provider that is up but slow causes the batch
  interval to stretch (bounded max-wait, still bounded queue depth), not the queue to grow past its
  configured cap.

### 8. The RAG-sync template — a registry-backed template, not a vendored one

The templates gallery's shipped MVP (`20260723-templates-gallery.md`) restricts its vendored,
`go:embed` mechanism to built-in connectors only, and explicitly names registry-backed template
distribution as the extension point for exactly this case (its §7, "a future template author
proposes one needing a non-built-in connector"). The RAG-sync template — Postgres CDC → chunk →
embed → pgvector — needs `conduit-processor-ai`'s two processors and
`conduit-connector-pgvector`, **none built in**. This doc's design:

- The template scaffolds a pipeline YAML **plus** a documented preflight step:
  `conduit connectors install pgvector@<version>` and the equivalent processor-install command for
  `conduit-processor-ai`'s two processors — using the registry install path that already ships
  (v0.18), not new install machinery.
- `conduit pipelines init --template postgres-pgvector-rag` (the exact name `ROADMAP.md` line 143
  already commits to) emits the pipeline YAML **and** a compatibility/prerequisite note (mirroring
  `conduit migrate kafka-connect`'s "never silently drop config it can't translate" spirit, applied
  here as "never silently assume a plugin is present") naming exactly which connectors/processors
  must be installed first, with the install commands spelled out — never a bare YAML that fails
  opaquely on first `conduit run` because a plugin isn't present.
- **This requires an explicit, scoped exception to the templates-gallery's existing CI
  enforcement** ("templates directory... must use only `builtin.DefaultBuiltinConnectors`",
  §7 of that doc) — the enforcement check needs to allow this one template's declared non-built-in
  dependencies while continuing to reject any other template that tries the same without going
  through this registry-backed path deliberately. This is flagged in Open Questions, not resolved
  unilaterally here, because it's a rule change to a different, already-shipped subsystem.
- **CI-tested end to end**, matching every other template's bar: docker-compose brings up Postgres
  and a pgvector-enabled Postgres instance (or the same instance, if pgvector is the target),
  records are asserted to actually land in the vector table with the expected dimension — "YAML
  parses" is explicitly not sufficient, per the templates-gallery convention this doc inherits.

## Alternatives considered

**§1 — where embedding processors run (built-in vs. new gRPC-standalone processor transport vs.
WASM + host-capability extension, chosen):**

- **Compile into the core engine as built-ins**, like `openai`/`ollama` today. Rejected: directly
  contradicts DeVaris's explicit "new dedicated repos" call for this subsystem (v0.20 plan, WS8) —
  it would also mean vendoring embedding-provider client code into the core engine, coupling the core
  binary's dependency surface to AI-pipeline-specific vendors, exactly the coupling "new dedicated
  repos" is meant to avoid.
- **Build a new gRPC-standalone processor plugin transport** (mirroring how connectors already work
  over HashiCorp go-plugin), giving processors full-Go-stdlib network access as a subprocess.
  Rejected for v0.20, not forever: this is genuinely the direction the pending WASM-deferred ADR
  gestures at ("gRPC standalone... for both connectors and processors") but it does not exist today,
  is not named as v0.20 scope in the execution plan, and building a new plugin transport is itself
  a non-trivial, protocol-adjacent core-engine change that would need its own design doc and
  Tier-1 review — stacking that inside WS8 would blow the release's already-tight Tier-1
  sign-off spacing (five sign-offs is already the stated maximum the ~8–10-week calendar was
  reframed to fit). Worth revisiting once a second processor family needs the same network access
  and the transport's cost is amortized across more than one use case.
- **Extend the existing host-capability channel with a bounded, allowlisted HTTP call (chosen).**
  Smallest possible new surface: reuses a pattern already reviewed and shipped
  (`pprocutils.SchemaService`), adds no new plugin transport, keeps the WASM sandbox boundary intact
  (guest never gets a socket), and is scoped narrowly enough that its own review (Open Questions) is
  proportionate to what it actually changes.

**§2 — one multi-backend vector connector vs. per-store connector repos (chosen: per-store):**

- **A single `conduit-connector-vector` repo with a pluggable backend** (pgvector/Qdrant/Pinecone
  selected by config, one Go module importing all three vendor clients). Rejected: every existing
  connector in the org is one repo per target system (`conduit-connector-postgres`, `-s3`, `-kafka`)
  — a single repo importing three unrelated vendor SDKs (a Postgres driver, Qdrant's client, a
  Pinecone client) bloats every user's binary/build with dependencies for backends they don't use,
  and couples three independent release cadences (a Pinecone API change forcing a rebuild of the
  pgvector destination) for no shared benefit — the three destinations share a _design pattern_
  (upsert + dimension validation), not implementation.
- **Per-store connector repos (chosen)**, each a normal `conduit-connector-sdk` consumer with its
  own acceptance suite and release cadence, following the existing convention exactly. The shared
  design pattern (upsert semantics, dimension validation, metadata mapping) is documented here and
  in each connector's README, not enforced via a shared Go interface across repos — consistent with
  "no speculative generality... interfaces earn their existence with two real implementations,"
  and even with two implementations (pgvector + Qdrant), the actual code shared (SQL vs. a REST
  client) is near zero, so a shared interface would buy nothing beyond docs, which this doc already
  provides directly.

**§3 — local embedding: delegate to Ollama vs. in-WASM model runtime (chosen: delegate):**

Covered in Decision §2 — an in-WASM ONNX/GGML runtime is rejected as unproven under WASI-P1,
dependency-heavy, and duplicative of Ollama's already-established role as Conduit's local-inference
answer.

**§4 — retrieval/query-time RAG features in scope vs. write-path only (chosen: write-path only):**

Rejected adding any retrieval/query surface to this subsystem: the roadmap's own framing
("keep your RAG index fresh from Postgres") is a write-path problem, and a query-time layer would
be a different product surface (closer to an application concern) with its own users, its own
latency/availability bar, and no natural fit with Conduit's connector/processor model. Keeping this
subsystem strictly to "get records into the vector store correctly" keeps its blast radius — and
its Tier-1-adjacent review surface — bounded to what the invariants already cover.

## Failure modes

Per CLAUDE.md's "think in failure modes first," mapped to the invariants:

1. **Embedding provider down, rate-limited, or erroring mid-batch (invariants 1, 3).** No record in
   an unsuccessful batch is embedded, upserted, or acked. Bounded retry with backoff (§2); exhausted
   retries route the batch's records through the pipeline's existing DLQ/error policy, never a
   silent drop. A provider auth failure (bad API key) is a coded, immediate, non-retried error
   (`ai.embedding_provider_error`, distinguishable from a transient rate-limit) so a misconfigured
   pipeline fails fast rather than burning through the retry budget on every batch.
2. **Upsert idempotency under retry (invariants 1, 3).** Covered in full in Decision §6 — the
   deterministic `chunk_id` + single-statement `ON CONFLICT` upsert converges under redelivery; the
   required kill-mid-write recovery test is named there and in Testing.
3. **Dimension validation at pipeline start (invariant 6).** Covered in Decision §5 — a mismatch
   between the configured embedding model's dimension and the target table/collection's actual
   vector dimension is a coded, actionable **startup** failure, never a silent per-row failure
   discovered mid-run, and never silent truncation/padding of a vector to force-fit a mismatched
   column.
4. **Token-cost backpressure (invariants 1, 3, 5).** Covered in full in Decision §7 — bounded batch
   size and synchronous `Process` semantics mean a slow provider backpressures the source rather
   than silently dropping records or growing memory unboundedly; the cost signal is observable via
   metadata/metrics, not silently accrued.
5. **The new HTTP host capability as an SSRF/egress-abuse vector (invariant-adjacent: this is a new
   security boundary, not a data-loss one, but it sits in the same record-processing code path).** A
   compromised or misconfigured processor config could attempt to point the "embedding provider" URL
   at an internal service. Mitigated by the host-enforced, pipeline-config-level allowlist (§1) —
   the processor cannot request a URL outside what the pipeline author configured, and the host
   rejects (not silently redirects away from) an out-of-allowlist request. Redirects returned by an
   allowlisted server to a non-allowlisted target are **not followed** by the host client — treated
   as a failed call, not silently resolved, closing the classic SSRF-via-redirect gap.
6. **Metadata mapping mismatch** (a configured metadata field doesn't exist on the record, or a type
   mismatch between record metadata and the destination's JSONB expectations). Per invariant 6
   (schema handling never silently mangles data): a missing configured field is a coded config-time
   validation error where the field name is statically known, or a per-record error routed to DLQ
   where it can only be detected per-record (e.g. a dynamic field reference) — never silently
   omitted or coerced.
7. **Tombstones / deletes → vector deletes (invariants 1, 3, 4).** Covered in Decision §5's delete
   note: deletes match on `source_key` metadata, not a guessed `chunk_id` set, so a source record
   whose chunk count changed between updates does not leave orphaned vector rows. A delete for a
   source record with no matching rows (already deleted, or never successfully embedded) is a
   no-op, not an error — at-least-once delivery means a delete can be redelivered.

## Observability

- **Tokens/cost per record**: `tokens_used`, provider, and model are attached to every embedded
  record's metadata (§2) and surfaced through the pipeline's existing metrics endpoint (a new
  counter, e.g. `conduit_embedding_tokens_total{provider,model}`) and through
  `conduit pipeline inspect --json`, following the "one code path, CLI/UI/MCP share it" rule.
- **Embedding latency**: per-batch call duration recorded as a histogram
  (`conduit_embedding_call_duration_seconds{provider}`), consumed by `inspect` and any future
  Grafana dashboard, distinct from the pipeline's overall per-record processing latency so a slow
  provider is diagnosable separately from a slow destination.
- **Provider errors as coded errors**: every failure mode in the previous section maps to a stable
  `conduiterr`-registered code (illustrative set below), each carrying the provider name and
  underlying status/message, never a raw stack trace — matching CLAUDE.md's "errors are API" rule.

  | Code (illustrative — exact set is an implementation-time `codes.go`) | Raised when |
  | --- | --- |
  | `ai.no_provider_configured` | Zero resolvable embedding-provider candidates. |
  | `ai.ambiguous_provider_configuration` | More than one candidate, no explicit selection. |
  | `ai.embedding_provider_error` | The provider call failed (network, timeout, rate limit, auth). |
  | `ai.embedding_host_not_allowed` | A requested URL fell outside the pipeline's configured allowlist (§1, §5.5). |
  | `ai.vector_dimension_mismatch` | Configured embedding dimension doesn't match the target table/collection at startup (§5). |
  | `ai.metadata_field_missing` | A configured metadata mapping references a field not present / not the expected type. |

- **`--json` conformance**: any new CLI surface this subsystem's processors/connectors expose
  (config validation, a future `doctor`-style provider check mirroring `generate`'s) follows the
  shared envelope (`20260707-cli-output-conventions.md`) and stable exit-code classifier, per every
  other CLI surface in the tree.
- **Cookbook recipe**: a processor cookbook entry (chunking strategies with worked examples) and a
  vector-destination README (config reference, delivery-semantics notes, a runnable example) ship
  in the same PRs that build them, per the standing "docs move with code" rule.

## Upgrade / rollback

- **Vector-schema/dimension changes.** Changing the embedding model (and therefore its output
  dimension) for an already-populated pgvector table is an **operator-initiated migration**, not
  something this subsystem does automatically: the dimension-validation-at-startup check (§5) means
  a config change to a new-dimension model fails fast against the existing table rather than
  silently writing mismatched vectors, forcing the operator to either migrate the column
  (`ALTER TABLE ... ALTER COLUMN embedding TYPE vector(N)`, a destructive operation outside this
  subsystem's scope) or point at a new table. This doc does not build a migration tool for that —
  it is named here as an explicit operational consequence, documented in the connector's README, not
  a silent gap.
- **Provider swaps.** Swapping providers (e.g. `openai` → `voyage`) for an existing pipeline
  produces a **different embedding space** — vectors from two providers are not comparable or
  interchangeable in the same vector-similarity search. This is a data-semantics fact, not a bug;
  the README states it plainly and recommends re-embedding the full corpus (a backfill/replay run,
  using the existing `conduit pipeline replay` verb once it ships per Phase 2, or a one-off
  full-table CDC snapshot re-run today) rather than mixing embedding spaces in one table.
- **Purely additive to the core engine.** The one core-engine-adjacent change (§1's host capability)
  is additive to `pprocutils` and the WASM host module — existing processors that never use the new
  capability are completely unaffected; there is no protocol version bump, and an older Conduit
  binary without the capability simply cannot run the embedding processor (a clear, discoverable
  compatibility failure at plugin-load time, not a silent one), while a newer binary is fully
  backward compatible with every existing standalone processor.
- **New repos, independent release cadence.** `conduit-processor-ai` and
  `conduit-connector-pgvector`/`-qdrant` version independently of the core engine and of each other,
  following the same compatibility discipline `conduit-connector-sdk` consumers already follow
  (acceptance-suite version as the compatibility bar for each processor/connector generation).

## Testing

- **Chunking processor**: unit tests per strategy (fixed-size with/without overlap, sentence
  boundary edge cases, recursive-splitter fallback chain) plus round-trip properties (concatenating
  chunks with overlap removed reconstructs the source text) — a property-based test per CLAUDE.md's
  data-path testing standard, since chunk boundaries are exactly the kind of serialization/transform
  logic that benefits from round-trip property tests.
- **Embedding processor**: unit tests against a mocked `HTTPService` covering full-batch success,
  full-batch failure (no partial acks), the mixed-partial-batch-result case (§4's per-record
  honoring requirement), rate-limit backoff/retry-exhaustion, and the bounded-batch/no-early-ack
  property (§7) — asserting the processor never returns a "success" for a record it did not
  actually embed.
- **Vector destination (pgvector)**: acceptance-test suite against `conduit-connector-sdk`'s
  standard harness, plus destination-specific tests: dimension-mismatch-at-startup (asserts a coded
  failure, no partial table writes), **the kill-mid-write recovery test named in Decision §6**
  (SIGKILL after embed, mid-upsert; restart; assert convergence to one correct row), and the
  delete-by-`source_key` test (assert all chunks from a source record are removed even when chunk
  count changed between the last embed and the delete).
- **The new host capability** (`pprocutils.HTTPService`): unit tests on the host-module wiring for
  allowlist enforcement (reject out-of-allowlist URLs, reject followed redirects to
  out-of-allowlist targets), timeout enforcement, and response-size-cap enforcement — this is the
  security-critical test set for §1, analogous in spirit to `generate`'s "never-auto-apply boundary"
  load-bearing test.
- **End-to-end RAG-sync template**: CI job (docker-compose Postgres + pgvector) running the full
  Postgres CDC → chunk → embed → pgvector pipeline, asserting records land in the vector table with
  the expected dimension and metadata — behind the WS0 chaos-CI gate and WS6 DBZ-2 suite per the
  v0.20 phased sequence, not merely "YAML parses."
- **Chaos coverage**: the embedding-processor and pgvector-destination chaos cases (kill-mid-batch,
  kill-mid-upsert) extend the existing `tests/chaos` harness pattern (the `prune`-toggle SIGKILL
  convention already used for DBZ-1/DBZ-2) rather than inventing a new chaos framework — run under
  the WS0 named chaos-CI job once these repos' CI wiring points at it.

## Open questions for DeVaris

1. **Repo count and naming.** This doc proposes `conduit-processor-ai` (chunking + embedding
   together, since they're always deployed as a pair in the canonical pipeline and share the new
   host-capability dependency) and per-system connector repos (`conduit-connector-pgvector`,
   `conduit-connector-qdrant`). Confirm names and whether chunking/embedding should instead be two
   separate repos (e.g. if they're expected to version independently, or if a future non-RAG use of
   chunking alone argues for splitting them).
2. **Scope and reviewer for the new host capability (Decision §1).** This is the one piece of this
   subsystem that touches the core engine (`conduit-processor-sdk`'s `pprocutils` +
   `pkg/plugin/processor/standalone/host_module.go`). Confirm whether this needs its own
   addendum-style sign-off before `conduit-processor-ai`'s embedding processor is built against it
   (mirroring WS3's Slice-2 addendum pattern), or whether it's small enough to review inline with
   this doc's own sign-off. Given it's a new security boundary (SSRF-relevant allowlist enforcement)
   inside the WASM sandbox, this doc's default assumption is it deserves its own focused review
   pass, not a footnote.
3. **The templates-gallery CI-rule exception (Decision §8).** The RAG-sync template is the first to
   need registry-backed, non-built-in dependencies. Confirm who owns updating the templates-gallery
   enforcement check to allow this one template's declared dependencies without weakening the rule
   for every other (still built-in-only) template.
4. **Qdrant timing.** Confirm it stays the named contingency-drop item (v0.20 plan) — i.e., pgvector
   ships in v0.20 regardless, Qdrant ships in v0.20 only if Phase C doesn't compress, else v0.21.
5. **Batch-size and timeout defaults** (§2: proposed 96 / 30s) are implementation-time constants
   this doc deliberately does not freeze — confirm that's the right level of specificity for a
   design doc versus naming exact defaults now.
6. **Whether the vector-destination README should recommend a specific replay/backfill mechanism
   for provider swaps (Upgrade/rollback)** given `conduit pipeline replay` is itself a Phase-2 item
   not yet shipped — confirm whether this doc should instead describe a documented manual full
   snapshot re-run as the interim answer.

## Related

- `v020-execution-plan.md`, Workstream 8 — the committed scope, acceptance criteria, and Phase A/C
  sequencing (behind WS0 + WS6) this doc satisfies.
- `ROADMAP.md`, Phase 2 "The AI data pipeline" — the roadmap line items (chunking/embedding
  processors, pgvector/Qdrant/Pinecone/Turbopuffer destinations, the `postgres-pgvector-rag`
  template name) this doc scopes into an implementable design.
- `CLAUDE.md`, "AI-pipeline components" session workflow — the source of the pluggable-provider,
  batching, rate-limit, cost-docs, upsert-semantics, dimension-validation, and RAG-template
  requirements this doc satisfies point for point.
- `docs/design-documents/20260722-conduit-generate.md` — the provider-decision rigor bar this doc
  matches (Decision §2), and the "bounded, not quantified" cost/latency stance this doc mirrors.
- `docs/architecture-decision-records/20260722-wasm-component-model-deferred.md` (pending
  ratification) — establishes gRPC-standalone as the primary any-language plugin direction and WASM
  processors as WASI-P1/Go/TinyGo-only today; the reasoning behind Decision §1's chosen approach
  and Alternatives §1's rejected gRPC-standalone-processor option.
- `docs/design-documents/20260723-templates-gallery.md` — the built-in-only MVP constraint and its
  own named registry-backed extension point that Decision §8 exercises for the RAG-sync template.
- `docs/design-documents/20260713-connector-registry-mvp.md` — the install path
  (`conduit connectors install`) the RAG-sync template's preflight step uses.
- `docs/design-documents/20260722-rust-connector-sdk.md` — cited for the per-language-SDK,
  per-repo-per-connector-system convention this doc follows for `conduit-connector-pgvector`/
  `-qdrant`.
- `docs/design-documents/20260707-cli-output-conventions.md` — the `--json` envelope and exit-code
  contract any new CLI surface here follows.
- `docs/postmortems/20260723-source-ack-persist-ordering.md` — the sev-0 whose invariant-1
  reasoning ("never ack before durable") this doc's §7 (token-cost backpressure) and §6 (upsert
  idempotency) directly apply to a new record-path surface.
