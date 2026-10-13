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
    `logrepl.import.debeziumOffset`, plus `snapshotMode: never`,
    `cdcMode: logrepl`, `logrepl.autoCleanup: false`,
    `logrepl.requireExistingSlot: true` (pending DeVaris confirmation), and
    the marker prefix in `debezium.message.prefix.exclude.list`. See
    Amendments, "Pipelines the CLI writes".
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
  - The other `--cutover` flags, all specified in the Amendments:
    `--without-import`, `--reset`, `--reset-timeout`, `--journal-dir`,
    `--debezium-secret <key>=env:<VAR>`, `--slot-create-timeout`,
    `--cutover-wait-timeout`, `--start-timeout`, `--no-start`,
    `--pipelines-path` and `--emit-cutover-marker`. `--reset` and `rollback` also accept
    `--emit-cutover-marker`; `rollback` takes `--rollback-timeout`.

  Every mode supports `--json`.
- **Never silently:** if the offset cannot be imported, the compatibility
  report says why, with the connector's stable error code
  (`postgres.import.*`). The CLI then emits a pipeline that re-snapshots only
  when the user passes `--allow-resnapshot`; otherwise it exits with an error.
- **Error codes** (`migrate.kafka_connect.*`), mapped to exit codes per
  `20260706-deterministic-cli-exit-codes.md`:
  - Offsets: `offset_not_found`, `ambiguous_partition`, `connector_running`,
    `offsets_unreachable`.
  - Before the stop: `slot_drop_on_stop`, `lsn_flush_mode_unsupported`
    (`manual`, `connector_and_driver`, or `flush.lsn.source=false`),
    `config_masked`, `pipeline_exists`. `conduit kc lint` reports the same
    Debezium settings as `kc.lint.postgres_slot_drop_on_stop` and
    `kc.lint.postgres_lsn_flush_mode`; the `--cutover` refusal is always the
    `migrate.kafka_connect.*` code (#3026).
  - Cutover: `cutover_timeout`, `cutover_marker_unsupported`,
    `identity_mismatch`, `identity_ahead` (a warning), `lossy_source_config`
    (a warning).
  - Slots and journal: `slot_advanced`, `slot_in_use`, `slot_unowned`,
    `slot_create_blocked`, `reset_unsafe`, `reset_pending`.
  - Start: `start_failed`, `import_pending`.
  - Rollback: `rollback_slot_lost`, `rollback_pending`.
  - Debezium startup options (round 7): `slot_advance_on_start_unsupported`.
  - Any mutating command: `operation_in_progress`, `lock_unavailable`
    (rounds 5 and 6).

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

**Round 4 (2026-10-12).** A restarted Debezium resumes at
max(Lc, F_d), not at Lc. The fixes, all marked **(Round 4)** below: F_stop is
journalled; `--reset` and `rollback` bound Debezium's slot before they
restart it; `--reset` refuses when F_stop > Lc or the position is non-empty;
`rollback` waits for Conduit's durable F_c ≥ F_stop; `connector_and_driver`
is refused before the stop; `--without-import` has preconditions and a
require-existing-slot mode; pipelines are written with
`logrepl.autoCleanup: false`; `config_masked` refuses only literal masks;
slot adoption and orphaned walsenders at `creating_slot` are specified.

**Round 7, partial (2026-10-12).** The round-7 blockers in the automated
`--reset` and rollback attach proof are waiting on DeVaris's decision whether
to keep those commands in v0.21; they are not addressed here. Marked
**(Round 7)** below: lock keepalives, hashed lock names, a second lock on
Debezium's slot, a headroom preflight, fail-closed lock errors, pgbouncer ≥
1.23, managed-platform verification; two reads per poll and the corrected
equality proof; refusal of Debezium startup options that move the slot or
offset; #3033 pinned at `53b767d1`; Kc for keyless checkpoints; default-refuse
recorded as recommended, pending DeVaris.

**Round 6 (2026-10-12).** Marked **(Round 6)** below: the attach proof
follows `confirmed_flush_lsn` across walsender pids, because Debezium
reconnects under a new pid on every normal restart; a pending operation can be
closed after a manual slot drop, with no slot dropped by the CLI; the lock is
a temporary physical replication slot; `--pipelines-path` deletes need an
engine restart and the file's status is specified; non-import slot
preconditions use `postgres.logrepl.*`; `--reset` at `stopping` calls resume
first; a degraded-pipeline re-run row and rollback without Conduit's slot.

**Round 5 (2026-10-12).** Marked **(Round 5)** below: every command finishes
the `stopping` observation before any Debezium restart, and `--reset` never
re-issues the stop; an attach proof carries slot continuity from the restart
to the drop; a per-slot advisory lock makes the commands mutually exclusive;
API- and file-provisioned pipelines are specified, with delete before drop;
`logrepl.requireExistingSlot` replaces `slotOnly` (pending DeVaris
confirmation); the marker prefix is excluded from pipeline output; rollback
validates the config with Kafka Connect and names the way out of the F_stop
deadlock. DeVaris decided: implement the attach proof and keep the runbook
warning; never terminate an orphaned walsender, refuse and print its pid.

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
    table, or to set a `heartbeat.action.query` that writes to a captured
    table. One write is not enough: its record carries the commit before it.
    Plain heartbeats are not enough either: they carry the current offset,
    and on PG 15+ keepalives and skipped empty transactions never move
    `lsn_commit`.
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
  below. Those rules allow it only when Conduit's slot is at or below Lc, the
  pipeline's position is empty, Debezium's slot is valid and not past
  max(Lc, F_stop), and F_stop ≤ Lc; otherwise the way out is `rollback`.

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
  - **(Round 7)** Debezium 3.4+ `offset.mismatch.strategy` set to
    `trust_offset`, `trust_greater_lsn` or `trust_slot`, and Debezium 2.7
    `slot.seek.to.known.offset.on.start=true`, are refused with
    `slot_advance_on_start_unsupported`. The first two advance Debezium's slot
    with `pg_replication_slot_advance` on every start and reconnect
    (`PostgresReplicationConnection` L738–739), with no flush behind it;
    `trust_slot` moves the offset forward to the slot, which skips
    (Lc, F_d] on a restart; the 2.7 option advances the slot at start. All
    three values and the 2.7 option are in the test matrix;
  - `lsn.flush.mode=manual`, `lsn.flush.mode=connector_and_driver`, or
    `flush.lsn.source=false` is refused with `lsn_flush_mode_unsupported`.
    **(Round 4)** `connector_and_driver` was accepted before. The driver
    flushes keepalive LSNs, which moves Debezium's slot past Lc, and a
    restarted Debezium resumes at that point, not at Lc. The same decision is
    in #341 S5 and #3026;
  - these checks run again against the config saved at `config_saved`, and
    again before any Debezium restart in `--reset` or `rollback`;
- literal masks in Debezium's config (`config_masked`, see S9);
- the publication, including Debezium's `publish` flags, row filters and
  column lists;
- the resolved tables;
- no pipeline with the target name, unless this cutover's journal wrote it
  (`pipeline_exists`). **(Round 4)** This holds whatever the pipeline's
  position. An existing pipeline with an empty position and no journal is
  refused too, never overwritten;
- `cdcMode` and `snapshotMode`.

After the stop, only two checks can still fail: Lc ≥ C, and the identity
re-check.

**Identity proof (S13).**

- **During the wait,** Debezium's slot on **this** server must reach
  `confirmed_flush_lsn ≥ C`, besides the committed Lc reaching C. The CLI
  records the value in the journal and in the setting.
- **After the stop,** Debezium's slot must still exist, with F ≥ C. That is a
  hard check: `identity_mismatch`. The CLI journals F_stop and R_stop, the
  slot's `confirmed_flush_lsn` and `restart_lsn` at that point. F_stop > Lc
  raises the `identity_ahead` warning: duplicates of (Lc, F_stop] for the
  import, no loss. **(Round 4)** It also changes the way back: `--reset`
  refuses, and `rollback` waits for Conduit's durable F_c ≥ F_stop, because a
  restarted Debezium would skip (Lc, F_stop].
- The `systemIdentifier` comparison alone cannot catch clones or promoted
  standbys.

**The fallback after `identity_mismatch`: `--cutover --without-import`.** It
starts the pipeline on Conduit's slot with no skip rule.

- **Preconditions,** each refused with the code shown and nothing changed:
  Debezium is STOPPED or deleted (`connector_running`); Conduit's slot exists
  (`postgres.logrepl.slot_missing`), is not lost or invalidated
  (`postgres.logrepl.slot_invalidated`), and uses `pgoutput`
  (`postgres.logrepl.slot_plugin`); F_c ≤ final Lc (`slot_advanced`); the
  pipeline's position is empty (`pipeline_exists`).
- **Require-existing-slot (round 5, pending DeVaris confirmation; supersedes
  the approved `slotOnly`).** A pipeline without an import setting goes
  through the connector's `CreateSubscription`, which creates the slot if it
  is missing, and the publication likewise. If Conduit's slot vanished before
  the start, that would silently start from the current WAL. The CLI writes
  the pipeline with no import setting and `logrepl.requireExistingSlot:
  true`: `Open` fails closed (`postgres.logrepl.slot_missing`,
  `postgres.logrepl.publication_missing`) instead of creating anything. No Lc,
  no flush cap and no F ≤ Lc check are needed in the connector. The resume
  point is Key zero, `MessageLSN = 0`, no `Partial` (#341 S13, B2); the
  import's `MessageLSN = Lc` applies only to a real import. The 4th-pass
  `"resume": "slotOnly"` is withdrawn. #3033, signed off at `53b767d1`,
  specifies the same resume point for `requireExistingSlot`.
- **(Round 6) Codes and scope.** Every non-import slot precondition, in the
  CLI and the connector, uses `postgres.logrepl.slot_missing`,
  `slot_invalidated` and `slot_plugin` (plus `publication_missing`), each with
  a fix text in #341 S13; `postgres.import.slot_*` is for the import path
  only. `requireExistingSlot` catches a **missing** slot, not one dropped and
  recreated under the same name.
- **(Round 7) Default-refuse, recommended, pending DeVaris.** #341 S13 makes
  `requireExistingSlot` tri-state: unset refuses slot creation when the
  position is non-empty (snapshot positions included), `true` always refuses
  (what the CLI writes), explicit `false` allows it with a WARN
  (`postgres.logrepl.slot_recreated`) and a `slot_recreated` metric. It ships
  with a behaviour-change release note and runbook entries for slot drop,
  restore, `pg_upgrade` below 17, failover and RDS blue/green.
- **Duplicates:** every change Debezium produced into Kafka with commit at or
  after C, up to the last record it produced before it stopped, which can be
  past Lc. No loss.
- `--reset` is never the fix here; it refuses when Debezium's slot is gone or
  recreated.

### Mutual exclusion (round 5, changed in round 6)

Round 5 used a session advisory lock. Round 6 showed it is not exclusive
behind a transaction-mode pooler (stale and re-entrant grants through
pgbouncer, and no notice when the session drops). Hardening it (require a
direct connection, detect a pooler, re-check `pg_locks`, keep alive) was
evaluated; the chosen fix is a **temporary replication slot** as the lock:

- Every mutating command (`--cutover`, `--cutover --without-import`,
  `--reset`, `rollback`) opens a replication connection and runs
  `CREATE_REPLICATION_SLOT <lock name> TEMPORARY PHYSICAL` without
  `RESERVE_WAL`. **(Round 7)** The lock name is `conduit_migrate_lock_` plus
  the first 32 hex characters of the SHA-256 of the protected slot's name
  (53 bytes), because Postgres silently truncates names over 63 bytes and
  long slot names would collide. If the name exists, it refuses with
  `operation_in_progress` and prints the holder's `active_pid`,
  `application_name`, `client_addr` and `backend_start`; the fix text says to
  end a stale holder's session with `SELECT pg_terminate_backend(<pid>)`,
  which drops the temporary slot with it, then re-run.
- **(Round 7) Two locks.** Two pipelines can cut over from one Debezium
  connector, so every command takes a lock keyed on Conduit's slot and one
  keyed on Debezium's slot, in a fixed order (by lock name).
- Postgres drops a temporary slot when its session ends, so a killed CLI
  leaves nothing. Replication connections cannot be multiplexed by a
  transaction pooler. Without `RESERVE_WAL` the slot retains no WAL and no
  `xmin`, and creating it does not wait for transactions.
- Each lock costs one `max_replication_slots` entry and one
  `max_wal_senders` connection. **(Round 7)** A headroom preflight requires
  at least 2 free walsenders and 1 free slot beyond the one Conduit's
  pipeline needs; otherwise `lock_unavailable`, naming the setting.
- **(Round 7)** "Already exists" is `operation_in_progress`; every other
  create error (exhausted slots or walsenders, missing `REPLICATION`
  privilege, a platform that rejects the command, a connection failure) is
  `lock_unavailable`, fail closed.
- **(Round 7)** Through pgbouncer, replication connections need pgbouncer
  1.23 or later in session mode; the CLI detects a pooler and refuses below
  that with `lock_unavailable`.
- **(Round 7)** The lock connection sets server-side keepalives in its
  startup options, `options=-c tcp_keepalives_idle=5 -c
  tcp_keepalives_interval=2 -c tcp_keepalives_count=2 -c
  tcp_user_timeout=10000`, so a partitioned CLI's session, and its lock,
  goes within about 20 s (the round-7 probe) instead of about two hours.
- Before every mutating step the command checks that both lock slots exist
  with `active_pid` equal to its own walsenders' pids; if not, it aborts with
  `lock_unavailable`, and a re-run resumes from the journal. A `SELECT 1`
  every 10 s keeps the idle connection busy.
- **(Round 7)** The test plan requires Aurora, RDS, Cloud SQL and Azure
  Database for PostgreSQL to be verified to accept `CREATE_REPLICATION_SLOT …
  TEMPORARY PHYSICAL` before sign-off; where it is not accepted, the CLI
  refuses with `lock_unavailable`.

`--cutover` also refuses with `operation_in_progress` while the journal
records a `--reset` or `rollback` in progress: re-run that operation to
finish it, or to close it after a manual slot drop (below).

### Pipelines the CLI writes (round 5)

**Deploying.** Through the Conduit API by default: the pipeline is
API-provisioned, so the CLI can create, update, start, stop and delete it.
With `--pipelines-path DIR`, the CLI writes `DIR/<pipeline>.yaml` (temp file,
fsync, rename). The API still starts and stops such a pipeline, but refuses an
update or a delete with `orchestrator.immutable_provisioned_by_config`
(`CodeImmutableProvisionedByConfig`), and the engine reads the file only at
start (or under `--dev`). So an update or a delete is a file change: the CLI
makes it, prints what the engine needs, and waits under the step's timeout
until the API shows the new config, or no pipeline. The wait is journalled and
resumable.

- **(Round 6) A delete needs an engine restart.** `--dev` applies new and
  changed files but never deletes: on a removed file it leaves the pipelines
  running (`pkg/conduit/dev/apply.go`, `handleDeleted`). Only the
  provisioning pass at engine start removes a pipeline whose file is gone,
  with its engine state (`deleteOldPipelines`).
- **(Round 6) The status in the file.** Written as `status: stopped`, and kept
  so until the `starting` step. At `starting` the CLI rewrites it as `status:
  running` and calls the API start. `--no-start` holds it at `stopped`. An
  engine restart before `starting` leaves the pipeline stopped: init turns a
  persisted running status into system-stopped, and provisioning applies the
  file's `stopped` as user-stopped.
- To stop a file-provisioned pipeline, the CLI first sets `status: stopped` in
  its file, so an engine restart does not start it again.

**Settings on every pipeline the CLI writes:**

- `logrepl.requireExistingSlot: true` (above);
- `logrepl.autoCleanup: false`: with the default, `true`, a pipeline delete
  runs the connector's `LifecycleOnDeleted`, which drops the slot and the
  publication;
- `conduit.migrate.cutover` in `debezium.message.prefix.exclude.list`, so the
  marker never reaches the pipeline's output (see the marker below).

**`LastActiveConfig`.** The engine runs `LifecycleOnDeleted` with the
config the source last **started** with (`pkg/connector/source.go`, `OnDelete`
uses `LastActiveConfig`). A never-started pipeline triggers nothing, and
`autoCleanup: false` protects only once the pipeline has started with it. The
order below makes this harmless: the delete happens only after every drop
precondition holds, so it no longer matters whether the delete or the CLI
drops the slot. The CLI's drop tolerates "does not exist".

**End of `--reset` and `rollback`.** Re-check that the pipeline is stopped and
its position is as expected (empty for `--reset`, K for `rollback`), then:
delete the pipeline (config and engine state) → drop Conduit's slot → drop the
publication → delete the journal.

**The publication.** Neither the import nor `requireExistingSlot` ever creates
it. The CLI creates or repairs it before the slot. A publication created after
the slot's start makes pgoutput skip its tables' earlier WAL; a PG 18 probe in
the round-5 review lost a row that way.

**After `complete`.** `autoCleanup: false` stays. The completion message and
the runbook say: remove `logrepl.import.debeziumOffset`; to have a delete
clean up the slot and publication, set `logrepl.autoCleanup: true` and restart
the pipeline once (the delete uses the config it last started with), or drop
them by hand when retiring the pipeline. Keep `logrepl.requireExistingSlot:
true`.

### Crash-safe cutover (S9)

**The journal.** `<pipeline>.cutover.json`, in `--journal-dir`, is written by
temp file, fsync and rename (invariant 5), with file mode 0600. It records:

- the slot name, C and the publication;
- the identity proof, the final O, and F_stop and R_stop;
- the saved Debezium config;
- the current step, the steps of a running `--reset` or `rollback`, and the
  attach proof's pid and `backend_start` (round 5).

**The saved Debezium config.**

- It is written **before** the stop or delete call.
- It is redacted in all output.
- Config-provider placeholders (`${file:...}`, `${env:VAR}`,
  `${secrets:...}`) are saved as they are: Kafka Connect resolves them again
  when the config is resubmitted. Only **literal masks**, `[hidden]` or a
  value made of `*` characters, are refused before the stop, with
  `config_masked`, unless the user supplies each one as
  `--debezium-secret <key>=env:<VAR>`. Only the reference is stored; the
  value is resolved from the CLI's environment at restart time.
- **(Round 5)** A resolved `env:` value is resubmitted to Kafka Connect as
  plaintext, and Kafka Connect stores it in its config topic. To keep a
  provider reference, put the reference itself in the variable
  (`VAR='${vault:secret/dbz:password}'`). The CLI warns when it resubmits a
  value that is not a `${…}` reference.
- It is re-validated against Debezium's settings at `config_saved`, before the
  stop, and before every restart.

**Steps:** preflight → publication → creating_slot (the name, and the
creating backend's pid and `backend_start`, written before
`CREATE_REPLICATION_SLOT`) → slot_created (C) → waiting → config_saved (the
config, re-validated) → stopping → stopped (the final O, F_stop, R_stop) →
pipeline_written → starting (written before the start call) → started →
complete.

**Finishing `stopping` (round 5).** Every command that finds the journal at
`stopping` finishes the observation before anything else, and before any
Debezium restart. If Debezium is STOPPED or deleted, it reads O, F_stop and
R_stop and journals `stopped`; a missing Debezium slot is `identity_mismatch`
for `--cutover`, `reset_unsafe` for `--reset`, `rollback_slot_lost` for
`rollback`. If Debezium is RUNNING, only `--cutover` re-issues the stop.
`--reset` never does. **(Round 6)** Kafka Connect's stop is asynchronous, so
RUNNING can hide a pending stop: `--reset` first calls `PUT
/connectors/{name}/resume`, which overrides it, waits for RUNNING, and then
proceeds as if Debezium had never been stopped, with the attach proof applied
from the resume call and the bound `reset_target`.
`rollback` refuses with `connector_running` ("Debezium was never stopped; use
`--reset`").

**Re-run rules for `--cutover` (step-aware),** where F_c is Conduit's slot's
`confirmed_flush_lsn`:

| Situation | Required |
| --- | --- |
| `creating_slot` (Debezium still running) | See "Slot adoption" below. |
| `slot_created` through `stopping` | F_c = C, then finish `stopping` as above |
| `stopped` and later, pipeline position empty | F_c ≤ Lc (the connector's cap holds it there) |
| `stopped` and later, position non-empty, pipeline running | The first ack has happened. Wait under `--start-timeout` for F_c > Lc, then record `complete`; on timeout `import_pending`, pipeline left running |
| **(Round 5)** `stopped` and later, position non-empty, pipeline stopped | The import is done (the setting is now ignored). Record `complete` if F_c > Lc; otherwise start the pipeline and continue as in the row above |
| **(Round 6)** `stopped` and later, pipeline degraded (for example `postgres.logrepl.slot_missing`) | Conduit's slot is gone or unusable. Stop the pipeline and refuse with the connector's code; the fix is `rollback`, which handles a missing Conduit slot by using the persisted position's commit Kc in place of F_c, or a re-snapshot if Debezium's slot is gone too |
| `complete` | done |

Otherwise the re-run fails with `slot_advanced`. The fix text depends on when:

- **before the stop:** "Debezium is still running, nothing is lost; run
  `--reset`, then `--cutover`";
- **after the stop:** "run `rollback`; Debezium's slot still holds everything
  from O".

**The journal's slot is active.**

- The re-run attaches if the running pipeline holds it.
- Otherwise it refuses with `slot_in_use`, showing the holder's pid,
  `application_name` and `client_addr`.

**Slot adoption at `creating_slot`.** Debezium is still running, so nothing
here can lose data. The CLI's replication connection uses
`application_name = conduit-migrate:<pipeline>`.

- No slot: create it.
- Inactive slot: adopt it only if `plugin = 'pgoutput'`, `two_phase = false`,
  `temporary = false`, `database = current_database()`, it is not lost or
  invalidated, and `confirmed_flush_lsn` is not null. C is its
  `confirmed_flush_lsn`. A lost slot is dropped and created again. Any other
  mismatch is `slot_unowned`.
- Active slot with `confirmed_flush_lsn` null: a slot still being created,
  usually by an orphaned walsender of the killed CLI. **Decided (DeVaris,
  2026-10-12, round 5): the CLI never terminates it.** It refuses with
  `slot_create_blocked` and prints the holder's pid, `backend_start` and
  `application_name`, the journalled pid and `backend_start`, and the blocking
  transactions and `pg_prepared_xacts`. The operator ends the blockers, or
  terminates that pid after checking it, and re-runs.
- Active slot with a non-null `confirmed_flush_lsn`: `slot_in_use`.

**The attach proof (round 5, rewritten in round 6).** Used by `--reset` and
`rollback` from the moment they restart (or, for `--reset` at `stopping`,
resume) Debezium. Round 5 required one walsender pid throughout, which fails
on every normal restart: with an `lsn_proc` offset Debezium searches the WAL
position and then calls `reconnect()`, under a new pid
(`PostgresStreamingChangeEventSource` main L202–216, 2.7 L162–176;
`PostgresReplicationConnection.reconnect` main L1186). Rebalances and network
blips change it too. The proof now follows the slot's position:

- Poll at least once a second; journal each poll's `active`, `active_pid`,
  `backend_start`, `confirmed_flush_lsn` and `pg_current_wal_lsn()`.
  **(Round 7)** Each poll reads `pg_replication_slots` and
  `pg_stat_activity` twice and requires agreement; a mismatch counts as a
  change of walsender. The WAL position is diagnostic only.
- The first `active` sighting must have `confirmed_flush_lsn` ≤ the bound
  (final Lc for `--reset` after the stop, `reset_target` before it;
  max(final Lc, F_stop) for `rollback`).
- A new walsender (pid or `backend_start` changed, or an inactive gap) is
  accepted only if its first sighting shows `confirmed_flush_lsn` **equal** to
  F_last, the last value under the old one. **(Round 7, corrected)**
  Equality is a proof: a recreated slot's `confirmed_flush_lsn` is
  **strictly above** the WAL insert position at its creation, because
  creating it writes its own `xl_running_xacts` (the round-7 probe measured
  insert + `0x38` on PG 17 and 18), and that insert position is at or above
  F_last. A slot never moves back, so a recreated slot cannot show F_last,
  on a busy or an idle database alike.
- Anything else ends in `reset_pending` or `rollback_pending`, both slots
  kept. The check before the drop applies the same rules.

`offset.flush.interval.ms` is liveness only: Debezium moves its slot only
after an offset commit, and a lower interval only raises the chance that a
flush lands between the old walsender's last poll and the new one's first,
which goes to `*_pending`, the safe side. `catalog_xmin` and `restart_lsn`
are not used. When the proof fails, the fix text says to check Debezium's task
log for a slot being created since the restart; if one was, or it is unclear,
run a Debezium incremental snapshot of the pipeline's tables first, then drop
Conduit's slot by hand under the `slot_unowned` conditions and re-run the same
command. There is no override flag (DeVaris decision; the round-6 review
agrees).

**Closing a pending operation (round 6).** A `--reset` or `rollback` whose
journal is at `*_restarting` or later, re-run after the operator dropped
Conduit's slot by hand, verifies the pipeline is stopped (empty position for
`--reset`, K for `rollback`), deletes the pipeline (configuration and engine
state; for `rollback` this includes the `requireExistingSlot` pipeline, which
can never start without its slot), drops Conduit's publication, and closes the
journal, which clears `--cutover`'s `operation_in_progress`. It never drops a
slot itself. It exits 0 with the note `closed after a manual slot drop`.

**Decision recorded (DeVaris, 2026-10-12).** The residual window after the
restart was acceptable with a runbook warning only if no cheap proof existed.
The attach proof is that proof, so it is implemented, and the runbook keeps
the warning: no other slot tooling against these slots during `--reset` or
`rollback`.

**`--reset`.** It never drops the only slot that holds WAL. It holds the lock,
and step 1 runs on every invocation, including a re-run after `reset_pending`.

1. Finish `stopping` if the journal is there. Debezium's slot must exist and
   not be lost or invalidated. After the stop, and until the reset has
   restarted Debezium itself, also: F_stop ≤ final Lc; and the slot's
   `confirmed_flush_lsn` ≤ max(final Lc, F_stop) and `restart_lsn` ≤ final Lc.
   Detection of a recreated slot rests on `confirmed_flush_lsn`. Any failure
   is `reset_unsafe`, and the fix points to completing the import or
   `--cutover --without-import`.
2. It refuses with `reset_unsafe` if F_c > Lc, if the pipeline's position is
   not empty, if the journal step is `complete`, or if the pipeline is
   running.
3. It fixes the wait target: the journalled final Lc after the stop, or,
   before the stop (including `stopping` with Debezium still RUNNING), the
   committed Lc read when the reset starts, journalled as `reset_target`.
4. **(Round 5, aligned with #341)** It restarts Debezium from the saved
   config if it was stopped or deleted: resume a STOPPED connector, or
   re-create a deleted one. Immediately before the call it re-runs step 1,
   re-validates the saved config, resolves every `--debezium-secret`, and
   journals `reset_restarting`. From the call on it runs the attach proof. It
   then waits until the attach proof holds **and** the committed Lc has moved
   past `reset_target`. RUNNING alone is not proof. `--reset-timeout`
   (default 30m) bounds the wait; on timeout or a failed proof it exits with
   `reset_pending` and keeps both slots. Before the stop (steps up to
   `config_saved`) there is no restart and no attach proof: Debezium's slot
   must be active and its committed Lc must pass `reset_target`. At
   `stopping` with Debezium RUNNING, the attach proof applies from the resume
   call (above).
5. It re-runs step 1's existence checks and the attach proof's pid check,
   re-checks that the pipeline is stopped with an empty position, then
   deletes the pipeline, drops Conduit's slot, drops the publication, and
   deletes the journal.

After the restart, Debezium's slot moves past max(final Lc, F_stop) as it
streams, so the step 1 bounds apply only up to the restart, and the attach
proof carries continuity from there to the drop.

**The marker with `--reset` and `rollback` (round 5, corrected).**
`--emit-cutover-marker` is accepted. The non-transactional message writes no
rows. Under Debezium-compatible output the pipeline's `messages` option is on
and the pipeline would receive the message, which is why its prefix is in
every CLI-written pipeline's exclude list. A committed Lc at the marker's end
proves that Debezium, on this server, processed everything before it. It does
**not** prove that it did so from its original slot, since a recreated slot
sees the marker too; that is the attach proof's job. If the marker does not
move Lc in time, the exit is `reset_pending` or `rollback_pending`.

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
  The fix: two separate transactions on a captured table, a
  `heartbeat.action.query` that writes to a captured table, or
  `--emit-cutover-marker`. Plain `heartbeat.interval.ms` does not move
  `lsn_commit` on an idle database, and PG 15+ keepalives and skipped empty
  transactions do not either.
- `--start-timeout` (default 10m) bounds waiting for the durable signal.
- `--without-import` is the S13 fallback (above).
- `--pipelines-path DIR` selects file provisioning (above).
- `rollback` takes `--rollback-timeout` (default 30m).

**If the start fails** (a refusal at `Open`, or a plugin error):

- the CLI stops the pipeline (setting `status: stopped` first when it is
  file-provisioned);
- the config stays written, and Debezium stays stopped;
- the journal records the step;
- it exits with `start_failed`, carrying the connector's code as the cause;
- **(Round 5, corrected)** the way forward: fix the cause and re-run
  `--cutover`. A failure at `Open` leaves the position empty, so the
  empty-position row applies; a pipeline that acked records before failing
  has a position, the import is done, and the non-empty-position rows apply;
- the way back is `rollback`. When F_stop > Lc, rollback cannot complete until
  Conduit has durably delivered past F_stop, which a pipeline that does not
  start never will. The text then names `--cutover --without-import` as the
  way out: it streams from C with no skip rule, so it gets past F_stop, after
  which `rollback` can run.

**If the durable signal does not arrive in time** (an idle database), the
pipeline is healthy and is left **running**. The CLI exits with
`import_pending`, and the fix is: "Commit one write to any pipeline table. No
re-run is needed. Do not use `--reset`."

The signal has a false negative. After the first durable ack, Conduit's slot
can stay at or below Lc for a while, because the gate reports the acked
change's LSN. So `import_pending` can be spurious, and its text says the
import may already be complete: a re-run applies the non-empty-position rows
above and records `complete` once F_c > Lc.

**Relation to #3026.** #3026, the migrate design, uses the same
`--slot-strategy fresh|reuse` flag, the same
`postgres.import.slot_reuse_unsupported` code, this start behaviour, and the
`migrate.kafka_connect.*` codes listed in the Decision section. Where the two
differ, this section is the source for the offset-import flow.

### Rollback (S12)

`conduit migrate kafka-connect rollback --pipeline <name>` holds the lock and:

1. **Checks before changing anything** (on every invocation, including a
   resume after `rollback_pending`):
   - **(Round 5)** F_stop must exist: finish `stopping` first; if Debezium is
     RUNNING there, refuse with `connector_running`; if its slot is missing,
     `rollback_slot_lost`. Rollback never re-issues the stop.
   - Debezium's slot exists and is not lost or invalidated; otherwise
     `rollback_slot_lost`, nothing changed, and Conduit's pipeline is the
     only complete consumer.
   - Until rollback has restarted Debezium: the slot's `confirmed_flush_lsn`
     and `restart_lsn` are ≤ max(final Lc, F_stop). A slot recreated after
     the stop fails on `confirmed_flush_lsn`: `rollback_slot_lost`.
   - **(Round 6)** If Conduit's slot is missing (a degraded pipeline), F_c is
     replaced by Kc, the persisted position's commit LSN (acks are FIFO, so
     every commit below it was delivered; **round 7:** for a keyless
     checkpoint, Kc is the checkpoint's LSN, as in #3033 L699); the wait in step 4 is then only the
     attach proof and Debezium's committed Lc moving, and step 5 skips the
     slot drop.
   - If F_stop > final Lc, Conduit's durable F_c ≥ F_stop. Debezium will
     resume at F_stop and skip (Lc, F_stop], so Conduit must have delivered
     it. Otherwise `rollback_pending`, nothing changed. The fix: keep the
     pipeline running and commit one write to a pipeline table; **(round 5)**
     if the pipeline never started or cannot start, run `--cutover
     --without-import` first.
   - The saved config passes Debezium's settings checks, every
     `--debezium-secret` resolves, Kafka Connect's REST API answers, and
     **(round 5)** `PUT /connector-plugins/{class}/config/validate` accepts
     the resolved config. All of this happens before step 2, so a rollback
     that cannot restart Debezium never stops Conduit.
2. Stops the Conduit pipeline (for a file-provisioned pipeline, setting
   `status: stopped` in its file first), so there are never two writers.
   Records its last persisted position K, commit Kc, and its slot's F_c after
   the stop.
3. Restarts Debezium from the saved config, after re-running step 1 and
   journalling `rollback_restarting`, under the attach proof. Waits until it
   is RUNNING with its slot active under the proven pid.
4. Waits until **Debezium's slot's `confirmed_flush_lsn` ≥ F_c**, with the
   attach proof holding. Both are slot positions; Debezium's is a commit end.
   When the condition holds, Debezium has processed every commit from
   max(Lc, F_stop) that starts below F_c, and Conduit's output covers
   (Lc, F_stop]. Progress (Debezium's committed Lc passing Kc) is shown, but
   is not the condition. The wait is bounded by `--rollback-timeout` (default
   30m). On timeout or a failed attach proof the CLI exits with
   `rollback_pending`, both slots are kept, and a re-run starts again at
   step 1. The idle-database fix is the same as for `cutover_timeout`.
5. Re-checks step 1's existence checks and the attach proof's pid, re-checks
   that the pipeline is stopped at position K, then deletes the pipeline,
   drops Conduit's slot and publication, and deletes the journal.

**Loss (round 5, qualified).** None for the pipeline's tables: every change
in them is in Conduit's output up to K or retained by Debezium's slot.
Debezium may capture tables outside the pipeline's subset. When F_stop > Lc,
their changes in (Lc, F_stop] are in neither output: Conduit never captured
them, and the restarted Debezium skips them. That is Debezium's own behaviour
after an ungraceful stop, not something rollback introduces; rollback prints
those tables and the range as a warning.

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
