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
    `logrepl.import.debeziumOffset`, plus `snapshotMode: never` and
    `cdcMode: logrepl`.
  - `--dry-run [--peek]` prints the computed resume point and the duplicate
    bound. It changes nothing.
  - `--cutover [--stop-connector] [--slot-strategy fresh|reuse]` orchestrates
    the switch. With the fresh strategy (the only one v0.21 runs), it:
    1. runs every offset-independent preflight;
    2. creates the publication, then the slot;
    3. waits until Debezium's committed `lsn_commit`, **and** Debezium's slot
       on this server, pass the slot's start;
    4. saves Debezium's config, then stops Debezium;
    5. re-checks, writes the pipeline, and starts it.

    The full sequence is in the Amendments.
  - `rollback` hands the pipeline back to Debezium. The safe order is in
    Amendments, "Rollback".

  Every mode supports `--json`.
- **Never silently:** if the offset cannot be imported, the compatibility
  report says why, with the connector's stable error code
  (`postgres.import.*`). The CLI then emits a pipeline that re-snapshots only
  when the user passes `--allow-resnapshot`; otherwise it exits with an error.
- **Error codes** (`migrate.kafka_connect.*`), mapped to exit codes per
  `20260706-deterministic-cli-exit-codes.md`:
  - Offsets: `offset_not_found`, `ambiguous_partition`, `connector_running`,
    `offsets_unreachable`.
  - Before the stop: `slot_drop_on_stop`, `lsn_flush_mode_unsupported`,
    `config_masked`, `pipeline_exists`.
  - Cutover: `cutover_timeout`, `cutover_marker_unsupported`,
    `identity_mismatch`, `identity_ahead` (a warning), `lossy_source_config`
    (a warning).
  - Slots and journal: `slot_advanced`, `slot_in_use`, `slot_unowned`,
    `slot_create_blocked`, `reset_unsafe`, `reset_pending`.
  - Start: `start_failed`, `import_pending`.
  - Rollback: `rollback_slot_lost`, `rollback_pending`.

  `rollback_unsupported` is not in v0.21. `--slot-strategy reuse` is refused
  with the connector's `postgres.import.slot_reuse_unsupported`, the single
  code for reuse.

## Consequences

- #3003's command layout must leave room for `--import-offset`, `--cutover`
  and the `rollback` subcommand. Connectors other than Postgres keep
  re-snapshotting in v0.21.
- The runbook `docs/operations/postgres-debezium-offset-import.md` ships with
  the CLI PR.

## Amendments (2026-10-12)

This section was added after review began. It records DeVaris's decisions on
the connector design's open questions, and the fixes from the Tier 1 design
review and the delta review of `ConduitIO/conduit-connector-postgres#341`.
Where it differs from the Decision section above, this section governs. The
Decision section's flag and error-code lists have been edited in place to
match. The connector doc's "Decisions" and "Review fixes" sections have the
full reasoning; the section names below (S9, S12, S13) refer to it.

### Decisions that change the CLI

- **Q4: `--slot-strategy fresh|reuse`, matching #3026.** `fresh` is the
  default and the only strategy v0.21 executes. `reuse` is refused with
  `postgres.import.slot_reuse_unsupported`. Reusing Debezium's slot, and its
  offset write-back, moves to v0.22, gated per Debezium version on a passing
  rollback test.
- **Q3: the cutover marker is opt-in.**
  - `--emit-cutover-marker` writes a non-transactional message:
    `pg_logical_emit_message(false, 'conduit.migrate.cutover', …)`. Debezium
    treats it as a commit and advances `lsn_commit` to the message's end.
  - It works only if Debezium's pgoutput `messages` option is on and the
    prefix passes `message.prefix.include.list` and
    `message.prefix.exclude.list`. It also produces a record on Debezium's
    `<topic.prefix>.message` topic.
  - When it does not move `lsn_commit` in time, the CLI fails with
    `cutover_marker_unsupported`.
  - Without the flag, the CLI never writes to the source database. Before the
    stop, the runbook says to write two separate transactions to a captured
    table, or to enable heartbeats. One write is not enough: its record
    carries the commit before it.
