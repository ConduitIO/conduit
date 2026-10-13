# `migrate kafka-connect`: import Debezium Postgres offsets (pointer)

## Summary

The design lives in the Postgres connector repo:
`ConduitIO/conduit-connector-postgres`,
`docs/design-documents/20261012-debezium-offset-import.md`. It is not merged
yet, so this doc refers to it by path. This doc covers only what is specific to
`conduit`: the CLI surface that `conduit migrate kafka-connect` (lane #3003)
gains for the Postgres offset import (lane #3004). It records what the CLI
lane must leave room for.

## Context

A migrated Debezium Postgres pipeline should resume from Debezium's committed
offset with no re-snapshot. Postgres semantics, meaning parsing the offset,
computing the resume point and checking the replication slot, belong to the
connector. Reading Kafka Connect and orchestrating the cutover belong to the
CLI. `conduit` embeds the Postgres connector as a built-in, so the CLI calls
the connector's exported `source/dbzoffset` package. The dry run therefore
runs the same code as the connector's `Open`. This needs the v0.15.x connector
bump, which is planned in #3004.

## Decision

- **Reading offsets:**
  - `--offsets-from rest` (default; Kafka 3.5+, `GET /connectors/{name}/offsets`).
  - `--offsets-from topic`: `connect-offsets` or the worker's or connector's
    `offsets.storage.topic`. Read with `read_committed`, all partitions, up to
    the high watermark. The last value per key wins, and a tombstone means no
    offset.
  - `--offsets-from file`: a saved REST export.
  - The standalone worker's Java-serialized offset file is not read. The error
    tells the user to export it through REST.

  The topic reader uses franz-go, which is already a dependency.
- **Flags:**
  - `--import-offset` writes the connector setting
    `logrepl.import.debeziumOffset`, plus `snapshotMode: never` and, when
    reusing Debezium's slot, `logrepl.autoCleanup: false`.
  - `--dry-run [--peek]` prints the computed resume point and the duplicate
    bound. It changes nothing.
  - `--cutover [--stop-connector] [--slot-strategy fresh|reuse]` orchestrates
    the switch. With the fresh strategy, it creates the Conduit slot while
    Debezium runs, waits until Debezium's committed `lsn_commit` passes the
    slot's start, then stops Debezium and writes the pipeline.
  - `rollback` hands the pipeline back to Debezium.

  Every mode supports `--json`.
- **Never silently:** if the offset cannot be imported, the compatibility
  report says why, with the connector's stable error code
  (`postgres.import.*`). The CLI then emits a pipeline that re-snapshots only
  when the user passes `--allow-resnapshot`; otherwise it exits with an error.
- **Error codes** (`migrate.kafka_connect.*`): `offset_not_found`,
  `ambiguous_partition`, `connector_running`, `offsets_unreachable`,
  `cutover_timeout` and `rollback_unsupported`. They map to exit codes per
  `20260706-deterministic-cli-exit-codes.md`.

## Consequences

- #3003's command layout must leave room for `--import-offset`, `--cutover`
  and the `rollback` subcommand. Connectors other than Postgres keep
  re-snapshotting in v0.21.
- The runbook `docs/operations/postgres-debezium-offset-import.md` ships with
  the CLI PR.
- ROADMAP (#2945) lists offset import under v0.22, but the v0.21 plan (#3002)
  moves the Postgres import into v0.21. The roadmap text needs that edit.

## Related

- `ConduitIO/conduit` issues #3002, #3003, #3004 and #3010.
- `ConduitIO/conduit-connector-postgres`:
  `docs/design-documents/20261012-debezium-offset-import.md`.
