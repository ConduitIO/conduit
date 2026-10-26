# What a failed pipeline status write means while a run is live

## Summary

A pipeline's status is written to the pipeline store at every lifecycle transition (`Running`, `Recovering`,
`Degraded`, `UserStopped`, `SystemStopped`). Today both engines let a failed write change the run's control flow:
the default engine (`pkg/lifecycle`, v1) rolls a live run out of `runningPipelines` and orphans it, arch-v2
(`pkg/lifecycle-poc`) reports a live run as a failed start, and in both a recovery restart whose `Running` write
fails leaves a `Degraded` pipeline that is still reading and acking. Every terminal arm in both engines returns early
when its status write fails, which also skips `OnFailure`. Separately, `Start`, `Stop` and the recovery restart
decide whether a run is live by reading the status field, which lags or contradicts the actual run in several
windows.

This document decides the semantics for both engines so the v0.20.1 code can follow:

1. **Once any goroutine of a run exists, no status-write error changes what the run does.** The status write is a
   report. It is retried until it lands or a newer status supersedes it. It never decides whether workers run.
2. **Liveness comes from the lifecycle's run registry, not from the status field.** `Start`, `Stop`, `Delete` and
   the recovery restart are admitted by asking "is a run for this pipeline live?" under the same lock that
   publishes runs.
3. **A run can only write state for itself.** Status, terminal error and failure notifications are fenced by a
   per-run token, so a superseded run cannot overwrite the current one.

Persisted state does not change format. Issues addressed: [#2898], [#2899]. [#2900] is out of scope (see
_Scope_). Risk tier 1 (lifecycle / data path). Each implementation PR needs DeVaris sign-off.

Verified against `origin/main` at `b3b611b8` (2026-10-07, after #2912 and #2917). All `file:line` references below
are at that commit.

## Context

### How a status write works

`pipeline.Service.UpdateStatus` (`pkg/pipeline/service.go:367`) sets the in-memory status first
(`SetStatus`, `:373`), then the error message (`:375`), then writes the whole instance to the store (`:378`). If the
store write fails, the in-memory instance already shows the new status and error, the status metrics have already
moved (`:372`, `:376`), and the store still holds the previous status. The function returns the error.

So after a failed write, **memory shows what the lifecycle intended, and the store shows the last write that
landed.** The API, the CLI, `Stop`'s and `Start`'s status checks all read memory. The store is read only at boot:
`pipeline.Service.Init` turns a stored `Running` into `SystemStopped` (`pkg/pipeline/service.go:77-80`), and
`lifecycle.Service.Init` starts every `SystemStopped` pipeline (`pkg/lifecycle/service.go:216`,
`pkg/lifecycle-poc/service.go:377`).

This design keeps that ordering. Reversing it (memory only after the store succeeds) makes things worse: a run
would be live while memory shows the previous status, which is exactly the state that admits a second `Start`
(see Alternative E).

### Where status-write failures come from

- **A store that is down, full or erroring.** The pipeline store and the connector position store are the same
  database. A database that refuses writes also fails position persistence, and the data path already fails the
  run for that on its own: `connector.Source.onPersistFlushed` sends the persist error to the source's error channel
  without acking the plugin (`pkg/connector/source.go:577-585`), so the run fails and goes through recovery. Status
  writes are not needed to keep data safe in that case.
- **A transient blip** that fails one status write while position writes keep landing.
- **A cancelled caller context (v1 only).** v1 writes `Running` with the caller's context
  (`pkg/lifecycle/service.go:1098`); for an API `Start` that is the gRPC request context
  (`pkg/http/api/pipeline_v1.go:199`). The SQLite and Postgres backends honour the context
  (`conduit-commons` v0.6.0 `database/sqlite/db.go:158`, `database/postgres/db.go:147`), so a client that times
  out after the nodes start but before the write lands fails the write. Badger (the default, `pkg/conduit/config.go:311`)
  ignores it. arch-v2 writes with the run's tomb context, not the caller's. This trigger was found by reading the
  code and has not been reproduced; PR 1's regression test will show whether it is reachable.
- **The pipeline was deleted** under a starting run. `Delete` is refused only when the status is `Running`
  (`pkg/orchestrator/pipelines.go:82`), so it is allowed while the status still shows the previous run's value,
  and while the pipeline is `Recovering`.

### What the store's status is used for

Only the boot decision: auto-start (`SystemStopped`, or `Running` converted to it) or leave alone (anything else).
Nothing in a running process reads the stored status back.

## Problem

Each interleaving below starts on `origin/main` with no other fault. Paths are relative to the repo root.

### I1: v1, a failed `Running` write orphans a live run ([#2898])

1. `Start` builds the nodes and calls `runPipeline`. The run is admitted (`pkg/lifecycle/service.go:969`) and every
   node is started with `rp.t.Go` (`:1000`). The source opens inside its node.
2. The run is published (`:1096`), then `UpdateStatus(Running)` (`:1098`) sets memory to `Running` and the store
   write fails.
3. The rollback deletes the entry (`:1110`) and `runPipeline` returns the error. The cleanup goroutine is registered
   only after the write (`:1116`), so it never exists.
4. Result: the nodes read and ack records, but the run is not in `runningPipelines`, memory says `Running`, and no
   code path will ever write a terminal status, record a terminal error or notify.
   - `Stop` answers "not running" (map miss, `:378-389`).
   - `Start` is refused with `ErrPipelineRunning` (memory is `Running`, `:249`), and so is `Delete`.
   - If a node fails later, the tomb dies and nothing records it: the pipeline shows `Running` forever and is never
     recovered.
   - On shutdown `StopAll` does not see the run. `Wait` does see it, through the live-run counter (`runs.wait`,
     `:572`), so it blocks for the full `exitTimeout` (30s, `pkg/conduit/runtime.go:85`) and returns
     `DeadlineExceeded`. The runtime then flushes the persister and closes the database with the nodes still
     running (`pkg/conduit/runtime.go:893-896`). That violates invariant 7.

### I2: v1 and arch-v2, a recovery restart whose `Running` write fails ([#2898], [#2899] item 1)

1. Run rp1 fails with a transient error. Its cleanup writes `Recovering`, waits out the backoff and calls `Start`
   from its own goroutine (v1 `pkg/lifecycle/service.go:1179`, `:352`; arch-v2 `pkg/lifecycle-poc/service.go:1854`,
   `:2192`).
2. The nested run rp2 starts its workers, is published over rp1, and its `Running` write fails.
   - v1: rp2 is rolled out of the map (`:1110`) and orphaned as in I1.
   - arch-v2: rp2 stays published with its cleanup registered (`pkg/lifecycle-poc/service.go:1984`, `:2033-2035`),
     but `Start` returns the error.
3. rp1's cleanup takes the "recovery failed" arm and writes `Degraded` over the live rp2 (v1 `:1205`, arch-v2
   `:1892`). It then records rp1's error as the pipeline's terminal error (v1 `:1224`, arch-v2 `:1910`) and calls
   `notify` (v1 `:1241`, arch-v2 `:1929`).
