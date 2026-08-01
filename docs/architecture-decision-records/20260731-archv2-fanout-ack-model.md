# arch-v2 destination fan-out: per-record ack tally, not per-batch

## Summary

For `funnel.Worker`'s M-destination fan-out (slice 3a of the arch-v2 multi-connector epic),
`multiAckNacker` tracks ack/nack votes **per original source position**, acks the source only once
**every** destination branch has acked a position (unanimity), routes a position to the DLQ exactly
once the instant **any** branch nacks it (nack wins), and releases resolved positions to the source
strictly in ascending source order. A per-batch counter — the shape the pre-existing (disabled) sketch
code implied — is rejected as a data-loss bug: destinations diverge per record, not per batch, and a
batch-level counter cannot represent that divergence at all.

## Context

`pkg/lifecycle-poc/funnel/worker.go` had a `multiAckNacker` type since the funnel architecture's
introduction (#1913), but it was never completed: `Ack`/`Nack` both `panic("not implemented")`, and it
was constructed as `newMultiAckNacker(parent, count)` — a single `*atomic.Int32` counter, decremented
once per branch's call, intended to fire the parent action once it reached zero. The `doNextTask`
caller that would have used it was itself disabled (`"multiple next tasks not supported yet"`).

Bringing this online for slice 3a required deciding how to represent "M destinations, each
independently voting ack or nack, for a batch of N records" — the counter's granularity (per batch,
per record, or something else) determines whether the implementation can even express the scenarios
a real fan-out produces.

## Decision

Track ack/nack state **per original source position**, not per batch. `multiAckNacker` holds parallel
slices (indexed by position, captured once at fan-out time from `b.originalBatch()`): an ack-vote
count, a terminal flag, and — for a nack — which branch's record/error/task-ID to use. `Ack`/`Nack`
each update every position in the batch they're handed (after collapsing splits back to root positions
via `originalBatch()`), then attempt to release a leading, contiguous run of now-terminal positions to
the parent, in source order.

Unanimity (ack) and nack-wins are enforced directly by this per-position structure: a position is
`acked[i]` only when its vote count reaches the branch count; it is `nacked` immediately on the first
nack vote, and a later vote (of either kind) for an already-terminal position is a no-op.

## Alternatives considered

### A per-batch counter (the pre-existing sketch's implied shape)

A single counter per batch, decremented once per branch's `Ack`/`Nack` call, firing the parent action
once it reaches zero (mirroring `stream.FanoutNode`'s v1 per-**message** counter, but applied at
batch — not record — granularity, since arch-v2 batches many records per call).

**Rejected — this is a data-loss bug, not a simplification.** Destination 1 might ack records 0-4 of a
5-record batch while destination 2 nacks record 3. There is no single moment where "the batch" is
uniformly ack'd or nack'd. A per-batch counter has no way to represent record 3 being nacked while
records 0, 1, 2, and 4 are acked: it either drops the divergence (silently acking a record that a
destination never durably wrote — violating invariant 1) or blocks forever (waiting for a nack vote on
a record every branch actually acked, since the count never reaches the value a uniform outcome would
need). This is not a hypothetical edge case — any batch containing more than one record and more than
one destination can diverge this way, and arch-v2's whole point is large batches (its committed
benchmark, `20260704-pipeline-architecture-v2.md`, uses 1000-record batches).

### Port v1's channel/async-handler model (`stream.FanoutNode` + `stream.SourceAckerNode`)

v1 (`pkg/lifecycle/stream`) already solves this correctly, at **per-message** granularity:
`FanoutNode.Run` tracks `remainingAcks` per message (an `atomic.Int32` per in-flight message, not per
batch), and each cloned message's ack/nack handler decrements it, propagating the original message's
ack only once every fan-out branch has acked its clone (or propagating a nack the instant one branch
nacks). Ordering to the source is separately enforced by `SourceAckerNode`'s semaphore
(`pkg/lifecycle/stream/source_acker.go`), which serializes forwarding to `Source.Ack` in strict
enqueue order regardless of which message's ack handler fires first.

**Rejected for arch-v2 specifically** (not rejected as a design in general — it is the correct
reference model, and this ADR's per-position tally is a batch-shaped adaptation of the same idea, not
a departure from it): v1's model is built around one goroutine-and-channel per in-flight **message**,
which is exactly the per-record overhead arch-v2 exists to eliminate — the committed benchmark
(~6.3× fewer allocations, ~3.3× less memory per record, `20260704-pipeline-architecture-v2.md`)
depends on batches moving through the pipeline as single objects with shared bookkeeping, not as N
independently-scheduled messages each carrying their own ack-handler closures. Porting v1's model
verbatim — a semaphore-and-channel entry per record — would reintroduce the per-record overhead
arch-v2's adoption ADR was written to move away from. `multiAckNacker`'s per-position **tally within
one batch-shaped struct**, updated under one mutex per fan-out call rather than one goroutine/channel
per record, gets the identical unanimity/nack-wins/ordering guarantees while keeping the batch as the
unit of work.

