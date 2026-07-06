# WaitPipeline returns false success on self-cleanup (root cause of #2521)

## Summary

`TestServiceLifecycle_Stop/user_stop:_forceful` flakes on CI. An earlier draft of
this doc blamed a tomb death-reason race; **independent review disproved that** by
empirical repro (the terminal _status_ is always correct; only the returned error
is wrong). The real defect is a **time-of-check/time-of-use race in
`Service.WaitPipeline`**: it looks the pipeline up in `runningPipelines` by ID
_after_ the pipeline's own cleanup goroutine may have already removed it, and in
that case returns a false `nil` — losing the terminal error (`ErrForceStop`). This
is a shipped-code bug in `pkg/lifecycle/service.go` (Tier 1), not a test artifact.
This doc proposes a bounded, minimal product fix plus the test hygiene, and calls
out the sibling `waitInternal`/`StopAll` path.

## Evidence (repro)

`go test ./pkg/lifecycle/ -run '^TestServiceLifecycle_Stop$' -race -count=550`
produced 5 failures, every one identical:

```text
service_test.go:448: not true: err != nil     # WaitPipeline returned nil
```

and in none of them did the following `is.Equal(tc.want, pl.GetStatus())`
(line 454) fail — the status was always correctly `StatusDegraded` with the fatal
force-stop reason. That rules out any death-reason race (which would corrupt the
status too) and points squarely at the returned error being dropped.

## Root cause

```go
// pkg/lifecycle/service.go
func (s *Service) WaitPipeline(id string) error {
    p, ok := s.runningPipelines.Get(id)   // (A) time-of-check
    if !ok || p.t == nil {
        return nil                         // (C) false success if already deleted
    }
    return p.t.Wait()                      // (B) correct if we got here
}
```

The pipeline's run goroutine, on completion, does (service.go ~L812):

```go
s.runningPipelines.Delete(rp.pipeline.ID) // then s.notify(...), then return
```

`runningPipelines` is a plain mutex-guarded `csync.Map`; `Get` and `Delete` are
each safe, but nothing orders the caller's `Get` (A) against the pipeline
goroutine's `Delete`. For a force-stopped **zero-record** pipeline the whole node
graph unblocks off the just-canceled tomb context and the cleanup `Delete` runs in
the same microsecond window as the test goroutine resuming from `Stop()` to call
`WaitPipeline`. When `Delete` wins, (A) misses, and (C) returns `nil`, masking the
`FatalError(ErrForceStop)` the tomb held. Zero records simply make the post-`Stop`
teardown fast enough to lose the race often; the bug is the lookup-after-delete,
not the record count.

Once `WaitPipeline`'s `Get` _succeeds_ (B), it is safe: `Delete` only removes the
map entry, not the `runnablePipeline`; `tomb.Wait()` on the retained handle returns
the death reason even after the tomb is dead. The entire defect is the missed
lookup at (A)/(C).

## Constraints

1. Preserve force-stop semantics and invariant 7 — no change to how the pipeline
   dies, only to how its terminal result is reported to a waiter.
2. `WaitPipeline` must return the pipeline's terminal error whether it is called
   before, during, or after cleanup; it must still block while the pipeline runs.
3. Bounded memory — no per-run leak.
4. Tier 1: failure-mode analysis + human sign-off before merge.

## Alternatives

### Option A — Record the terminal result before deleting (recommended)

Add `s.terminalErrors csync.Map[string, error]` (or a small struct if we want a
timestamp). In the cleanup goroutine, **set the terminal error before** removing
the pipeline from `runningPipelines`:

```go
s.terminalErrors.Set(rp.pipeline.ID, err)   // new, ordered BEFORE Delete
s.runningPipelines.Delete(rp.pipeline.ID)
```

`WaitPipeline` falls back to it:

```go
p, ok := s.runningPipelines.Get(id)
if ok && p.t != nil {
    return p.t.Wait()
}
if err, ok := s.terminalErrors.Get(id); ok {
    return err
}
return nil // genuinely never ran / unknown id — unchanged behavior
```

Eviction: clear `terminalErrors[id]` at the start of the next `Start(id)` (a
pipeline about to run has no stale terminal result). Worst case one error value
per stopped-and-not-restarted pipeline — bounded by pipeline count, negligible.

- **Why it wins:** closes the exact window with correct ordering (set-before-
  delete means a waiter sees either the live tomb or the recorded result — never a
  gap); no change to `runningPipelines` membership semantics, so every existing
  reader (`Start` guard, `IsRunning`, `StopAll`) is untouched; bounded.