4. With `pipelines.exit-on-degraded` set, `notify` kills the runtime (`pkg/conduit/runtime.go:1504` arch-v2,
   `:1513` v1). Shutdown proceeds while rp2 is live: v1 cannot reach rp2 at all (I1), and arch-v2 stops it through
   `StopAll`'s alive check.
5. Without exit-on-degraded, the pipeline shows `Degraded` while rp2 moves data.
   - `Stop` refuses (`pkg/lifecycle/service.go:396`, `pkg/lifecycle-poc/service.go:461`: status is neither
     `Running` nor `Recovering`).
   - `Start` passes its status check (`:249`, `:409`) and builds a third run. That build is usually refused, but only
     by an incidental guard: `connector.Instance.Connector` returns `connector.running` when the instance already has
     an open connector (`pkg/connector/instance.go:119`). That field is set by `Source.Open`
     (`pkg/connector/source.go:257`) and read without synchronisation. In v1 the source opens inside the node
     goroutine, so the guard does not apply until rp2's source has opened. Nothing in the lifecycle intends this
     guard to be an admission check.

### I3: both engines, the `StartWithBackoff` guard races an external `Start` ([#2899] item 2)

1. rp1 is waiting out its recovery backoff and the status is `Recovering`.
2. An operator calls `Start`. The status check passes, because only `Running` is refused, and the operator's run rpX
   starts building.
3. The backoff timer fires. The guard reads the map, still finds rp1 (v1 `pkg/lifecycle/service.go:341-342`, arch-v2
   `pkg/lifecycle-poc/service.go:2173-2174`), and calls `Start`, which builds rp2.
4. Both builds pass the connector guard, because neither has opened yet. rpX is published, then rp2 is published
   over it.
5. rpX is live but no longer in the map, so `Stop` and `StopAll` cannot reach it. Two readers on one source give
   duplicate delivery, and positions become last-writer-wins and can move backwards (invariant 2).

The same shape exists without recovery. Two concurrent `Start` calls on a stopped pipeline both pass the status check,
because the status turns `Running` only after publication. `PipelineOrchestrator.Start` carries
`// TODO lock pipeline` (`pkg/orchestrator/pipelines.go:30`). The processor `running` flag happens to catch this
(`pkg/processor/service.go:150`, a compare-and-swap), but only for pipelines that have processors.

### I4: both engines, terminal writes keyed by pipeline ID ([#2899] item 3)

