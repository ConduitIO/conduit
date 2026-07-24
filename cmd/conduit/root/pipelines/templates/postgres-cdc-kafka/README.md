# Template: postgres-cdc-kafka

Scaffold with:

```shell
conduit pipelines init --template postgres-cdc-kafka
# edit the placeholder url/servers/topic settings below, then:
conduit run
```

Requires a reachable Postgres database with logical replication enabled
(`wal_level=logical`, a free replication slot, and a role with `REPLICATION`
privilege) and a reachable Kafka cluster.

## What it does

Streams Postgres row-level changes (insert/update/delete) via logical
replication straight to a Kafka topic, with **no initial snapshot** — this is
the CDC-flavored template: it starts from "now" and streams forward, the
classic event-streaming shape (the counterpart to `postgres-s3`'s
batch/analytics shape).

## Config reference

| Connector | Setting | Meaning |
| --- | --- | --- |
| `builtin:postgres` (source) | `url` | Postgres connection string. **Placeholder — must be replaced.** |
| | `tables` | Comma-separated table name(s), or `*` for all tables. **Placeholder — must be replaced.** |
| | `snapshotMode` | `never` — CDC-only, no initial full-table read. |
| | `cdcMode` | `logrepl` — forces logical replication; refuses (via the connector's own startup validation) rather than silently degrading to long-polling if replication isn't set up. |
| `builtin:kafka` (destination) | `servers` | Comma-separated Kafka bootstrap server(s). **Placeholder — must be replaced.** |
| | `topic` | Destination topic name. **Placeholder — must be replaced.** |
| | `acks` | `all` — wait for the full in-sync replica set to acknowledge each write before Conduit acks the source record. |

## Runnable example

The exact bytes above (minus the placeholder values) are what
`conduit pipelines init --template postgres-cdc-kafka` writes (module:
`cmd/conduit/root/pipelines/templates/postgres-cdc-kafka/pipeline.yaml`). CI
runs this template end to end against a real Postgres (logical replication
enabled) and a real Kafka broker — see
`cmd/conduit/root/pipelines/template_gallery_e2e_integration_test.go`
(build tag `templates_e2e`, run by `make test-integration-templates`).

## Delivery semantics

- **At-least-once (Invariant 3), not exactly-once.** A row change is only
  acknowledged upstream (advancing the replication slot's confirmed LSN)
  once Kafka has acknowledged the write per `acks: all` (Invariant 1). A
  crash between "Kafka accepted the write" and "LSN confirmed" redelivers
  the change on restart — consumers must handle duplicates (e.g. by keying
  on the record's primary key and applying idempotently, or relying on
  Kafka's own dedup/compaction if configured).
- **`cdcMode: logrepl` is a deliberate, not a defaulted, choice.** This
  template forces logical replication rather than the more forgiving `auto`
  mode `postgres-s3` uses, because a silent fallback to polling would
  contradict this template's own name and README claim of being
  "CDC-flavored" — if logical replication isn't available, `conduit run`
  fails loudly at startup instead of quietly becoming a poller.
- **Ordering is per-table, not global**, and is delivered in WAL commit
  order for a given table. Records from different tables are not ordered
  relative to each other (Invariant 4).
- **Invariant 6 (schema handling):** the connector does not coerce column
  types; unsupported/unknown Postgres types surface as a connector error
  rather than silently truncating data.
- No initial snapshot means changes that happened before the pipeline first
  started are never captured — this is by design (see `snapshotMode: never`
  above), not a gap. Use the `postgres-s3` template (or set
  `snapshotMode: initial` yourself) if you need historical rows too.
