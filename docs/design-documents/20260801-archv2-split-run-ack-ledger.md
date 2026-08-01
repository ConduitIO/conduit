# Split-run ack ledger: fixing #2723 without touching RecordFlag semantics

## Summary

Fixes **#2723** (a split run's head can be acked to the source before its tail is delivered) and
**#2730** (split-run membership is defined two different ways, and a position rewrite defeats
both). This is the **third** attempt: #2725 (per-write flag escalation) and #2727 (normalize-once
after `Task.Do`) were both rejected by adversarial review, for reasons this design deliberately
avoids by construction rather than by patching around them — see
[Why the two prior attempts failed](#why-the-two-prior-attempts-failed).

The fix does not change `RecordFlag` semantics at all. `Retry`, `Filter`, and `Nack` behave in
`pkg/lifecycle-poc/funnel` exactly as they do on `main`. Instead, a new decorator —
`runAckNacker`, wrapping the existing `ackNacker` interface (`worker.go`) — sits between
`Worker.doTask` and whatever it would otherwise call directly (`Worker` itself, or a
`multiAckNacker` branch). It tracks, per split run, how many of the run's current members have
reached a terminal disposition, and only forwards the run's original source position once all of
them have. This mirrors `multiAckNacker`'s per-position unanimity tally (`worker.go`,
`docs/design-documents/20260731-archv2-multiconnector.md`) one level down: unanimity **across
branches** for one position there, unanimity **across a run's members** for one position here.

**Tier 1** — data path, ack/position logic.

## Context

