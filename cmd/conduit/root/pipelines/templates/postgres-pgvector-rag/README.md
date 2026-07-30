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
| `standalone:pgvector` (destination) | Registry-installed Go connector (`conduit-connector-pgvector`) | `conduit connectors install pgvector@<version>` |
| `standalone:ai.chunk` (processor) | Standalone WASM processor (`conduit-processor-ai`) | `conduit processor-plugins install ai.chunk` once it's published to the signed registry; until then `conduit processor-plugins install --bundle <signed.tgz>`, or build `./cmd/chunking` (`GOOS=wasip1 GOARCH=wasm go build -tags wasm -o ai-chunk.wasm ./cmd/chunking`) and place the `.wasm` under `--processors.path`. |
| `standalone:ai.embed` (processor) | Standalone WASM processor (`conduit-processor-ai`) | `conduit processor-plugins install ai.embed` (same options as above; build `./cmd/embedding`). |

The `conduit processor-plugins install` / `uninstall` commands exist, and
`conduit pipelines init --template postgres-pgvector-rag` names them in its prerequisite note every
time this template is scaffolded. The hosted `install ai.chunk` / `install ai.embed` fetch goes live
once `conduit-processor-ai` publishes signed processor artifacts to the registry; until then use the
offline `--bundle` path or a local build.

## Requires pipeline architecture v2

The chunking processor fans one source record into **many** chunk records (one per chunk). Record
fan-out (`sdk.MultiRecord`) is only supported by **pipeline architecture v2**; the default engine is
one-record-in-one-record-out and rejects a fan-out with an `"unknown record type"` error at the
chunk step. Run this pipeline with `--preview.pipeline-arch-v2` (or `preview.pipeline-arch-v2: true`
in the config). Architecture v2 is currently a **preview** engine — it is more allocation-efficient
than the default but does not yet have automatic error-recovery parity; review its status before
depending on it for production data.

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
| | `ollama.baseURL` | Ollama's default local address. |
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
