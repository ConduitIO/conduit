# Conduit ships one pipeline engine, and users never choose it

## Summary

Conduit has exactly one pipeline engine. Users do not choose between engines, now or later. Its principles are:

1. One engine, and no user-facing way to select another.
2. Source acknowledgements are released through a per-source **ledger**, one position at a time, in source order, as the
   longest contiguous run of positions that are durably handled.
3. Memory is bounded by **credits**: a source may have only a bounded number of positions read and not yet durably
   persisted.
4. The seven data-integrity invariants hold, as stated below.
5. Ordering is per source, per destination, and nothing across sources.
6. Processors are placed by the documented pipeline flow: per-source instances by default, with a serial stage only for
   processors that declare themselves stateful.

This ADR **supersedes**
[20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md) and
[20261006-archv2-graduation-gate](20261006-archv2-graduation-gate.md). The mechanisms that implement these principles
(stages, goroutines, batching, defaults, observability, tests, rollout) are deliberately **not** in this ADR. They live
in
[20261009-staged-batch-engine](../design-documents/20261009-staged-batch-engine.md), which stays mutable.

**Status: proposed, held.** Direction approved by DeVaris on 2026-10-09, refined the same day after a fresh-context
  Tier 1
review. This ADR **merges only after the design document records the prototype results and the Alternative A run**
(batched
arch-v2 against batched v1), and not before the v0.20.0 tag. If those results undercut the decision, this ADR is revised
before merge or withdrawn.

## Context