- **Q5: the possible-duplicate metadata key is deferred** (DeVaris,
  2026-10-12). v0.21 logs the duplicate set: a count at INFO, and the
  records' keys at DEBUG, because keys can be personal data. The key is
  future work and is in no v0.21 slice.
- **Q6: the setting is `logrepl.import.debeziumOffset`.** The CLI writes:
  - `database`;
  - `systemIdentifier`, from `IDENTIFY_SYSTEM`;
  - `plugin` (Debezium's `plugin.name`, defaulting to `decoderbufs`);
  - `debeziumSlotName`, `publicationName`, and the resolved `tables`;
  - an `identity` block (S13);
  - the raw offset.

### The CLI owns slot creation

**Version gate (B1).** Before it creates the slot, the CLI refuses servers
below the first release that contains the fix "Fix possibility of logical
decoding partial transaction changes" (2024-07-11):

| Major | First fixed release | Commit |
| --- | --- | --- |
| 14 | 14.13 | `f7d3caf9` |
| 15 | 15.8 | `aee8c2b9` |
| 16 | 16.4 | `2f3304ce` |
| 17 | 17.0 | `068674f4` |
| 18 | any | master `bb19b70081` |

All five commits are the same fix, as committed to each branch.

The code is `postgres.import.server_unpatched`. Below those releases, a new
slot created while Debezium's slot exists can start in the middle of a
transaction and lose that transaction's head. Refusing Postgres 13
(`server_unsupported`) is a policy choice, since 13.16 also has the fix: 13 is
end of life and outside the test matrix. The connector repeats the check at
`Open`.

**Publication ordering (B3).**

- Create or repair Conduit's publication, verify that it covers the
  pipeline's tables, and only then create the slot.
- Never add tables to a publication that an existing slot will decode from.
  pgoutput uses the historic catalog, so their earlier WAL would be skipped
  silently.
- A mismatch found after the slot exists means `--reset`, within S9's rules
  below. Those rules allow it only when Conduit's slot is at or below Lc and
  Debezium's slot is valid; otherwise the way out is `rollback`.

**Preflight before the stop.** Every offset-independent check runs before
`PUT /connectors/{name}/stop`, against the latest committed offset. Checks
marked _before slot creation_ also run before `CREATE_REPLICATION_SLOT`.

- blocking report entries;
- _before slot creation:_ `pg_is_in_recovery()` is false, and Lc ≤
  `pg_current_wal_lsn()`. This closes the standby case, including PG 17
  `sync_replication_slots` copies of Debezium's slot.
- _before slot creation:_ the server version (above);
- the plugin and the database identity;
- Debezium's settings:
  - `slot.drop.on.stop=true` is refused with `slot_drop_on_stop`;
  - `lsn.flush.mode=manual` or `flush.lsn.source=false` is refused with
    `lsn_flush_mode_unsupported`;
  - `connector_and_driver` is accepted, and S13 treats Debezium's slot
    passing Lc as a warning;
- masked secrets (`config_masked`, see S9);
- the publication, including Debezium's `publish` flags, row filters and
  column lists;
- the resolved tables;
- no pipeline with the target name that already holds a position
  (`pipeline_exists`);
- `cdcMode` and `snapshotMode`.

After the stop, only two checks can still fail: Lc ≥ C, and the identity
re-check.

**Identity proof (S13).**

- **During the wait,** Debezium's slot on **this** server must reach
  `confirmed_flush_lsn ≥ C`, besides the committed Lc reaching C. The CLI
  records the value in the journal and in the setting.
- **After the stop,** Debezium's slot must still exist, with F ≥ C. That is a
  hard check: `identity_mismatch`. F ≤ Lc is only a heuristic in fresh mode,
  and F > Lc raises the `identity_ahead` warning. It means duplicates of
  (Lc, F]; it does not mean loss.
- **The fallback after `identity_mismatch`** is
  `--cutover --without-import`. It starts the pipeline on Conduit's intact
  slot with no import, so it re-sends from C: duplicates of (C, Lc], and no
  loss. `--reset` is never the fix here; it refuses when Debezium's slot is
  gone.
- The `systemIdentifier` comparison alone cannot catch clones or promoted
  standbys.

### Crash-safe cutover (S9)

**The journal.** `<pipeline>.cutover.json`, in `--journal-dir`, is written by
temp file, fsync and rename (invariant 5), with file mode 0600. It records:

- the slot name, C and the publication;
- the identity proof and the final O;
- the saved Debezium config;
- the current step.

**The saved Debezium config.**

- It is written **before** the stop or delete call.
- It is redacted in all output.
- Masked values (`[hidden]`, or config-provider placeholders) are detected
  before the stop, and refused with `config_masked` unless the user supplies
  each one as `--debezium-secret <key>='${ENV_VAR}'`. Only the reference is
  stored.

**Steps:** preflight → publication → creating_slot (the name, written before
`CREATE_REPLICATION_SLOT`) → slot_created (C) → waiting → config_saved →
stopping → stopped → pipeline_written → starting (written before the start
call) → started → complete.

**Re-run rules (step-aware),** where F_c is Conduit's slot's
`confirmed_flush_lsn`:

| Situation | Required |
| --- | --- |
| `creating_slot` (Debezium still running) | Adopt C from an existing, inactive, not-lost slot with the journalled name, since it was never consumed. If it is lost, drop it and create it again. If there is none, create it. |
| `slot_created` through `stopping` | F_c = C |
| `stopped` and later, pipeline position empty | F_c ≤ Lc (the connector's cap holds it there) |
| F_c > Lc with a non-empty position, or `complete` | done; the re-run records `complete` |

Otherwise the re-run fails with `slot_advanced`. The fix text depends on when:

- **before the stop:** "Debezium is still running, nothing is lost; run
  `--reset`, then `--cutover`";
- **after the stop:** "run `rollback`; Debezium's slot still holds everything
  from O".

**The journal's slot is active.**

- The re-run attaches if the running pipeline holds it.
- Otherwise it refuses with `slot_in_use`, showing the holder's pid,
  `application_name` and `client_addr`.

**`--reset`.** It never drops the only slot that holds WAL.

1. Debezium's slot must exist and not be lost or invalidated. Otherwise it
   fails with `reset_unsafe`, and the fix points to `--without-import`.
2. It refuses with `reset_unsafe` if F_c > Lc, if the journal step is
   `complete`, or if the pipeline is running. Before the stop, only check 1
   applies.
3. It deletes the written pipeline, which has an empty position.
4. It restarts Debezium from the saved config. It then waits for **proof
   that Debezium is streaming**: its slot is active **and** its committed Lc
   has moved past the journalled Lc. RUNNING alone is not proof.
   `--reset-timeout` (default 30m) bounds the wait; on timeout it exits with
   `reset_pending` and keeps both slots.
5. Only then does it drop Conduit's slot and delete the journal.

**A slot with Conduit's name and no journal** is refused with `slot_unowned`.
The fix text covers ephemeral runners:

- restore the journal from the earlier `--journal-dir`;
- otherwise drop the slot by hand **only** when all of these hold:
  - no pipeline uses it, or that pipeline has no position;
  - its `confirmed_flush_lsn` is at or below Debezium's committed
    `lsn_commit`;
  - Debezium's slot is valid, and Debezium is running and streaming;
- use a persistent `--journal-dir` in CI.

**Slot creation.** `--slot-create-timeout` (default 5m) bounds
`CREATE_REPLICATION_SLOT <name> LOGICAL pgoutput`, which waits for open and
prepared transactions. The slot is created without `TWO_PHASE`, and the
connector never enables streaming.

- On timeout the CLI fails with `slot_create_blocked`.
- The message lists the oldest `pg_stat_activity` transactions and
  `pg_prepared_xacts`.

### `--cutover` starts the pipeline

**Default.** `--cutover` starts the imported pipeline, and reports success
only after a **durable** import-complete signal. Either of these counts:

- the connector's `import complete` event, emitted from inside its `Ack`;
- Conduit's slot reaching `confirmed_flush_lsn > Lc`, which v0.21 uses.

The pipeline's source position from the Conduit API does **not** count:
`Source.Ack` sets `Instance.State` before the persister flushes
(`pkg/connector/source.go`).

**Why.** Between Debezium's stop and Conduit's start, both slots hold WAL. A
pipeline that is never started leaves the connector's flush cap, and with it
the WAL and `catalog_xmin` retention, in place with no limit.

**Flags.**

- `--no-start` writes the pipeline and stops after the import preflight.
- `--cutover-wait-timeout` (default 30m) bounds waiting for Lc ≥ C. On
  timeout, Debezium is still running and the CLI exits with `cutover_timeout`.
- `--start-timeout` (default 10m) bounds waiting for the durable signal.
- `--without-import` is the S13 fallback.
- `rollback` takes `--rollback-timeout` (default 30m).

**If the start fails** (a refusal at `Open`, or a plugin error):

- the CLI stops the pipeline;
- the config stays written, and Debezium stays stopped;
- the journal records the step;
- the CLI prints `rollback` as the way back, and a fix-then-re-run as the way
  forward;
- it exits with `start_failed`, carrying the connector's code as the cause.

**If the durable signal does not arrive in time** (an idle database), the
pipeline is healthy and is left **running**. The CLI exits with
`import_pending`, and the fix is: "Commit one write to any pipeline table. No
re-run is needed. Do not use `--reset`."

The signal has a false negative. After the first durable ack, Conduit's slot
can stay at or below Lc for a while, because the gate reports the acked
change's LSN. So `import_pending` can be spurious, and its text says the
import may already be complete: a re-run re-checks it.

**Relation to #3026.** #3026, the migrate design, uses the same
`--slot-strategy fresh|reuse` flag, the same
`postgres.import.slot_reuse_unsupported` code, and this start behaviour.
Where the two differ, this section is the source for the offset-import flow.

### Rollback (S12)

`conduit migrate kafka-connect rollback --pipeline <name>`:

1. Refuse with `rollback_slot_lost` if Debezium's slot is missing, has
   `wal_status = lost`, or has an `invalidation_reason`. Nothing is changed:
   Conduit's pipeline is then the only complete consumer. This check runs
   again on every resume, and immediately before step 5.
2. Stop the Conduit pipeline, so there are never two writers. Record its last
   persisted commit Kc, and its slot's F_c after the stop.
3. Restart Debezium from the saved config, and wait until it is RUNNING with
   its slot active.
4. Wait until **Debezium's slot's `confirmed_flush_lsn` ≥ F_c**. Both are slot
   positions; Debezium's is a commit end. When the condition holds, Debezium
   has processed every commit that starts below F_c. Progress (Debezium's
   committed Lc passing Kc) is shown, but is not the condition. The wait is bounded by `--rollback-timeout`
   (default 30m). On timeout the CLI exits with `rollback_pending`, both slots
   are kept, and a re-run resumes at step 1.
5. Re-check step 1, then drop Conduit's slot and publication, and delete the
   pipeline.

Debezium's slot holds WAL and `catalog_xmin` for the whole soak. Size
`max_slot_wal_keep_size` for it, and drop the slot when the soak ends.

### Other review fixes on the CLI side

- **The compatibility report** warns with `lossy_source_config` on:
  - `errors.tolerance=all`;
  - Filter SMTs;
  - non-empty `skipped.operations` (including the 2.x default `t`);
  - `column.exclude.list`;
  - Debezium publication `publish` flags, row filters and column lists that
    differ from the pipeline's.

  The no-loss guarantee covers changes Debezium processed under its own
  configuration.
- **Tables.** The CLI resolves Debezium's include and exclude regexes against
  the catalog, and shows the resolved list in the dry run.
- **KIP-618 exactly-once.** The CLI reads both the global offsets topic and
  the connector's own `offsets.storage.topic`. The per-connector offset wins.

## Related

- `ConduitIO/conduit` issues #3002, #3003, #3004 and #3010.
- `ConduitIO/conduit-connector-postgres`:
  `docs/design-documents/20261012-debezium-offset-import.md`.
