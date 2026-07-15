# Pipeline pause semantics

## Summary

Recommendation, up front: **do not add a warm-idle "pause" primitive to the engine for v0.18.**
Model pause/resume in the UI and API as the existing user-initiated **stop/start**, which already
delivers correct, crash-safe, at-least-once park-and-resume from the last committed position, and
is already distinguishable from a system stop via `stopped_reason = USER` (P3, #2629).

A dedicated pause that keeps connector plugin subprocesses warm is a genuinely new data-path
lifecycle path touching invariants 1, 3, 5, and 7 — for a resume-latency optimization with no
demonstrated demand. It is deferred. This document captures its design sketch and the invariants it
must satisfy so future work, if demand appears, starts from here rather than from scratch.

This overturns an assumption in the greenfield-UI design doc
(`20260713-greenfield-built-in-ui.md`), which lists `pause` as prerequisite Tier-2 engine work. The
practical consequence is a positive one: **UI-6 (operate) is not blocked on any pause engine work —
it can ship against the existing `StartPipeline`/`StopPipeline` RPCs.**

## Context

### What "pause" is asked to do

Operators want to temporarily halt a pipeline's processing and later resume it, without deleting it
and without losing position — to relieve a struggling downstream, take a sink offline for
maintenance, or investigate a problem. The greenfield-UI epic wants a clear pause/resume affordance
in the operate surface (UI-6).

### What a user stop already does (verified against the tree)

The park-and-resume need is, in substance, already met by a graceful user-initiated stop:

- **Stops reading, drains in-flight to durable.** Graceful stop injects a
  `ControlMessageStopSourceNode` into the stream; the source stops reading new records but in-flight
  records continue draining downstream (`pkg/lifecycle/stream/source.go:193-244`). The source node
  defers `openMsgTracker.Wait()` _before_ `Source.Teardown`
  (`pkg/lifecycle/stream/source.go:120-129`) — teardown blocks until every in-flight message is
  acked or nacked end-to-end. The destination mirrors this
  (`pkg/lifecycle/stream/destination.go:85-99`). This upholds invariants 1 and 3.
- **Persists position per ack.** `Source.Ack` records the last position and triggers
  `persister.Persist` (`pkg/connector/source.go:207-238`); the persister flushes transactionally on
  connector stop (`pkg/connector/persister.go:125-130, 238-259`). `StopAndWait` additionally waits
  for drain and `WaitPersisted()` durability (`pkg/lifecycle/service.go:475-490`). Invariants 2, 5.
- **Is not auto-restarted.** `Init` auto-restarts only pipelines in `StatusSystemStopped`
  (`pkg/lifecycle/service.go:171`). A user stop sets `StatusUserStopped`
  (`pkg/pipeline/instance.go:24-30`; enforced for a manual stop at `pkg/lifecycle/service.go:883`)
  and stays parked until an explicit `StartPipeline`.
- **Resumes from the last committed position.** `Source.open` re-opens the plugin from
  `s.state().Position` (`pkg/connector/source.go:302-311`), giving at-least-once resume (the
  un-flushed tail may replay — the floor, not a regression).
- **Is already labelable.** P3's `stopped_reason = USER` distinguishes "an operator parked this"
  from a system stop, so the UI and agents can already reason about intent without a new state.

### The only things a dedicated pause would add

1. **Warm plugins.** A graceful stop tears down and **kills** connector plugin subprocesses:
   `Teardown` → standalone dispenser `teardown()` → `client.Kill()`
   (`pkg/connector/source.go:150-180`, `pkg/plugin/connector/standalone/dispenser.go:76-90`).
   Resuming re-dispenses (re-spawns) the subprocess and re-opens the stream
   (`pkg/connector/source.go:64-129`). A warm pause would avoid that re-open cost. This is primarily
   a **latency optimization**; the one exception is a **snapshotting source that cannot resume
   mid-snapshot** — cold resume re-snapshots from the start, producing large duplicate volume. That
   is a real cost/capability delta, not just latency, but it stays **within** the at-least-once
   floor (invariant 3) and is identical to what a crash-restart already does today — not a
   correctness gain a warm pause would uniquely provide.
2. **A distinct `Paused` status label** — presentational; `stopped_reason = USER` already carries
   the signal.

## Constraints

- **Data-integrity invariants 1–7 are non-negotiable.** Any pause must never ack early (1), skip or
  corrupt positions (2), drop a record (3), reorder within a partition (4), tear a checkpoint
  write (5), mangle schema (6), or lose data on `SIGTERM`/`kill -9` (7).
- **Serialized-format discipline.** The pipeline status is persisted (JSON, key prefix
  `pipeline:instance:`, `pkg/pipeline/store.go:31-47`). Adding a status value is a serialized-format
  change requiring a versioned migration path and an upgrade test (N reads N+1's store safely, and
  vice versa).
- **No speculative generality; state-layer discipline.** Interfaces and subsystems earn their place
  with real demand or two implementations. A pause subsystem with no demonstrated operator workflow
  that needs warm resume is a YAGNI risk.

## Alternatives

### Alternative A — warm-idle pause (keep plugins alive, idle in place) — deferred

**Sketch.** A new control-message type injected into `SourceNode.Run`'s loop that stops calling
`Source.Read` but does **not** return from `Run` — because returning runs the deferred
`Teardown`/`openMsgTracker.Wait()` and kills the plugin (`pkg/lifecycle/stream/source.go:120-129`).
The loop idles in place: the tomb stays alive
because the idling source-node goroutine never returns (`pkg/lifecycle/service.go:802-856`) — note
this is _not_ the `keepAlive` startup guard at `service.go:812-817`, which is closed by its own
`defer` — and the connector context stays uncanceled. A new `StatusPaused` is persisted; `Init` must
neither auto-restart nor auto-resume it. The destination, which only drains and tears down on channel
close (`pkg/lifecycle/stream/destination.go:85-99`), needs its own idle state to keep its plugin
warm.
In-flight records still cannot be "held" mid-DAG — `OpenMessagesTracker` forces every message to a
terminal acked/nacked state (`pkg/lifecycle/stream/message.go:296-314`) — so even a warm pause must
drain in-flight to durable before it can declare itself paused, exactly like stop.

**Why it loses (for now).** It adds substantial new data-path surface — an idle path in the source
run loop, a matching destination idle path, interaction with the recover/backoff restarter
(`pkg/lifecycle/service.go:896, 937`), a defined meaning for force-stop against a paused pipeline
(`service.go:346`), a new persisted status with migration, and a `kill -9`-during-warm-pause
recovery story — all to save connector re-open latency on resume. The operator workflows that
motivate pause (relieve pressure, sink maintenance, investigate) are low-frequency and tolerate a
cold resume. The high-frequency pause/resume that would justify warm-idle is not a real Conduit
operator workflow (flow control is already handled by in-stream backpressure). Building this now
would violate "no speculative generality" and put invariants 1/3/5/7 at risk for an optimization
nobody has asked for.

### Alternative B — first-class `Paused` state that reuses stop machinery — available, deferred

**Sketch.** Pause is a user stop that persists a distinct `StatusPaused` (or reuses
`StatusUserStopped` plus a flag): it drains, tears down, and resumes via the existing
re-open-from-position path — **identical data-path behavior to stop**, cold resume included. The only
delta is a first-class state so the API and agents can say "paused by operator" as distinct from
"stopped."

**Why deferred.** `stopped_reason = USER` already carries "an operator parked this." A separate
`StatusPaused` buys a purely presentational distinction at the cost of a serialized-format
migration, enum churn, and touching the auto-restart logic. Worth doing only if product or agent
ergonomics later need "paused (expected to resume)" as a distinct first-class state from "stopped
(may be torn down)." Low risk, but not yet earning its keep.

### Alternative C — no engine change: pause/resume _is_ user stop/start — recommended for v0.18

UI-6 offers **Pause**/**Resume** mapped to the existing `StopPipeline{force:false}` /
`StartPipeline` RPCs (`proto/api/v1/api.proto:457-487`). A user-stopped pipeline
(`stopped_reason = USER`) is presented as paused/resumable. No engine change, no format migration,
full reuse of proven drain/resume. Resume is cold (plugin re-open from the last committed position),
which is acceptable for the motivating operator workflows.

**Cost / caveat.** A "Pause" label over a stop can imply warm resume. Mitigate with honest UI copy
(e.g. "Resuming re-opens connectors from the last committed position"). If cold-resume latency
proves painful in real use, escalate to Alternative A **with numbers** (a benchi measurement of
re-open cost on representative connectors), not on speculation.

## Recommendation

Adopt **Alternative C** for v0.18. Defer **A** (warm-idle) pending demonstrated demand backed by
benchi numbers; keep **B** (first-class `Paused` state) as a cheap, low-risk middle step if a
distinct state is later wanted for API/agent ergonomics. This unblocks UI-6 with zero engine risk
and no data-path change.

## Failure modes

Because the recommended path is exactly the existing stop/start, its failure modes are already
covered by that machinery; the UI must respect them:

- **Stop is only valid from `Running` or `Recovering`** (`pkg/lifecycle/service.go:297`). Pausing a
  `Degraded`/`Recovering`/already-stopped pipeline must surface the same stable error the RPC
  returns, not a UI-invented state. (Errors are API — machine-actionable code + path.)
- **Stop returns before drain completes.** Plain `Stop` injects the control message via
  `stopGraceful` and returns; it does not block on drain (`pkg/lifecycle/service.go:309-336`;
  described in the `StopAndWait` godoc at `service.go:449-456`; CLI at
  `cmd/conduit/root/pipelines/stop.go`).
  The UI must render a transient "pausing…" state and reconcile from polled status, not assume the
  pipeline is quiesced the instant the call returns.
- **Crash mid-drain / crash while paused.** Resume is from the last flushed position (at-least-once;
  the un-flushed tail replays). No new recovery story — identical to stop. Invariants 2, 3, 7.
- **Resume with drifted remote state.** A source whose upstream changed while paused (e.g. a rotated
  WAL slot) re-opens from the persisted position. This is the crux of the deferral argument: a cold
  resume is **byte-for-byte the same operation as crash recovery**, which invariant 7 already
  requires every connector to survive without loss. Therefore a warm pause adds **zero correctness**
  over stop/start — only resume latency. Any connector that would lose data across a park is already
  broken across a crash, independent of pause.
- **Double-resume / double-pause.** `StartPipeline`/`StopPipeline` idempotency is the RPCs' existing
  contract; the UI disables the control while a transition is in flight.
- **Force-stop.** `--force` is crash-equivalent, not lossy (`cmd/conduit/root/pipelines/stop.go`).
  It is not offered as "pause"; if surfaced at all it is a separate, clearly-labeled destructive
  action.

If Alternative A or B is pursued later, its failure modes (warm-pause `kill -9` recovery, backoff
interaction, destination idle, unknown-status compatibility) must be enumerated in that doc before
any code — they are the reason this document defers it.

## Upgrade / rollback

- **Recommended path (C):** no serialized-format change, no migration, no new status value. The
  change is UI/label and API usage only. Rollback is reverting the UI.
- **If B/A is pursued later:** `StatusPaused` is a new persisted enum value. Define the
  compatibility rule first — an older engine reading a newer store must treat an unknown status
  safely (e.g. as stopped, not as running), and a newer engine must read old stores unchanged. This
  requires an upgrade/downgrade test and is a gating requirement for that future work, per
  serialized-format discipline.

## Observability

- **Recommended path (C):** reuse the existing pipeline status surface; ensure a user-stopped
  ("paused") pipeline is queryable via the same status metric/label the fleet view already reads. No
  new metric.
- **If a distinct `Paused` state is added later:** add it to the pipeline status gauge and write a
  runbook entry — a paused pipeline that is not processing is expected, not an alert — so operators
  and alerting do not treat an intentional pause as an outage.

## Related

- P3 additive `Pipeline.State.stopped_reason` (USER/SYSTEM), #2629 — the signal that already
  distinguishes an operator park from a system stop.
- `20260713-greenfield-built-in-ui.md` — the epic that assumed pause was prerequisite engine work;
  this doc revises that. UI-6 (operate) proceeds against existing `Start`/`Stop`.
- `20260704-graceful-shutdown-sigterm.md`, `20240812-recover-from-pipeline-errors.md` — the drain,
  checkpoint, and recovery behavior this design relies on.
- ROADMAP v0.18. If accepted, the "no warm-pause primitive" decision is a candidate to promote to an
  immutable ADR so the rationale is not re-litigated.