Two engines exist. v1 (`pkg/lifecycle`) is the default: a record-at-a-time node graph whose stages overlap, which
honours
processor `workers > 1`, and which cannot run a processor that returns several records for one input. arch-v2
(`pkg/lifecycle-poc`, `--preview.pipeline-arch-v2`) moves batches through a `funnel.Worker`, ignores `workers`, and is
required by the RAG template. The 20260704 ADR adopted arch-v2 as the target on an allocation microbenchmark. The
20261006
ADR then fixed a go/no-go gate because every earlier cross-engine throughput number had been retracted (#2748).

An early, non-gating cross-engine read now suggests arch-v2 is meaningfully slower than v1 at one source and one
destination, and the working diagnosis is that its loop is stop-and-wait: one batch in flight per source. The figures
are
not in the repository yet and are deliberately not quoted here; they are in the design document's mutable section and
are
to be committed with the benchmark harness (#2956).

Independently of speed, two engines selectable by a flag is a product problem: every data-path change is reasoned about
twice, behaviour already differs by engine (persister failures, one-to-many processors, processor `workers`), and users
cannot make an informed choice.

## Decision

1. **One engine.** Conduit ships a single pipeline engine. There is no user-facing switch. A hidden, dated fallback to
   v1
   may exist for one release.
2. **The ledger and in-order release.** Each source has a ledger of positions. A position becomes terminal when every
   destination has durably handled it, or a processor filtered it, or it has been written to the dead-letter queue.
   Source
   acknowledgements are released only for the longest contiguous run of terminal positions, in source order. Release is
   per position, not per batch, so that a failure part-way through a batch, and split records, are handled exactly.
3. **Bounded credits.** A source reads only while it has credits. Credits are returned when a position is durably
   persisted,
   not merely handed to the persister. This bounds memory, the duplicate window after a crash, and the engine's pending
   acknowledgement state.
4. **Invariants 1 to 7 are preserved**, with these engine-level statements:
   1. A source record is acknowledged only after every destination has durably handled it, it was filtered, or it is in
      the
      DLQ. Only an explicit positive acknowledgement from a destination, or a processor's own filter or nack decision,
      can
      cast a vote. Cancellation, timeouts and errors never do.
   2. Positions advance monotonically through one writer per source, in contiguous order, and the persisted position
      format
      is unchanged.
   3. At-least-once is the floor: any record not terminal and released is replayed; no queue drops; a DLQ failure fails
      the
      pipeline without acknowledging past the last confirmed DLQ write.
   4. Ordering is as stated in the next item.
   5. The engine adds no durable state; any future state is checkpointed atomically with the released prefix.
   6. The engine never reads, rewrites or coerces payloads.
   7. Graceful shutdown stops reading, flushes destinations before waiting for acknowledgements, drains within a
      deadline,
      and checkpoints; a `kill -9` at any instant is recoverable by replay.
5. **Ordering contract.** For each source and each destination, the records a destination first receives arrive in the
   order
   the source produced them, through filters, splits and DLQ removals. Pieces of a split record are adjacent. There is
   no
   ordering across sources. Redelivery after a crash repeats records but does not reorder them within the replay.
6. **Processor placement follows the documented flow.** Source processors run in their source's path. Pipeline-level
   processors run, by default, as **one instance per source in that source's path**, in parallel across sources, which
   keeps per-source order and the absence of cross-source ordering. A processor that declares itself stateful (an
   opt-out)
   runs once, on a serial stage after fan-in, together with every processor after it in the chain. Destination
   processors
   run once per destination, before the write. This replaces processor `workers > 1`. The stateful declaration is a
   public
   processor-spec contract change and is versioned with the processor SDK.
7. **One-to-many processors are supported natively**, including across destination fan-out.
8. **No new tuning knobs** by default, **no connector-protocol change**, and **no persisted position or state format
   change**.
9. **No performance claim** about the new engine is made without reproducible benchmark results committed to the
   repository.
10. **Graduation.** The new engine becomes the default only when, measured on the committed harness against
    same-session A/A
    controls, it is not slower than v1 beyond the noise floor, at lower memory, including when users set batch sizes and
    compared against batched arch-v2; when the processor model has shipped; and after Tier 1 sign-off. A result the
    harness
    cannot resolve is not a pass. There is **no "stay opt-in" outcome**: failing the bar delays the default, it does not
    keep a second engine alive. The detailed bar, numbers and tests are in the design document.

## Consequences

- The dual-maintenance tax ends when v1 is deleted. Until then new data-path behaviour targets the new engine and v1
  receives fixes only.
- Users of `--preview.pipeline-arch-v2` and, at the default flip, everyone get behaviour changes (bounded memory, a
  credit-window duplicate bound, per-source processor instances, restart instead of in-place processor reconfigure,
  normalised metrics). The design document lists each with its audience.
- **What replaced the criteria of the superseded ADRs.**
  - 20260704: multi-source and multi-destination parity with v1 is a graduation condition (the ported N-source and N x M
    suites plus the differential test); error-recovery parity per
    [20240812-recover-from-pipeline-errors](../design-documents/20240812-recover-from-pipeline-errors.md) is a
    graduation
    condition; the chaos bar (SIGKILL and SIGTERM, mid-batch and mid-checkpoint, invariants 1 to 7) is a graduation
    condition, extended to many positions in flight; the committed end-to-end benchi comparison is a graduation
    condition
    under the rules below; Tier 1 human sign-off is retained; the dual-maintenance portability rule is replaced by the
    first
    consequence above; "do not rush graduation" is replaced by the no-pass-if-unresolved rule.
  - 20261006: the harness rules (ground-truth counting at the sink, 60-second runs with warmup discarded, alternating
    runs,
    A/A control in session, quiesced hardware, committed configs and results) are retained for the graduation evidence;
    the
    1x1 and 2x2 shapes are extended to 4x4, to configurations with batch sizes set, and to destinations with injected
    latency; "fix the superlinear fan-out cost first" is subsumed by the engine's shared-batch dispatch; the v0.21
    midpoint
    go/no-go is replaced by the bar; the **"no-go keeps arch-v2 opt-in" outcome is removed**; "no performance claims
    until
    the harness exists" is retained as decision 9.
- **Mechanisms from the other arch-v2 ADRs and design documents.** They stay in force until their code is deleted, and
  this
  ADR does not supersede them.
  - Survive: the per-position unanimity, nack-wins tally released in source order (20260731-archv2-fanout-ack-model),
    now the
    ledger; the split-run ledger semantics and per-destination run metadata (20260801-archv2-split-run-ack-ledger); the
    requirement that split runs never stall or lose a position under fan-out (20260801-archv2-run-join); per-source
    workers,
    per-source DLQ naming and the rule that the shared sink closes only after every worker has exited
    (20260801-archv2-multiconnector-nsource); the bounded `StopAndWait` drain and the refusal of live processor
    reconfigure
    (20260731-archv2-drain-reconfigure); stop-never-recovers (20261007); single-node, local-state-only and the columnar
    scoping ADRs, unchanged.
  - Retire with the funnel's loop: `TaskNode.sharedMu`, the poisoning mechanism and
    `pipeline.shared_destination_poisoned`
    (nsource); the per-pass branch goroutine pool and the batch-level `multiAckNacker` (multiconnector); the
    defer-the-fan-out
    buffer, which was never implemented on main (20260801-archv2-run-join, whose requirement is met by completing a
    batch's
    processing before dispatch); `pipeline.split_run_straddles_fanout` as an operator-facing condition.
  - The implementing change records each retirement, since immutable documents cannot be edited.
- Cost accepted: a rewrite of the Tier 1 core of the engine, reviewed by one human. Mitigations are the differential
  test
  against v1, extended chaos and upgrade tests against released binaries, the invariant arguments, and the bar.
- Open questions that gate implementation slices are in the design document. The ones that can still change this
  decision:
  whether the Alternative A run closes the gap without a rewrite, and how processor `workers > 1` on a single-source
  pipeline is covered before the default flip.

## Related

- [20261009-staged-batch-engine](../design-documents/20261009-staged-batch-engine.md): the mechanisms, tests, migration
  and
  rollout.
- Superseded: [20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md),
  [20261006-archv2-graduation-gate](20261006-archv2-graduation-gate.md).
- [20260731-archv2-fanout-ack-model](20260731-archv2-fanout-ack-model.md),
  [20260801-archv2-run-join](20260801-archv2-run-join.md),
  [20261007-stop-requested-never-recovers](20261007-stop-requested-never-recovers.md),
  [20260704-single-node-engine](20260704-single-node-engine.md),
  [20260704-local-state-only](20260704-local-state-only.md),
  [20260823-columnar-record-representation-scoped-to-archv2](20260823-columnar-record-representation-scoped-to-archv2.md).
- `benchi/METHODOLOGY.md`; #2748, #2956, #2929, #2945.
