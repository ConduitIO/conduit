# `migrate kafka-connect`: import Debezium Postgres offsets (CLI side)

## Summary

The import design lives in the Postgres connector repo:
`ConduitIO/conduit-connector-postgres`,
`docs/design-documents/20261012-debezium-offset-import.md` (#341). It is not
merged yet, so this doc refers to it by path, and section names such as S9 or
S12 refer to it. This doc covers what is specific to `conduit`: the CLI
surface that `conduit migrate kafka-connect` (lane #3003) gains for the
Postgres offset import (lane #3004), and the manual reset and rollback
runbook.

**Scope (DeVaris, 2026-10-12, at the round-7 completion).** v0.21 ships a
forward-only cutover. Automated `--reset` and `rollback` are cut: seven
Tier 1 review rounds kept finding loss paths in the commands that restart
Debezium and drop Conduit's slot. v0.21 instead ships a manual runbook,
gated by a read-only `--check-rollback`. Automated reset and rollback are
v0.22 (#3044), to be built against a real Kafka Connect and Debezium harness,
with the round-4 to round-7 findings as their requirements.

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
    `logrepl.requireExistingSlot: true`, and the marker prefix in
    `debezium.message.prefix.exclude.list` ("Pipelines the CLI writes").
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
  - `--cutover` also takes `--without-import` (the fallback after
    `identity_mismatch`), `--journal-dir`, `--slot-create-timeout`,
    `--cutover-wait-timeout`, `--start-timeout`, `--no-start`,
    `--pipelines-path` and `--emit-cutover-marker`.
  - `--check-rollback [--watch] [--watch-timeout]` is read-only. It runs the
    safety checks for going back to Debezium and prints a verdict ("Going
    back to Debezium").

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
    `slot_advance_on_start_unsupported`, `config_masked`, `pipeline_exists`.
    `conduit kc lint` reports the same Debezium settings as `kc.lint.*`
    findings; the `--cutover` refusal is always the `migrate.kafka_connect.*`
    code (#3026).
  - Cutover: `cutover_timeout`, `cutover_marker_unsupported`,
    `identity_mismatch`, `identity_ahead` (a warning), `lossy_source_config`
    (a warning).
  - Slots and journal: `slot_advanced`, `slot_in_use`, `slot_unowned`,
    `slot_create_blocked`.
  - Start: `start_failed`, `import_pending`.
  - Lock: `operation_in_progress`, `lock_unavailable`.
  - `--check-rollback`: `rollback_check_no_journal`,
    `rollback_check_stop_incomplete`, `rollback_check_cutover_running`,
    `rollback_check_slot_lost`, `rollback_check_slot_moved`,
    `rollback_check_conduit_behind`, `rollback_check_config_unsafe`,
    `rollback_check_two_writers`, `rollback_check_continuity_unproven`, and
    `rollback_check_not_caught_up` (a `waiting` verdict).

  `--slot-strategy reuse` is refused with the connector's
  `postgres.import.slot_reuse_unsupported`, the single code for reuse.

## Consequences

- #3003's command layout must leave room for `--import-offset`, `--cutover`
  and `--check-rollback`. Connectors other than Postgres keep re-snapshotting
  in v0.21.
- The runbook `docs/operations/postgres-debezium-offset-import.md` ships with
  the CLI PR. Its manual reset and rollback part is drafted below.
- Going back to Debezium in v0.21 is an operator procedure, not a command.
  That is slower and asks more of the operator; in exchange, no v0.21 command
  can drop the only slot that holds the WAL.

## Amendments (2026-10-12)

This section records DeVaris's decisions on the connector design's open
questions and the fixes from seven Tier 1 review rounds of the three PRs
(#341, #3029 and #3026). Where it differs from the Decision section, this
section governs; the Decision section's lists have been edited to match.

**Review history.**

| Round | Found | Where it lives now |
| --- | --- | --- |
| 1–3 | B1 (unpatched Postgres), B3 (publication ordering), preflight order, `slot.drop.on.stop`, the journal | "The CLI owns slot creation", "Crash-safe cutover" |
| 4 | Debezium resumes at max(requested, F_d), not Lc; `connector_and_driver`; `--without-import` preconditions; `autoCleanup` | Preflight, Identity proof, "Pipelines the CLI writes"; reset and rollback parts are #3044 requirements |
| 5 | Step 7 handling, the lock, provisioned pipelines, the marker | "Crash-safe cutover", "Mutual exclusion", "Pipelines the CLI writes" |
| 6 | Slot continuity across walsender pids, the lock under poolers, `--pipelines-path`, `postgres.logrepl.*` | "Going back to Debezium" (read-only), "Mutual exclusion", "Pipelines the CLI writes" |
| 7 | Lock hardening, Debezium startup options, the equality proof; two blockers in automated reset and rollback | "Mutual exclusion", Preflight, "Going back to Debezium"; the blockers are resolved by the scope cut |

**Decided (DeVaris, 2026-10-12):** the scope cut above;
`logrepl.requireExistingSlot` is tri-state (#341 S13); the equal-position
proof is accepted; an orphaned walsender is never terminated by the CLI; the
residual window around a manual Debezium restart is covered by the
continuity rules and kept as a runbook warning.

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
- **Q5: the possible-duplicate metadata key is deferred.** v0.21 logs the
  duplicate set: a count at INFO, and the records' keys at DEBUG, because
  keys can be personal data.
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
- A mismatch found after the slot exists means starting over: the manual
  reset ("Going back to Debezium"), then `--cutover` again.

**Preflight before the stop.** Every offset-independent check runs before
`PUT /connectors/{name}/stop`, against the latest committed offset. Checks
marked _before slot creation_ also run before `CREATE_REPLICATION_SLOT`.

- blocking report entries;
- _before slot creation:_ `pg_is_in_recovery()` is false, and Lc ≤
  `pg_current_wal_lsn()`. This closes the standby case, including PG 17
  `sync_replication_slots` copies of Debezium's slot.
- _before slot creation:_ the server version (above);
- the plugin and the database identity;
- the lock headroom ("Mutual exclusion");
- Debezium's settings:
  - `slot.drop.on.stop=true` is refused with `slot_drop_on_stop`;
  - `lsn.flush.mode=manual`, `lsn.flush.mode=connector_and_driver`, or
    `flush.lsn.source=false` is refused with `lsn_flush_mode_unsupported`.
    The driver mode flushes keepalive LSNs, which moves Debezium's slot past
    Lc, and a restarted Debezium resumes at that point, not at Lc;
  - Debezium 3.4+ `offset.mismatch.strategy` set to `trust_offset`,
    `trust_greater_lsn` or `trust_slot`, and Debezium 2.7
    `slot.seek.to.known.offset.on.start=true`, are refused with
    `slot_advance_on_start_unsupported`. The first two advance Debezium's slot
    with `pg_replication_slot_advance` on every start and reconnect
    (`PostgresReplicationConnection` L738–739), with no flush behind it;
    `trust_slot` moves the offset forward to the slot, which skips
    (Lc, F_d] on a restart; the 2.7 option advances the slot at start;
  - these checks run again against the config saved at `config_saved`;
- literal masks in Debezium's config on workers older than Kafka 3.5
  (`config_masked`, see "The saved Debezium config");
- the publication, including Debezium's `publish` flags, row filters and
  column lists;
- the resolved tables;
- no pipeline with the target name, unless this cutover's journal wrote it,
  whatever its position (`pipeline_exists`). An existing pipeline is never
  overwritten;
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
  import, no loss. It also matters for going back: a restarted Debezium would
  skip (Lc, F_stop], so `--check-rollback` requires Conduit's durable
  progress ≥ F_stop first.
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
- **`logrepl.requireExistingSlot: true`.** A pipeline without an import
  setting goes through the connector's `CreateSubscription`, which creates
  the slot if it is missing, and the publication likewise. If Conduit's slot
  vanished before the start, that would silently start from the current WAL.
  With the setting `true`, `Open` fails closed
  (`postgres.logrepl.slot_missing`, `postgres.logrepl.publication_missing`)
  instead of creating anything. The resume point is Key zero,
  `MessageLSN = 0`, no `Partial` (#341 S13; #3033, signed off at
  `53b767d1`, specifies the same); the import's `MessageLSN = Lc` applies
  only to a real import.
- **The setting is tri-state (decided, #341 S13).** Unset refuses slot
  creation when the position is non-empty (snapshot positions included);
  `true` always refuses (what the CLI writes); an explicit `false` allows it
  with a WARN (`postgres.logrepl.slot_recreated`) and a `slot_recreated`
  metric. It ships with a behaviour-change release note and runbook entries
  for slot drop, restore, `pg_upgrade` below 17, failover and RDS
  blue/green. It catches a **missing** slot, not one dropped and recreated
  under the same name.
- Every non-import slot precondition, in the CLI and the connector, uses the
  `postgres.logrepl.*` codes, each with a fix text in #341 S13;
  `postgres.import.slot_*` is for the import path only.
- **Duplicates:** every change Debezium produced into Kafka with commit at or
  after C, up to the last record it produced before it stopped, which can be
  past Lc. No loss.
- Going back to Debezium is not the fix here: `--check-rollback` reports
  Debezium's slot as lost or moved.

### Mutual exclusion

Round 5 used a session advisory lock. Round 6 showed it is not exclusive
behind a transaction-mode pooler (stale and re-entrant grants through
pgbouncer, and no notice when the session drops). Hardening it (require a
direct connection, detect a pooler, re-check `pg_locks`, keep alive) was
evaluated; the chosen lock is a **temporary replication slot**:

- Every mutating command (`--cutover` and `--cutover --without-import`, the
  only ones v0.21 has) opens a replication connection and runs
  `CREATE_REPLICATION_SLOT <lock name> TEMPORARY PHYSICAL` without
  `RESERVE_WAL`. The lock name is `conduit_migrate_lock_` plus
  the first 32 hex characters of the SHA-256 of the protected slot's name
  (53 bytes), because Postgres silently truncates names over 63 bytes and
  long slot names would collide. If the name exists, it refuses with
  `operation_in_progress` and prints the holder's `active_pid`,
  `application_name`, `client_addr` and `backend_start`; the fix text says to
  end a stale holder's session with `SELECT pg_terminate_backend(<pid>)`,
  which drops the temporary slot with it, then re-run.
- **Two locks.** Two pipelines can cut over from one Debezium
  connector, so every command takes a lock keyed on Conduit's slot and one
  keyed on Debezium's slot, in a fixed order (by lock name).
- Postgres drops a temporary slot when its session ends, so a killed CLI
  leaves nothing. Replication connections cannot be multiplexed by a
  transaction pooler. Without `RESERVE_WAL` the slot retains no WAL and no
  `xmin`, and creating it does not wait for transactions.
- Each lock costs one `max_replication_slots` entry and one
  `max_wal_senders` connection. A headroom preflight requires
  at least 2 free walsenders and 1 free slot beyond the one Conduit's
  pipeline needs; otherwise `lock_unavailable`, naming the setting.
- "Already exists" is `operation_in_progress`; every other
  create error (exhausted slots or walsenders, missing `REPLICATION`
  privilege, a platform that rejects the command, a connection failure) is
  `lock_unavailable`, fail closed.
- Through pgbouncer, replication connections need pgbouncer
  1.23 or later in session mode; the CLI detects a pooler and refuses below
  that with `lock_unavailable`.
- The lock connection sets server-side keepalives in its
  startup options, `options=-c tcp_keepalives_idle=5 -c
  tcp_keepalives_interval=2 -c tcp_keepalives_count=2 -c
  tcp_user_timeout=10000`, so a partitioned CLI's session, and its lock,
  goes within about 20 s (the round-7 probe) instead of about two hours.
- Before every mutating step the command checks that both lock slots exist
  with `active_pid` equal to its own walsenders' pids; if not, it aborts with
  `lock_unavailable`, and a re-run resumes from the journal. A `SELECT 1`
  every 10 s keeps the idle connection busy.
- The test plan requires Aurora, RDS, Cloud SQL and Azure
  Database for PostgreSQL to be verified to accept `CREATE_REPLICATION_SLOT …
  TEMPORARY PHYSICAL` before sign-off; where it is not accepted, the CLI
  refuses with `lock_unavailable`.

`--check-rollback` takes no lock. It reports a held lock as
`rollback_check_cutover_running`, so the manual procedure never runs beside a
`--cutover`.

### Pipelines the CLI writes

**Deploying.** Through the Conduit API by default: the pipeline is
API-provisioned, so the CLI can create, update, start and stop it. With
`--pipelines-path DIR`, the CLI writes `DIR/<pipeline>.yaml` (temp file,
fsync, rename). The API still starts and stops such a pipeline, but refuses an
update or a delete with `orchestrator.immutable_provisioned_by_config`
(`CodeImmutableProvisionedByConfig`), and the engine reads the file only at
start (or under `--dev`). So an update (the `--without-import` rewrite) is a
file change: the CLI makes it, prints what the engine needs, and waits under
the step's timeout until the API shows the new config. The wait is journalled
and resumable. The CLI never deletes a pipeline in v0.21; the runbook does.

- **A delete needs an engine restart.** `--dev` applies new and changed files
  but never deletes: on a removed file it leaves the pipelines running
  (`pkg/conduit/dev/apply.go`, `handleDeleted`). Only the provisioning pass
  at engine start removes a pipeline whose file is gone, with its engine
  state (`deleteOldPipelines`).
- **The status in the file.** Written as `status: stopped`, and kept so until
  the `starting` step. At `starting` the CLI rewrites it as `status: running`
  and calls the API start. `--no-start` holds it at `stopped`. An engine
  restart before `starting` leaves the pipeline stopped: init turns a
  persisted running status into system-stopped, and provisioning applies the
  file's `stopped` as user-stopped.
- To stop a file-provisioned pipeline (after `start_failed`, or in the
  runbook), set `status: stopped` in its file first, so an engine restart
  does not start it again.

**Settings on every pipeline the CLI writes:**

- `logrepl.requireExistingSlot: true` (above);
- `logrepl.autoCleanup: false`: with the default, `true`, a pipeline delete
  runs the connector's `LifecycleOnDeleted`, which drops the slot and the
  publication;
- `conduit.migrate.cutover` in `debezium.message.prefix.exclude.list`, so the
  `--emit-cutover-marker` message never reaches the pipeline's output. This
  is a stated difference from Debezium's own output.

**`LastActiveConfig`.** The engine runs `LifecycleOnDeleted` with the config
the source last **started** with (`pkg/connector/source.go`, `OnDelete` uses
`LastActiveConfig`). A never-started pipeline triggers nothing, and
`autoCleanup: false` protects only once the pipeline has started with it. The
runbook's order makes this harmless: the delete comes only after
`--check-rollback` returns `ready_to_drop`.

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
- the saved Debezium config, as `debeziumConfig` (`name` and `config`, the
  body `POST /connectors` takes);
- the current step.

**The saved Debezium config.**

- It is written **before** the stop or delete call, and redacted in all
  output. The CLI never resubmits it; it is the runbook's record, and
  `--check-rollback` compares the live config with it.
- On a Kafka 3.5+ worker the connector is stopped and Kafka Connect keeps
  its config, so the runbook resumes it in place. On an older worker the
  connector is deleted and the runbook re-creates it from this file, so on
  that path the file must be complete. Config-provider placeholders
  (`${file:...}`, `${env:VAR}`, `${secrets:...}`) are fine; literal masks
  (`[hidden]`, a value made of `*`) are refused before the stop with
  `config_masked`. The fix: use a Kafka 3.5+ worker, or replace the masked
  values with config-provider references in the connector's config first.
- It is re-validated against Debezium's settings at `config_saved`, before the
  stop.

**Steps:** preflight → publication → creating_slot (the name, and the
creating backend's pid and `backend_start`, written before
`CREATE_REPLICATION_SLOT`) → slot_created (C) → waiting → config_saved (the
config, re-validated) → stopping → stopped (the final O, F_stop, R_stop) →
pipeline_written → starting (written before the start call) → started →
complete.

**Finishing `stopping`.** A re-run of `--cutover` that finds the journal at
`stopping` finishes the observation first. If Debezium is STOPPED or deleted,
it reads O, F_stop and R_stop and journals `stopped`; a missing Debezium slot
is `identity_mismatch`. If Debezium is RUNNING, the stop did not take effect
or is still pending (Kafka Connect's stop is asynchronous), and the re-run
issues the stop again and waits for STOPPED.

**Re-run rules for `--cutover` (step-aware),** where F_c is Conduit's slot's
`confirmed_flush_lsn`:

| Situation | Required |
| --- | --- |
| `creating_slot` (Debezium still running) | See "Slot adoption" below. |
| `slot_created` through `stopping` | F_c = C, then finish `stopping` as above |
| `stopped` and later, Debezium RUNNING again | Someone restarted it, most likely a manual rollback. Never two writers: refuse with `connector_running` and change nothing |
| `stopped` and later, pipeline position empty | F_c ≤ Lc (the connector's cap holds it there) |
| `stopped` and later, position non-empty, pipeline running | The first ack has happened. Wait under `--start-timeout` for F_c > Lc, then record `complete`; on timeout `import_pending`, pipeline left running |
| `stopped` and later, position non-empty, pipeline stopped | The import is done (the setting is now ignored). Record `complete` if F_c > Lc; otherwise start the pipeline and continue as in the row above |
| `stopped` and later, pipeline degraded (for example `postgres.logrepl.slot_missing`) | Conduit's slot is gone or unusable. Stop the pipeline and refuse with the connector's code; the fix is the manual rollback, gated by `--check-rollback` (which then uses the persisted position's commit Kc), or a re-snapshot if Debezium's slot is gone too |
| `complete` | done |

Otherwise the re-run fails with `slot_advanced`. The fix text depends on when:

- **before the stop:** "Debezium is still running, nothing is lost; start over
  with the manual reset before the stop (runbook), then `--cutover`";
- **after the stop:** "follow the manual rollback (runbook), gated by
  `--check-rollback`; Debezium's slot still holds everything from O".

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
  2026-10-12): the CLI never terminates it.** It refuses with
  `slot_create_blocked` and prints the holder's pid, `backend_start` and
  `application_name`, the journalled pid and `backend_start`, and the blocking
  transactions and `pg_prepared_xacts`. The operator ends the blockers, or
  terminates that pid after checking it, and re-runs.
- Active slot with a non-null `confirmed_flush_lsn`: `slot_in_use`.

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

**If the start fails** (a refusal at `Open`, or a plugin error):

- the CLI stops the pipeline (setting `status: stopped` first when it is
  file-provisioned);
- the config stays written, and Debezium stays stopped;
- the journal records the step;
- it exits with `start_failed`, carrying the connector's code as the cause;
- the way forward: fix the cause and re-run `--cutover`. A failure at `Open`
  leaves the position empty, so the empty-position row applies; a pipeline
  that acked records before failing has a position, the import is done, and
  the non-empty-position rows apply;
- the way back is the manual rollback, starting with `--check-rollback`.
  When F_stop > Lc it reports `rollback_check_conduit_behind` until Conduit
  has durably delivered past F_stop, which a pipeline that does not start
  never will. The text then names `--cutover --without-import` as the way
  out: it streams from C with no skip rule, so it gets past F_stop, after
  which the check can pass.

**If the durable signal does not arrive in time** (an idle database), the
pipeline is healthy and is left **running**. The CLI exits with
`import_pending`, and the fix is: "Commit one write to any pipeline table. No
re-run is needed."

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

### Going back to Debezium: `--check-rollback` and the manual runbook

v0.21 has no command that restarts Debezium or drops Conduit's slot. The
checks, their codes, verdicts and continuity rules are specified in #341 S12
and S15; this section is the CLI surface and the runbook.

```sh
conduit migrate kafka-connect --check-rollback --pipeline <name> \
  --journal-dir <dir> --connect-url <url> [--watch] [--watch-timeout 30m] [--json]
```

**It is read-only.** It never drops or creates a slot (it takes no lock),
never stops, starts or resumes a connector or pipeline, never deletes a
pipeline, never writes the cutover journal and never writes to the database.
Its only write is its own watch log, `<pipeline>.rollback-watch.json`, under
`--watch`. Its Kafka Connect calls are `GET`s and `PUT
/connector-plugins/{class}/config/validate`, which changes nothing.

**What it checks** (#341 S12), with Lc, C, F_stop and R_stop from the
journal and B = max(final Lc, F_stop):

| # | Check | Code when it fails |
| --- | --- | --- |
| R1 | A journal at step 8+, or at step 6 or earlier (Debezium never stopped); at step 7 it reads F_stop itself if Debezium is STOPPED | `rollback_check_no_journal`; `rollback_check_stop_incomplete` at step 7 with Debezium RUNNING |
| R2 | No `--cutover` holds the lock | `rollback_check_cutover_running` |
| R3 | Debezium's slot exists, is not lost and has no `invalidation_reason` | `rollback_check_slot_lost` |
| R4 | Before the restart: Debezium's slot `confirmed_flush_lsn` ≤ B and `restart_lsn` ≤ B | `rollback_check_slot_moved` |
| R5 | If F_stop > Lc: Conduit's durable progress ≥ F_stop (F_c; or Kc, the persisted position's commit LSN, when Conduit's slot is missing; the checkpoint LSN for a keyless checkpoint) | `rollback_check_conduit_behind` |
| R6 | Debezium's live config passes the preflight's Debezium-settings checks and Kafka Connect's validation | `rollback_check_config_unsafe` |
| R7 | Never two writers | `rollback_check_two_writers` |
| R8 | After the restart, continuity: the watch log covers the restart, the first `active` sighting is ≤ B, and every change of walsender shows the same `confirmed_flush_lsn` as the last value before it | `rollback_check_continuity_unproven` |
| R9 | Before the drop: Debezium's slot `confirmed_flush_lsn` ≥ F_c (skipped when Conduit's slot is missing), and the pipeline is stopped at the position seen at R7, or deleted | `rollback_check_not_caught_up` (`waiting`) |

**Verdicts:** `ready_to_stop_conduit`, `ready_to_restart_debezium`,
`waiting`, `ready_to_drop`, `unsafe`. Exit code 0 for a `ready_*` verdict;
`waiting` and `unsafe` have their own non-zero codes per the exit-code ADR.
`--json` prints the verdict, every check with its observed values, and the
next runbook step.

**The watch, and resuming it.** `--watch` polls at least once a second,
reading `pg_replication_slots` and `pg_stat_activity` twice per poll, and
appends each poll to the watch log (0600, temp file, fsync, rename). A watch
that is killed or times out leaves its log; a new `--watch` judges its first
sighting against the log's last entry, as a change of walsender, so a gap in
observation is handled like an inactive gap. A restart with no watch log at
all cannot be proven: `rollback_check_continuity_unproven`.

**Why equality is a proof (decided).** A recreated slot starts strictly above
the WAL insert position at its creation (its own `xl_running_xacts`; the
round-7 probe measured insert + `0x38` on PG 17 and 18), so it can never show
the last value seen before the change. Debezium reconnects under a new pid on
every normal restart with an `lsn_proc` offset (WAL search, then
`reconnect()`), so the rule follows the slot's position, not its pid.
`offset.flush.interval.ms` matters for liveness only: a lower value only
raises the chance that a flush lands inside a change of walsender, which
fails the check, the safe side.

### Runbook: manual reset and rollback

Ships as the "Going back to Debezium" part of
`docs/operations/postgres-debezium-offset-import.md`. The commands assume:

```sh
export PG='postgres://user@host:5432/db'      # the source database
export CONNECT='http://connect:8083'          # Kafka Connect REST
export CONDUIT='http://conduit:8080'          # Conduit HTTP API
export NAME=orders                            # the pipeline and journal name
export DIR=/var/lib/conduit-migrate           # the cutover's --journal-dir
export DBZ=inventory-connector                # the Debezium connector
check() { conduit migrate kafka-connect --check-rollback --pipeline "$NAME" \
  --journal-dir "$DIR" --connect-url "$CONNECT" "$@"; }
```

Never run other slot tooling against Debezium's or Conduit's slot while going
back: no rule can exclude another actor dropping Debezium's slot between the
last check and Debezium's attach, and the continuity check only catches it
afterwards.

**1. Find where you are.**

- **Symptom:** you want Debezium back, after `--cutover`, a `start_failed`,
  a `slot_advanced`, or a decision to abandon the migration.
- **Diagnosis:** `check --json`. Read `verdict`, `stage` and the failed
  checks. To see what it sees, the slot query it runs is:

  ```sql
  SELECT slot_name, plugin, active, active_pid, wal_status,
         restart_lsn, confirmed_flush_lsn          -- add invalidation_reason on PG 17+
  FROM pg_replication_slots
  WHERE slot_name IN ('<debezium slot>', '<conduit slot>');

  SELECT confirmed_flush_lsn <= GREATEST('<Lc>'::pg_lsn, '<F_stop>'::pg_lsn) AS within_bound
  FROM pg_replication_slots WHERE slot_name = '<debezium slot>';
  ```

  The check is the gate; the queries are for understanding, not a
  substitute.

**2. Reset before the stop** (journal at step 6 or earlier: Debezium was never
stopped). Verdict `ready_to_drop`. Debezium is running and its slot holds
everything, so remove what the cutover created:

```sh
psql "$PG" -c "SELECT pg_drop_replication_slot('<conduit slot>')"
psql "$PG" -c "DROP PUBLICATION IF EXISTS <conduit publication>"
mv "$DIR/$NAME.cutover.json" "$DIR/$NAME.cutover.json.abandoned"
```

No pipeline exists before step 9. Then `--cutover` can start again.

**3. Stop pending** (`rollback_check_stop_incomplete`: journal at
`stopping`, Kafka Connect still shows RUNNING). Finish the stop, never cancel
it with a resume (round 7: a resume after a pending stop restarts the tasks
and can flush past any target taken earlier):

```sh
curl -fsS -X PUT "$CONNECT/connectors/$DBZ/stop"
curl -fsS "$CONNECT/connectors/$DBZ/status"   # repeat until the connector is STOPPED
check
```

**4. Rollback or reset after the stop** (journal at step 8 or later).

1. `check` until the verdict is `ready_to_stop_conduit` or
   `ready_to_restart_debezium`. On `conduit_behind`, keep the pipeline
   running and commit one write to a pipeline table; if it never started or
   cannot start, run `conduit migrate kafka-connect --cutover
   --without-import ...` first, then check again.
2. Stop Conduit's pipeline. For a file-provisioned pipeline, first set
   `status: stopped` in its file.

   ```sh
   conduit pipelines stop "$NAME"
   check        # expect ready_to_restart_debezium
   ```

3. In one terminal, start the watch **before** Debezium:

   ```sh
   check --watch
   ```

4. In another terminal, start Debezium. On Kafka 3.5+:

   ```sh
   curl -fsS -X PUT "$CONNECT/connectors/$DBZ/resume"
   ```

   On an older worker, where the cutover deleted the connector:

   ```sh
   jq '{name: .debeziumConfig.name, config: .debeziumConfig.config}' \
     "$DIR/$NAME.cutover.json" > /tmp/dbz.json
   curl -fsS -X POST -H 'Content-Type: application/json' \
     --data @/tmp/dbz.json "$CONNECT/connectors"
   rm /tmp/dbz.json
   ```

5. Wait for the watch to print `ready_to_drop`. On `waiting` with an idle
   database, commit two separate transactions to a table Debezium captures
   (one write is not enough), for example:

   ```sql
   BEGIN; UPDATE <captured table> SET <column> = <column> WHERE <key> = <value>; COMMIT;
   BEGIN; UPDATE <captured table> SET <column> = <column> WHERE <key> = <value>; COMMIT;
   ```

   Or, if Debezium has pgoutput messages on and the prefix passes its filters:
   `SELECT pg_logical_emit_message(false, 'conduit.migrate.cutover', 'rollback');`
   (it lands on Debezium's `<topic.prefix>.message` topic).
6. Delete the pipeline, then confirm the check still passes, then drop
   Conduit's slot **last**:

   ```sh
   curl -fsS -X DELETE "$CONDUIT/v1/pipelines/$NAME"   # API-provisioned
   # file-provisioned: remove the file, restart Conduit, and confirm
   # `conduit pipelines describe "$NAME"` reports it missing
   check        # expect ready_to_drop
   psql "$PG" -c "SELECT pg_drop_replication_slot('<conduit slot>')"
   psql "$PG" -c "DROP PUBLICATION IF EXISTS <conduit publication>"
   mv "$DIR/$NAME.cutover.json" "$DIR/$NAME.cutover.json.rolled-back"
   mv "$DIR/$NAME.rollback-watch.json" "$DIR/$NAME.rollback-watch.json.done"
   ```

   The pipeline was written with `logrepl.autoCleanup: false`, so the delete
   does not drop the slot. If someone set it to `true` and restarted the
   pipeline, the delete drops the slot itself; that is still safe here,
   because the check had already passed.

**5. Special cases.**

- **Debezium's slot recreated** (`slot_moved`, or `continuity_unproven` after
  the restart). Stop. Keep Conduit's slot: it is the only copy of the WAL
  since C. Either go forward with `--cutover --without-import`, or check
  Debezium's task log since the restart for a replication slot being
  created; if one was, or you cannot tell, run a Debezium incremental
  snapshot of the pipeline's tables (through its signalling table, for
  example `INSERT INTO <signal table> (id, type, data) VALUES ('rb-1',
  'execute-snapshot', '{"data-collections": ["public.orders"], "type":
  "incremental"}')`), wait for it to finish, then drop Conduit's slot by hand
  under the `slot_unowned` conditions above.
- **Degraded pipeline** (Conduit's slot missing, the pipeline failing with
  `postgres.logrepl.slot_missing`). The check uses Kc instead of F_c and
  skips the slot drop. Follow section 4 without the `pg_drop_replication_slot`
  step.
- **Failover to a standby.** Slots exist on the new primary only if they
  were synchronised (PG 17 `sync_replication_slots`, or a platform
  equivalent). Run the check against the new primary. `slot_lost` for
  Debezium's slot means Debezium cannot take over without a re-snapshot;
  keep Conduit running if its slot survived. If both slots are gone, both
  sides need a snapshot.
- **Idle database:** section 4 step 5.
- **Debezium's slot invalidated** (`slot_lost`): Conduit's pipeline is the
  only complete consumer; keep it. Returning to Debezium needs a Debezium
  re-snapshot.
- **Two writers** (`two_writers`): stop one of them now, then check again.

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

- `ConduitIO/conduit` issues #3002, #3003, #3004, #3010, and #3044 (v0.22:
  automated reset and rollback, with the round-4 to round-7 findings as
  requirements).
- `ConduitIO/conduit-connector-postgres`:
  `docs/design-documents/20261012-debezium-offset-import.md` (#341).
- #3026 (the migrate design) and #3033 (Debezium-compatible output, signed
  off at `53b767d1`).
