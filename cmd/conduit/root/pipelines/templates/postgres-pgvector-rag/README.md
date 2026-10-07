# Template: postgres-pgvector-rag

Scaffold with:

```shell
conduit pipelines init --template postgres-pgvector-rag
```

Unlike every other template in this gallery, this one prints a **prerequisite note** —
`conduit pipelines init --template postgres-pgvector-rag`'s result (`--json`'s
`result.prerequisites`, or the human-readable output) names the exact installs you need before
`conduit run` will do anything useful. This template is the first in the gallery to reference
plugins that are not built into `conduit` (see [Non-built-in dependencies](#non-built-in-dependencies) below) —
`conduit pipelines init` still writes the pipeline YAML, but it will not run until those plugins
are in place.

## What it does

Syncs a Postgres table — an initial full-table snapshot, then ongoing change data capture — into a
RAG-ready vector store: each row's text is **chunked**, **embedded**, and **upserted into
pgvector**, keyed so redelivery and in-place updates converge to one row per chunk and a source-row
delete removes every chunk ever derived from it. This is the canonical RAG-sync pipeline shape from
`docs/design-documents/20260724-ai-pipeline-components.md` (CDC → chunk → embed → vector store).

## Non-built-in dependencies

Three of this pipeline's four plugins are **not** compiled into `conduit` (only `builtin:postgres`
is):

| Plugin | Kind | Install |
| --- | --- | --- |
| `standalone:pgvector` (destination) | Standalone Go connector (`conduit-connector-pgvector`) | Published to the signed registry at **v0.1.0** — install with `conduit connectors install pgvector`. Offline or air-gapped: clone the repo, `go build -o conduit-connector-pgvector ./cmd/connector`, and place the binary under `--connectors.path`. |
| `standalone:ai.chunk` (processor) | Standalone WASM processor (`conduit-processor-ai`) | Published to the signed registry at **0.1.0** — install with `conduit processor-plugins install ai.chunk` (the `registry.incompatible_version` refusal tracked as [#2818](https://github.com/ConduitIO/conduit/issues/2818) is fixed). Requires a running Conduit that satisfies `minConduitVersion` **0.20.0** (a v0.20.0 nightly or later; v0.19.0 stable is correctly refused as too old). On an older Conduit, build it yourself: clone `conduit-processor-ai`, `GOOS=wasip1 GOARCH=wasm go build -tags wasm -o ai-chunk.wasm ./cmd/chunking`, and place the `.wasm` under `--processors.path`. |
| `standalone:ai.embed` (processor) | Standalone WASM processor (`conduit-processor-ai`) | Published to the signed registry at **0.1.0** — install with `conduit processor-plugins install ai.embed`, same `minConduitVersion` 0.20.0 requirement as `ai.chunk`. On an older Conduit, build it the same way (`./cmd/embedding`). |

`ai.chunk` and `ai.embed` ARE published to the signed registry (0.1.0) and ARE installable via
`conduit processor-plugins install`: the compatibility gate used to compare this build's
`conduit-connector-protocol` module version (a connector protocol version) against each
processor's `minProtocolVersion`, refusing every install regardless of build — tracked as
[issue #2818](https://github.com/ConduitIO/conduit/issues/2818), now fixed. What remains is the
processors' genuine `minConduitVersion: 0.20.0` requirement: a v0.19.0 stable Conduit is still
correctly refused with `registry.incompatible_version` (it really predates the release these
processors target), while any v0.20.0 nightly or the eventual v0.20.0 stable release installs
them successfully. `--bundle` applies the identical version algebra offline, so it is not a
workaround for that version requirement. The pgvector destination has no such requirement
(`minConduitVersion` 0.15.0) and installs with `conduit connectors install pgvector`; build it from
source only when the host cannot reach the registry (see the table above). `conduit pipelines init
--template postgres-pgvector-rag` names all three installs in its prerequisite note every time this
template is scaffolded.

## Requires pipeline architecture v2

The chunking processor fans one source record into **many** chunk records (one per chunk). Record
fan-out (`sdk.MultiRecord`) is only supported by **pipeline architecture v2**; the default engine is
one-record-in-one-record-out and fails, at the chunk step, with a
`pipeline.fanout_requires_arch_v2` error (`FailedPrecondition`) naming this flag. Run this pipeline
with `--preview.pipeline-arch-v2` (or `preview.pipeline-arch-v2: true` in the config). Architecture v2
is a **preview** engine: graduation to the default engine is evaluated in v0.21 against a written
gate. Pipelines that need record fan-out cannot run on the classic engine at all, so until then this
template depends on the preview engine; review its status before relying on it for production data.

## Requires network egress for the embedding provider

The embedding processor is a WASM guest, and WASM processors have no network access unless both
the pipeline and the operator allow it:

1. **Pipeline opt-in (already in the template).** The embed processor carries
   `sdk.egress.allow: http://127.0.0.1:11434`, matching its `ollama.baseURL`.
2. **Engine ceiling (you set this).** Engine egress is deny-all by default and the template cannot
   change that. Start Conduit with:

   ```shell
   conduit run --preview.pipeline-arch-v2 \
     --processors.egress.enabled --processors.egress.allow http://127.0.0.1:11434
   ```

   or set the same in `conduit.yaml`:

   ```yaml
   processors:
     egress:
       enabled: true
       allow:
         - http://127.0.0.1:11434
   ```

Without the ceiling, every embed call fails with `ai.embedding_provider_error` wrapping
`http egress is not enabled for this processor`, and the source record is nacked.

Ollama must be listening on `127.0.0.1:11434` with the model pulled (`ollama pull
nomic-embed-text`). Keep the address an IP literal in both places: the egress gate refuses loopback
addresses unless the exact (IP, port) pair is allowlisted, and `http://localhost:11434` is rejected
as an allowlist entry. If Ollama runs elsewhere (another host, a container IP), change
`ollama.baseURL`, `sdk.egress.allow` and `--processors.egress.allow` together.

To use a hosted provider instead, for example OpenAI, replace the embed processor's `provider`,
`model`, `ollama.baseURL` and `sdk.egress.allow` settings with:

```yaml
          provider: openai
          model: text-embedding-3-small    # 1536 dimensions: update the destination's dimension and the vector(N) column
          openai.authSecretRef: openai_key
          sdk.egress.allow: api.openai.com
          sdk.egress.secretRefs: openai_key
```

then start Conduit with `--processors.egress.enabled --processors.egress.allow api.openai.com
--processors.egress.secret-refs openai_key` and `CONDUIT_SECRET_OPENAI_KEY="Bearer sk-..."` in its
environment. The key is injected into the request by the host; it never appears in the pipeline
file or reaches the processor. See the "Processor host egress" section of the Conduit README for
the full model.

You'll also need the pgvector target table created ahead of time, matching the `dimension` you
configure (768 for the template's default `nomic-embed-text` model):

```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE document_chunks (
    id text PRIMARY KEY,
    embedding vector(768),
    metadata jsonb,
    source_key text
);
CREATE INDEX ON document_chunks (source_key);
```

## Config reference

| Component | Setting | Meaning |
| --- | --- | --- |
| `builtin:postgres` (source) | `url` | Postgres connection string. **Placeholder — must be replaced.** |
| | `tables` | Comma-separated table name(s), or `*` for all tables. **Placeholder — must be replaced.** |
| | `snapshotMode` | `initial` — sync existing rows immediately, not just future changes. |
| | `cdcMode` | `auto` — logical replication if available, otherwise long-polling. |
| `standalone:ai.chunk` (processor) | `strategy` | `recursive` — try progressively finer separators until each chunk fits `chunkSize`. |
| | `chunkSize` / `overlap` | Target chunk size and overlap, in Unicode runes. |
| | `inputField` | The record field read as chunk-input text. **Placeholder (`.Payload.After.content`) — point this at your table's actual text column.** |
| | `outputField` | Left at its default (`.Payload.After.text`) — composes with `ai.embed`'s default `inputField` with zero configuration. |
| `standalone:ai.embed` (processor) | `provider` | `ollama` — local, keyless, no cloud credentials needed to try this template. |
| | `model` | `nomic-embed-text` (768-dimensional). Change this and `dimension` below together. |
| | `ollama.baseURL` | `http://127.0.0.1:11434`, Ollama's default port as a loopback IP literal (`localhost` cannot pass the egress gate). |
| | `sdk.egress.allow` | Host-reserved egress opt-in for this processor: exactly the `ollama.baseURL` target. Needs the engine ceiling as well (see [Requires network egress](#requires-network-egress-for-the-embedding-provider)). |
| `standalone:pgvector` (destination) | `url` | Connection string for the pgvector-enabled Postgres instance — can be the same database as the source, or a dedicated one. **Placeholder — must be replaced.** |
| | `table` | Target table for embedding rows. Must already exist (see the `CREATE TABLE` above). |
| | `dimension` | **Must match the embedding model's output dimension** (768 for `nomic-embed-text`). Validated at connector startup; a mismatch refuses to run. |
| | `vectorColumn` / `keyColumn` / `metadataColumn` | Database column names (defaults: `embedding`, `id`, `metadata`). |
| | `vectorField` | The record **payload field** (not a DB column) carrying the vector — default `vector`, matching `ai.embed`'s default `outputField` basename. |
| | `sourceKeyColumn` / `sourceKeyMetadataKey` | What makes a source-row delete remove every chunk ever derived from it, even across a chunk-count change — do not disable for a RAG-sync pipeline. |

## Runnable example

The exact bytes above (minus the placeholder values) are what
`conduit pipelines init --template postgres-pgvector-rag` writes (module:
`cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/pipeline.yaml`). The chunk → embed →
pgvector leg of this pipeline is proven end to end against real WASM processor guests, a real
egress-allowlist host module, and a real out-of-process `conduit-connector-pgvector` binary talking
to real Postgres in `pkg/plugin/processor/standalone/rag_e2e_test.go` (build tag `rag_e2e`) — that
suite is what validates the exact record shape this template's processors/destination compose
around (chunk's `.Payload.After.text` → embed's `.Payload.After.vector` → pgvector's `vectorField`).
The full template-gallery end-to-end job now exists: `TestTemplateGalleryRAG_Integration`
(`cmd/conduit/root/pipelines/template_gallery_rag_e2e_integration_test.go`, build tag
`rag_template_e2e`) scaffolds this template via the real `conduit pipelines init`, makes the
`ai.chunk`/`ai.embed` WASM guests and the `pgvector` connector discoverable by the engine's own
plugin registries, boots the real `conduit.Runtime`, and asserts embedding rows land in pgvector
(right dimension, `id` = `<source_key>:<chunk_index>`, populated `source_key`) then that a
source-row delete removes every derived chunk row through the engine's tombstone fan-out. Run it
with `make test-integration-rag-template` (needs `CONDUIT_PROCESSOR_AI_DIR` and
`CONDUIT_CONNECTOR_PGVECTOR_DIR` sibling checkouts; skips cleanly without them). It runs in CI via
the `rag-template-e2e` workflow — a **non-required** check for now (like `rag-e2e`), gated on changes
to this template and the harness.

## Delivery semantics

- **At-least-once (Invariant 3), not exactly-once.** A source row is only acknowledged upstream
  (advancing the Postgres source's position) once pgvector has durably upserted every chunk
  derived from it (Invariant 1) — the embedding processor's `Process` call does not return until
  every chunk it was handed is embedded or definitively failed, so there is no window where a
  record is acked while still waiting on an embedding call.
- **Idempotent upserts.** Each chunk's `id` (`{source_row_key}:{chunk_index}`) is deterministic, and
  the pgvector write is a single `ON CONFLICT ... DO UPDATE` statement — a retried/redelivered chunk
  converges to the same row rather than duplicating it.
- **No orphaned chunks on update or delete.** Deletes (and a source row's delete tombstone) remove
  every chunk row matching that row's `source_key`, not just the chunk IDs the current chunk count
  would guess — so a document that shrinks (fewer chunks after an edit) doesn't leave stale rows
  behind, and a row delete removes all of its chunks regardless of how the chunk count has changed
  over time.
- **Invariant 6 (schema handling):** the chunking/embedding processors do not coerce or drop
  content; an unrecognized `inputField` path or a provider error surfaces as a processor error
  (routed through the pipeline's configured DLQ/error policy), never silent truncation.
- Ordering is per-table (Postgres CDC), then per-chunk-index within a source row's fan-out; chunks
  from different source rows are not ordered relative to each other.
