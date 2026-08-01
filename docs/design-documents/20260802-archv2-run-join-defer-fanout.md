# Cross-branch run join for arch-v2's destination fan-out: defer, don't join after the fact

## Summary

Closes the gap `docs/design-documents/20260801-archv2-split-run-ack-ledger.md` left open on purpose:
a split run whose pieces straddle a multi-destination fan-out point is refused with
`CodeSplitRunStraddlesFanOut` (`run_ledger.go:341-369`), which is _correct_ but takes down the whole
pipeline for an ordinary, recoverable condition — a pre-fan-out processor (a rate-limited embedder)
returning fewer records than it received. This is the RAG chunk-then-embed shape, and it is fatal on
every run today.

Two prior designs (#2725, #2727) tried to fix this by making a split run's `RecordFlag`s agree with
each other; both were rejected by adversarial review for reasons documented in
`20260801-archv2-split-run-ack-ledger.md`'s own "Why the two prior attempts failed" section. The
third attempt (#2731, merged) fixed the _ack_ path without touching flags at all, but deliberately
left the fan-out-straddling case as a fatal refusal — the PR's own review explicitly rejected joining
a run across fan-out branches as speculative generality, with no concrete use case
(`gh pr view 2731`, "Not adopted" section).

This document is that use case's design. **DeVaris approved building it** and chose **defer-the-fan-out**:
instead of joining a run's tally _after_ dispatching partial groups to branches (the option #2731's
reviewer floated and rejected), buffer a fan-out-bound group _before_ dispatch, inside the same
single-goroutine pass that already produces it, and call `doNextTask` exactly once, with the
reassembled complete run. `validateRunsWholeBeforeFanOut` needs zero changes and stays trivially true
by construction. **Tier 1** — data path, ack/position logic, touches the same files as #2731.

## Context

### The problem, traced

1. A chunking processor splits source record `p1` into 5 chunks via `Batch.SplitRecord`
   (`batch.go:233-327`), creating one `splitRun{total: 5}` (`run_ledger.go:60-98`) shared by all 5
   pieces via `Batch.runs` (`batch.go:60-89`).
2. A pre-fan-out processor (a rate-limited embedder) is invoked via `ProcessorTask.Do`
   (`processor.go:101-137`). It returns fewer records than it received; `Do` pads the shortfall with
   `nil` entries (`processor.go:111-115`), and `markBatchRecords`'s `case nil:` classifies that
   remainder `RecordFlagRetry` (`processor.go:195-199`), which also sets `b.tainted = true`
   (`batch.go:141-144`).
3. `Worker.doTaskAttempt`'s tainted loop calls `subBatchByFlag` (`worker.go:775-805`) repeatedly,
   partitioning the batch into contiguous same-flag spans. `Ack` and `Filter` are coalesced into one
   span (`worker.go:783-788`); `Retry` is not. For the 5-chunk run this produces `[Ack,Ack] [Retry]
   [Ack,Ack]` — three separate sub-batches, none of which by itself holds all 5 members.
4. The first sub-batch (`RecordFlagAck`/`RecordFlagFilter` case, `worker.go:669-686`) reaches
   `w.doNextTask` (`worker.go:683`), which for a multi-destination `TaskNode.Next` calls
   `validateRunsWholeBeforeFanOut(b)` before cloning per branch (`worker.go:846`). It finds 2 of 5
   members present and returns `CodeSplitRunStraddlesFanOut` (`run_ledger.go:341-369`,
   `codes.go:62-82`) — a `FatalError`, which tombs the whole pipeline (every source, every
   destination, ×N blast radius in an N-source topology).

This is the `postgres-pgvector-rag` gallery template's shape
(`cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/pipeline.yaml`: `ai.chunk` → `ai.embed`
→ a pgvector destination) generalized to two destinations (e.g. a second vector store, or an
analytics sink added alongside pgvector) — a dual-write RAG pipeline is exactly the shape a rate
limit on the embedding provider now kills outright. **ASSUMED**: the checked-in template currently
has one destination; the two-destination fan-out is the reachable extension this design targets, not
something already shipping. The single-destination path is not affected by any of this — see
[Why the single-destination path is already safe](#why-the-single-destination-path-is-already-safe).

### Why this is a genuinely new decision, not a re-litigation

`20260801-archv2-split-run-ack-ledger.md`'s "Limits" section states the fan-out-straddle case is
"deliberately not attempted without a real use case for the shape," and #2731's merged review comment
records two options it considered and rejected as speculative: hoisting the ledger into a per-pass
registry, or making `multiAckNacker` the join point (`gh pr view 2731`, "Not adopted" paragraph). What
changed is not the code — it's that a concrete, reachable, flagship shape (RAG dual-write hitting an
everyday rate limit) now exists, and DeVaris chose to build against it. See the companion ADR,
`docs/architecture-decision-records/20260802-archv2-run-join.md`, for why **defer** was chosen over
the two previously-floated **join-after** options.

## Decision

### Naming: two ledgers, not one

`splitRun.total`/`terminalCount` (`run_ledger.go:71-82`) already tracks a run's completion for the
**ack path** — "terminal" there means acked, filtered, or nacked, all the way through any destination
write (`run_ledger.go:34-44`). This design introduces a **second**, earlier-firing counter: whether a
run's currently-known members have all reached a **pre-fan-out** disposition (`Ack`/`Filter` — never
`Retry`, and never `Nack`, since `Nack` propagates across the whole run atomically and bypasses
`doNextTask` entirely, see [Nack](#nack-and-filter-are-unaffected)). Conflating the two would be
exactly the kind of misattribution #2731's own postmortem flagged as "what hid this bug" for the
ack-side ledger (`gh pr view 2731`'s comment: "Corrected comments naming `runAckNacker` as the
isolation boundary — it is stateless; the tally lives in `Batch.runs`. That misattribution is what hid
this bug."). This document calls the new counter the **fan-out stage** to keep it visibly distinct
from the ack ledger throughout.

### Where it hooks

`Worker.doNextTask`'s multi-destination branch (`worker.go:825-884`) is the single choke point every
path to a fan-out already funnels through — both the tainted loop's `Ack`/`Filter` case
(`worker.go:683`) and the clean-batch shortcut (`worker.go:630`) call it, and it already runs
`validateRunsWholeBeforeFanOut` before cloning (`worker.go:846`). The fan-out stage sits **immediately
before** that existing call, as a new first step:

1. Walk the incoming batch left to right, exactly like `runAckNacker.vote`'s existing walk
   (`run_ledger.go:229-255`) and `multiAckNacker.releaseLocked`'s existing prefix scan
   (`worker.go:1390-1420`): group contiguous records by `*splitRun` identity (`nil` = standalone).
2. For each span, decide readiness: a standalone span is always ready. A run span is ready only if
   the fan-out stage's accumulated count for that run (buffered-so-far, from earlier calls, plus this
   span) reaches `run.total`.
3. **Take the longest ready _prefix_.** The first span that is an incomplete run halts the scan —
   everything from that point to the end of the batch, including any run or standalone span that
   would independently be ready, is buffered for a later call. See
   [Why a prefix scan, not "buffer only the incomplete run"](#why-a-prefix-scan-not-buffer-only-the-incomplete-run)
   for why this conservative rule is load-bearing, not merely cautious.
4. If the ready prefix is non-empty, reconstruct one `*Batch` from it (concatenating any spans pulled
   out of the fan-out stage's buffer with the current call's contribution, in original order,
   carrying forward `records`/`recordStatuses`/`positions`/`runs`/`splitRecords` the same way
   `Batch.sub` already does for a slice — see
   [Reconstruction must carry `splitRecords` forward](#reconstruction-must-carry-splitrecords-forward))
   and proceed into the **existing, unchanged** `validateRunsWholeBeforeFanOut` → clone → `pool.Go`
   path (`worker.go:846-878`) with that reconstructed batch.
5. If the ready prefix is empty, return `nil` without calling into the fan-out machinery at all —
   **accumulate and return**, control unwinds back to whichever caller invoked `doNextTask` (the
   tainted loop or a nested `RecordFlagRetry` recursion), which continues exactly as it does today.

Step 5 is what makes this non-blocking: nothing waits. The call that would have dispatched a partial
group instead returns immediately, and the tainted loop (or the retry recursion nested inside it)
keeps running. The next opportunity to make progress on a buffered run is whatever produces its next
piece — ordinarily the very next loop iteration, which is the `RecordFlagRetry` sub-batch's recursive
`doTask` call (`worker.go:761`).

### Why a prefix scan, not "buffer only the incomplete run"

An earlier, simpler version of this design assumed an incomplete run is always the **trailing** span
in any group reaching the fan-out stage — because `ProcessorTask.Do`'s padding
(`processor.go:111-115`) always appends `nil` entries at the **end** of whatever the processor
returned, and `Task`'s own doc comment describes the intended shape as "a task processed only part of
the batch... and skipped the rest" (`worker.go:69-71`) — a prefix-processed, suffix-skipped
convention. Under that assumption, buffering could be scoped to just the one trailing incomplete run,
with everything before it in the group always safe to dispatch immediately.

That assumption does not hold in general, and finding the counterexample is why this design uses a
strict prefix scan instead. `CodeRetryNotConverging`'s own documented trigger is "a `ProcessedRecord`
whose `Record` oneof is unset... a malformed or version-mismatched plugin" (`codes.go:102-104`) — and
`markBatchRecords`'s `isSameType`/`case nil:` classification (`processor.go:195-199, 206-224`) treats
**any** Go-nil `ProcessedRecord` as `Retry`, wherever it occurs in the processor's returned slice, not
only in the padded suffix. A malformed plugin that returns a full-length response with an unset
entry in the **middle** — index 2 of 5, say — produces a `Retry` classification sandwiched between
otherwise-`Ack` neighbors, which can place an incomplete run's boundary anywhere in a group, including
before an otherwise-complete run or standalone span that textually follows it.

Rather than prove the "trailing-only" shape holds for every processor (it doesn't, per the above), the
design borrows the discipline `multiAckNacker.releaseLocked` already uses and has already survived
review for: **never release past the first thing that isn't ready** (`worker.go:1391-1394`,
"`m.released` only ever moves forward, and never past a position that is not yet terminal"). Applying
the identical rule one layer earlier — never dispatch past the first span that isn't a complete run —
means the fan-out stage's correctness does not depend on any assumption about where a processor places
its unresolved entries. The cost is a small, deliberate one: a run or standalone span that is already
complete but sits after an incomplete one in the same group waits for the earlier one to resolve
before it is dispatched, even though it could otherwise go immediately. Given split-run pipelines are
not the common case and this only delays dispatch (never correctness), this is the same trade CLAUDE.md
calls for ("boring, obviously correct beats clever") and the one the fan-out ack ADR made explicitly
for the same reason (`docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md`,
"Lock-free / atomic per-position tally" alternative, rejected on simplicity grounds).

### Reconstruction must carry `splitRecords` forward

`Batch.originalBatch()` (`batch.go:611-653`), which `Worker.Ack`/`Nack` and `multiAckNacker` all call
before doing position-keyed bookkeeping, resolves a run's true pre-split record via
`b.splitRecords[pos.String()]` (`batch.go:628-631`). `Batch.sub` already reconstructs this map
correctly for an ordinary slice (`batch.go:528-539`), copying forward only the entries whose keys
appear in the slice's own positions. The fan-out stage's reconstructed batch is not produced by `sub`
(it concatenates non-adjacent spans pulled from the buffer), so it must perform the equivalent union
explicitly: the reconstructed batch's `splitRecords` is the union of the `splitRecords` entries for
every run represented in it. Skipping this would not break the destination write (branches write
`ActiveRecords()`, the current per-piece content, unaffected by `splitRecords`) but would break
`originalBatch()`'s collapse for **any** branch's later `Ack`/`Nack` call on that run, silently
substituting whatever the run's head record currently holds for the true original — exactly the class
of bug #2730 was filed against (`run_ledger.go:220-232`'s design-doc citation). Called out here as an
explicit implementation requirement, not a new invariant: it is `Batch.sub`'s existing contract,
applied to a reconstruction `sub` itself cannot produce because the spans are not contiguous in the
parent batch.

### Where the fan-out stage lives

It must satisfy the same constraint `runAckNacker` already satisfies: state that survives across
multiple `doTask`/`doTaskAttempt`/`doNextTask` calls within one batch-processing pass, without being a
new cross-call registry or shared mutable state visible to concurrently-running branches.

`newRunAckNacker(w)` is created exactly once per batch-read pass, in `Worker.Do`
(`worker.go:300`), and threaded through the whole call tree as `acker` — itself stateless
(`run_ledger.go:181-195`), with the actual state living in the `*splitRun` values reachable via
`Batch.runs`. The fan-out stage follows the identical shape: a value created once per relevant scope
(once per batch-read pass for the common case; once per branch inside `doNextTask`'s fan-out loop if a
run is ever created **after** an outer fan-out point and reaches a **second, nested** fan-out — not a
shape any current in-tree topology produces, since `doNextTask`'s own doc comment notes only one
fan-out axis, destinations, is supported today, `worker.go:813-816`) and threaded alongside `acker`
through `doTask`/`doTaskAttempt`/`doNextTask`.

Concretely: it holds, per fan-out-bound `*splitRun` currently buffered, the accumulated pieces
(records/positions/statuses) collected so far, plus any standalone spans buffered behind an
incomplete run per the prefix-scan rule. Nothing here is written or read by more than one goroutine:
a pre-fan-out run's `SplitRecord` calls, its retries, and the fan-out stage's own buffering all happen
on the single goroutine executing this batch's `doTask` call tree, strictly before the point (inside
`doNextTask`'s `pool.Go` loop, `worker.go:859-878`) where concurrency begins. By the time any second
goroutine exists for this batch, the fan-out stage's job for that dispatched span is already done —
this satisfies "no new shared mutable state across concurrently-running branches" precisely, not by
adding synchronization but by construction: it is never touched after the pass that owns it becomes
concurrent.

### Why the single-destination path is already safe

`doNextTask`'s single-next-task branch (`worker.go:822-824`) recurses directly into `doTask`, with no
`validateRunsWholeBeforeFanOut` call at all — there is nothing to join, because there is only one
branch and the existing `runAckNacker` (unmodified) already withholds a run's original position until
every member is terminal, regardless of how many separate `Ack`/`Retry` sub-batches its pieces travel
through (`run_ledger.go:155-176`'s in-order argument). The fan-out stage therefore only needs to
engage in `doNextTask`'s `default` (`len(taskNode.Next) > 1`) branch — the change described here is
zero-touch for every pipeline that does not fan out to multiple destinations.

### `validateRunsWholeBeforeFanOut` needs zero changes

Once the fan-out stage only ever hands `doNextTask`'s existing logic a batch it has already verified
is a ready prefix (every run represented in it satisfied `total`), `validateRunsWholeBeforeFanOut`
(`run_ledger.go:341-369`) is trivially true by construction on every call — it cannot fail in the
happy path. It is kept, unmodified, as the backstop: if the fan-out stage's own bookkeeping ever has a
bug (an off-by-one in the accumulated count, a run wrongly judged complete), this is what still catches
it loudly, with the exact same coded error and rollback story (nothing acked, redelivered on restart)
it already provides today. This is deliberate, not an oversight: inventing a **second**, fan-out-stage-
specific guard on top of an already-correct one would be exactly the kind of redundant complexity
CLAUDE.md's "no speculative generality" warns against, and it would blur which guard is actually load
bearing.

### `Nack` and `Filter` are unaffected

`Batch.Nack`'s `setFlagWithErr` already propagates a nack across an entire split run
(`batch.go:370-399`, specifically 384-397) — unchanged by this design, and unaffected by it: a nacked
run's sub-batch is routed straight to `acker.Nack` by `subBatchByFlag`'s `RecordFlagNack` case
(`worker.go:687-691`), which never calls `doNextTask` at all. The fan-out stage never sees a nacked
run.

`Batch.Filter` does not set `b.tainted` (`batch.go:146-163` — only `Nack` and `Retry` do,
`batch.go:129-144`) and is coalesced with `Ack` by `subBatchByFlag` (`worker.go:783-788`). Filtered
records are treated identically to `Ack`'d ones for fan-out-stage readiness (both count as "resolved,"
per `splitRun`'s own existing definition of terminal, `run_ledger.go:34-44`) and are carried through
the reconstructed batch unchanged, exactly as `originalBatch()`/`ActiveRecords()` already handle a
filtered record traveling alongside active ones (`batch.go:568-589`).

Neither flag's semantics change. This design changes only **when** a batch reaches `doNextTask`'s
fan-out logic, never **what** any `RecordFlag` means or how `Retry`/`Filter`/`Nack` behave once
assigned.

## Constraints traced

| Constraint | How this design satisfies it | Evidence |
| --- | --- | --- |
| Never touch flag semantics | No `RecordFlag` write, read, or interpretation changes. The fan-out stage only changes when `doNextTask`'s existing dispatch logic runs. | See [`Nack` and `Filter` are unaffected](#nack-and-filter-are-unaffected) |
| Never re-feed a processor its own output | The fan-out stage never calls `Task.Do`, `SetRecords`, `Retry`, or any mutation method — it only reads `Batch.runs`/`recordStatuses`/`positions` and reorders _dispatch_, never _content_ | No new call sites into `ProcessorTask`/`Processor` anywhere in this design |
| Non-blocking (no waiting on the same stack) | Step 5 of [Where it hooks](#where-it-hooks): an incomplete-prefix outcome returns `nil` immediately: control unwinds to the caller (tainted loop or retry recursion), which proceeds exactly as today | `worker.go:761`'s existing retry recursion is what makes further progress, not a wait inside the fan-out stage |
| In-source-order release (invariant 4) | The prefix-scan rule (never dispatch past the first incomplete span) is the same discipline `multiAckNacker.releaseLocked` already uses for the identical reason | `worker.go:1390-1420`; see [Why a prefix scan](#why-a-prefix-scan-not-buffer-only-the-incomplete-run) |
| Unanimity / nack-wins across branches (invariants 1/3) | Unaffected: once a complete run reaches `doNextTask`, everything downstream (`clone`, `pool.Go`, `multiAckNacker`, per-branch `runAckNacker`) is the existing, unmodified machinery already proven for a run that was whole from the start | `worker.go:846-878`; `run_ledger.go`'s existing tests already cover this shape once a batch is whole |
| Crash safety (invariant 3) | The fan-out stage is pure in-memory state reachable only from the live call stack of the goroutine processing this batch. A crash at any point discards it with no separate cleanup; nothing is acked for a buffered run | See [Failure modes](#failure-modes), row 2 |
| Termination | Bounded by the existing `maxRetryStall`/`maxRetryAttempts`/`CodeRetryNotConverging` mechanism (#2726, merged as part of #2732), unmodified. This design does not add any new waiting condition | See [Failure modes](#failure-modes), row 1 |
| `Filter`/`Nack` keep working unchanged | Traced above | [`Nack` and `Filter` are unaffected](#nack-and-filter-are-unaffected) |
| No new shared mutable state across concurrent branches | The fan-out stage is single-goroutine-owned, created once per relevant scope exactly like `runAckNacker`, and never touched after the point concurrency begins | [Where the fan-out stage lives](#where-the-fan-out-stage-lives) |

## Failure modes

| # | Failure mode | Behavior under this design |
| --- | --- | --- |
| 1 | A run's remaining members never resolve (processor permanently withholds them) | The pending piece is always the input to some `RecordFlagRetry` recursion (`worker.go:692-765`), bounded by the existing `maxRetryStall`/`maxRetryAttempts` (`worker.go:308-411`). That recursion fails with `CodeRetryNotConverging` (`codes.go:84-112`), a `FatalError`, exactly as it would today for a non-fan-out pipeline. The fatal error propagates up through every buffering frame; nothing was ever dispatched or acked for that run, so it replays on restart (invariant 3). This design changes **when** the pipeline can fail-fast on a real, transient rate limit (it no longer does, on the first partial group) but does not change **whether or how** a genuinely non-convergent processor is eventually caught — that bound is #2726's, untouched. |
| 2 | Crash (SIGKILL) with a buffered, undispatched group | The fan-out stage holds no durable state — nothing is serialized, checkpointed, or written anywhere. On restart, the source resumes from whatever position was last durably acked (which may be **before** this batch even started, or may be partway through it if an earlier, independent run/span in the same batch already completed and acked — see row 5). The buffered run's chunks are never acked, so the source redelivers them; the chunker/embedder reprocess them from scratch. No gap, no double-ack: at-least-once, same as any other in-flight-but-unacked content in a batch. |
| 3 | Buffered group + graceful stop/drain (`Worker.Stop`) | `Worker.Stop` needs no changes. `acquireProcessingLock` (`worker.go:600-616`) is held for the entire synchronous call tree rooted at the first task's `doTaskAttempt` — which does not return until this batch's processing (including every nested retry and every fan-out-stage buffering decision) fully resolves or errors. `Worker.Stop`'s bounded wait (`docs/design-documents/20260731-archv2-drain-reconfigure.md`, "O2 — bounding the drain," `DefaultStopAndWaitTimeout`) already covers this exactly as it covers any other slow batch; a buffered run mid-retry is indistinguishable, from `Stop`'s perspective, from any other still-processing batch. |
| 4 | Buffered group + the bounded retry cap firing (#2726) | Identical to row 1 — the cap is what bounds this failure mode, and this design does not move, raise, lower, or duplicate that bound. `CodeRetryNotConverging` fires from inside the same recursion frame it always has. |
| 5 | Multiple runs in one batch | Runs occupy disjoint, non-overlapping contiguous spans (`SplitRecord` only ever grows a run in place, `batch.go:284-290`), and the tainted loop processes spans strictly left to right, with each span's processing — including any nested retry recursion — fully returning before the loop advances (`worker.go:640-768`). Consequently at most one run is ever "mid-resolution" (buffered and incomplete) at a given instant in this single goroutine's execution; an earlier run in source order always finishes (dispatch, or a fatal error) before a later run's processing even begins. Independent runs never interfere with each other's fan-out-stage entries. |
| 6 | A run spanning 3+ flag groups (e.g. `Ack \| Retry \| Ack`) | Reachable: `markBatchRecords` marks by contiguous output-type ranges (`processor.go:117-134`), and a processor can legitimately return a middle chunk of `Retry` between two `Ack` ranges for the same run (e.g. two separate shortfalls in one `Process` call). The fan-out stage accumulates across however many groups it takes; each `Ack`/`Filter` group's contribution is added to the run's running count, and dispatch happens the moment the count reaches `total`, regardless of how many groups that took. No group-count limit is assumed anywhere in this design. |
| 7 | One flag group holding partial members of several different runs | Reachable — see [Why a prefix scan](#why-a-prefix-scan-not-buffer-only-the-incomplete-run) for the constructive example (a malformed plugin's mid-list `nil`) and a benign one (two adjacent runs whose boundary pieces both happen to resolve `Ack` in the same `Process` call, e.g. run A's last chunk and run B's first chunk are contiguous and both fully resolved together). The prefix-scan rule handles this without special-casing: walk the group's spans in order, dispatch the ready prefix, buffer everything from the first incomplete span onward — including a later run that would, on its own, already be complete. |

## Test plan

All new tests live in `pkg/lifecycle-poc/funnel`, alongside the existing `run_ledger_test.go` suite,
run with `-race`:

- **The motivating shape**: a 5-piece run, a rate-limited-processor double that returns 2, then (after
  one retry round) the remaining 3, fanning out to 2 destination branches. Asserts exactly one
  `doNextTask` dispatch for the whole run, both branches receive all 5 pieces, and the source position
  acks exactly once. This is the direct regression test for the bug this document opens with.
- **Multiple flag groups, one run** (`Ack | Retry | Ack`, failure mode 6): asserts the fan-out stage
  accumulates across 3 groups and dispatches once, in original piece order.
- **Two adjacent runs, one straddling group** (failure mode 7, benign construction): run A (2 pieces)
  immediately followed by run B (3 pieces) both resolving `Ack` in the same `Process` call, but B's
  last piece needs a retry. Asserts A dispatches only once B's remainder resolves too — proving the
  prefix-scan rule's deliberate over-buffering (row 7's "even a later, independently-ready run waits").
- **Malformed-plugin mid-list nil** (failure mode 7, adversarial construction): a double that returns a
  full-length response with a Go-nil `ProcessedRecord` at a middle index. Asserts no out-of-order ack
  reaches the (mocked) parent — i.e., this is the regression test proving the prefix-scan design,
  not the rejected trailing-only shortcut, is what's implemented.
- **Non-convergent processor behind a fan-out** (failure mode 1/4): asserts `CodeRetryNotConverging`
  still fires, from the same recursion depth as the non-fan-out case, and that nothing was acked for
  any buffered content when it does.
- **`splitRecords` carried forward through reconstruction**: asserts a branch's later `Nack` on a
  reconstructed (multi-span) run correctly resolves the true pre-split original record via
  `originalBatch()`, not a stale current-content substitute — the regression test for
  [Reconstruction must carry `splitRecords` forward](#reconstruction-must-carry-splitrecords-forward).
- **Property test**: extend the existing `TestRunLedger_Property_SplitFilterRetryNackRewrite`
  generator (`run_ledger_test.go:790`) with a fan-out destination count > 1 and a processor double that
  can shortfall by an arbitrary amount per call, asserting every original position is acked-once XOR
  DLQ'd-once and every destination branch receives every non-filtered piece exactly once.
- **Fail-without/pass-with**: short-circuit the fan-out stage to always treat the ready prefix as "the
  whole batch" (i.e. restore today's behavior) and confirm the motivating-shape test fails with
  `CodeSplitRunStraddlesFanOut`; restoring the real logic returns it to green.
- **Chaos**: extend `tests/chaos/fanout_sigkill_test.go` with a SIGKILL-mid-buffering variant — kill
  the process while a run is buffered but not yet dispatched, restart, and verify the run's chunks are
  redelivered and eventually land exactly once at both destinations (failure mode 2). This is new
  chaos coverage; `tests/chaos` today has no split-run-specific case (verified: no existing chaos file
  references `splitRun`/`SplitRecord`).
- Existing suites unaffected and re-run for regression: `run_ledger_test.go`,
  `worker_fanout_test.go`, `multi_ack_nacker_test.go`, `full_pipeline_property_test.go`.

## Open questions

- **Multiple, nested fan-out points.** This design's "where the fan-out stage lives" section notes
  today's topology supports at most one fan-out axis per `Worker` (`worker.go:813-816`), so a run
  created after one fan-out and reaching a second, nested one is not currently reachable. If a future
  slice adds nested fan-out, the fan-out stage's per-branch scoping (mirroring `runAckNacker`'s own
  per-branch freshness inside `doNextTask`'s loop) should generalize directly, but this has not been
  built or tested against an actual nested topology.
- **Observability.** Should a run sitting in the fan-out stage for an unusually long time (many retry
  rounds, still under `maxRetryStall`) surface a metric or log line before it either resolves or hits
  `CodeRetryNotConverging`? Today's retry recursion has no such signal either; this document does not
  propose adding one, but flags it as a reasonable follow-up given operators currently have no way to
  distinguish "slow but converging" from "about to fail" until the fatal error actually fires.
- **Buffer growth bound.** The fan-out stage's accumulated buffer for one run is bounded by that run's
  own `total`, which is itself bounded by the same finite-pipeline-topology argument that bounds
  `SplitRecord` growth generally (see the termination analysis in
  [Constraints traced](#constraints-traced)) — but this document does not add an explicit ceiling on
  `total` itself (e.g. "a run may not exceed N members"). If a pathological processor could grow a
  single run unboundedly within the existing retry-attempt cap, the fan-out stage's memory footprint
  would grow with it. Worth a follow-up measurement, not a blocker: the existing `maxRetryAttempts`
  cap (10,000) already bounds how many split-then-retry rounds are possible, which bounds this too,
  just not with an explicit named constant.

## Related

- #2723, #2726, #2730 (closed, prerequisites)
- #2725, #2727 (superseded, rejected — the flag-uniformity attempts)
- `docs/design-documents/20260801-archv2-split-run-ack-ledger.md` (merged; the ack-path ledger this
  design's fan-out stage is modeled on, and whose "Limits" section first named this gap)
- `docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md` (the per-position unanimity
  model `multiAckNacker` implements; unchanged by this design, and what everything downstream of a
  reconstructed complete run still relies on)
- `docs/architecture-decision-records/20260802-archv2-run-join.md` (companion ADR: why defer, not
  join-after)
- `docs/design-documents/20260731-archv2-drain-reconfigure.md` (Worker.Stop's bounded-drain mechanism,
  unmodified by and sufficient for this design — see failure mode 3)
- `cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/` (the flagship shape this closes the
  gap for, extended to a second destination)