### Lock-free / atomic per-position tally

Considered and rejected on simplicity grounds, not correctness grounds: a mutex-guarded tally is easy
to state, easy to review, and easy to prove correct by inspection (the entire critical section — tally
update plus release attempt — is one function, `releaseLocked`, called under one lock). This is
Tier-1, highest-data-loss-risk code; CLAUDE.md's own review criterion applies directly ("if a reviewer
can't explain the approach back in two sentences, it's too clever"). Given the contention window is
one mutex acquisition per branch-vote (not per record within a large batch — each branch calls
`Ack`/`Nack` once per contiguous sub-batch, not once per record), the throughput cost of the lock is
expected to be negligible relative to the underlying destination I/O it's coordinating; revisit only
if profiling ever shows otherwise.

## Consequences

- **Correctness first, coalescing where it's free.** Acks are coalesced into contiguous runs before
  reaching the parent (preserving the parent's own batch-call semantics and keeping `Source.Ack` call
  volume low); nacks are released one position at a time, because a run of nacked positions can
  legitimately span different branches/task-IDs and `parent.Nack` takes one task ID per call (used for
  DLQ metadata) — coalescing would misattribute the failure reason. This asymmetry is deliberate: nacks
  are the rare/exceptional path, so the extra parent calls there are an acceptable tradeoff for
  attribution correctness.
- **No new durable state.** `multiAckNacker` is entirely in-memory, rebuilt fresh from the replayed
  batch on every restart — see the companion design doc's crash-safety argument
  (`20260731-archv2-multiconnector.md`). This ADR's decision has no serialization format of its own and
  therefore no migration/upgrade-test obligation beyond what the source's existing position-persistence
  already covers.
- **Composition with record-splitting is load-bearing, and holds only for uniform-flag split runs.**
  Because the tally is keyed by `originalBatch()`-collapsed positions, not raw batch indices, a
  per-destination processor that splits records differently on different branches still converges on
  one shared key. This only holds because `Batch.clone()` was fixed (in the same PR) to copy
  `splitRecords` rather than drop it — an ADR-level dependency worth naming explicitly: if `clone()`
  regresses to dropping that map, `multiAckNacker`'s correctness claim above no longer holds for
  pipelines with pre-fan-out splitting.

  **Gap closed (was: narrowing this claim):** convergence used to assume a split run carries a
  _uniform_ status flag by construction, which wasn't true — `Batch.Nack` propagated across a run,
  but `Retry` and `Filter` did not, so a sub-batch could cover only the _head_ of a split run and
  vote ack for the original position before the tail had been delivered (issue #2723).
  `validateAckPositions` already contained the nil-tail half of that shape (an empty position is
  refused rather than acked, which would otherwise overwrite the durable source position), but the
  head case remained open through two review rounds (PR #2725, reverted after an adversarial
  review found the fix's per-write flag-escalation mutated `filterCount` mid-`Task.Do` and could
  corrupt content or hang on a non-converging processor).
  The fix that closed it does not touch `Do` at all: `Batch.normalizeSplitRuns` runs once, after a
  task's `Do` returns and before `Worker.doTask` partitions the batch, and resolves each split run
  by rank (`Nack` > `Retry` > `Filter`/`Ack`). The one deliberate asymmetry — `Filter` is never
  escalated to `Retry` even when the rest of its run is — is itself load-bearing for this ADR's
  convergence claim: it is what lets a retry pass on a split run actually shrink (filtered pieces
  stay excluded from `Batch.ActiveRecords`) instead of re-feeding the processor an identical-sized
  input forever. `Worker.subBatchByFlag` (via `Batch.groupFlagAt`) treats a normalized run as one
  indivisible unit when partitioning, so it can no longer be torn apart at all; a defensive
  `CodeSplitRunPartitioned` guard covers the case where some future write path bypasses
  normalization. This closes #2723 for `Retry`/`Filter`-partitioned split runs, with M=1 and M>1
  (fan-out) both covered by test. It does **not** close #2726 (a non-converging processor's retry
  recursion has no bound) — a separate, still-open concern this fix was required not to make any
  more likely.
- **Extending to N sources is out of scope for this decision.** This ADR only covers 1 source, M
  destinations. A future N-source slice would need its own decision about how (or whether) acks
  compose across multiple `funnel.Worker` instances — not addressed here.

## Related

- Design doc: [20260731-archv2-multiconnector.md](../design-documents/20260731-archv2-multiconnector.md)
  — the full implementation, composition with record-splitting (OQ-1), failure modes, and crash-safety
  argument.
- ADR: [20260704-pipeline-architecture-v2.md](20260704-pipeline-architecture-v2.md) — adopts arch-v2 as
  the target architecture on the strength of its batching model, the same model this ADR's rejected
  v1-port alternative would have undermined.
- `pkg/lifecycle/stream/fanout.go`, `pkg/lifecycle/stream/source_acker.go` — v1's per-message reference
  model this decision adapts to arch-v2's batch shape.
- `pkg/lifecycle-poc/funnel/worker.go` — `multiAckNacker`.