A **split run** is N contiguous `Batch` entries produced by `Batch.SplitRecord` from one original
source record: the head keeps the original position, the rest get `nil` positions, and
`Batch.splitRecords` maps the run back to the pre-split original record (needed for DLQ content and
for `Batch.originalBatch()`'s collapse). `Worker.doTask`'s tainted-batch loop partitions a batch into
contiguous same-flag sub-batches (`subBatchByFlag`) and processes them one at a time — Ack/Filter
sub-batches either ack directly or continue to the next task; Nack sub-batches go to the DLQ; Retry
sub-batches recurse back into the same task.

`Batch.Nack`'s `setFlagWithErr` already propagates a nack across an entire split run. `Retry` and
`Filter` do not. So a sub-batch can cover only **part** of a run — e.g. the head, while the tail is
still being retried by a separate, later loop iteration (or a nested recursion) — and
`originalBatch()` will happily collapse that partial sub-batch to the run's original position and
hand it to `acker.Ack`, before the rest of the run has gone anywhere. Under fan-out, if a sibling
branch also acks that position (it will, if it didn't split), `multiAckNacker` reaches unanimity and
`Source.Ack` persists the position. The branch then errors on the undelivered tail (or, per the
`main` mitigation already in place, `Worker.Ack`'s `validateAckPositions` refuses an empty-position
sub-batch and stops the pipeline instead) — but the head's position is already durable. On restart,
the source resumes past it, and the tail's data is gone. Invariants 1 and 3.

`#2730` compounds this: `SplitRecord` keyed `Batch.splitRecords` on `b.records[i].Position` (the
record's own, processor-rewritable position field) while every consumer (`findSplitRecord`,
`originalBatch`, and both rejected fixes' partition-boundary checks) looked up `b.positions[i]`
(`Batch`'s own, never-rewritten tracking of the true original position). A processor that rewrites a
position before a later processor splits the record breaks that mapping, so run membership silently
stops being recognized for that record.

## Decision

### One field, correctly keyed: fixing #2730 first

`Batch.SplitRecord` now keys `splitRecords` (and the new run-tracking below) on `b.positions[i]`
instead of `b.records[i].Position`. This is the one-line option #2730 named as preferred. Every
consumer already used `b.positions[i]`; only the writer was wrong. See `batch.go`'s `SplitRecord`
doc comment.

### A per-run ledger, not a flag-uniformity rule

`Batch` gains a new parallel field, `runs []*splitRun` (`batch.go`): `runs[i]` is `nil` for a record
that was never split (the overwhelming common case, zero overhead), or a pointer to a shared
`splitRun` value for every currently-live piece of a run — the head, and every tail piece
`SplitRecord` has produced, however many further rounds of splitting have happened to any of them.

```go
type splitRun struct {
    origPos    opencdc.Position // captured once, at first split - never changes
    origRecord opencdc.Record   // the pre-split record, for the eventual Ack/Nack call

    total         int  // current LIVE member count - can grow (see below)
    terminalCount int  // how many of the current members have voted so far

    nacked     bool // sticky: any member nacking makes the run's verdict Nack
    nackErr    error
    nackTaskID string

    released bool // guards against ever forwarding the same run twice
}
```

`runAckNacker` (`run_ledger.go`) wraps an `ackNacker` and intercepts every `Ack`/`Nack` call that
would otherwise reach it. It walks the batch left to right; standalone records (`runs[i] == nil`)
pass straight through unchanged; a span of records belonging to the same run credits
`terminalCount += span length` and, once `terminalCount >= total`, forwards **one** call to the
parent for that run's original position — `Ack` if no member ever nacked, `Nack` (using whichever
member nacked first) otherwise.

**Where it's inserted** (`worker.go`):

- `Worker.Do` wraps `w` itself in a fresh `runAckNacker` once per batch read from the source —
  covers the single-destination (and pre-fan-out shared-prefix) path.
- `Worker.doNextTask`'s destination fan-out wraps each branch's `multiAckNacker` in its **own**
  fresh `runAckNacker`, created inside the per-branch loop before `pool.Go`. Each branch's clone can
  diverge independently (split further, retry, filter) from the moment it's cloned, so each needs an
  independent ledger; `multiAckNacker` itself stays exactly what it already was — purely about
  per-position unanimity **across** branches, with no notion of run structure. A branch's
  `runAckNacker` guarantees it votes for a run's position to `multiAckNacker` exactly once, which is
  precisely what `multiAckNacker`'s existing one-vote-per-branch-per-position contract requires.

### What "terminal" means

A run member is terminal once it has reached a disposition `Worker.doTask` will never revisit with
another `Task.Do` call: **acked** (reached the true end of the pipeline, including a destination
write), **filtered**, or **nacked** (routed to the DLQ). A member flagged `Retry` is explicitly
**not** terminal — it is still in flight and, per `ProcessorTask.Do`'s documented behavior, may be
fed to the same task again and split further there.

This is also why `total` is a **live**, growable counter rather than a value fixed at the first
`SplitRecord` call: a member can only be split further while it is still active (fed to some
`Task.Do`), and by definition nothing active remains once every currently-known member is terminal —
so `terminalCount == total` can only ever be observed at a point where no further growth is
possible. `SplitRecord` increments `total` by `len(recs) - 1` every time it splits an existing
member (whether that's the very first split, or the fifth round on some tail piece three tasks
later) — see its doc comment.

### In-source-order release needs no extra bookkeeping (invariant 4)

`multiAckNacker` needs an explicit released-prefix scan (`releaseLocked`) because it arbitrates M
**concurrently running** branches that can finish any position first. `runAckNacker` doesn't share
that problem: within one branch, `Worker.doTask`'s tainted-batch loop and its `RecordFlagRetry`
recursion are both fully sequential and synchronous — there are no goroutines until a fan-out point,
and each branch created there gets its own `runAckNacker` anyway. `subBatchByFlag` advances the
loop's cursor strictly left to right, and a `RecordFlagRetry` sub-batch's recursive `doTask` call —
including everything it does: further splits, nested retries, and its own `Ack`/`Nack` calls —
completes **before** the loop advances past it. A split run always occupies a contiguous span
(`SplitRecord` only ever grows a run in place, never moves it), so by the time the loop's cursor
reaches any position after the run's span, every member of that run has already voted through this
type. That structural guarantee is what makes "buffer until `terminalCount == total`, then forward
immediately" sufficient on its own — see `run_ledger.go`'s `runAckNacker` doc comment for the
argument in full, and `TestRunLedger_InSourceOrderRelease_DeferredRunBlocksLaterPosition` /
`TestRunLedger_FanOut_PendingRunOnOneBranch_NoPrematureUnanimity` for the tests that exercise it.

This is a **caller-provided** guarantee, not one `runAckNacker` independently enforces the way
`multiAckNacker` does (see [Limits](#limits-and-what-this-does-not-cover)).

### Decision on item 5: `SplitRecord`'s zero-value `Ack` status

`make([]RecordStatus, n)` still defaults new pieces to `RecordFlagAck` — unchanged from `main`, and
unchanged from what attempt 1 found dangerous. It is safe here specifically because run completion
is tracked independently, via `splitRun.total`/`terminalCount`, and never by requiring
`recordStatuses` to agree across a run. A freshly split piece defaulting to "no error from this
task, proceed" is exactly the right behavior regardless of what flag its siblings currently carry —
including a sibling still flagged `Retry`, attempt 1's exact failure shape.
`TestBatch_SplitRecord_ZeroValueAck_SafeUnderRunLedger` drives precisely that shape (`Retry` the
original, then split it) and confirms the run still resolves correctly, once, through the ledger.

### Decision on item 6: no `validateSplitRunBoundary`-style guard

Both rejected attempts added a defensive check (`CodeSplitRunPartitioned`) that fired if a sub-batch
boundary would ever cut across a run. Under this design that guard's premise is wrong: a sub-batch
covering only part of a run is the **expected, legal, and correctly-handled** case — that's the
entire point of moving the fix from the flag layer to the ack layer. Adding a check that fires on
normal operation would be actively misleading. It is omitted entirely, not ported in a modified
form.

## Why the two prior attempts failed

**#2725 (per-write flag escalation)**: introduced a tier ranking (`Nack > Retry > Ack/Filter`) and
made every `Retry`/`Filter` write escalate the whole run to that tier. Review found `SplitRecord`'s
zero-value `Ack` pieces could still land in an already-`Retry` run within the same `Task.Do` pass
(since `ProcessorTask.Do` marks ranges end-to-start), reopening #2723 under a fatal guard instead of
closing it (607/3000 randomized pipelines hit it); escalation mutated `filterCount` mid-`Do`,
corrupting the active-index mapping calls still in flight were relying on; and refusing `Filter`
inside an already-`Retry` run made the retry input never shrink, risking non-convergence.

**#2727 (normalize once, after `Task.Do`)**: fixed the mid-`Do` mutation and the resulting O(n²)
cost by doing one linear normalization pass after `Task.Do` returns instead of escalating on every
write. But normalizing meant escalating an entire run to `Retry` uniformly — which starves the retry
input of any shrink mechanism for a deterministic output-capped processor (an embedding processor
with a per-call batch limit: `main` converges via `CodeEmptySourcePosition`; that design fed it the
same 3 records forever), and re-fed already-`Ack`'d, already-transformed pieces back into the
processor, silently double-encoding them before they reached the destination.

**The common thread**: both tried to make a run's `RecordFlag`s agree with each other. Doing so
either reopens the bug (attempt 1) or breaks something Retry/Filter's _current_, flag-preserving
behavior was quietly relying on (attempt 2: bounded retry convergence, and a de facto no-re-feed
guarantee). This design's core move is to stop trying to make the flags agree at all — the ack
layer doesn't need them to.

### Explicit evidence against the two attempt-2 killers

- **Convergence (C1)**: `TestRunLedger_OutputCapProcessor_ConvergesAndAcksOnce` feeds a 5-piece
  split run through a processor that always processes 2 and retries the rest — the exact shape
  review used to falsify #2727. It converges in 3 rounds (bounded, asserted), identical to what
  `main`'s unmodified `Retry`/`subBatchByFlag` logic already does, because this design never
  escalates anything: `Retry`/`Filter` are untouched, so nothing changes about _how_ a retry pass
  shrinks. This is not a claim that #2726 (unbounded retry recursion) is fixed — it isn't, and isn't
  in scope here — only that this change does not make non-convergence any _more_ likely, and the one
  scenario review used to prove the opposite is now proven to converge.
- **No double transformation (C2)**: `TestRunLedger_NonIdempotentProcessor_TransformsExactlyOnce`
  runs a non-idempotent processor (appends a marker) over a 3-piece split run with partial retries,
  and asserts the destination receives each piece transformed exactly once. This holds structurally,
  not by luck: `runAckNacker` never calls `Retry`, `Filter`, or `SetRecords` itself — it only
  intercepts calls doTask was already about to make to the parent acker. A piece that reaches `Ack`
  is, by construction, never fed to a `Task.Do` again.

## Failure modes

| Failure | Before this fix | After |
| --- | --- | --- |
| Processor returns fewer records than received, mid split run (#2723) | head acked before tail delivered → data loss on restart | run withheld until every member votes; released once, atomically |
| Filter on the head of a run, tail retried separately | same premature-ack shape | same withholding, via the Filter/Ack call path |
| Position rewritten, then split, then partially retried (#2730) | run membership silently not recognized → head acked with tail undelivered | `splitRecords`/`runs` keyed on the true original position; withheld correctly |
| Deterministic output-cap processor (embedding, rate limiter) | converges on `main` via existing Retry/subBatchByFlag logic | unchanged - this fix doesn't touch that path |
| Non-idempotent processor + partial retry | converges correctly on `main` (no re-feed) | unchanged - `runAckNacker` never re-feeds anything |
| Fan-out, one branch's run pending | N/A (bug didn't exist pre-fan-out either) | per-branch ledger withholds that branch's vote; `multiAckNacker` still requires all branches |
| A future code path calls `parent.Ack`/`Nack` twice for the same already-released run | silent double-ack (would violate invariant 1) | `splitRun.released` guard returns a `(bug)`-prefixed error instead |

**Rollback**: revert. This is entirely internal to `pkg/lifecycle-poc/funnel` (preview-gated via
`Preview.PipelineArchV2`, off by default) — no serialized format, protocol, or public API changes.

## Limits and what this does not cover

- **Not fixed here, by design**: #2726 (unbounded `RecordFlagRetry` self-recursion in `doTask`). This
  design's convergence evidence above shows it does not make #2726 _more_ likely to trigger, but a
  genuinely non-convergent processor still recurses unbounded on this branch exactly as it does on
  `main`.
- **`runAckNacker`'s in-order guarantee is caller-provided, not self-enforced.** Unlike
  `multiAckNacker`, it does not independently track a released-prefix or assert against an
  out-of-order external caller — it relies on `Worker.doTask`'s single-goroutine-per-branch,
  strictly-sequential execution model (verified structurally above, not merely assumed). A
  hypothetical future caller that invoked `runAckNacker.Ack`/`Nack` out of source order directly
  (bypassing `doTask` entirely) could release out of order. Adding prefix-buffering to guard against
  a call shape that cannot currently occur was judged speculative complexity against a
  non-existent failure mode (CLAUDE.md's "no speculative generality"); if a future caller changes
  that assumption, this section is the flag to revisit it.
- **A split run cut across a fan-out boundary is explicitly refused, with a coded error.** If a
  processor _before_ a multi-destination fan-out splits a record and a later pre-fan-out processor
  resolves only part of that run, the sub-batch reaching `doNextTask` holds only some of the run's
  members. `Batch.clone` must give each branch its own ledger (branches diverge, and a shared tally
  would race and bleed decisions across branches), but a clone carries `splitRun.total` — the
  whole-run member count. The branch could then never complete the run, and neither could the parent
  side: two disjoint tallies, no join point. `validateRunsWholeBeforeFanOut` detects this before the
  fan-out and returns `CodeSplitRunStraddlesFanOut`. Nothing is acked, so the records are redelivered
  on restart (invariant 3).

  **An earlier draft of this document claimed this shape "already fails loud today … this fix does
  not touch that path." Both halves were wrong**, and adversarial review caught it. The tail group
  does not always reach `newMultiAckNacker` at all — `doTask` routes a `RecordFlagNack` group
  straight to `acker.Nack`, and a group with no active records straight to `acker.Ack`, bypassing
  `doNextTask` entirely. And the ledger _does_ touch that path: wrapping `Worker.Do` in a
  `runAckNacker` intercepts the nil-position batch and withholds it, so `Worker.Ack`'s
  `validateAckPositions` never runs either. Withholding silently would therefore have **disarmed both
  of the backstops that caught this shape before the ledger existed**, turning a loud failure into a
  permanently withheld position while later positions acked past it — silently skipping the record on
  restart. The explicit refusal restores the loud failure.

  Joining a run's tally _across_ fan-out branches is possible (hoist the ledger into a per-pass
  registry, or have each branch complete against the member count it actually received rather than
  `run.total`). It is deliberately not attempted without a real use case for the shape.

- The shape this ledger positively supports is a run split **after** a fan-out point, independently
  per branch — such a run is created wholly inside one branch's ledger, so its total and its members
  agree. That is the shape #2723 was filed against.

## Testing

All in `pkg/lifecycle-poc/funnel/run_ledger_test.go`, run with `-race`:

- `TestRunLedger_RetryHead_NotAckedUntilTailResolves`, `TestRunLedger_FilterHead_NotAckedUntilTailResolves`
  — the original #2723 shapes.
- `TestRunLedger_OutputCapProcessor_ConvergesAndAcksOnce`,
  `TestRunLedger_NonIdempotentProcessor_TransformsExactlyOnce` — the two attempt-2 killers.
- `TestRunLedger_PositionRewriteThenSplit_NoPrematureAck` — the #2730 shape.
- `TestRunLedger_InSourceOrderRelease_DeferredRunBlocksLaterPosition` — invariant 4, single branch.
- `TestRunLedger_FanOut_PendingRunOnOneBranch_NoPrematureUnanimity` — fan-out composition, M=2, with
  a gated retry pass proving no premature unanimity and in-order release together.
- `TestBatch_SplitRecord_ZeroValueAck_SafeUnderRunLedger` — item 5's decision.
- `TestRunLedger_Property_SplitFilterRetryNackRewrite` — 300 randomized iterations of
  split/filter/retry/nack/position-rewrite combinations; every original position acked-once XOR
  DLQ'd-once, cross-checked by destination/DLQ delivery counts.
- `BenchmarkRunLedger_VoteLinear` — resolves a single N-member run one vote at a time (n=1000/4000/
  16000); scales roughly linearly (measured ~2.6-3.8x time per 4x N), nothing like #2725's
  measured ~42000x blowup for the same 4x step.

Fail-without/pass-with was verified by temporarily short-circuiting `runAckNacker.vote` to forward
every call straight through (simulating pre-fix behavior): every new test in this file fails,
including `TestRunLedger_RetryHead_NotAckedUntilTailResolves` (asserts exactly 1 release, observed
2 — the head released separately from the tail) and
`TestRunLedger_FanOut_PendingRunOnOneBranch_NoPrematureUnanimity` (asserts p0 alone acked at the
checkpoint, observed nothing acked at all — the whole batch raced ahead of the withheld run
incorrectly). Restoring the fix returns the suite to green.

## Related

- #2723, #2726 (not fixed here), #2730
- #2725, #2727 (superseded, rejected)
- `docs/design-documents/20260731-archv2-multiconnector.md` (the model this design's per-run ledger
  mirrors one level down)
- `docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md` (per-position ack model
  this design composes with, unchanged)
