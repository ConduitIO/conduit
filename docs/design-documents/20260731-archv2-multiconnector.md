# M-destination fan-out for arch-v2 (slice 3a of the multi-connector epic)

## Summary

Lights up `funnel.Worker`'s destination fan-out: one source, M destinations, wired through
`doNextTask`'s previously-disabled `default` branch and a fully-implemented `multiAckNacker`. This is
**slice 3a** of the arch-v2 multi-connector epic — 1 source, multiple destinations. Multiple
**sources** (N workers per pipeline) are explicitly out of scope and stay guarded off
(`buildSourceTasks`'s existing check in `pkg/lifecycle-poc/service.go`). **Tier 1** — this is the
highest-data-loss-risk code the multi-connector epic adds: a wrong ack here silently drops customer
data.

`multiAckNacker` replaces a per-batch ack counter (which cannot represent per-record divergence
across destinations and is a data-loss bug — see the companion ADR,
[20260731-archv2-fanout-ack-model.md](../architecture-decision-records/20260731-archv2-fanout-ack-model.md))
with a per-position tally: a source record is acked to the source only once **every** destination
branch has acked it (unanimity), and is routed to the DLQ exactly once the instant **any** branch
nacks it (nack wins), released to the source in strict source order regardless of which branch
finishes first.

Two latent bugs in `Batch.clone()` were found and fixed while building this (they were unreachable
before this slice, since `clone()` had no caller): a positions-slice-aliasing hazard that could
corrupt a sibling branch's positions on concurrent splits, and a silently-dropped `splitRecords` map
that would have broken DLQ record-content fidelity for records a shared upstream processor already
split before the fan-out point. Neither shipped in production — this PR is what makes `clone()`
correct for its first real caller.

## Context

`pkg/lifecycle-poc/funnel.Worker` processes a pipeline as a `TaskNode` linked list/tree: a source
task, zero or more shared processor tasks, then — at a branch point — one task chain per destination.
`TaskNode.Next` has always supported multiple children (`[]*TaskNode`), but `doNextTask`'s `default`
case (more than one `Next`) has, since the funnel architecture's introduction (#1913), unconditionally
returned `"multiple next tasks not supported yet"`, with a dead sketch underneath showing the intended
shape: clone the batch per branch, run branches concurrently via a worker pool, and track acks with a
`multiAckNacker` whose `Ack`/`Nack` both `panic("not implemented")`. `pkg/lifecycle-poc/service.go`
mirrors this with an explicit guard: `buildDestinationTasks` refuses a pipeline with more than one
destination connector ("pipelines with multiple destination connectors currently not supported").

Two prior slices in this epic landed the surrounding machinery this slice builds on:

- **Slice 01 (recovery)**: `pkg/lifecycle-poc.Service`'s error-recovery loop
  (`recoverPipeline`/`StartWithBackoff`), so a fan-out worker that fatally errors (e.g. a DLQ
  threshold exceeded on one branch) recovers the same way a single-destination one does.
- **Slice 02 (drain)**: `StopAndWait`/`ReconfigureProcessor` drain semantics
  (`20260731-archv2-drain-reconfigure.md`), establishing that `Worker.Stop`'s
  `processingLock`-plus-source-teardown discipline gives the same quiescence guarantee this slice's
  fan-out inherits unchanged — `doNextTask`'s pool of branch goroutines all complete (or the batch is
  thrown away, unacked) before `Stop` returns, exactly like the single-destination path.

This slice's job is narrowly: replace the `panic`/`not supported` stubs with a correct
implementation, for exactly the M-destination (not M-source) axis.

## Decision

### The two fan-out axes and their composition

Two independent things can multiply a single source record into several pieces, and this slice has
to compose them correctly:

1. **Destination fan-out** (this slice): one batch, cloned once per destination branch
   (`Batch.clone()`), each branch processing its own copy concurrently.
2. **Record splitting** (pre-existing, `Batch.SplitRecord`): a processor can split one record into
   several (e.g. a decompression/expansion processor), tracked via `Batch.splitRecords` (a
   position-string-keyed map back to the pre-split original) and collapsed back via
   `Batch.originalBatch()`.

