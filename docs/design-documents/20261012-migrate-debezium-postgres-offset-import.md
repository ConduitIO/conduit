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

## Amendments (2026-10-12)

This section was added after review began. It records DeVaris's decisions on
the connector design's open questions, and the fixes from the Tier 1 design
review of `ConduitIO/conduit-connector-postgres#341`. Where it differs from the
Decision section above, this section governs. The connector doc's "Decisions"
and "Review fixes" sections have the full reasoning.

### Decisions that change the CLI

- **Q4: fresh slot only in v0.21.**
  - The flag stays `--slot-strategy fresh|reuse`, matching #3026. `fresh` is
    the default and the only strategy v0.21 executes. `--slot-strategy reuse`
    is refused with `postgres.import.slot_reuse_unsupported`.
  - Reusing Debezium's slot (and its rollback offset write-back) moves to
    v0.22, gated per Debezium version on a passing rollback test.
  - In v0.21, `rollback` restarts the Debezium connector from the saved config
    and drops Conduit's slot.
- **Q3: the cutover marker is opt-in.**
  - `--emit-cutover-marker` writes a non-transactional message:
    `pg_logical_emit_message(false, 'conduit.migrate.cutover', …)`.
    Debezium treats it as a commit and advances `lsn_commit` to the message's
    end.
  - It works only if Debezium's pgoutput `messages` option is on and the
    prefix passes `message.prefix.include.list` and
    `message.prefix.exclude.list`.
  - The message also produces a record on Debezium's `<topic.prefix>.message`
    topic.
  - When it does not move `lsn_commit` in time, the CLI fails with
    `migrate.kafka_connect.cutover_marker_unsupported`.
  - Without the flag, the CLI never writes to the source database. The runbook
    step is "write two separate transactions to a captured table, or enable
    heartbeats". One write is not enough: its record carries the commit before
    it.
- **Q5: the possible-duplicate metadata key is pending.** DeVaris approved it,
  but the reviewer recommends deferring it, so it awaits DeVaris. The dry run
  and the connector log the duplicate key range at INFO in the meantime.
- **Q6: the setting is `logrepl.import.debeziumOffset`.** The CLI writes
  these fields into it:
  - `database`;
  - `systemIdentifier`, from `IDENTIFY_SYSTEM`;
  - `debeziumSlotName`;
  - `plugin`, Debezium's `plugin.name`, defaulting to `decoderbufs`.

  It also writes `cdcMode: logrepl` and `snapshotMode: never`.

### The CLI owns slot creation, so it owns these (B1, B3, S9)

- **B1: version gate at slot creation.**
  - Before it creates the slot, the CLI refuses servers below the first
    release with Postgres commit 2f3304ce: 14.13, 15.8, 16.4, 17.0, or any
    18. The code is `postgres.import.server_unpatched`, and majors below 14
    get `postgres.import.server_unsupported`. These are the connector's codes,
    from the shared `source/dbzoffset` package.
  - On unpatched builds, a new slot created while Debezium's slot exists can
    start in the middle of a transaction, and lose that transaction's head.
  - The connector repeats the check at `Open`.
- **B3: publication ordering.**
  - The steps are: create or repair Conduit's publication, verify that it
    covers the pipeline's tables, and only then create the slot.
  - The CLI never adds tables to a publication that an existing slot will
    decode from. pgoutput uses the historic catalog, so the earlier WAL of
    those tables would be skipped silently.
  - A publication mismatch found after the slot exists means `--reset`: drop
    the slot, repair the publication, create a new slot.
- **S9: crash-safe re-runs.**
  - `--cutover` keeps a journal next to the pipeline
    (`<pipeline>.cutover.json`). It holds the slot name, C, the publication,
    the saved Debezium config, and the last completed step.
  - A re-run reuses its own slot if it exists, is `pgoutput` and is inactive.
  - It refuses with `migrate.kafka_connect.slot_advanced` when that slot's
    `confirmed_flush_lsn` is past the recorded C. The fix is `--reset`.
  - It refuses a slot with Conduit's name and no journal, with
    `migrate.kafka_connect.slot_unowned`.
  - `--slot-create-timeout` (default 5m) bounds `CREATE_REPLICATION_SLOT`,
    which waits for every open transaction and every prepared transaction to
    finish. On timeout the CLI fails with
    `migrate.kafka_connect.slot_create_blocked`. It lists the oldest
    `pg_stat_activity` transactions and `pg_prepared_xacts`.

### `--cutover` starts the pipeline

- **Default.** `--cutover` starts the imported pipeline. It reports success
  only after the import-complete signal: the first ack, which lifts the
  connector's flush cap. The CLI sees it as the pipeline's source position
  becoming non-empty through the Conduit API.
- **Why.** From Debezium's stop until Conduit's start, both slots hold WAL. A
  pipeline that is never started leaves the cap, and its WAL and
  `catalog_xmin` retention, in place with no limit.
- **`--no-start`.** Writes the pipeline and stops after the import preflight.
  It is for operators who deploy the pipeline themselves, and it prints that
  the pipeline must be started promptly.
- **If the start fails** (a refusal at `Open`, a plugin error, or no first ack
  within `--start-timeout`, default 10m):
  - the pipeline config stays written;
  - Debezium stays stopped (the CLI never restarts it on its own);
  - the journal records the step;
  - the CLI prints `rollback` as the way back and a `--cutover` re-run as the
    way forward;
  - it exits with `migrate.kafka_connect.start_failed`, carrying the
    connector's code as the cause when there is one.

  On an idle database, a first-ack timeout adds the "write two transactions"
  step.
- **Alignment with #3026.** This replaces the statement in the migrate design
  (#3026) that cutover does not start the pipeline. That doc will be aligned
  to this one.

### Other review fixes on the CLI side

- **S6.** The compatibility report warns, with
  `migrate.kafka_connect.lossy_source_config`, on:
  - `errors.tolerance=all`;
  - Filter SMTs;
  - non-empty `skipped.operations` (including the 2.x default `t`);
  - `column.exclude.list`.

  The no-loss guarantee covers changes Debezium processed under its own
  configuration.
- **Nits.**
  - The CLI resolves Debezium's include and exclude regexes against the
    catalog, and shows the resolved table list in the dry run.
  - With KIP-618 exactly-once, it reads both the global offsets topic and the
    connector's own `offsets.storage.topic`. The per-connector offset wins.

## Related

- `ConduitIO/conduit` issues #3002, #3003, #3004 and #3010.
- `ConduitIO/conduit-connector-postgres`:
  `docs/design-documents/20261012-debezium-offset-import.md`.
