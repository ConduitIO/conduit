# Cross-branch run join for arch-v2's destination fan-out: defer, don't join after the fact

## Summary

Closes the gap `docs/design-documents/20260801-archv2-split-run-ack-ledger.md` left open on purpose:
a split run whose pieces straddle a multi-destination fan-out point is refused with
`CodeSplitRunStraddlesFanOut` (`run_ledger.go:341-369`), which is _correct_ but takes down the whole
pipeline for an ordinary, recoverable condition — a pre-fan-out processor (a rate-limited embedder)
returning fewer records than it received. This is the RAG chunk-then-embed shape, and it breaks the
pipeline on every run today. (Precisely: it drives a restart crash-loop, not a tomb — see the
correction in step 4 of [The problem, traced](#the-problem-traced).)

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
reassembled complete run. **Tier 1** — data path, ack/position logic, touches the same files as #2731.

An earlier draft of this document claimed `validateRunsWholeBeforeFanOut` needed zero changes and
would stay "trivially true by construction." Adversarial review showed that claim was not merely
optimistic but backwards: the guard rejects precisely the batch this design is built to produce. It
must be **relaxed**, and a **new** end-of-pass check has to exist, or this design silently drops
records. See [Members that go terminal without reaching `doNextTask`](#members-that-go-terminal-without-reaching-donexttask)
— that section is the load-bearing one; read it before implementing anything here.

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
   span (`worker.go:783-788`); `Retry` is not. For the 5-chunk run this produces `[Ack,Ack]
   [Retry,Retry,Retry]` — two sub-batches, neither of which by itself holds all 5 members.

   (A three-group `[Ack,Ack] [Retry] [Ack,Ack]` shape is also reachable, but _not_ from an ordinary
   shortfall: `ProcessorTask.Do` pads at the **end** (`processor.go:111-115`), so a plain shortfall
   always yields a trailing `Retry` span. The interleaved shape requires a `nil` `ProcessedRecord`
   at a middle index — see [Why a prefix scan](#why-a-prefix-scan-not-buffer-only-the-incomplete-run),
   where it matters. An earlier draft used the three-group shape here, which contradicted this very
   step's own citation.)
4. The first sub-batch (`RecordFlagAck`/`RecordFlagFilter` case, `worker.go:669-686`) reaches
   `w.doNextTask` (`worker.go:683`), which for a multi-destination `TaskNode.Next` calls
   `validateRunsWholeBeforeFanOut(b)` before cloning per branch (`worker.go:846`). It finds 2 of 5
   members present and returns `CodeSplitRunStraddlesFanOut` (`run_ledger.go:341-369`,
   `codes.go:62-82`).

   **Correction to an earlier draft:** that error is _not_ a `FatalError`. `run_ledger.go` returns a
   bare `conduiterr.New` and never wraps it (contrast `worker.go:740` and `worker.go:758`, which wrap
   explicitly). Since `service.go` routes only `cerrors.IsFatalError` to `StatusDegraded` and sends
   everything else to the bounded-backoff recovery arm — whose default `MaxRetries` is infinite —
   today's real behavior is a **restart crash-loop** on an identical, deterministic failure, not an
   immediate tomb. That is arguably worse for an operator than a tomb (it looks like flapping rather
   than a decision), but it is not what the earlier text claimed, and an implementer writing an
   assertion against "tombs the pipeline" would have written a failing test. Whether
   `CodeSplitRunStraddlesFanOut` _should_ be fatal is a real question this design does not settle;
   after this change it is a bug backstop rather than an operator-facing condition, which is an
   argument for making it fatal at the same time.

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
`docs/architecture-decision-records/20260801-archv2-run-join.md`, for why **defer** was chosen over
the two previously-floated **join-after** options.

## Decision

### Naming: two ledgers, not one

`splitRun.total`/`terminalCount` (`run_ledger.go:71-82`) already tracks a run's completion for the
**ack path** — "terminal" there means acked, filtered, or nacked, all the way through any destination
write (`run_ledger.go:34-44`). This design introduces a **second**, earlier-firing counter: whether a
run's currently-known members are all **accounted for** at the fan-out point — each one either
buffered awaiting dispatch (having reached `Ack`/`Filter`, never `Retry`) or already terminal by a
route that bypasses `doNextTask` entirely. That second half is not a refinement; it is what makes
the counter satisfiable at all, and an earlier draft of this document omitted it, which is what
produced a silent data-loss hole. See
[Members that go terminal without reaching `doNextTask`](#members-that-go-terminal-without-reaching-donexttask).
Conflating the two ledgers would be
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
2. For each span, decide readiness: a standalone span is always ready. A run span is ready when

   ```text
   buffered(run) + run.terminalCount == run.total
   ```

   — that is, when **every** member of the run is accounted for: either parked in the fan-out
   stage's buffer awaiting dispatch, or already terminal by some other route.

   Counting `run.terminalCount` is not an optimization; it is what makes the predicate satisfiable
   at all. See [Members that go terminal without reaching `doNextTask`](#members-that-go-terminal-without-reaching-donexttask).

   This is safe against `total` being a **live, growing** counter (`run_ledger.go:71-82`:
   `SplitRecord` increments it whenever it splits an existing member further). A buffered member is
   not being fed to any `Task.Do` call — it is parked — and a terminal member is by definition never
   revisited. So when the equality holds, no live member remains that could grow `total`, which is
   the same "growth stops exactly when completion is checked" argument `splitRun.total`'s own field
   doc already makes for the ack path.
3. **Take the longest ready _prefix_.** The first span that is an incomplete run halts the scan —
   everything from that point to the end of the batch, including any run or standalone span that
   would independently be ready, is buffered for a later call. See
   [Why a prefix scan, not "buffer only the incomplete run"](#why-a-prefix-scan-not-buffer-only-the-incomplete-run)
   for why this conservative rule is load-bearing, not merely cautious.
4. If the ready prefix is non-empty, reconstruct one `*Batch` from it (concatenating any spans pulled
   out of the fan-out stage's buffer with the current call's contribution, in original order,
   carrying forward `records`/`recordStatuses`/`positions`/`runs`/`splitRecords`/`filterCount` the
   same way `Batch.sub` already does for a slice — see
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

### Members that go terminal without reaching `doNextTask`

**This is the section an earlier draft of this document got wrong, and getting it wrong turns the
loud failure this design replaces into a silent one. It is the reason the readiness rule counts
`run.terminalCount`, and the reason an end-of-pass reconciliation step exists at all.**

The fan-out stage only ever runs inside `doNextTask`. But two paths take run members terminal
**without** ever calling `doNextTask` — both verified in `worker.go`'s tainted loop:

- a `RecordFlagNack` group goes straight to `acker.Nack` (`worker.go:687-691`);
- a group with no active records goes straight to `acker.Ack` — both the sub-batch path
  (`worker.go:669-681`) and the clean-batch shortcut (`worker.go:624-628`).

Both credit `splitRun.terminalCount` through `runAckNacker.vote` (`run_ledger.go:269`). Neither
contributes anything to the fan-out stage.

So a rule of the form "dispatch when the fan-out stage has buffered `total` members" is
**unsatisfiable** whenever any member takes one of those routes after some of its siblings were
already buffered. Worked through, with no malformed plugin and entirely inside the motivating shape:

1. A batch `[r0, run(5 pieces), r2]`. The chunker split `p1` into 5.
2. The embedder shortfalls: `[r0, piece1, piece2]` is an `Ack` group, `[piece3, piece4, piece5]` is a
   `Retry` group. The prefix scan dispatches `r0`, buffers pieces 1–2 (run incomplete), and — per the
   prefix rule — buffers `r2` behind them too.
3. The `Retry` group recurses into `doTask`. The embedder is called again and this time returns three
   `ErrorRecord`s (a provider 4xx) → `markBatchRecords` calls `Batch.Nack` → `subBatchByFlag` routes
   the whole group to `acker.Nack`, which never touches `doNextTask`.

Result under the naive rule: `terminalCount == 3`, buffered `== 2`, `total == 5`. The buffer never
reaches 5, so it never dispatches. Pieces 1 and 2 are never written to **any** destination. `p1` is
forwarded to neither `Worker.Ack` nor the DLQ, because the run never completes. `r2` is stranded with
them. `doTaskAttempt` returns `nil` — no error, no log, no metric — and the next batch's ack advances
`State.Position` past `p1` and `p2`. That is exactly the "silently skipping the record on restart"
outcome `run_ledger.go:326-329` describes, which is what `validateRunsWholeBeforeFanOut` was built to
prevent. **The design would have re-introduced the bug it exists to fix.**

The all-filtered variant is the same failure by the other route: if the retried pieces come back as
`FilterRecord`s (an embedder dropping sub-threshold chunks), `Batch.Filter` does not set `tainted`
(`batch.go:146-163`), so the retried batch is clean, `HasActiveRecords()` is false, and `acker.Ack`
is called directly.

Two changes close it:

**1. Count terminal members in the readiness rule** (above). Pieces 3–5 being terminal is not a
reason to wait for them — it is the reason it is now correct to go. This composes with the existing
machinery rather than fighting it: `cloneRuns` (`batch.go:498-517`) copies `terminalCount` and
`nacked` into every branch's clone, so each branch inherits the pre-fan-out tally, reaches `total`
on its own two acks, and completes. `multiAckNacker` then collapses the M branch completions into
exactly one release of `p1`. A run with any nacked member still resolves to a single DLQ write of the
original record, because `nacked` is sticky and copied too — the same nack-wins rule, unchanged.

**2. Reconcile at the end of the pass.** Change (1) alone is not enough, because the event that makes
the run ready — the `Nack` or direct `Ack` — happens on a path that never re-enters `doNextTask`.
Nothing would ever re-evaluate the predicate. So the fan-out stage gets one final step, at the
pass boundary in `Worker.Do` (`worker.go:300`, the `doTask(..., newRunAckNacker(w))` call that
already owns run lifetime for exactly one batch-read pass):

- Walk the buffered spans in original order. Any run now satisfying the readiness rule is dispatched
  through the ordinary, unchanged `validateRunsWholeBeforeFanOut` → clone → `pool.Go` path.
- If **anything is still buffered** after that, return a **fatal** error
  (`CodeRunAbandonedAtFanOut`, new). Nothing was acked for buffered content, so every record in it is
  redelivered on restart: invariant 3 holds, and the operator gets a loud stop instead of a silent
  gap.

The failure is **fatal**, not merely an error, and deliberately so. A non-fatal worker error is
classified into the bounded-backoff **recovery** arm (`service.go`), whose default `MaxRetries` is
infinite — so a deterministic stranded buffer would restart-loop forever, re-running the pipeline and
re-writing the DLQ on every iteration. Fatal takes the pipeline to `StatusDegraded` once, which is
the correct operator experience for a condition that will recur identically on every replay. This
mirrors `DLQ.Nack`'s own reasoning for wrapping its write failure
(`dlq.go`: "recovering could lead to an endless loop of restarts").

Reaching this error means the fan-out stage's bookkeeping is wrong — it is a bug backstop, not an
expected operating condition — which is why it is worth a distinct code from
`CodeSplitRunStraddlesFanOut` rather than reusing it: they now fail for different reasons at
different points, and an operator should not have to disambiguate.

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
of bug #2730 was filed against (`batch.go:221-232`). Called out here as an explicit implementation
requirement, not a new invariant: it is `Batch.sub`'s existing contract, applied to a reconstruction
`sub` itself cannot produce because the spans are not contiguous in the parent batch.

**`filterCount` must be carried forward too, and it is not optional.** It is a separate `Batch`
field that `sub` recomputes by counting `RecordFlagFilter` statuses in its own range
(`batch.go:520-527`), and both `HasActiveRecords()` and `ActiveRecords()` depend on it
(`batch.go:568-589`). A hand-reassembled batch left at `filterCount == 0` while holding
Filter-flagged records reports those records as **active** — so every destination branch would be
handed filtered records it must never write, and `activeRecordIndices()` would return `nil`,
breaking index arithmetic for anything downstream. This matters precisely because this design
deliberately carries filtered records through the reconstructed batch (see
[`Nack` and `Filter`](#nack-and-filter-change-no-semantics-but-they-are-not-unaffected)), so the
reconstruction must recompute it over the concatenated spans rather than inheriting any one span's
value. `tainted` is reset to `false` on the reconstructed batch, matching what `sub` does.

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

### `validateRunsWholeBeforeFanOut` must be relaxed, not left alone

An earlier draft claimed this guard needed zero changes and would stay "trivially true by
construction." The opposite is true: **as written today it rejects precisely the batch this design
is built to produce.**

The guard counts members of each run _present in the batch being dispatched_ and fails if that count
is below `run.total` (`run_ledger.go:341-369`). But once the readiness rule counts terminal members,
a legitimate dispatch carries only the buffered ones. In the worked example above the reconstructed
batch holds 2 members of a run whose `total` is 5 — `present=2 < total=5` — and the guard fires
`CodeSplitRunStraddlesFanOut`, the very error this design exists to eliminate. An implementer
following the old text would write the motivating test, watch it fail with that exact code, and have
no idea whether the design or their implementation was wrong.

The guard must apply the same accounting the readiness rule does:

```text
present(run) + run.terminalCount >= run.total
```

It keeps its job. It is still the backstop that catches a fan-out stage bug — an off-by-one in the
buffered count, a run wrongly judged complete — and it still fails with the same coded error and the
same rollback story (nothing acked, redelivered on restart). What changes is only that it now asks
the correct question: not "is every member of this run in this batch?" but "is every member of this
run accounted for?"

This is a Tier-1 edit to already-merged, already-reviewed code. It must ship with a test that fails
without the relaxation (the motivating shape) **and** a test that proves the relaxed guard still
catches a genuinely fragmented run (drop a member from the buffer and confirm it still fires).

### `Nack` and `Filter` change no semantics, but they are not "unaffected"

An earlier draft asserted that `Batch.Nack`'s `setFlagWithErr` "propagates a nack across the whole
run atomically", concluded that "the fan-out stage never sees a nacked run", and treated the
interaction as closed. Both halves were wrong, and the second is what produced the silent-drop hole
in [Members that go terminal without reaching `doNextTask`](#members-that-go-terminal-without-reaching-donexttask).

On propagation: `setFlagWithErr` propagates only when `len(b.splitRecords) > 0` **on the batch
receiving the call**, and `findSplitRecord` walks contiguous nil positions **within that batch only**
(`batch.go:370-415`). A pure-tail sub-batch produced by `Batch.sub` has `splitRecords == nil` —
`sub` copies forward only entries whose keys appear in its own positions (`batch.go:528-539`), and a
nil position stringifies to `""`, which never matches a key. So a nack arriving at a tail-only
sub-batch propagates to nothing. Propagation is real and useful when the head is present; it is not
the universal atomic guarantee the earlier text relied on.

On "never sees a nacked run": true and irrelevant. The hazard was never that the fan-out stage would
be handed a nacked run — it is that the fan-out stage is **already holding half of a run** when the
other half nacks somewhere it cannot observe. That is the case the readiness rule and the end-of-pass
reconciliation now handle.

`Batch.Filter` does not set `b.tainted` (`batch.go:146-163` — only `Nack` and `Retry` do,
`batch.go:129-144`) and is coalesced with `Ack` by `subBatchByFlag` (`worker.go:783-788`). Filtered
records count as resolved for readiness, per `splitRun`'s existing definition of terminal
(`run_ledger.go:34-44`). Note the consequence that made the all-filtered variant of the hole
reachable: because `Filter` leaves the batch untainted, a retried group that comes back entirely
filtered takes the `!HasActiveRecords()` shortcut straight to `acker.Ack`, bypassing `doNextTask`
exactly as a `Nack` group does.

What remains true, and is the point worth keeping from the earlier text: **this design changes no
flag semantics.** `RecordFlagRetry`, `RecordFlagFilter` and `RecordFlagNack` mean exactly what they
mean today, and behave identically once assigned. What changes is only _when_ a batch reaches
`doNextTask`'s fan-out logic, and how the fan-out stage accounts for members that never will.

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
| Never touch flag semantics | No `RecordFlag` write, read, or interpretation changes. The fan-out stage only changes when `doNextTask`'s existing dispatch logic runs. | See [`Nack` and `Filter`](#nack-and-filter-change-no-semantics-but-they-are-not-unaffected) |
| Never re-feed a processor its own output | The fan-out stage never calls `Task.Do`, `SetRecords`, `Retry`, or any mutation method — it only reads `Batch.runs`/`recordStatuses`/`positions` and reorders _dispatch_, never _content_ | No new call sites into `ProcessorTask`/`Processor` anywhere in this design |
| Non-blocking (no waiting on the same stack) | Step 5 of [Where it hooks](#where-it-hooks): an incomplete-prefix outcome returns `nil` immediately: control unwinds to the caller (tainted loop or retry recursion), which proceeds exactly as today | `worker.go:761`'s existing retry recursion is what makes further progress, not a wait inside the fan-out stage |
| In-source-order release (invariant 4) | The prefix-scan rule (never dispatch past the first unready span) is the same discipline `multiAckNacker.releaseLocked` already uses for the identical reason. Note the analogy is not exact and the difference is why the end-of-pass step is needed: `releaseLocked` scans one fixed, complete position vector under one lock, whereas the fan-out stage scans a different, partial batch on each call with the buffer as state that scan does not itself consult. Ordering across calls is preserved by two facts together — the prefix rule buffers everything after the first unready span, so nothing later can overtake it, and the buffer is walked in original order and drained (or failed) before the pass returns, so it can never leak into a subsequent pass | `worker.go:1390-1420`; see [Why a prefix scan](#why-a-prefix-scan-not-buffer-only-the-incomplete-run) and [Members that go terminal](#members-that-go-terminal-without-reaching-donexttask) |
| Unanimity / nack-wins across branches (invariants 1/3) | Unaffected: once a complete run reaches `doNextTask`, everything downstream (`clone`, `pool.Go`, `multiAckNacker`, per-branch `runAckNacker`) is the existing, unmodified machinery already proven for a run that was whole from the start | `worker.go:846-878`; `run_ledger.go`'s existing tests already cover this shape once a batch is whole |
| Crash safety (invariant 3) | The fan-out stage is pure in-memory state reachable only from the live call stack of the goroutine processing this batch. A crash at any point discards it with no separate cleanup; nothing is acked for a buffered run — and, critically, nothing is acked _past_ one either | See [Failure modes](#failure-modes), rows 2 and 9 |
| Termination | Bounded by the existing `maxRetryStall`/`maxRetryAttempts`/`CodeRetryNotConverging` mechanism (#2726, merged as part of #2732), unmodified. This design does not add any new waiting condition | See [Failure modes](#failure-modes), row 1 |
| `Filter`/`Nack` keep working unchanged | Traced above | [`Nack` and `Filter`](#nack-and-filter-change-no-semantics-but-they-are-not-unaffected) |
| No new shared mutable state across concurrent branches | The fan-out stage is single-goroutine-owned, created once per relevant scope exactly like `runAckNacker`, and never touched after the point concurrency begins | [Where the fan-out stage lives](#where-the-fan-out-stage-lives) |

## Failure modes

| # | Failure mode | Behavior under this design |
| --- | --- | --- |
| 1 | A run's remaining members never resolve (processor permanently withholds them) | The pending piece is always the input to some `RecordFlagRetry` recursion (`worker.go:692-765`), bounded by the existing `maxRetryStall`/`maxRetryAttempts` (`worker.go:308-411`). That recursion fails with `CodeRetryNotConverging` (`codes.go:84-112`), a `FatalError`, exactly as it would today for a non-fan-out pipeline. The fatal error propagates up through every buffering frame; nothing was ever dispatched or acked for that run, so it replays on restart (invariant 3). This design changes **when** the pipeline can fail-fast on a real, transient rate limit (it no longer does, on the first partial group) but does not change **whether or how** a genuinely non-convergent processor is eventually caught — that bound is #2726's, untouched. |
| 2 | Crash (SIGKILL) with a buffered, undispatched group | The fan-out stage holds no durable state — nothing is serialized, checkpointed, or written anywhere. On restart, the source resumes from whatever position was last durably acked (which may be **before** this batch even started, or partway through it if an earlier, independent span already completed and acked). The buffered run's chunks are never acked, so the source redelivers them; the chunker/embedder reprocess them from scratch. At-least-once, same as any other in-flight-but-unacked content in a batch. **This conclusion is conditional, and the condition is the whole design:** it holds only because nothing is ever acked _past_ a buffered run. The prefix-scan rule (never dispatch past the first unready span) and the end-of-pass reconciliation (never return from a pass with a live buffer) are jointly what enforce that. Remove either and this row becomes "gap on restart", not "at-least-once". |
| 3 | Buffered group + graceful stop/drain (`Worker.Stop`) | `Worker.Stop` needs no changes. `acquireProcessingLock` (`worker.go:600-616`) is held for the entire synchronous call tree rooted at the first task's `doTaskAttempt` — which does not return until this batch's processing (including every nested retry and every fan-out-stage buffering decision) fully resolves or errors. `Worker.Stop`'s bounded wait (`docs/design-documents/20260731-archv2-drain-reconfigure.md`, "O2 — bounding the drain," `DefaultStopAndWaitTimeout`) already covers this exactly as it covers any other slow batch; a buffered run mid-retry is indistinguishable, from `Stop`'s perspective, from any other still-processing batch. |
| 4 | Buffered group + the bounded retry cap firing (#2726) | Identical to row 1 — the cap is what bounds this failure mode, and this design does not move, raise, lower, or duplicate that bound. `CodeRetryNotConverging` fires from inside the same recursion frame it always has. |
| 5 | Multiple runs in one batch | Runs occupy disjoint, non-overlapping contiguous spans (`SplitRecord` only ever grows a run in place, `batch.go:284-290`), and the tainted loop processes spans strictly left to right, with each span's processing — including any nested retry recursion — fully returning before the loop advances (`worker.go:640-768`). **Correction to an earlier draft:** it claimed "at most one run is ever mid-resolution" and that "an earlier run always finishes before a later run's processing even begins." Neither holds under the prefix-scan rule, which deliberately buffers _everything_ after the first incomplete span — including a later run that is independently complete. Several runs are therefore routinely buffered at once, and an earlier run can finish as neither dispatch nor error (that is failure mode 8). This does not break anything, because the buffer is walked in original order and reconciled at the pass boundary, but the "only one at a time" simplification must not be relied on by an implementer. |
| 8 | A run's remainder goes terminal via `Nack`, or via an all-filtered group, while earlier members sit buffered | **The case an earlier draft missed entirely; it is what the readiness rule's `terminalCount` term and the end-of-pass reconciliation exist for.** Both routes bypass `doNextTask` (`worker.go:687-691` and `worker.go:624-628`/`669-681`), so they credit `splitRun.terminalCount` without ever touching the fan-out stage. With the corrected rule the run becomes ready the moment its last member is accounted for, and the end-of-pass step dispatches it because nothing else will re-evaluate the predicate. Under the naive rule the buffer strands silently and the position is skipped on restart — see [Members that go terminal without reaching `doNextTask`](#members-that-go-terminal-without-reaching-donexttask) for the full trace. |
| 9 | The end-of-pass reconciliation finds a buffer it cannot dispatch | Fatal (`CodeRunAbandonedAtFanOut`), one time, to `StatusDegraded`. Means the fan-out stage's own accounting is wrong — a bug, not an operating condition. Nothing buffered was ever acked, so every affected record replays on restart (invariant 3). Deliberately fatal rather than recoverable: the condition is deterministic, so the bounded-backoff recovery arm (default `MaxRetries` infinite) would replay the identical failure forever, re-writing the DLQ on each iteration — the same reasoning `DLQ.Nack` already applies to its own write failure. |
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
- **Remainder terminates via `Nack` while siblings are buffered** (failure mode 8, the case that
  broke the earlier design): a 5-piece run, 2 pieces buffered, the remaining 3 returned as
  `ErrorRecord`s on retry. Asserts the buffered 2 ARE dispatched and written to both branches, the
  original position is DLQ'd exactly once (nack-wins, sticky across `cloneRuns`), and nothing is
  stranded. **Must be verified against the naive readiness rule too**: with `terminalCount` dropped
  from the predicate, this test must fail — otherwise it is not covering the hole it exists for.
- **Remainder terminates via an all-filtered group while siblings are buffered** (failure mode 8,
  second route): same shape, but the retry returns `FilterRecord`s, so the batch stays untainted and
  takes the `!HasActiveRecords()` shortcut to `acker.Ack`. Asserts the same outcome, and specifically
  that the reconstructed batch's `filterCount` is correct so the filtered pieces are NOT written to
  any destination.
- **End-of-pass reconciliation fires fatally on a stranded buffer** (failure mode 9): force the
  fan-out stage's accounting wrong (a member deliberately dropped from the buffer), assert
  `CodeRunAbandonedAtFanOut`, assert `cerrors.IsFatalError` is true — the recovery-loop distinction is
  the point, so a test that only checks the code would miss the actual requirement — and assert
  `Source.Ack` was never called for the stranded run.
- **Relaxed `validateRunsWholeBeforeFanOut` still catches a genuinely fragmented run**: with the
  `present + terminalCount >= total` form in place, a run that is fragmented for a real reason (a
  member neither buffered nor terminal) must still fire `CodeSplitRunStraddlesFanOut`. Without this,
  the relaxation could be over-broad and silently retire the guard.
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

## Observability

Not a follow-up. Every failure mode this design introduces is, by construction, a _quiet_ one — a
buffer that does not drain looks exactly like a pipeline that is merely slow, right up until the
end-of-pass fatal fires. An earlier draft declined to propose anything here; that is what let the
silent-drop hole read as acceptable. CLAUDE.md requires observability in a design doc, and this
design in particular is not reviewable without it.

Ships with the implementation:

- **`fanout_stage_buffered_runs`** (gauge) and **`fanout_stage_buffered_records`** (gauge) — how much
  is parked right now. Steady non-zero is the signal that a run is not converging; it is the only
  externally visible difference between "slow but converging" and "about to fail".
- **`fanout_stage_deferred_dispatches_total`** (counter) — how often a dispatch was deferred rather
  than made immediately. Distinguishes "this pipeline never hits the split-run path" from "this
  pipeline defers constantly", which is what tells an operator whether the embedder's rate limit is
  the practical bottleneck.
- **A `WARN` log when a run is dispatched by the end-of-pass reconciliation rather than inline**,
  with the run's original position and its member accounting. That path is correct but rare: it means
  the run was completed by a `Nack`/filter route, and seeing it in logs is how we find out whether
  the case is as unusual in production as it is in this document.
- **The end-of-pass fatal names the run's `origPos`, `total`, `terminalCount`, and buffered count.**
  A bug backstop that says only "abandoned buffer" would leave a maintainer with nothing to reason
  from.

## Upgrade and rollback

Required by CLAUDE.md and absent from an earlier draft.

- **No serialized state changes.** The fan-out stage is in-memory, per-pass, rebuilt empty on every
  batch read and every restart. There is no format to migrate and no N+1 compatibility obligation.
- **Rollback is revert-or-unflag.** arch-v2 is behind `--preview.pipeline-arch-v2` until the v0.20
  flip; a pipeline that hits a problem here can be moved back to v1, which has no split-run concept
  at all (`sdk.MultiRecord` is refused outright on v1 — a one-way door for _pipelines that split_,
  documented in the graduation work, not created by this design).
- **Downgrade to a build without this change** is safe for the same reason: an older binary reading
  the same persisted positions sees nothing new, because nothing new is persisted. The only
  behavioral difference is that the straddling shape resumes failing loudly.
- **Kill-switch.** The fan-out stage is skippable: with it disabled, `doNextTask` behaves exactly as
  it does today and the straddling shape returns `CodeSplitRunStraddlesFanOut`. Wiring that as an
  explicit internal toggle is what makes "revert" a config change rather than a redeploy during an
  incident, and it is what the fail-without half of the test plan exercises anyway.

## Open questions

- **Multiple, nested fan-out points.** This design's "where the fan-out stage lives" section notes
  today's topology supports at most one fan-out axis per `Worker` (`worker.go:813-816`), so a run
  created after one fan-out and reaching a second, nested one is not currently reachable. If a future
  slice adds nested fan-out, the fan-out stage's per-branch scoping (mirroring `runAckNacker`'s own
  per-branch freshness inside `doNextTask`'s loop) should generalize directly, but this has not been
  built or tested against an actual nested topology.
- **Buffer growth bound.** An earlier draft said the buffer "for one run is bounded by that run's own
  `total`". That understates it and contradicts the prefix-scan rule, which buffers everything from
  the first unready span to the end of the batch — including unrelated standalone spans and
  independently-complete later runs. **The real bound is the source batch size**, held for as long as
  the retry chain runs. For the RAG shape that is embedded vectors, kilobytes per record, pinned in
  memory across every retry round of a rate-limited embedder — the workload where this matters most.
  The existing `maxRetryAttempts` cap bounds the number of rounds but not the resident footprint.
  Needs a measurement against a realistic batch size before the v0.20 flip, and possibly an explicit
  ceiling that fails loud rather than growing without limit.
- **Composition with the N-source shared-destination machinery.** Carried here from the PR
  description, because a PR body is ephemeral and this artifact is not. N workers serialize into a
  shared destination subtree through a per-shared-root mutex; a pass that now buffers and defers
  holds `acquireProcessingLock` for longer, which changes how long a worker can occupy that shared
  boundary. Nothing about that is obviously wrong — the lock order is unchanged, see
  [Constraints traced](#constraints-traced) — but the interaction wants its own adversarial pass and
  a chaos test before code, not a paragraph of reassurance.

## Related

- #2723, #2726, #2730 (closed, prerequisites)
- #2725, #2727 (superseded, rejected — the flag-uniformity attempts)
- `docs/design-documents/20260801-archv2-split-run-ack-ledger.md` (merged; the ack-path ledger this
  design's fan-out stage is modeled on, and whose "Limits" section first named this gap)
- `docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md` (the per-position unanimity
  model `multiAckNacker` implements; unchanged by this design, and what everything downstream of a
  reconstructed complete run still relies on)
- `docs/architecture-decision-records/20260801-archv2-run-join.md` (companion ADR: why defer, not
  join-after)
- `docs/design-documents/20260731-archv2-drain-reconfigure.md` (Worker.Stop's bounded-drain mechanism,
  unmodified by and sufficient for this design — see failure mode 3)
- `cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/` (the flagship shape this closes the
  gap for, extended to a second destination)