- **Cost:** one map + eviction on `Start`.

### Option B — Keep the dead pipeline in `runningPipelines` until next Start (rejected)

Don't `Delete` on stop; let `WaitPipeline` always find the dead-tomb entry; purge
on the next `Start`.

- **Why it loses:** changes the meaning of `runningPipelines` membership. Every
  reader that treats "present" as "running" (`Start`'s already-running guard,
  `IsRunning`, `StopAll`) would need to additionally test tomb liveness — a wider,
  riskier edit on the shutdown path for no extra benefit over Option A.

### Option C — Reconstruct the error from persisted status (rejected)

On a miss, read the pipeline's status via the pipeline service and synthesize an
error from a `Degraded` state.

- **Why it loses:** lossy (the original typed error, e.g. `ErrForceStop`, is gone),
  couples `lifecycle` to pipeline-status string semantics, and is fuzzy about
  non-error terminal states.

## Decision

Adopt **Option A**. The added regression test
(`TestServiceLifecycle_WaitPipeline_AfterCleanup`) forces the exact cleanup-race
window deterministically (wait for removal from `runningPipelines`, then
`WaitPipeline`) and asserts `ErrForceStop`. Note: a poll-on-`StatusRunning` is
_not_ a sufficient startup barrier here — `connectorCtxCancel` is set inside the
source node's `Run` goroutine independently of the status flip, so the test retains
a short startup settle before force-stopping. Force-stopping mid-startup is a
separate nil-panic robustness gap (filed #2539) and is intentionally out of scope.

## Failure modes

- **The diagnosed race isn't the whole story.** Mitigation: acceptance is
  empirical — `-race -count=200` and `-shuffle=on -count=100` on
  `TestServiceLifecycle_Stop` must be 100% green (the earlier wrong hypothesis was
  caught precisely because status never failed; the same instrumentation guards
  this one). If flakes persist after Option A, the fix is wrong and we stop.
- **`terminalErrors` unbounded if pipelines are created-run-once at high
  cardinality and never restarted.** Mitigation: entries are tiny (an error); if
  ever a concern, evict on `WaitPipeline` read or add a periodic sweep. Documented,
  not built now (YAGNI).
- **Concurrent waiters.** Multiple `WaitPipeline(id)` callers: all either get the
  live tomb (each `Wait()` returns the same reason) or the recorded error. Reads
  are safe; no eviction-on-read means no waiter loses the value.
- **Sibling path — `Wait(timeout)`/`waitInternal` uses `runningPipelines.Copy()`
  then waits** (service.go ~L338-372); a fast-finishing pipeline can vanish from
  the copy before being waited on. Lower severity (omitting an already-finished
  pipeline from an _aggregate_ wait is closer to correct), but it is the real
  SIGTERM/`StopAll` shutdown path (`pkg/conduit/runtime.go`,
  `20260704-graceful-shutdown-sigterm.md`). **In scope to audit**; fix or
  explicitly justify-as-safe in the same PR.
- **Restart after stop.** `terminalErrors` cleared on `Start(id)` so a restarted
  pipeline never returns a stale prior error.

## Test / verification strategy

- `go test ./pkg/lifecycle/ -run 'TestServiceLifecycle_Stop' -race -count=200` and
  `-shuffle=on -count=100` — 100% green.
- Full `pkg/lifecycle` `-count=20 -race` green.
- A direct unit test for the race: force-stop a 0-record pipeline, then
  `WaitPipeline` must return non-nil `ErrForceStop` (the exact failing assertion),
  run under `-count` to prove determinism.
- `waitInternal`/`StopAll` behavior verified or its safety justified in-PR.

## Rollback / compatibility

No serialized formats or public contracts change. `WaitPipeline`'s observable
behavior changes only from "sometimes wrongly returns nil" to "returns the terminal
error" — strictly more correct. Rollback is reverting the diff.

## Related

- #2521 — the flaky test (fixed by this once the product bug is fixed).
- New issue (to file) — the `WaitPipeline` false-success product bug; distinct
  from #1659 (which concerns _which_ error surfaces when two nodes race), this is
  the wait API dropping the error entirely.
- #2534 — CI flakiness tracking; this clears the last blocking-suite offender.
- `20260704-graceful-shutdown-sigterm.md` — the `StopAll`/`Wait` shutdown path the
  sibling concern touches.