These compose at the fan-out boundary: a **shared** processor (running before the branch point, e.g.
a pipeline-level processor) can already have split a record by the time `doNextTask`'s `default` case
runs; each **branch**'s own processors can then split (or not split, or split differently)
independently on top of that. `multiAckNacker` must therefore be keyed by the same **root** original
position on every branch, regardless of how each branch split further — this is `OQ-1` below.

`doNextTask`'s fan-out:

```go
orig := b.originalBatch()                                    // capture root positions BEFORE cloning
multiAcker := newMultiAckNacker(acker, len(taskNode.Next), orig.positions)
for _, nextTask := range taskNode.Next {
    branchBatch := b.clone()                                  // independent copy per branch
    p.Go(func() error { return w.doTask(ctx, nextTask, branchBatch, multiAcker) })
}
```

`multiAckNacker.Ack`/`Nack` each call `batch.originalBatch()` on the batch they're handed — exactly
mirroring how the single-destination `Worker.Ack`/`Worker.Nack` already do it — so a branch's own
splits are collapsed to the root position before the tally ever sees them. Since `Batch.clone()`
(fixed by this PR — see "Batch.clone() bugs" below) carries over the pre-existing `splitRecords` map
as an independent copy per branch, every branch's `originalBatch()` resolves the **same** root
position, even though each branch's own subsequent splitting is independent.

### `multiAckNacker` semantics

`multiAckNacker` (`pkg/lifecycle-poc/funnel/worker.go`) is a mutex-guarded per-position tally —
deliberately not lock-free or channel-based; this is Tier-1, highest-data-loss-risk code, and
boring-and-obviously-correct beats clever. It is constructed once per fan-out invocation
(`newMultiAckNacker(parent, branches, positions)`), where `positions` is the **original** (pre-split)
position set captured from `b.originalBatch()` right before cloning.

Per position, it tracks: how many branches have voted ack so far, whether it has reached a terminal
decision (ack — all branches voted ack — or nack — any branch voted nack), and (for a nack) which
branch's record/error/task-ID to use when finally routing to the DLQ.

1. **Ack-only-when-unanimous** (invariant 1 enforcement site): a position is only forwarded to the
   parent's `Ack` once every branch has voted ack for it.
2. **Nack-wins** (invariant 3 enforcement site): the first nack vote for a position is terminal —
   routed to the parent's `Nack` (DLQ) exactly once. A record durably written by some but not all
   branches is a **failure of the whole record**, never a partial success; it is never acked to the
   source as if every branch had succeeded. The DLQ write is itself the "durable handling" that earns
   the eventual source ack (via the parent `Worker.Nack`'s own existing DLQ-then-ack logic, unchanged
   by this slice).
3. **Idempotent, race-free**: once a position is terminal, any further vote for it (a slow branch's
   ack arriving after a sibling already nacked the same position) is a no-op. This relies on the
   existing invariant that each branch votes exactly once per position (ack xor nack, never both,
   never skipped) — the same assumption `doTask`'s tainted-batch splitting already makes for the
   single-destination path.
4. **In-source-order release** (invariant 4 enforcement site): positions are released to the parent
   strictly in ascending source order — never out of branch-completion order. A branch that finishes
   position 10 before a sibling finishes position 3 must not let position 10's ack reach the parent
   first, or `Source.Ack`'s monotonically-advancing `State.Position` (`pkg/connector/source.go`) would
   skip ahead of a record not actually resolved yet. Contiguous **ack** runs are coalesced into a
   single `parent.Ack` call (preserving the parent's batch semantics and Source.Ack call volume);
   **nacks** are released one position at a time, since `parent.Nack` takes one task ID for the whole
   batch it's given (used for DLQ metadata) and a run of nacked positions can legitimately come from
   different branches/tasks — coalescing them would misattribute the DLQ failure reason. Nacks are
   the rare/exceptional path, so the extra parent calls are an acceptable tradeoff for that
   correctness.
5. **Fatal DLQ error propagation**: if the parent's `Nack` returns a fatal error (DLQ nack-threshold
   exceeded, `funnel.DLQ.Nack`), `multiAckNacker` returns it unchanged, so the branch pool
   (`pool.New().WithErrors()`) errors out and the worker tombs — identical to the single-destination
   path's behavior.

