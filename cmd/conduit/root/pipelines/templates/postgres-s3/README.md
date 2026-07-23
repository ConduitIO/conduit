# Template: postgres-s3

Scaffold with:

```shell
conduit pipelines init --template postgres-s3
# edit the placeholder url/aws.* settings below, then:
conduit run
```

Requires a reachable Postgres database and an S3 (or S3-compatible) bucket
with write access — this template has real infrastructure dependencies,
unlike the two `generator`-sourced templates.

## What it does

Reads a Postgres table — an initial full-table snapshot, followed by ongoing
change data capture — and writes each record as a JSON object to an S3
bucket. This is the "batch/analytics export" shape: land an operational
table in object storage for downstream querying (Athena, Spark, etc.).

## Config reference

| Connector | Setting | Meaning |
| --- | --- | --- |
| `builtin:postgres` (source) | `url` | Postgres connection string. **Placeholder — must be replaced.** |
| | `tables` | Comma-separated table name(s), or `*` for all tables. **Placeholder — must be replaced.** |
| | `snapshotMode` | `initial` — take a full snapshot on first run. |
| | `cdcMode` | `auto` — use logical replication if available, otherwise fall back to long-polling. See delivery-semantics note below. |
| `builtin:s3` (destination) | `aws.accessKeyId` / `aws.secretAccessKey` | AWS (or S3-compatible) credentials. **Placeholders — must be replaced.** |
| | `aws.region` | Bucket region. **Placeholder — must be replaced.** |
| | `aws.bucket` | Destination bucket name. **Placeholder — must be replaced.** |
| | `format` | `json` — one JSON object per record. (`parquet` is also supported by the connector but not used by this template.) |

## Runnable example

The exact bytes above (minus the placeholder values, which are intentionally
unusable until you fill them in) are what
`conduit pipelines init --template postgres-s3` writes (module:
`cmd/conduit/root/pipelines/templates/postgres-s3/pipeline.yaml`). CI runs
this template end to end against a real Postgres and a real S3-compatible
store (MinIO) — see `cmd/conduit/root/pipelines/template_gallery_e2e_integration_test.go`
(build tag `templates_e2e`, run by `make test-integration-templates`).

## Delivery semantics

- **At-least-once (Invariant 3), not exactly-once.** Postgres source
  acknowledgment follows the connector's own position tracking (snapshot
  cursor, then WAL/replication position); a record is only acked upstream
  once accepted by the S3 write path (Invariant 1). A crash between "S3
  accepted the write" and "position advanced" can redeliver a record — never
  drop one.
- **CDC mode is `auto`, not forced `logrepl`.** Unlike the
  `postgres-cdc-kafka` template, this one does not force logical replication
  — if the source database doesn't have logical replication enabled, the
  connector transparently falls back to polling. This template's job is
  "get the table into S3," not "demonstrate CDC," so the more forgiving mode
  is the right default; if you need guaranteed logical replication, set
  `cdcMode: logrepl` explicitly and provision `wal_level=logical`.
- **Invariant 6 (schema handling):** the connector does not coerce or drop
  columns — it emits whatever the source row contains as a structured
  record; the S3 JSON writer serializes that structure as-is. Unsupported
  Postgres types (e.g. custom composite types) may fail to serialize; that
  surfaces as a connector error, not silent data loss.
- Ordering is per-table, not global: concurrent changes across different
  tables are not ordered relative to each other.