A run's cleanup writes `UpdateStatus`, `terminalErrors.Set` and `notify` by pipeline ID, not by run. When that run
has been superseded (I2 step 3), its writes land on the pipeline that a newer run now owns. That newer run's status
gets overwritten, `WaitPipeline` returns the old run's error, and exit-on-degraded fires for an error that no longer
describes the pipeline.

### I5: both engines, a failed terminal write skips the tail ([#2899] item 4)

Every terminal arm returns as soon as its `UpdateStatus` fails (v1 `pkg/lifecycle/service.go:1152`, `:1162`,
`:1173`, `:1195`, `:1205`; arch-v2 `pkg/lifecycle-poc/service.go:1801`, `:1819`, `:1830`, `:1848`, `:1873`,
`:1881`, `:1892`). The terminal error, the compare-and-delete and `notify` are skipped. No workers are left, so no
data is affected, but:

- the dead run stays in `runningPipelines`;
- a fatal error whose `Degraded` write fails never reaches `OnFailure`, so exit-on-degraded does not trip. That
  silently drops the fail-fast behaviour the operator configured.

### Data-integrity impact

| Interleaving | Invariant | Effect |
| --- | --- | --- |
| I1, I2 (v1) | 7 | A live run that shutdown cannot stop; the database is closed under it after `exitTimeout` |
| I2, I3 | 2 | Two runs on one source: duplicate delivery, position last-writer-wins, possible regression |
| I2 | 3 / operator intent | A data-moving pipeline reported `Degraded` and refused by `Stop` |
| I4 | none directly | Wrong status, wrong terminal error, spurious exit-on-degraded |
| I5 | none directly | Stale dead entry; a fatal error with no `OnFailure` |

The reviews of #2894 and of this document found no path to a skipped record. At-least-once still holds, because
positions are persisted separately and an upstream ack is sent only after its position write lands.

## Constraints

- **Invariants 1, 2, 3, 7.** Upstream acks only after durable handling; one reader per source position; no drop on
  error or shutdown paths; shutdown drains every live run before the database closes.
- **Both engines.** v1 is the default, and arch-v2 is required by the RAG template. The arch-v2 ADR
  ([20260704-pipeline-architecture-v2]) bounds the cost of maintaining both: new lifecycle behaviour lands in both,
  or the arch-v2 gap is written down.
- **[20261007-stop-requested-never-recovers] stands.** A stop request recorded on a run is final for that run;
  shutdown refuses new runs; `Wait` counts live runs.
- **No clustering primitives.** This is one process's view of its own runs, with no leases or consensus.
- **Persisted status stays backward compatible.** The stored `encodableInstance` (`pkg/pipeline/store.go:132-145`)
  and the `Status` enum (`pkg/pipeline/instance.go:24-30`) keep their shape and values. A v0.20.0 store must load in
  v0.20.1 and the reverse. Anything new is in memory only.
- **No lock held across I/O.** `publishMu` is never held across a store write or a node operation
  (`pkg/lifecycle/service.go:99-100`); that stays true.

## Alternatives

### A: Fail closed. A run whose `Running` write fails is torn down

On a failed `Running` write, request a stop with a new reason, wait for the run's tomb to die, mark the pipeline
`Degraded` in memory with a coded error (`pipeline.status_persist_failed`), and return the error from `Start`.

- For: "`Start` returned an error, so nothing is running" becomes true. It is the simplest contract for API clients.
- Against:
  - A status-store blip kills a pipeline whose data path is healthy. During a recovery restart it turns a transient
    failure into `Degraded`, and with exit-on-degraded it shuts the process down.
  - It protects no data. When the store is really down, position writes fail and the data path already fails the run
    (see _Where status-write failures come from_). Fail-closed adds a second, coarser trip on top of that one.
  - `Start` has to wait for a drain to finish before it can return. That needs a bounded wait with a fallback to a
    forced stop, all on the error path of an API call.
  - The `Degraded` write that reports the teardown is likely to fail too, so the inconsistency it was meant to remove
    remains.
  - It does nothing for I3 (the races are not caused by status writes), and it still needs I5's tail fix.

Lost: it adds failure surface without protecting an invariant, and it covers only part of the problem.

### B: Fail open. Keep running and retry status persistence

Once workers are live, a failed status write is logged, counted and retried in the background. `Start` returns nil.
Memory stays authoritative for the process, and the store converges to memory.

- For: availability. It removes the orphan in I1 and the `Degraded`-over-live in I2 at the root, because after this
  change a run cannot fail once it is live. The data path keeps its own durability checks.
- Against, if adopted alone: it does not fix I3, which is an admission race and not a status-write problem, or I4 for
  superseded runs. The store can lag memory for the length of the outage, so a crash in that window boots from a
  stale status (see _Failure modes_).