### `Batch.clone()` bugs found and fixed

`clone()` had no caller before this slice (the only production call site was the dead sketch code in
`doNextTask`). Making it a real caller surfaced two bugs:

**(a) Positions-slice aliasing.** `clone()` shared `b.positions` by reference across every branch —
same slice header, same length **and capacity** as the original. `Batch.SplitRecord` grows
`b.positions` via `append(b.positions[:i+1], newElems...)`, and Go's `append` reuses spare backing-
array capacity whenever it exists (slicing to a shorter length does not reduce capacity). A batch
fresh out of `NewBatch` always has `cap == len` (no headroom), so a single split on it always
reallocates regardless of this bug — but confirmed empirically, splitting once already gives the
result spare capacity (`cap=10, len=7` from a 5-record batch split into one 3-piece group). So the
reachable failure mode is: a **shared** processor splits a record before the fan-out point (giving the
shared batch spare capacity), the batch is then cloned into M branches (still sharing that same
backing array, uncapped), and two branches independently call `SplitRecord` concurrently — one
branch's `append` can silently overwrite the other's still-live positions in the shared array, a data
race whose blast radius is a corrupted position reaching `Source.Ack` (invariant 2). Fixed via
`slices.Clip(b.positions)` in `clone()`: zero-cost (no reallocation at clone time, since it's a
3-index reslice), but forces the _first_ subsequent `SplitRecord` call on either branch to allocate a
fresh backing array, leaving every sibling's slice header untouched. See
`TestBatch_Clone_PositionsAliasing` (verified to fail without the fix, both a serial reproduction and
a concurrent one run under `-race`).

**(b) Dropped `splitRecords`.** `clone()` returned a `nil` `splitRecords` map unconditionally, silently
losing the ability to restore a record's true pre-split content via `originalBatch()` once cloned —
relevant whenever a shared processor split a record before the fan-out point. This wasn't a
position/ack-accounting bug (Source.Ack only cares about positions, which stayed intact even with
`splitRecords` dropped), but it would have corrupted DLQ record **content** fidelity (a nacked record
whose DLQ entry contains a post-split fragment instead of the true original). Fixed by having `clone()`
copy the map (not share the reference — two branches calling `SplitRecord` concurrently on a _shared_
map is a fatal, unrecoverable `concurrent map write` crash, not just a benign race; the stored
`opencdc.Record` values themselves are read-only after being stored, so a shallow copy — reusing the
same `Record` values across branches — is safe). See `TestBatch_Clone_PreservesSplitRecords`.

Both bugs were latent in a feature that was fully disabled until this PR (the sketch code always hit
the `panic`/error stub first), so nothing shipped was ever exposed to them — but they would have been
the first thing a multi-destination pipeline with a splitting processor hit had this slice shipped
`clone()` unchanged.

### `doNextTask` wiring

```go
default:
    orig := b.originalBatch()
    multiAcker := newMultiAckNacker(acker, len(taskNode.Next), orig.positions)
    p := pool.New().WithErrors()
    for _, nextTask := range taskNode.Next {
        branchBatch := b.clone()
        p.Go(func() error { return w.doTask(ctx, nextTask, branchBatch, multiAcker) })
    }
    return p.Wait()
```

Each branch runs `doTask` exactly as the single-destination path does, with `multiAcker` standing in
for the `Worker` itself as the `ackNacker` the branch's tasks (ultimately `Worker.Ack`/`Worker.Nack`
via `multiAcker`'s own `Ack`/`Nack`) call into.

### Service wiring (`pkg/lifecycle-poc/service.go`)

- `buildDestinationTasks`'s "more than one destination" guard is removed; it now builds one task
  branch per destination connector, same as it always built one per source (still capped at one
  source — see below).
- `buildTaskNodes` now attaches all M destination branches to the shared prefix's tail in a single
  `AppendToEnd(destBranches...)` call, instead of assuming exactly one. For M=1 this is byte-for-byte
  the previous behavior.
