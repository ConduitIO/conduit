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
  - `--check-rollback [--watch] [--watch-timeout] [--pg-url] [--connect-url]
    [--conduit-url] [--json]`, and `--check-rollback --abandon --reason
    <text> [--json]`, are read-only apart from the watch log, which only
    `--watch` writes and `--abandon` archives. It runs the
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
  - Lock: `operation_in_progress`, `lock_unavailable`; and
    `rollback_in_progress` while a rollback watch log exists.
  - `--check-rollback`: `rollback_check_no_journal`,
    `rollback_check_stop_incomplete`, `rollback_check_cutover_running`,
    `rollback_check_slot_lost`, `rollback_check_slot_moved`,
    `rollback_check_conduit_behind`, `rollback_check_config_unsafe`,
    `rollback_check_two_writers`, `rollback_check_continuity_unproven`,
    `rollback_check_connector_state`, `rollback_check_privileges`,
    `rollback_check_pipeline_state`, `rollback_check_conduit_unreachable`,
    `rollback_check_restart_interrupted`, `rollback_check_state_unmapped`,
    `rollback_check_offset_unreadable`, `rollback_check_connect_unreachable`,
    and `rollback_check_not_caught_up` (a `waiting` verdict).

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
  Debezium is exactly `STOPPED`, or 404 with `debezium.stopMethod: delete`
  (otherwise `connector_running`, #341 D11); Conduit's slot exists
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
  only ones v0.21 has) opens **one** replication session
  (`replication=database`). On it, it runs `CREATE_REPLICATION_SLOT
  <lock name> TEMPORARY PHYSICAL`, without `RESERVE_WAL`, for each lock, and
  the same session later runs `CREATE_REPLICATION_SLOT` for Conduit's logical
  slot. The review's probe confirmed this on PG 17; PG 14–18 are on the
  pre-merge list. The lock name is `conduit_migrate_lock_` plus
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
- Headroom preflight: free walsenders ≥ 2 (the lock session, plus one for a
  Debezium reconnect while its old walsender is still exiting) and free
  replication slots ≥ the 2 lock slots plus Conduit's slot (just the 2 lock
  slots when Conduit's slot already exists); otherwise `lock_unavailable`,
  naming the setting and the counts.
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
temp file, fsync, rename, then fsync of the directory (invariant 5), with file
mode 0600. It records:

- what `--check-rollback` and the runbook use: `postgres.url` (the
  connection string without its password), `postgres.timeline` from
  `IDENTIFY_SYSTEM`, `connect.url`, `conduit.url`, `debezium.connector`,
  `debezium.stopMethod` (`stop` or `delete`, recorded at `stopping`),
  `debezium.slotName`, `slot.name` and `publication.name`;
