# Drain-safe live reconfigure for arch-v2 (Preview.PipelineArchV2 / funnel lifecycle)

## Summary

Ports `StopAndWait` and `ReconfigureProcessor` from `pkg/lifecycle` (v1) to the experimental
`pkg/lifecycle-poc` (arch-v2 / funnel) lifecycle service, removing the `CodeStopAndWaitUnsupported`
refusal guard those two methods have carried since the `Preview.PipelineArchV2` flag was introduced.
This closes the "Open parity item" flagged in
[20260708-live-server-deploy-apply.md](20260708-live-server-deploy-apply.md): `ApplyPlanLive`
(`provisioning.Service`) can now apply live changes to a running pipeline under arch-v2 exactly as it
already does under v1, with the same drain/durability guarantee. **Tier 1** — this is a data-path
change (stop/drain/restart of a running pipeline) plus a fix to the arch-v2 error-recovery loop the
same PR train landed a few commits ago (`a61d4bc`, #2718).

This PR also fixes a race the recovery port introduced (O3, below): a deliberate, operator-initiated
`Stop(force=false)` that races a transient (non-fatal) error surfacing from the drain itself was
being misclassified as a spontaneous failure and auto-restarted — restarting a pipeline the operator
just told to stop. This is fixed generically, for any caller of `Stop`, not just `StopAndWait`.

## Context

`pkg/lifecycle-poc` is the experimental `funnel`-based pipeline runtime behind
`Preview.PipelineArchV2` (`runtime.go:444`). Until this PR, its `StopAndWait` and
`ReconfigureProcessor` unconditionally refused with `CodeStopAndWaitUnsupported`
(`lifecycle-poc/service.go`, pre-this-PR): the funnel's `Stop`/`WaitPipeline`/`Persister` interaction
had never been audited the way v1's was for the original live-apply review, so building
`provisioning.Service.ApplyPlanLive`'s stop-drain-restart on top of it would have skipped the exact
safety case that review existed to establish.

Since that guard was added, two things changed the ground under it:

1. **The arch-v2 error-recovery loop was ported** (`a61d4bc`, #2718): `pkg/lifecycle-poc/service.go`
   now has `recoverPipeline`, `StartWithBackoff`, and a live (no longer commented-out) recovery arm
   in `runPipeline`'s cleanup goroutine. A prerequisite fix for that port (`83e6abf`, #2716) already
   fatal-tags a user force-stop so it is never auto-recovered — but that fix only covers the
   **forceful** stop path. The **graceful** path (`Stop(force=false)`, which `StopAndWait` calls) had
   no equivalent protection: see O3 below.
2. **The funnel's specific drain mechanics can now be audited** (§3.1) with the recovery port's own
   worker/source code in front of us, closing the parity gap the original guard was waiting on.

### What "arch-v2" changes about the drain mechanism

v1's `Stop`/`StopAndWait` (`pkg/lifecycle/service.go:278-490`) work over a node graph
(`stream.Node`), where `Stop` injects a stop-control-message into the source node and
`stream.SourceNode.Run`'s `openMsgTracker.Wait()` is the quiescence barrier.

Arch-v2's `Stop`/`Worker.Stop` (`pkg/lifecycle-poc/funnel/worker.go:199-228`) work over a single
`funnel.Worker` processing one batch at a time through a linear task chain (source → processors →
destination), synchronized by `processingLock` — a different mechanism, which is why the original
guard insisted on a fresh audit rather than assuming v1's proof transferred.

## Decision

### O1 — `ReconfigureProcessor`: reuse the v1 sentinel, no live-swap capability

Arch-v2 has no equivalent of `stream.ProcessorNode.Reconfigure` — no live in-place processor swap at
all. `ReconfigureProcessor` therefore **unconditionally** returns
`lifecyclev1.ErrProcessorNotLiveReconfigurable` (wrapped with the pipeline/processor IDs for
diagnostics), reusing the v1 sentinel rather than minting a v2-specific one:

```go
func (s *Service) ReconfigureProcessor(_ context.Context, pipelineID, processorID string) error {
	return cerrors.Errorf("%w: processor %q in pipeline %q (Preview.PipelineArchV2 has no live in-place reconfigure yet)",
		lifecyclev1.ErrProcessorNotLiveReconfigurable, processorID, pipelineID)
}
```

This keeps `provisioning.applyInPlace`'s existing `cerrors.Is(err, lifecycle.ErrProcessorNotLiveReconfigurable)`
match (`plan.go:663`) unchanged — no arch-v2-specific branch in `provisioning` at all. The package
coupling this relies on already exists: `lifecycle-poc/service.go` already imports
`lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"` for `ErrRecoveryCfg`.
`TestApplyPlanLive_ArchV2_ProcessorUpdate_FallsBackToRestart` (`pkg/provisioning`) wires a **real**
`lifecycle-poc.Service` (not a mock) into `provisioning.Service` and proves the same
`processorUpdateFixture` that applies genuinely in place under v1 falls through to the
`StopAndWait`+`Start` restart path under arch-v2.

### O2 — bounding the drain

The plan called this "the single most important correctness/liveness question" of this port, and the
answer is: **yes, `WaitPipeline`/`Stop` can hang, and now it is explicitly bounded.**

`funnel.Worker.Stop` acquires `processingLock` before doing anything else
(`worker.go:205-209`). That lock is held by `doTask` for the **entire duration** of a batch's
processing — from right after the source read succeeds until the batch has flowed all the way
through every downstream task (`worker.go:332-337`). If a destination `Write`/`Ack` round-trip never
returns (a wedged connection, a stuck disk, a hung plugin), the batch holding `processingLock` never
releases it, and `Worker.Stop`'s `acquireProcessingLock` — and therefore `Stop`, `StopAndWait`, and
transitively `provisioning.Service.ApplyPlanLive` — blocks forever.

**Resolution:** `StopAndWait` now bounds its entire sequence (`Stop` → drain-wait → persist-wait) by
a single deadline: `DefaultStopAndWaitTimeout` (30s), or a tighter deadline already set on the
caller's `ctx`, whichever is sooner.

```go
deadline := time.Now().Add(DefaultStopAndWaitTimeout)
if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
	deadline = d
}
```

This deadline is threaded into `Stop`'s own `ctx`, which `acquireProcessingLock`'s `select` already
honors (`worker.go:233-241`) — no new cancellation plumbing was needed inside `funnel.Worker` at all;
the bound was missing purely at the `StopAndWait` call site. The drain-wait (`WaitPipeline`) and
persist-wait (`connectors.WaitPersisted`) are each separately bounded by the same remaining budget via
a small `waitBounded` helper (goroutine + `select`/`time.After`, mirroring the existing `Wait(timeout)`
method's own idiom and `connector.Persister.WaitPendingWritesContext`'s doc on why the underlying
wait is not itself abortable).

On timeout, `StopAndWait` returns a coded `CodeStopAndWaitTimeout` error and does **not** force-kill
anything:

- If `Stop` itself times out (the common case: the wedge holds the lock), `Worker.Stop` never reaches
  `w.stop.Store(true)` — the worker's internal stop flag is never set, so the pipeline is left exactly
  as it was: still genuinely `StatusRunning`, still working through the (from the caller's point of
  view, still wedged) batch. Nothing is torn down, nothing is acked, nothing is lost — safe to retry
  later, or to leave running while an operator investigates the stuck destination.
- If `Stop` succeeds but the drain or persist wait times out (a slower, more contained case — the
  worker did stop, but the tomb join or persister flush itself is slow), the same reasoning applies:
  the bound only stops **this caller from waiting**, it never cancels the underlying work.

This is deliberately the same "benign, never a silent gap" contract
`connector.Source.Teardown`'s own bounded-wait fallback already establishes
(`connector/source.go`, `DefaultTeardownFlushTimeout`): a caller that gives up waiting is not the same
as an operation that gives up trying. `TestServiceLifecycle_StopAndWait_Timeout`
(`pkg/lifecycle-poc`) proves the bound fires against a deterministically wedged destination
(`pmock.DestinationPluginWithControlledError`) and that the pipeline is left `StatusRunning`, not
torn down or degraded, at the moment the timeout is observed.

**A correctness nuance found while implementing this:** `stopRunnablePipeline` marks a run as
`intentionalStop` (see O3) **before** calling `Worker.Stop`. If that call then fails outright because
of this very timeout (rather than because the drain genuinely happened), the worker's stop flag was
never set — the pipeline is still running unattended. If `intentionalStop` were left `true` in that
case, a **later, unrelated** transient error on this same (never-actually-stopped) run would be
misclassified as an already-completed intentional stop and would skip auto-recovery it should still
be eligible for. `stopRunnablePipeline` therefore rolls `intentionalStop` back to `false` when
`Worker.Stop` returns a non-nil error, so this only applies to a stop attempt that actually happened.

### O3 — recovery-suppression on intentional stop

The recovery port (`a61d4bc`) added a live `runPipeline` cleanup-goroutine switch that classifies a
worker's terminal error as fatal (degrade), graceful (`isGracefulShutdown`, `StopAll`-triggered →
`StatusSystemStopped`), or — the `default` arm — transient, and hands transient errors to
`recoverPipeline` for bounded-backoff auto-restart. This is correct for a **spontaneous** failure. It
is wrong for a **deliberate** one: if an operator (or, after this PR, `StopAndWait`) calls
`Stop(force=false)` and the drain itself then surfaces a non-fatal error — e.g. a batch already
in-flight when `Stop` was called finishes unwinding because its destination write failed — that error
falls into the same `default` arm and auto-restarts a pipeline the caller just told to stop.

`83e6abf` (#2716) had already closed the equivalent gap for **forceful** stop, by fatal-tagging
`ErrForceStop` so it routes to the fatal arm (`StatusDegraded`) instead of `default`. Graceful stop
has no error to fatal-tag — `Stop(force=false)` doesn't kill the tomb directly at all; the worker's
own `Do` loop returns whatever error the drain produces, and that return value is not under
`stopRunnablePipeline`'s control. So this needed a different fix.

**Resolution:** a new field on `runnablePipeline`:

```go
// intentionalStop is set by stopRunnablePipeline's graceful-stop branch,
// before it calls rp.w.Stop, to mark this run as one an operator ... deliberately
// asked to stop...
intentionalStop atomic.Bool
```

`stopRunnablePipeline`'s graceful (`force=false`) branch sets it **before** calling `rp.w.Stop`:

```go
rp.intentionalStop.Store(true)
err := rp.w.Stop(ctx)
if err != nil {
	rp.intentionalStop.Store(false) // see O2's nuance above
}
return err
```

`runPipeline`'s cleanup switch gains a new arm, checked **after** `isGracefulShutdown` (so a
`StopAll`-triggered system shutdown still reports `StatusSystemStopped`, unchanged) and **before**
the recovery `default`:

```go
case rp.intentionalStop.Load():
	err = nil
	if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusUserStopped, ""); updateErr != nil {
		return updateErr
	}
```

**Design choice: plain field, not pointer-shared like `recoveryAttempts`.** `backoff` and
`recoveryAttempts` are deliberately carried over to a new `runnablePipeline` across a recovery
restart (`Start` copies them from the old `rp`), because `MaxRetries` must bound the **whole** retry
sequence, not reset every attempt. `intentionalStop` must do the **opposite**: an intentional stop
must never survive a restart, because there is no restart to survive — a stopped pipeline is stopped.
A fresh `runnablePipeline` (built by a later `Start`, whether operator-initiated or a genuine
recovery restart from an unrelated, later failure) always begins with `intentionalStop` false, so it
is not permanently marked "user-stopped" by a previous run's marker.

**Regression test** (`TestServiceLifecycle_Stop_TransientErrorMidDrain_NoRecovery`,
`pkg/lifecycle-poc/service_test.go`): a single record is read and reaches the destination, in flight
and holding `processingLock`, deterministically parked there via
`pmock.DestinationPluginWithControlledError` (a new, small mock helper). The test calls
`Stop(force=false)` in a goroutine, polls the real `runnablePipeline.intentionalStop` field directly
(this test is in-package) until it observes `true` — the precise condition `runPipeline`'s cleanup
goroutine will check — and only then releases the destination to fail with a plain, non-fatal error.
This reproduces "Stop already recorded this as intentional when the transient error surfaced"
deterministically, with no timing-based flakiness. Verified to **fail without the fix** (the pipeline
enters `StatusRecovering` and the source/destination dispensers are invoked a second time, violating
their `Times(1)` gomock expectations) and **pass with it** (status sequence is exactly
`[Running, UserStopped]`, dispensers each called exactly once).

### O4 — DLQ / `WaitPersisted` coverage

Checked as part of the §3.1 audit below: `connector.Service.WaitPersisted` (`connector/service.go:106`)
delegates to `Persister.WaitPendingWrites`, which the shared `connector.Persister` batches **across
every connector it manages** — including the DLQ destination, which `lifecycle-poc.Service.buildDLQ`
creates through the same `s.connectors.Create` (and thus the same persister) as every other
connector. There is no DLQ-specific persistence path this misses. **No gap found.**

### §3.1 — the funnel drain audit

This is the audit the original `CodeStopAndWaitUnsupported` guard was waiting on. It walks the same
four properties v1's audit established
([20260708-live-server-deploy-apply.md](20260708-live-server-deploy-apply.md), "Blocker 1"), against
arch-v2's actual code.

**1. Quiescence: no batch is mid-flight the instant `Stop`'s lock acquisition succeeds.**
`processingLock` (`worker.go:88-90`) is a buffered channel of size 1, acquired (send) by `doTask` for
the **first** task in a batch right after that task's own work succeeds (`worker.go:332-337`), and
released (receive, via `defer`) only once that entire batch — first task through every downstream
task — has finished (`worker.go:337`, the `defer release()` sits at the frame that wraps the whole
recursive `doNextTask` chain). `Worker.Stop` acquires the same lock before doing anything else
(`worker.go:205-209`). So the instant `Stop`'s acquisition succeeds, no batch is between "read from
source" and "fully processed" — quiescence, by construction of a single mutual-exclusion point every
batch and every stop both go through.

**2. A batch read but never finished before the stop signal is thrown away without acking — a
benign duplicate, never a gap.** `doTask`'s handling of the **next** batch after `Stop` has already
run: if a new batch's first-task read succeeds but, by the time it re-acquires `processingLock`,
`w.stop.Load()` is already true, the batch is discarded **without ever calling `Ack`**
(`worker.go:339-347`, "stop signal received just before starting to process next batch, gracefully
stopping without flushing the batch"). Because it was never acked, `Source.Ack` never advanced the
persisted position past it — a restart re-reads and redelivers exactly this batch. At-least-once
(invariant 3) holds; this can never produce a gap, only (at worst) a duplicate, which the record
pipeline already tolerates end-to-end.

**3. `Source.Teardown` (invoked by `Worker.Stop`'s `tearDownSource`, `worker.go:224`) forces a flush
and drains the deferred-ack delivery goroutine before returning.** This is the exact machinery
Approach A2 (issue #2680) built, and the SIGKILL chaos suite already exercises it
(`connector/source.go:292-346`, `DefaultTeardownFlushTimeout`): every position `Source.Ack` accepted
before teardown is forced to flush, and the deferred plugin-ack for it is drained (sent) before the
stream is torn down, bounded so a stalled disk cannot hang shutdown forever. This machinery is
**identical** whether it is reached from arch-v2's `Worker.Stop` or v1's node-graph teardown path —
it lives entirely in `pkg/connector`, below both lifecycle implementations. Nothing arch-v2-specific
had to be re-verified here; it inherits v1's proof by sharing the same `connector.Source`.

**4. `WaitPipeline` (the tomb join) and `connectors.WaitPersisted` (the persister's pending-write
barrier) are the pipeline-wide barriers a caller needs.** `WaitPipeline` blocks on `rp.t.Wait()` —
every goroutine `runPipeline` spawned (the worker goroutine and the cleanup goroutine) has exited by
the time it returns. `connectors.WaitPersisted` blocks on `Persister.WaitPendingWrites`, which the
persister's own doc establishes is race-free to call **after** learning (via the tomb join) that
`ConnectorStopped` has already fired for the connector in question (`persister.go:189-226`) — exactly
the ordering `StopAndWait` uses (`Stop` → `WaitPipeline` → `WaitPersisted`, strictly sequential).

**Conclusion:** the funnel's `Stop`/`WaitPipeline`/`Persister` interaction gives the same
quiescence-and-durability guarantee v1's audit established for the node graph, via a different but
equally sound mechanism (`processingLock` instead of `openMsgTracker`). It is therefore safe to build
`StopAndWait` on top of it, **provided** the drain is explicitly bounded (O2) and a deliberate stop is
never misread as a failure needing recovery (O3) — both of which this PR also fixes, in the same PR,
per AC-7.

### AC-7: guard removal in the same PR as the chaos test

`CodeStopAndWaitUnsupported` is removed from `pkg/lifecycle-poc/codes.go` in this same PR, alongside
the chaos test proving the new `StopAndWait` (`tests/chaos`). All prior references (the two refusal
call sites, `stop_and_wait_test.go`'s pinning tests, and `llms-full.txt`) are updated or removed in
this PR; `apply_plan_live_test.go`'s descriptive comment referencing the old code is reworded (it
never depended on the real symbol — it used a stand-in error). `CodeStopAndWaitTimeout` is the only
registered code this package needs going forward; it means something different from the removed one
("this specific attempt did not complete in time," not "this arch cannot do this at all").

## Failure modes

- **Wedged destination during `StopAndWait`.** Covered by O2: bounded, benign, no force-kill, no gap.
  Chaos-tested (AC-6).
- **Operator `Stop` racing a transient drain error.** Covered by O3: finalizes `StatusUserStopped`,
  never auto-restarted. Regression-tested.
- **`StopAndWait` timeout leaves `intentionalStop` stale on a run that never actually stopped.**
  Found and fixed during implementation (see O2's "correctness nuance"): rolled back on a failed
  `Worker.Stop` call, so a later unrelated error on the same run is still recovery-eligible.
- **Crash (SIGKILL) mid-`StopAndWait`.** Unchanged from the existing, already-proven-safe crash path:
  `Source.Teardown`'s bounded flush-and-drain and the SIGKILL chaos suite already establish "at worst
  a benign duplicate, never a gap" independent of which lifecycle service (v1 or arch-v2) initiated
  the stop. This PR does not weaken that; the new O2 bound only governs how long a **live, running**
  process waits before giving up, not what happens on an actual crash.
- **Concurrent `Stop` and `StopAndWait` calls for the same pipeline.** Both funnel through the same
  `Service.Stop`/`stopRunnablePipeline`, which already serializes via the single `runningPipelines`
  entry and `Worker`'s own `processingLock`; no new concurrency surface is introduced by this PR.
- **DLQ writes outstanding at `WaitPersisted` time.** Checked under O4: no gap, the DLQ shares the
  same `connector.Persister` as every other connector.
- **`ReconfigureProcessor` silently no-op'ing instead of falling back.** Guarded by the existing
  `provisioning.applyInPlace` `cerrors.Is` match, which this PR's `ReconfigureProcessor` is
  specifically designed to satisfy; pinned by
  `TestApplyPlanLive_ArchV2_ProcessorUpdate_FallsBackToRestart` using the **real** v2 service, not a
  mock.

## Acceptance criteria

| # | Criterion | Test |
| --- | --- | --- |
| AC-1 | A live-apply-under-load drain delivers every pre-apply-acked record durably downstream | `tests/chaos` new drain test |
| AC-2 | (see plan; folded into AC-1/AC-3 coverage below) | — |
| AC-3 | The final position is checkpointed (durably persisted) after a successful `StopAndWait` | `TestServiceLifecycle_StopAndWait_DrainsAndPersists`; `tests/chaos` new drain test |
| AC-4 | (see plan; folded into O1 coverage below) | — |
| AC-5 | `ReconfigureProcessor` always falls back to restart under arch-v2 | `TestServiceLifecycle_ReconfigureProcessor_FallsBackToRestart`; `TestApplyPlanLive_ArchV2_ProcessorUpdate_FallsBackToRestart` |
| AC-6 | The O2 bound fires against a stuck destination and still yields no-gap (never force-kill) | `TestServiceLifecycle_StopAndWait_Timeout`; `tests/chaos` stuck-destination variant |
| AC-7 | The `CodeStopAndWaitUnsupported` guard is removed only alongside a green chaos test, in the same PR | this PR |
| AC-8 | A deliberate operator `Stop` racing a transient drain error never auto-restarts | `TestServiceLifecycle_Stop_TransientErrorMidDrain_NoRecovery` |

## Consequences

**Positive:**

- `provisioning.Service.ApplyPlanLive` now works uniformly across both lifecycle implementations —
  no arch-v2-specific branch anywhere in `provisioning`.
- The recovery port's O3 race is fixed generically (any `Stop(force=false)` caller benefits, not just
  `StopAndWait`), closing a latent bug the recovery port (`a61d4bc`) introduced before it could reach
  a release.
- The drain is now explicitly bounded end-to-end; previously an operator-triggered `Stop` against a
  wedged destination could hang a CLI/API call forever with no way to know why.

**Costs / follow-ups:**

- `StopAndWait`'s restart-based interim design (stop, drain, restart) is unchanged from v1's own
  shape — this PR ports the primitive, it does not add a true zero-downtime in-place swap for
  arch-v2. That remains future work, same as v1.
- `DefaultStopAndWaitTimeout` (30s) is a single, process-wide constant, not yet configurable per
  pipeline or per destination class. If a legitimate destination write can validly take longer than
  30s under load, this will need to become configurable — flagged here rather than guessed at now;
  no evidence of this in the current benchi reference pipelines.
- The `intentionalStop` fix is scoped to `pkg/lifecycle-poc`; `pkg/lifecycle` (v1) does not have this
  bug (its node-graph `Stop` doesn't route through the same recovery classification a batch error
  does in the funnel model), so no equivalent change was needed there.
- Multi-connector generalization (Plan 03): `StopAndWait`'s current shape drains a single
  `funnel.Worker` (one source, per `buildRunnablePipeline`'s `TODO(multi-connector)` markers already
  in the file). The drain reasoning above (quiescence via `processingLock`, discard-unacked-batch
  correctness) is per-worker and composes: a future multi-worker pipeline needs `StopAndWait` to wait
  on **all** of its workers, not assume one. No code change was needed to leave room for this — the
  existing `WaitPipeline`/tomb-based join already waits on every goroutine `runPipeline` spawns, and
  `buildRunnablePipeline`'s single-source restriction is enforced (and marked `TODO(multi-connector)`)
  well upstream of `Stop`/`StopAndWait` themselves — but this is called out explicitly so Plan 03 does
  not have to retrofit drain correctness onto a change that assumed single-worker.

## Related

- Issue tracked by the arch-v2 recovery-port series: `a61d4bc` (#2718, recovery port),
  `83e6abf` (#2716, fatal-tag force-stop).
- [20260708-live-server-deploy-apply.md](20260708-live-server-deploy-apply.md) — v1's `StopAndWait`
  audit and the "Open parity item" this PR closes.
- [20260723-source-ack-persist-ordering-fix.md](20260723-source-ack-persist-ordering-fix.md) and
  [20260728-snapshot-handoff-deferred-ack-deadlock.md](20260728-snapshot-handoff-deferred-ack-deadlock.md) —
  the `Source.Teardown`/deferred-ack machinery §3.1 point 3 relies on.
- `pkg/lifecycle-poc/service.go`, `pkg/lifecycle-poc/funnel/worker.go`, `pkg/connector/source.go`,
  `pkg/provisioning/plan.go`.