- The multi-**source** guard in `buildSourceTasks` ("pipelines with multiple source connectors
  currently not supported") is untouched and stays enforced — `runnablePipeline` remains single-worker
  in this slice.
- `Preview.PipelineArchV2`'s flag usage text (`pkg/conduit/config.go`) is updated from "supports only
  1 source and 1 destination" to "supports only 1 source, but multiple destinations" — an interim
  description, since multi-source is a later slice. `docs/architecture-decision-records/
  20260704-pipeline-architecture-v2.md` quotes the **old** text verbatim as historical context; per
  this repo's ADR-immutability convention that file is not edited — the flag's live behavior is
  documented by `config.go`'s own usage string and this doc, not retroactively in that ADR.

## Consequences

- Pipelines can now fan out to multiple destinations under `Preview.PipelineArchV2`, with the same
  at-least-once, no-silent-partial-ack guarantee the single-destination path has always had.
- The nack path is intentionally less throughput-optimized than the ack path (one `parent.Nack` call
  per nacked position, vs. coalesced runs for acks) — acceptable because nacks are the rare/exceptional
  case; revisit only if profiling ever shows DLQ-heavy workloads bottlenecked here.
- `Batch.clone()` is now used in production for the first time; the two bugs fixed here mean any
  _future_ caller of `clone()` (e.g. a later fan-out-like feature) inherits a correct implementation
  rather than rediscovering the same aliasing/dropped-map hazards.
- No serialized-state format changes: `multiAckNacker` is entirely in-memory, rebuilt fresh on every
  restart from the source's own replayed batch — see "Crash-safety argument" below. No migration, no
  upgrade test needed for this slice specifically (beyond the existing single-destination coverage,
  which is unaffected).
- Metrics: `ConnectorMetrics.Observe` is called once per destination branch (each `DestinationTask`
  independently), so per-destination byte/record counters are already correct without change; no new
  aggregate "fan-out" metric is added in this slice.

## Failure modes

- **One destination slow, others fast**: unanimity means the source position does not advance past
  a record any branch hasn't yet acked, regardless of how far ahead a faster sibling gets. Covered by
  `TestDoNextTask_FanOut_SlowDestination_SourceDoesNotAdvancePastWithheld` (AC-3).
- **One destination nacks, others ack**: nack wins — the record is DLQ'd once (using the nacking
  branch's own error/task-ID), and the source is still acked for it once the DLQ write succeeds
  (existing single-destination `Worker.Nack` behavior, unchanged). The other branch's already-written
  copy is a harmless partial write, not a leak. Covered by
  `TestDoNextTask_FanOut_PartialNack_RoutesToDLQ_NotSourceLoss` and the `multiAckNacker` unit suite.
- **DLQ nack-threshold exceeded on one branch**: the fatal error propagates out of the branch pool
  exactly like the single-destination path — the worker tombs, `pkg/lifecycle-poc.Service`'s existing
  recovery loop (slice 01) takes over. Covered by `TestMultiAckNacker_FatalDLQError_Propagates`.
- **Differential per-destination splitting** (OQ-1): a processor on branch A splits a record into
  pieces that branch B never splits at all. Both branches must still collapse to the _same_ root
  position via `originalBatch`, or `multiAckNacker`'s tally would silently diverge per-branch and
  either double-count or lose a vote. Resolved — see "OQ-1" below.
- **Crash mid-fan-out** (SIGKILL, a subset of destinations durably wrote a record before it was
  unanimously acked): see "Crash-safety argument" below and `tests/chaos/fanout_child.go`'s SIGKILL
  scenario.
- **Positions-slice aliasing under concurrent splits** (found during this slice, not a live bug — see
  "`Batch.clone()` bugs" above): fixed and regression-tested
  (`TestBatch_Clone_PositionsAliasing`, run under `-race`).
- **Malformed task graph** (a branch somehow produces a record whose position doesn't trace back to
  one of the positions captured at fan-out time — an internal bug, not a runtime condition):
  `multiAckNacker.Ack`/`Nack` return a `(bug)`-prefixed error rather than panicking or silently
  dropping the vote. Covered by `TestMultiAckNacker_UnknownPosition_ReturnsBugError`.

## Crash-safety argument (edge (e))

`multiAckNacker` holds no durable state of its own — no serialized format, no on-disk representation,
nothing written to the connector store. It exists only as an in-memory struct for the lifetime of one
`doNextTask` fan-out call (one batch). If the process is killed at any point during a fan-out:

- Any position `multiAckNacker` had already released to the parent (`Worker.Ack`/`Worker.Nack`) is
  handled exactly as the single-destination path already handles a crash after ack — durable per
  `pkg/connector.Source.Ack`'s own invariant-1 persist-before-plugin-ack ordering (unchanged by this
  slice).