Adopted, together with C and D.

### C: Derive liveness from the run registry for admission

`Start`, `Stop`, `Delete` and the recovery restart decide from `runningPipelines` and the run's phase, under
`publishMu`, instead of from the status field.

- For: it closes I3 and the plain concurrent-`Start` race, makes `Stop` work for a live run whatever its status
  shows, and stops `Delete` from removing a pipeline under a starting or recovering run.
- Against, if adopted alone: it makes v1's I1 **worse**. The orphaned run is not in the registry, so an
  admission-by-registry `Start` would admit a second run where today the stale `Running` status refuses it. C is only
  safe once B guarantees that every live run is registered.

Adopted, together with B and D.

### D: Key every per-run write by run

Each run carries a token. `UpdateStatus`, the terminal error and `notify` take effect only if the token is still the
pipeline's current run.

- For: I4 becomes impossible by construction, without reasoning about every path a superseded run's tail can take.
- Against, if adopted alone: it does not prevent the orphan (I1), the double start (I3) or the skipped tail (I5).

Adopted, together with B and C.

### E: Set memory only after the store write succeeds

Considered because the in-memory-first ordering looks like the source of the disagreement.

Rejected. A live run whose `Running` write failed would show the previous status (`UserStopped`, `SystemStopped`),
which admits `Start` and `Delete` and refuses `Stop`. The process would also be wrong about what it is doing itself.
Memory-first is the right order. The fix is to stop using the status field as a liveness check.

### F: Write status in the same transaction as positions

Rejected. Status transitions and position flushes have different owners and timing (the persister batches positions
asynchronously). Coupling them would make a status write wait on, or roll back with, a position batch, and it does
not address admission at all.

## Decision

Adopt B + C + D as five rules. Each rule names the interleaving it closes.

### R1: Status persistence never decides a live run's control flow (I1, I2)

- `runPipeline` may return an error only for failures before any goroutine of the run is on its tomb: admission,
  sink open and worker open in arch-v2, and nothing after node start in v1. Once any goroutine of the run exists,
  the run is registered and owns its own cleanup.
- v1 registers the cleanup goroutine **before** the `Running` write, gated on a `startupDone` channel exactly as
  arch-v2 does (`pkg/lifecycle-poc/service.go:1771`, `:2034`). The rollback delete at `pkg/lifecycle/service.go:1110`
  is removed. Its premise ("this run never went live") is false once the nodes are running.
- A failed `Running` write is logged at warn with code `pipeline.status_persist_failed`, counted, and handed to R5.
  `Start` returns nil. In arch-v2 `Start` stops returning that error.
- v1 writes status with `context.WithoutCancel(ctx)`, so a cancelled caller cannot fail the write.
- One exception fails closed: `UpdateStatus` returning `pipeline.not_found` for a live run means the pipeline was
  deleted under it. R3 makes that unreachable. If it happens anyway, the run is stopped (stop request recorded, as a
  user stop) and the condition is logged at error level.

Invariant comment at the enforcement site:
`// Invariant 7: once a run has goroutines it is registered and owns its cleanup; a status write cannot unpublish it`.

### R2: Every run's terminal tail always runs (I5)

The terminal status write's error is logged, counted and handed to R5. It no longer returns early. The terminal error
record, the compare-and-delete and `notify` always run. The cleanup goroutine returns the run's terminal error, as
today, and not the status-write error.

This restores exit-on-degraded for a fatal error whose `Degraded` write failed.

### R3: Admission by liveness, under `publishMu` (I3, part of I2)

Each registry entry gets a phase: `starting`, `live`, `backoff` (nodes dead, cleanup waiting out a recovery delay),
or `finishing` (nodes dead, terminal tail in progress).

- **`Start` reserves the pipeline ID under `publishMu` before it builds.** The reservation is granted if there is no
  entry, if the entry is `finishing`, or if the entry is `backoff`. A `backoff` entry is marked _superseded_ in the
  same critical section, so its pending restart is abandoned. Any other entry is refused with `ErrPipelineRunning`.
  The reservation becomes the published entry, or is released if the build fails.
- **The recovery restart reserves the same way**, passing its predecessor. It is granted only if the entry is still
  that predecessor and has not been superseded. Exactly one of a concurrent external `Start` and the pending restart
  wins. This replaces the unlocked guard at `pkg/lifecycle/service.go:341` and `pkg/lifecycle-poc/service.go:2173`.
- **`Stop` is admitted for an entry in `starting`, `live` or `backoff`**, whatever the status says. This replaces the
  status checks at `:396` and `:461`.
- **`Delete` and `Update` of a pipeline ask the lifecycle** whether it has an entry in `starting`, `live` or
  `backoff`, and are refused if it does. This replaces the status check at `pkg/orchestrator/pipelines.go:82`.