- `pipeline.id`, recorded at `pipeline_written` (an API-provisioned pipeline
  gets a UUID; a file-provisioned one uses the file's `id`). The connector
  and processor ids are recorded for diagnosis, but the runbook re-reads
  them from the API at delete time;
- the slot name, C and the publication;
- the identity proof, the final O, and F_stop and R_stop;
- the saved Debezium config, as `debeziumConfig` (`name` and `config`, the
  body `POST /connectors` takes). On a Kafka 3.5+ worker it is redacted
  (secret-typed values replaced by `<redacted>`); on an older worker it is
  complete, credentials included, because the runbook re-creates the
  connector from it (runbook, "Security notes");
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

**A manual rollback in progress.** While `<pipeline>.rollback-watch.json`
exists in `--journal-dir`, `--cutover` refuses with `rollback_in_progress`
and changes nothing. Fix text: "A manual rollback was started for this
pipeline (watch log `<path>`). Finish it with the runbook, or end it with
`--check-rollback --abandon --reason <text>` (Debezium must be stopped),
then re-run." An abandoned log
(`<pipeline>.rollback-watch.abandoned.<UTC timestamp>.json`) does not block.
The re-run reads the R7 snapshot from the newest archive (#341 S9):

| Re-run after `--abandon` | Required | Otherwise |
| --- | --- | --- |
| Conduit's slot missing | — | `postgres.logrepl.slot_missing`: re-snapshot |
| pipeline exists with a position | its topology is intact (the journalled connectors and processors are present) and the current F_c = the snapshot's F_c; then start it | `pipeline_exists` (topology changed) or `slot_advanced` |
| pipeline exists with an empty position, was never written, or was deleted before it ran (F_c = C ≤ Lc) | F_c ≤ Lc; then write and start the import pipeline | `slot_advanced` |
| pipeline deleted after it ran (F_c > Lc), snapshot present | current F_c = the snapshot's F_c; then write the pipeline without the import and start it | `slot_advanced` |
| pipeline deleted after it ran, no snapshot | — | `slot_advanced` |

**Finishing `stopping`.** A re-run of `--cutover` that finds the journal at
`stopping` finishes the observation first. If Debezium is exactly `STOPPED`,
or 404 after the delete the journal records (`debezium.stopMethod: delete`),
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

v0.21 has no command that restarts Debezium or drops Conduit's slot. These
are specified once, in #341 S12 and S15 (doc
`docs/design-documents/20261012-debezium-offset-import.md` in
`conduit-connector-postgres`), and not repeated here so the two documents
cannot drift:

- the checks R1–R11 (R11, Debezium's config against the journal, runs
  before the restart too) and the state mapping;
- the verdict table, and terminal versus transient findings;
- the continuity rules, the watch, and the fix texts;
- "Going forward after a failed restart".

This section is the CLI surface and the runbook.

```sh
conduit migrate kafka-connect --check-rollback --pipeline <name> \
  --journal-dir <dir> [--pg-url <url>] [--connect-url <url>] \
  [--conduit-url <url>] [--offsets-from rest|topic] [--bootstrap-servers <s>] \
  [--offsets-topic <t>] [--watch] [--watch-timeout 30m] [--json]
conduit migrate kafka-connect --check-rollback --pipeline <name> \
  --journal-dir <dir> --abandon --reason <text> [--json]
```

**It is read-only.** It never drops or creates a slot (it takes no lock),
never stops, starts or resumes a connector or pipeline, never deletes a
pipeline, never writes the cutover journal and never writes to the database.
Only `--watch` writes, and only its own watch log,
`<pipeline>.rollback-watch.json`; `--abandon` archives that log. A one-shot
check never writes anything. Its Kafka Connect calls are `GET`s and `PUT
/connector-plugins/{class}/config/validate`, which changes nothing; its
Conduit calls are `GET`s. Every HTTP call has a 10 s connect and 30 s request
timeout, and Postgres connections use `connect_timeout=10` and
`statement_timeout=10s`.

**In short** (details in #341 S12):

- Verdicts: `ready_to_stop_conduit`, `ready_to_restart_debezium`,
  `ready_to_delete_pipeline`, `ready_to_drop`, `waiting`, `unsafe`. Any
  state not in #341's table is `unsafe` (`rollback_check_state_unmapped`).
- Terminal findings stick, across watch restarts, until `--abandon`:
  `continuity_unproven`, `slot_moved` (including a Debezium config change to
  another slot after the restart, R11), `slot_lost`, `two_writers`
  (including a pipeline started after the R7 snapshot, or Conduit's slot
  turning active or moving), `restart_interrupted` (including a 404 after the
  restart when the cutover only stopped the connector), and, after the
  restart, `conduit_behind` and `state_unmapped`. Inside `--watch`,
  transient findings (an unreachable API, a `FAILED` or `RESTARTING` task,
  and the like) are `waiting`.
- R5 (Conduit's durable progress ≥ F_stop when F_stop > Lc) runs in every
  state that can reach `ready_to_drop`, after the restart too, and
  `ready_to_drop` requires the pipeline to be absent. Both
  `ready_to_delete_pipeline` and `ready_to_drop` require the journalled
  Debezium slot to be `active` under an accepted walsender.
- A `DEGRADED` pipeline counts as stopped (only a user Start runs it again);
  `RECOVERING` and stopped-by-the-system do not.
- Its Postgres role needs `pg_read_all_stats` (or `pg_monitor`), and
  `track_activities` must be on for Debezium's role: a NULL `query_start` or
  a `disabled` state fails closed (`privileges`).
- **The CLI owns the final verdict.** The watch records its final verdict in
  its own log. A one-shot check returns `ready_to_drop` only when the
  recorded verdict is `ready_to_drop`, no terminal finding is recorded, and
  the current state passes too, with Debezium's walsender showing the same
  identity tuple as the watch's last entry.
- **Observation gaps accept only the same walsender** (DeVaris decision):
  across a killed watch, a watch restart, or the time between the watch's
  exit and the final check, a Debezium reconnect is `continuity_unproven`.
  The cost: a killed watch cannot resume across a reconnect. Before the
  restart, start a fresh watch; after it, go forward. A gap is any break in
  one watch's Postgres sightings longer than 2 s (Postgres sampling has its
  own timer, so slow HTTP calls never create one), a killed or restarted
  watch, the time after the watch exits, or any time Kafka Connect is
  unreachable, since R11 cannot run then.
- **Names.** Before `ready_to_drop` the check prints the Conduit slot and
  publication it was computed for, after checking that they equal the
  journal's `.slot.name` and `.publication.name` and differ from Debezium's.
- `--json` prints exactly one JSON document; progress goes to stderr.
- **Stop or delete.** The cutover journals how it stopped Debezium
  (`debezium.stopMethod`: `stop` or `delete`), and the check keys its 404
  rule and the `nextStep` commands on that. It reports the worker's version
  only for diagnosis, as the **Kafka** version: Confluent Platform is
  identified by a `-ccs` or `-ce` suffix (for example `7.5.0-ccs`), 7.n maps
  to Kafka 3.n and 8.x to Kafka 4.x, 5.x and 6.x are before Kafka 3.5, and
  any other format fails closed.
- The Postgres password comes from `PGPASSFILE` or `PGPASSWORD`, never from
  `--pg-url`.

### Runbook: manual reset and rollback

Ships as the "Going back to Debezium" part of
`docs/operations/postgres-debezium-offset-import.md`.

**Runbook scope.** v0.21 ships `--check-rollback` and the numbered steps
below, not a script. Step-by-step automation of the procedure is v0.22
(#3044). Each step that changes state needs a specific verdict from a check
run just before it. If the check returns anything else, stop and follow the
check's fix text. Commands are shown one at a time. Every name and URL in
them comes from the journal or from the check's output; the check prints the
exact commands with the names filled in, under `nextStep`.

**Security notes.** On a Kafka 3.5+ worker the journal's saved Debezium
config is redacted. On an older worker it holds Debezium's credentials,
because step 5 may re-create the connector from it. Keep the journal at mode
0600, and archive it with the same care. Never run other slot tooling against
Debezium's or Conduit's slot while going back.

**Reset before the stop** (the check reports the journal at step 6 or
earlier: Debezium was never stopped, and no pipeline exists yet):

1. Run `conduit migrate kafka-connect --check-rollback --pipeline <name>
   --journal-dir <dir>`. It must return `ready_to_drop`.
2. Check the names the check printed: the Conduit slot and publication
   must equal the journal's `.slot.name` and `.publication.name`, and must
   not be Debezium's. Then drop them by those names:
   `SELECT pg_drop_replication_slot('<conduit slot>');`, then
   `DROP PUBLICATION IF EXISTS <conduit publication>;`.
3. Archive the journal. `--cutover` can then start again.

**Rollback or reset after the stop:**

1. **Check.** Run the check. If it reports `stop_incomplete`, finish the
   stop first, and never cancel it with a resume. On Kafka 3.5+ that is
   `PUT /connectors/<connector>/stop`; on an older worker,
   `DELETE /connectors/<connector>`. Wait until `/status` shows `STOPPED`
   (or 404), then check again. If it reports `conduit_behind` while
   Debezium is stopped, keep the pipeline running and commit one write to a
   pipeline table; if the pipeline never started or cannot start, run
   `--cutover --without-import` first.
2. **Stop Conduit's pipeline** only after the check returns
   `ready_to_stop_conduit`. Use `POST /v1/pipelines/<pipeline id>/stop`
   (`pipeline.not_running` means it was already stopped). For a
   file-provisioned pipeline, first set `status: stopped` in its file. Then
   check again until it returns `ready_to_restart_debezium`.
3. **Start the watch:** `conduit migrate kafka-connect --check-rollback
   --pipeline <name> --journal-dir <dir> --watch`. Leave it running. It
   records the stop observation, the pipeline snapshot and every sighting,
   and runs until `ready_to_drop`, a terminal `unsafe`, or its timeout.
4. **Restart Debezium** only when the **running watch itself** reports
   `ready_to_restart_debezium` with what it needs recorded in its log (its
   progress output says so): the R7 baseline (Conduit's slot position, which
   is recorded even when no pipeline exists), the pipeline snapshot when a
   pipeline exists, and, when the journal is at step 7, the stopped
   sighting. The one-shot result from step 2 is not enough. Do not change
   Debezium's config at any point; on an older worker, re-create the
   connector **unmodified** (below).
   - On Kafka 3.5+: `PUT /connectors/<connector>/resume`.
   - On an older worker, where the cutover deleted the connector, re-create
     it from the journal's `debeziumConfig` with `POST /connectors`, sending
     the body on stdin (`--data-binary @-`) and discarding the response, so
     the credentials never reach a file or the terminal. Before the restart,
     R11 compares the journal's saved config; after it, the live one.
5. **Wait** until the watch reports `ready_to_delete_pipeline`. On an idle
   database, commit two separate transactions to a table Debezium captures:
   one write is not enough.
6. **Delete the pipeline** only after a check returns
   `ready_to_delete_pipeline`, with the watch still running. The API refuses
   while connectors or processors are attached. So, by the ids
   `GET /v1/pipelines/<pipeline id>` returns, delete:
   - each connector's processors (`DELETE /v1/processors/<id>`);
   - the pipeline's processors;
   - the connectors (`DELETE /v1/connectors/<id>`);
   - the pipeline (`DELETE /v1/pipelines/<pipeline id>`).

   Deleting the source connector runs `OnDelete` with `LastActiveConfig`,
   which is inert with `logrepl.autoCleanup: false`. For a file-provisioned
   pipeline, remove its file and restart Conduit; the `--dev` watcher never
   deletes. That restart restarts the whole engine, so **every** pipeline on
   it restarts; plan it like any other engine restart.
7. **Drop Conduit's slot, as the last step**, only after the watch has
   ended at `ready_to_drop` **and** a fresh one-shot check, run straight
   away, returns `ready_to_drop`. If Debezium reconnected in between, the
   check says `continuity_unproven`; go forward.
   - The slot and publication it prints must equal the journal's
     `.slot.name` and `.publication.name`, and must not be Debezium's.
   - Then, with those names:
     `SELECT pg_drop_replication_slot('<conduit slot>');` and
     `DROP PUBLICATION IF EXISTS <conduit publication>;`.
   - Archive the journal and the watch log; that also clears `--cutover`'s
     `rollback_in_progress`.

**Going forward after a failed restart** (a terminal `unsafe` after step 4:
`slot_moved`, `continuity_unproven`, `conduit_behind` with Debezium running,
or `restart_interrupted`). That attempt cannot be resumed. `--abandon` ends
it. A new attempt would have to pass R4 again, which a Debezium that has
streamed usually fails. So:

1. Stop Debezium (`PUT /connectors/<connector>/stop`, or `DELETE` on an older
   worker) and wait until it is exactly `STOPPED`, or 404.
2. Run `conduit migrate kafka-connect --check-rollback --pipeline <name>
   --journal-dir <dir> --abandon --reason "<why>"`. It refuses unless
   Debezium is exactly `STOPPED`, or 404 when the cutover deleted it
   (`UNASSIGNED` and everything else is refused with `connector_running`).
   It archives the watch log, with its recorded verdict and findings, as
   `<pipeline>.rollback-watch.abandoned.<UTC timestamp>.json`, and prints how
   much WAL each slot retains. The `--cutover` re-run reads the newest
   archive.
3. Re-run `--cutover` with the original flags. It follows the abandon table
   in "Crash-safe cutover":
   - a pipeline with a position restarts, if its topology is intact and its
     slot has not moved;
   - a pipeline that never ran, including one deleted before it ran, is
     written as an import;
   - a pipeline deleted after it ran is written without the import, if its
     slot has not moved;
   - with Conduit's slot missing, or after `slot_advanced`, there is no
     loss-free way forward: take the re-snapshot path (#341 S12). Create a
     **new** pipeline with `snapshotMode: initial` and a **new** slot and
     publication name, never the journal's `.slot.name`. Once it is
     streaming, drop the journal's `.slot.name` and `.debezium.slotName`
     slots (and their publications) by those explicit names. Before each
     drop, check that the name equals the journal's field, differs from the
     new pipeline's slot, and is not `active`; `--check-rollback --json`
     prints all three names.
4. Both slots retain WAL until the forward pipeline runs. Once it runs and
   you give up on Debezium, drop Debezium's slot by hand; until then, watch
   `max_slot_wal_keep_size`.

**Special cases.**

- **Conduit's slot missing** (the pipeline failing with
  `postgres.logrepl.slot_missing`). The check uses Kc from the pipeline
  snapshot instead of F_c, and fails closed without one. Follow the same
  steps; step 7 skips the slot drop, and the verdict says so.
- **Failover to a standby.** The check reports a timeline change. Slots exist
  on the new primary only if they were synchronised (PG 17
  `sync_replication_slots`, or a platform equivalent); run the check against
  the new primary. `slot_lost` for Debezium's slot means Debezium cannot take
  over without a re-snapshot: keep Conduit running if its slot survived.
- **Debezium's slot invalidated** (`slot_lost`): Conduit's pipeline is the
  only complete consumer; keep it.
- **Connector `PAUSED`, `FAILED`, `RESTARTING` or `UNASSIGNED`**, or the
  pipeline `RECOVERING` or stopped by the system before the R7 snapshot:
  follow the fix text. Inside the watch these are `waiting`, not terminal.
  After the R7 snapshot, any pipeline state other than stopped or absent is
  terminal `two_writers`.
- **Pipeline `DEGRADED`:** it counts as stopped; continue with step 3.
- **Two writers** (`two_writers`): stop one of them now. The finding is
  terminal; go forward.

### Pre-merge gate for the implementation PRs (final review)

Each item must be verified, with the result linked from the implementation
PR it belongs to, before that PR merges. The list is the same as in #341
("Pre-merge gate for the implementation PRs").

The final review's list, items 1–10, followed by the items the author lane
had that it does not cover (11–15):

1. Aurora, RDS, Cloud SQL and Azure accept `CREATE_REPLICATION_SLOT …
   TEMPORARY PHYSICAL` on a replication connection with the `-c tcp_*`
   startup options, and drop it on disconnect (#341 S15).
2. On PG 14–18, one session can hold both locks and also create the slot.
   Only PG 17 has been probed (#341 S15).
3. Debezium `config/validate` while the slot is active: does it error, and
   does it open a JDBC connection only? (#341 S12, R6)
4. Does Debezium send a status update carrying its offset LSN when it
   attaches? If it does, normal restarts give a false
   `continuity_unproven` (#341 S15, R8).
5. Debezium 3.4+: the effective default of `offset.mismatch.strategy`. If it
   is not "unset", the refusal fires on every cutover (#341 S5 item 9).
6. Kafka 3.5+: `PUT /resume` on a STOPPED connector resumes from the stored
   offsets, and a STOPPED status in `/status` means the final offset commit
   has happened (#341 S9, S12).
7. The check role has `pg_read_all_stats` on each managed platform (#341 S12,
   "Privileges").
8. Fixture capture of `lsn_commit` and `lsn` semantics for each Debezium
   version, as #341 already plans.
9. How the CLI picks the pipeline id, and the delete sequence (connectors and
   processors first) (#341 S16, "Pipelines the CLI writes" here).
10. The `0x38` strictly-above result on PG 14–16 (#341 S15).
11. The runbook, executed end to end in the `test/debezium` harness for every
    case in #341 S12's Tests list, on Kafka 3.5+ and on an older worker.
12. Debezium's WAL position search followed by `reconnect()` under a new
    walsender, per Debezium version, which the equality rule is built for
    (#341 S15).
13. Workers older than Kafka 3.5: after the stop-equivalent `DELETE`,
    `/status` returns 404 only once the task has stopped and committed its
    final offset (#341 S9, S12).
14. The pgjdbc version Debezium ships, per Debezium version, and whether its
    `automaticFlush` flushes received LSNs to the slot on its own (Debezium
    3.1 and earlier with a newer pgjdbc), which makes F_stop > Lc and fails
    every rollback closed (#341 S12).
15. A `START_REPLICATION` retry on the same connection changes
    `pg_stat_activity.query_start` (#341 S15).

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