- Any position `multiAckNacker` had NOT yet released (still tallying votes, or waiting for a slower
  branch) is, from the source's perspective, simply never acked — its position never advanced past it.
  On restart, the source re-reads and re-delivers that record (and everything after it) from its own
  last durably persisted position, exactly as it would for any other in-flight, unacked batch.
- A record that was durably written to a **subset** of the M destinations before the crash (the
  precise scenario `multiAckNacker` exists to get right) may be **re-delivered and re-written** to
  that same destination on restart — a duplicate, which every destination in this architecture is
  already expected to tolerate under at-least-once (the same tolerance the single-destination path has
  always required). It is never silently skipped on the destinations that hadn't gotten it yet, because
  the source never advanced past it in the first place.

There is therefore no migration path to design and no upgrade test to add for this slice specifically:
`multiAckNacker`'s entire state is rebuilt from scratch, from the replayed batch, on every restart.
`tests/chaos/fanout_child.go`'s `TestSIGKILL_MidFanout_GaplessResume` proves this directly: SIGKILL a
child process mid-fan-out (one destination deliberately slower than the other, so a kill reliably lands
with a record durably in destination A's ledger but not yet in B's), then restart against the same
on-disk state and assert every position `1..total` reaches **both** destinations' durable ledgers —
duplicates allowed, gaps forbidden (run `-race -count=10`, non-flaky).

## Open questions resolved

**OQ-1 (highest composition risk): does differential per-destination splitting collapse to the same
original position on both sides?** Yes — resolved by construction (see "Composition" above:
`multiAckNacker`'s positions are captured from `b.originalBatch()` before cloning, and each branch's
own `Ack`/`Nack` calls `batch.originalBatch()` again on its own, independently-split batch, which
resolves back to the same root position via the per-branch-copied `splitRecords` map). Verified by
`TestFanOut_OQ1_DifferentialSplitCollapsesToSameOriginalPosition`: 200 random iterations (deterministic
seed, logged on failure — see the test's own doc comment on why this repo uses a seeded pseudo-random
driver rather than a new `pgregory.net/rapid`/`gopter` dependency, matching the precedent already set by
`builder_roundtrip_test.go`) over 2-3 branches, 3-8 records, random per-branch splits and at most one
nack per branch, asserting every original position is acked exactly once — never twice, never zero
times — regardless of how differently each branch split it.

**OQ-5: does anything external depend on the destination-branch structure?** N/A for slice 3a — the
DLQ remains singular and unchanged (one `funnel.DLQ` shared across all branches, exactly as it was for
the single-destination path); no external format or API exposes per-branch structure in this slice.

## Related

- ADR: [20260731-archv2-fanout-ack-model.md](../architecture-decision-records/20260731-archv2-fanout-ack-model.md)
  — the decision to use per-record ack counting with unanimity/nack-wins/in-order-release, and the
  rejected alternatives.
- ADR: [20260704-pipeline-architecture-v2.md](../architecture-decision-records/20260704-pipeline-architecture-v2.md)
  — adopts arch-v2 as the target architecture; this slice closes part of the "incomplete" gap it named.
- Design doc: [20260731-archv2-drain-reconfigure.md](20260731-archv2-drain-reconfigure.md) — slice 02
  (drain), whose `Stop`/quiescence guarantee this slice's fan-out pool inherits unchanged.
- `pkg/lifecycle-poc/funnel/worker.go` — `multiAckNacker`, `doNextTask`.
- `pkg/lifecycle-poc/funnel/batch.go` — `Batch.clone()`, `Batch.originalBatch()`.
- `pkg/lifecycle-poc/service.go` — `buildTaskNodes`, `buildDestinationTasks`.
- `tests/chaos/fanout_child.go`, `tests/chaos/fanout_sigkill_test.go` — the SIGKILL-mid-fan-out chaos
  scenario.