- The status field becomes a report only. Nothing in the lifecycle or the orchestrator branches on it.

`StopAll` already ignores status and stops every live tomb (#2912). That does not change.

Invariant comment: `// Invariant 2: at most one run per pipeline ID holds a reservation or a live entry`.

### R4: Per-run fencing of status, terminal error and notify (I4)

- `pipeline.Instance` gains an in-memory `run` token. It is not encoded (`encodableInstance` is unchanged).
- Publication sets the token under `publishMu`.
- The lifecycle calls a new `UpdateRunStatus(ctx, id, run, status, msg)`. It applies the write, in memory and in the
  store, only if `run` is the instance's current token. A stale write is dropped and logged at debug.
- The terminal error record and `notify` check the same token. A superseded run's failure does not reach `OnFailure`,
  so it cannot trip exit-on-degraded for a pipeline that is live again.
- `UpdateStatus` without a token stays available for non-lifecycle callers (provisioning, tests).

### R5: Write-behind for status writes that failed

The rule lives in `pipeline.Service`, so both engines share it.

- A failed `store.Set` marks the pipeline _unpersisted_. A single background goroutine retries unpersisted pipelines
  with capped backoff (1s doubling to 30s). Each retry writes the **current** in-memory snapshot, so the latest
  status wins and older failures are never replayed. A later synchronous write that succeeds clears the mark.
- `UpdateStatus`, `UpdateRunStatus` and the retrier serialise on a per-instance mutex that covers `Error`, the status
  and the encode. Today `Error` and the encode are unguarded, which is the race the `startupDone` comment at
  `pkg/lifecycle-poc/service.go:1613-1636` works around. API readers that read `Instance.Error` directly stay
  unguarded; moving them to a locked snapshot getter is in the same PR if the diff stays small, or a follow-up if not.
- `Flush(ctx)` makes one bounded pass (5s) over the unpersisted set. The runtime calls it after lifecycle `Wait` and
  before `connectorPersister.Wait()` / `CloseDB()`, in both cleanup paths (`pkg/conduit/runtime.go:893-896`,
  `:937-951`). After the flush the retrier stops. A pipeline still unpersisted at that point is logged at error
  level, together with what the next boot will see.
- `pipeline.Service.Init` also treats a stored `Recovering` as `Running` and converts it to `SystemStopped`. Today a
  crash during a recovery backoff leaves a stored `Recovering` that boot neither converts nor starts, so the pipeline
  shows `Recovering` with nothing running until someone starts it by hand. **This changes boot behaviour and needs
  DeVaris's decision** (see _Decisions needed_).

### What stays the same

- [20261007-stop-requested-never-recovers] in full: stop requests, terminal-status table, shutdown mode, live-run
  counter.
- The status enum, the persisted shape, the API and CLI output shape. No proto change.
- The data path: acks, positions, persister and DLQ are untouched.

## Failure modes

| Fault | Today | With this design |
| --- | --- | --- |
| Store blip fails one `Running` write | v1: orphaned live run (I1). arch-v2: `Start` errors while running | Run continues; write-behind lands it; warn log + metric |
| Same, on a recovery restart | `Degraded` over a live run, spurious exit-on-degraded (I2) | Restart is live and reported `Running` once the write lands |
| Store down for minutes (writes fail) | Positions fail, run fails, recovery loops; status writes fail in every arm (I5) | Same data-path behaviour (it is correct); status in memory is right; store converges when back; `OnFailure` fires |
| Store down permanently | As above, then `Degraded` after `MaxRetries`; tail skipped | `Degraded` in memory, `OnFailure` fires (exit-on-degraded works); shutdown flush logs what was not persisted |
| Disk full | As "store down" for writes; reads work | Same as "store down" |
| Crash mid status write | Store holds old or new value (single-key write, atomic in every backend) | Unchanged; the boot decision uses whichever landed |
| SIGKILL during a status outage | Boot reads the last landed status | Same; see the stale-status cases below |
| SIGTERM during a status outage | Terminal writes fail; tail skipped (I5) | Tail runs; flush tries once more; unpersisted pipelines logged with their boot outcome |
| Caller cancels `Start` (SQLite/Postgres, v1) | Orphaned live run (I1) | Write uses a detached context |
| Pipeline deleted while starting or in backoff | Allowed; the run lives on for a deleted pipeline | `Delete` refused (R3); if reached anyway, the run is stopped (R1 exception) |
| External `Start` racing a pending restart | Two live runs (I3) | One reservation wins; the other gets `ErrPipelineRunning` or abandons its restart |
| Two concurrent API `Start` calls | Two live runs unless the pipeline has processors | One wins (R3) |

Stale stored status after a crash during an outage. This is inherent: an unreachable store cannot record intent, and
none of the alternatives avoids it.

- Store says `UserStopped`, `Degraded` or `SystemStopped` for a run that was live: boot does or does not auto-start
  according to the stored value. The usual result is a pipeline that was running and is not restarted. That is an
  availability problem, not a data problem, and it is visible as a stopped pipeline.
- Store says `Running` for a pipeline the user had stopped: boot converts it to `SystemStopped` and starts it. The
  operator's stop is lost, but data is safe, because the run resumes from its last persisted position with
  at-least-once replay. The shutdown flush (R5) logs this case by name when the process exits cleanly. A SIGKILL
  gives no log line.
- Store says `Recovering`: with R5's `Init` change it is auto-started; without it, nothing runs and the pipeline
  shows `Recovering` until it is started by hand.

Write-behind failure modes:

- The retrier writes after `CloseDB`: prevented, because `Flush` stops it before `CloseDB` runs. A write that races
  the close returns an error and is dropped.
- The retrier and a synchronous write collide: both serialise on the per-instance mutex and both write the current
  snapshot, so whichever lands last carries the latest status.
- The retrier blocks shutdown: `Flush` is bounded at 5s and runs inside the existing `exitTimeout` budget.

## What operators see

- **Status.** API, CLI and UI show the in-memory status, unchanged. A pipeline that is moving data shows `Running`
  even while its status is not yet persisted. The pipeline's error field is not used for persistence problems, so its
  meaning from [20261007-stop-requested-never-recovers] is kept.
- **Errors.** A new code, `pipeline.status_persist_failed` (registered in `pkg/pipeline/codes.go`, gRPC `Unavailable`),
  appears in logs. `Start` and `Stop` no longer return it, because it is not a failure of the request. `Start` racing
  another `Start` or a pending restart returns the existing `pipeline.running`. `Delete` of a starting or recovering
  pipeline returns the existing running-pipeline error, with a suggestion to stop it first.
- **Metrics.** These are new and documented in `docs/metrics.md` in the same PR:
  - `conduit_pipeline_status_persist_failures_total{pipeline_name}` (counter): failed status writes, sync or retry.
  - `conduit_pipeline_status_unpersisted` (gauge): pipelines whose stored status lags memory. It should be 0;
    alert when it is above 0 for more than a few minutes.
- **Logs.**
  - Warn on the first failed write: pipeline ID, intended status, error code.
  - Info when the write-behind lands: attempts and elapsed time.
  - Error at shutdown for every pipeline still unpersisted: what is stored and what the next boot will do with it.
- **Health.** `/healthz` already reports the database (`docs/health_check.md`). No change.
- **Runbook.** `docs/operations/pipeline-status-persistence.md` ships with R5's PR:
  - _Symptom:_ `conduit_pipeline_status_unpersisted` above 0, or `pipeline.status_persist_failed` in logs.
  - _Diagnosis:_ `/healthz`, database logs, free disk. Check whether position writes are failing too (source errors,
    recovery attempts); if they are, the data path is already handling it.
  - _Remediation:_ restore the database, then watch the gauge return to 0. If Conduit must restart before then, stop
    it with SIGTERM, not SIGKILL, and read the shutdown error lines for any pipeline whose boot outcome will not match
    its current state.

## Test plan

Every PR's regression test is shown failing on `main` and passing with the fix, with both outputs in the PR body.

- **Fault-injecting pipeline service.** Generalise `failNthRunning` (`pkg/lifecycle-poc/cleanup_identity_test.go:39`)
  into a helper in each engine package that fails `UpdateStatus` or `UpdateRunStatus` selected by status, call index
  or run. Add one for v1, which has none today.
- **Fault-injecting database.** For R5: a `database.DB` wrapper that fails `Set` for the pipeline key prefix N times,
  or until released. It drives the write-behind, convergence, latest-wins and `Flush` tests without a real outage.
- **Deterministic hooks, no sleeps.** Reuse `testBeforePublish` (`pkg/lifecycle/service.go:122`) and
  `testWorkersReleased` (`pkg/lifecycle-poc/service.go:132`). Add one hook at the reservation and one after
  `StartWithBackoff`'s wait, so I3 is driven by construction.
- **Regression tests, one per interleaving, in both engines:**
  - I1: fail the first `Running` write. Assert that `Stop` reaches the run, the run drains, `Wait` returns before the
    timeout with the live-run count at 0, and the status reads `UserStopped`.
  - I2: fail the restart's `Running` write. Assert that the status ends `Running`, that `OnFailure` is not called and
    that `Stop` works. The #2811 test (`TestServiceLifecycle_Recovery_SupersededCleanupKeepsLiveEntry`) keeps its
    interleaving, and its `Degraded` expectation flips.
  - I3: hold the restart at the post-wait hook, call `Start`, release. Assert exactly one live run and that the loser
    gets `ErrPipelineRunning` or abandons its restart. Add a two-`Start` variant with no processors.
  - I4: a superseded run's terminal write. Assert that the current run's status and terminal error are untouched and
    that there is no `notify`.
  - I5: fail a `Degraded` write. Assert that `OnFailure` fires, the terminal error is recorded and the entry is gone.
  - R5: a failed write converges after the fault is released; latest wins over an older failed write; `Flush` is
    bounded; a stored `Recovering` boots as `SystemStopped`, if that decision is accepted.
  - All of the above under `-race -count=50`. The lifecycle flake history (#2897) is the reason for that count.
- **Chaos (`tests/chaos`, required check).** Add a status-outage scenario to the recovery child
  (`tests/chaos/recovery_child.go`, arch-v2) and a v1 equivalent. Pipeline-key writes fail while the run moves data.
  - SIGTERM: assert no record lost, no write after `CloseDB`, and `Wait` within budget.
  - SIGKILL mid-outage, then reboot: assert at-least-once and the boot decision the stored status implies.
- **Upgrade.** The persisted shape does not change, so the existing store round-trip tests cover it. Add one that
  loads a v0.20.0-written instance with a stored `Recovering` and checks the `Init` decision.
- **Not live, and not claimed.** Coverage floor and benchi gates (see CLAUDE.md _Process maturity_). These changes
  are off the record hot path. R5 adds no work per record.

## Implementation plan (v0.20.1)

Each item below is one small PR. All are Tier 1 and need DeVaris sign-off in a fresh session. They are ordered so each
is safe on its own.

1. **`fix(lifecycle): a failed Running write never unpublishes a live run` — #2898, #2899 item 1.** R1 in both
   engines: v1 registers its cleanup before the write and drops the rollback; arch-v2 stops returning the error; v1
   uses a detached context; adds the `pipeline.status_persist_failed` code and the failures counter. Tests I1 and I2.
   This PR alone removes the orphan and the `Degraded`-over-live.
2. **`fix(lifecycle): run the terminal tail when the status write fails` — #2899 item 4.** R2 in both engines.
   Test I5.
3. **`fix(lifecycle): admit Start, Stop and Delete by run liveness` — #2899 item 2 and the concurrent-`Start` race.**
   R3: entry phases, the reservation, the `Stop` and `Delete` admission changes, the hooks. Test I3. It depends on PR 1,
   because C without B worsens I1.
4. **`fix(lifecycle): fence status, terminal error and notify by run` — #2899 item 3.** R4: the `run` token,
   `UpdateRunStatus`, notify fencing. Test I4.
5. **`feat(pipeline): retry failed status writes and flush before shutdown` — closes the persistence half of #2898
   and #2899.** R5: write-behind, per-instance lock, `Flush` wired into both runtime cleanup paths, gauge, runbook,
   `docs/metrics.md`, plus the `Init` change if accepted. R5 tests, the chaos scenario, and the upgrade test.

PRs 1 and 2 are the minimum to close the invariant-7 exposure and can ship even if 3 to 5 slip. #2898 closes with
PR 1. #2899 closes when PRs 2 to 4 have merged.

## Implementation notes (PR 3, PR 4)

These are the places where the implementation differs from the rules above. All were found in review of the
implementation PRs (#2955, #2962) and accepted there.

- **`Start` is refused while a run is finishing.** R3 grants a `Start` when the registered run is `finishing`. In the
  implementation the run's terminal status write could then land over the new run's `Running`: a live run reported
  `UserStopped`, and two `UpdateStatus` calls raced on one `pipeline.Instance`.
  - So `Start` is refused while the previous run is finishing, with a new retryable code, `pipeline.stopping` (gRPC
`Unavailable`).
  - Only a run in recovery `backoff` is ever superseded.
  - PR 4's fencing would make the `finishing` grant safe, but the refusal stays. Relaxing it is a separate change.
- **`IsActive` counts a finishing run.** `Delete`, `Update` and the other orchestrator mutations wait for the finishing
  window as well, because a finishing run can still move to `backoff`.
- **`Stop` records the stop request under `publishMu`**, in every admitted phase: starting, live, backoff, finishing.
  - A recovery restart's reservation refuses a run whose stop was requested.
  - A `Stop` that lands on a recovery restart's reservation, or on a takeover `Start`'s reservation, is also recorded on
the run that reservation would replace. So if the start fails, the pipeline ends stopped.
- **The R4 fence lives in the lifecycle, not in `pipeline.Service`.** R4 puts a `run` token on `pipeline.Instance` and
  adds `UpdateRunStatus`. The implementation keeps the same guarantee at the only writer of run statuses, using the
  run's registry entry as the token:
  - a run writes the pipeline's status only while it is the registered run, under a per-pipeline status lock (reference-
counted) that the new run's `Running` write also takes;
  - a run records its terminal error and notifies only while it owns the pipeline.

  `pipeline.Service`, the `PipelineService` interface and the persisted instance are unchanged.
- **A failed takeover is recorded the same way whichever side finishes last.** The two sides are the `Start` that
  superseded a backoff run and then failed, and that run's cleanup. The outcome is:
  - the superseded run's own terminal decision, if it made one;
  - otherwise `SystemStopped` on shutdown;
  - `UserStopped` if a stop was requested;
  - `Degraded` with the start error in all other cases.

  This case is not in the rules above.
- **Status writes are bounded** at 30s (`statusWriteTimeout`). On badger, which ignores the context, the bound covers
  `Start`'s wait but not the write itself (#2964).
- **Follow-ups found in review:** a per-pipeline lock across the orchestrator's check and mutation (#2961), and
  provisioning still deciding "running" from status (#2965).

## Scope

- **[#2900] is out of scope.** It is a latency bug in `connector.Source.Teardown`: deferred acks are retried against
  a dead stream for the whole 10s budget. It has no status write in it and no correctness impact. The fix belongs in
  the ack delivery path, which is Tier 1 and needs its own review. The issue also has an open question to answer
  first: does a real gRPC plugin's `Send` fail the same way as the mock's? Two interactions are noted here:
  - The tests in this design must not depend on teardown time.
  - #2900 spends 10s of the 30s `exitTimeout` for each pipeline with a broken source. Pipelines drain in parallel, so
    that does not compound, but R5's 5s flush has to fit in what is left.

  It ships as its own v0.20.1 PR.
- **Observed while reading, not in scope:** `connector.Persister.flushNow` declares `err` again inside its batch
  loop (`pkg/connector/persister.go:381`). A failing per-connector `storeFunc` therefore does not stop the commit or
  reach the callbacks as an error. This was found by reading the code and has not been reproduced. It needs its own
  triage against invariant 1 and is noted here so it is not lost.
- **Not addressed:** the general "status lags intent across a crash during an outage" property (see _Failure modes_).
  Fixing it would need a store that accepts writes during its own outage.

## Decisions needed from DeVaris

1. **Fail open (B) over fail closed (A)** for status writes once a run is live. Recommended: fail open. The API
   contract changes slightly: arch-v2 `Start` no longer returns an error when only the status write failed.
2. **External `Start` during a recovery backoff supersedes the pending restart** (R3), rather than being refused.
   Recommended: supersede. It is what the existing guard intends, and it is the least surprising result for an
   operator.
3. **`Delete` and `Update` refused while a run is in recovery backoff** (R3). Recommended: yes. Today they are
   allowed, and the pending restart then runs against a deleted or changed pipeline.
4. **Boot treats a stored `Recovering` as `Running`** (R5): it is auto-started after a crash during recovery.
   Recommended: yes. The alternative is the current state: a stuck `Recovering` that needs a manual start.
5. **A superseded run's failure does not notify `OnFailure`** (R4), so it cannot trip exit-on-degraded. Recommended:
   yes.
6. **#2900 is out of scope** of this design and ships as its own v0.20.1 PR.

## Related

- [#2898], [#2899], [#2900]: the issues this document decides.
- #2894 (compare-and-delete in arch-v2 cleanup), #2904 (tomb `Kill` before `Done`), #2912 (stop requested never
  recovers, first request wins, `StopAll` stops every live tomb, live-run counter in `Wait`,
  `pipeline.shutting_down`), #2917 (API docs for stopped status and error).
- [20261007-stop-requested-never-recovers]: the stop semantics this design builds on.
- [20260704-pipeline-architecture-v2]: why every lifecycle rule lands in both engines.
- [20240812-recover-from-pipeline-errors]: the recovery mechanism whose restart path R3 changes.
- [20260704-graceful-shutdown-sigterm]: the shutdown sequence R5's `Flush` joins.

[#2898]: https://github.com/ConduitIO/conduit/issues/2898
[#2899]: https://github.com/ConduitIO/conduit/issues/2899
[#2900]: https://github.com/ConduitIO/conduit/issues/2900
[20261007-stop-requested-never-recovers]: ../architecture-decision-records/20261007-stop-requested-never-recovers.md
[20260704-pipeline-architecture-v2]: ../architecture-decision-records/20260704-pipeline-architecture-v2.md
[20240812-recover-from-pipeline-errors]: 20240812-recover-from-pipeline-errors.md
[20260704-graceful-shutdown-sigterm]: 20260704-graceful-shutdown-sigterm.md
