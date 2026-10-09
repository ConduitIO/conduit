# Conduit ships one pipeline engine, and users never choose it

## Summary

Conduit has exactly one pipeline engine. Users do not choose between engines, now or later. The target is the **staged
batch engine** described in
[20261009-staged-batch-engine](../design-documents/20261009-staged-batch-engine.md): long-lived goroutines per source
and per destination, bounded queues, several batches in flight, and source acks released only as the longest
contiguous completed prefix.

This ADR **supersedes**
[20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md) and
[20261006-archv2-graduation-gate](20261006-archv2-graduation-gate.md). The graduation criteria those ADRs set are
carried forward into the bar below, with the measurable gate made stricter, not dropped.

Direction approved by DeVaris on 2026-10-09. This ADR takes effect when it merges, which requires Tier 1 review of the
design document from a fresh-context session. It must not merge before the v0.20.0 tag.

## Context

Two engines exist. v1 (`pkg/lifecycle`) is the default: a record-at-a-time node graph whose stages overlap but which pays
per-record allocation and cannot run processors that return several records for one input. arch-v2
(`pkg/lifecycle-poc`, `--preview.pipeline-arch-v2`) moves batches through a `funnel.Worker`, and the RAG template needs
it. The 20260704 ADR adopted arch-v2 as the target on the strength of a 6.3x allocation reduction measured on a mocked
1000-record-batch microbenchmark. The 20261006 ADR then fixed a go/no-go gate at the v0.21 midpoint, because every
earlier cross-engine throughput number had been retracted (#2748).

The first valid cross-engine read exists now (AWS c7i.4xlarge, main `ce758f96`, records counted at the sink, A/A
controls in session, non-gating): arch-v2 against v1 is -25.0% at 1x1 (A/A floor 2.3%) and -6.0% at 2x2 (floor 1.8%).
Sharing records across fan-out branches (#2946) moved arch-v2 by +0.9% against a floor of 1.2%. Allocation was not the
limit.

The working diagnosis, to be confirmed by the prototype's profile, is that `funnel.Worker.Do` is stop-and-wait: read,
process, write, wait for every ack, ack the source, and only then read again. One batch is in flight per source, and
behind the N-source shared-tail lock one per destination. At the default batch size of one record that is a full round
trip per record. v1 overlaps its stages and so is bounded by its slowest stage instead of the sum.

Separately, two engines selectable by a flag is a product problem independent of speed. Every data-path change has to be
reasoned about twice, behaviour already differs by engine (persister failures, one-to-many processors), and users
cannot make an informed choice. The 20260704 ADR bounded that cost; it did not remove it.

## Decision

1. **One engine.** Conduit ships a single pipeline engine. There is no user-facing switch between engines. The only
   selector that may exist is a hidden fallback to v1 during one release (v0.22), with a tracking issue and a runbook
   entry, deleted in v0.23.
2. **The target is the staged batch engine.** Its architecture, invariant arguments, ordering contract, failure modes,
   migration and test plan are in the design document and are part of this decision by reference. In outline: a reader
   and a runner per source, a merge stage only when pipeline-level processors exist, an inbox, a writer and an AckReader
   per destination, and a ledger with a coordinator per source. Credits bound memory. Writes do not wait for acks.
   `Source.Ack` is called only for the longest contiguous prefix whose every record is durably handled by all
   destinations, filtered, or written to the DLQ. Positions are persisted exactly as today.
3. **One-to-many processors are supported natively.** The engine runs `sdk.MultiRecord` processors (`ai.chunk`, `split`,
   `clone`) and split-run semantics, including across destination fan-out, because the RAG template depends on them.
4. **No new tuning knobs by default.** Batch size, linger, credit window and write size are derived. Anything that
   becomes configurable needs its own justification.
5. **No protocol, position or state-format change.** The connector protocol, the persisted `SourceState{Position}`, and
   the persister are unchanged. This is tested, not asserted (upgrade and downgrade across engines, a golden test, a
   key-layout check).
6. **Rollout.** v0.21: prototype, this design, the staged engine implemented in slices, nightly differential test.
   v0.22: the staged engine is the default; v1 is a hidden fallback; `--preview.pipeline-arch-v2` is accepted and
   ignored with a deprecation warning. v0.23: v1 and the fallback are deleted. The ignored flag is removed no earlier than
   v0.24, two minors after the first warning, per the deprecation policy.
7. **Graduation bar for the v0.22 default** (all required; otherwise the flip moves, and that is a legitimate outcome):
   - median throughput not lower than v1 by more than the same-session A/A floor at 1x1, 2x2 and 4x4 on the AWS harness,
     and higher by more than the floor on latency-injected destination shapes;
   - lower steady-state memory and allocations per record than v1, measured;
   - differential test, chaos suite (including SIGKILL with k batches in flight) and upgrade/downgrade tests green;
   - recovery parity with v1, including persister-failure behaviour;
   - pipelined-write acceptance test passing on built-in and certified connectors;
   - a clean 24 h soak, run manually until a CI soak exists;
   - the benchi regression gate live in CI;
   - Tier 1 sign-off by DeVaris from a fresh-context session.
8. **Fallback plan.** If the prototype does not bring 1x1 to v1 parity because the bottleneck is below the engine,
   this decision is reassessed before further investment, and batching messages inside v1's node graph is evaluated
   next. A new ADR would record that.
9. **Performance claims** about the staged engine follow the existing rule: none until reproducible benchi results are
   committed.

## Consequences

- The dual-maintenance tax ends at v0.23. Until then, the portability rule of the 20260704 ADR is replaced by a simpler
  one: new data-path behaviour targets the staged engine; v1 receives only fixes, and any fix that lands only in v1 is
  recorded as an issue.
- Users of `--preview.pipeline-arch-v2` get a behaviour change: bounded memory, a larger duplicate window after a crash
  (the credit window, not one batch), persister failures degrading the pipeline as in v1, DLQ writes in source order at
  prefix release, processors receiving batches of more than one record, and normalised connector metrics. Users of the
  default engine see the same changes at v0.22, plus the processor batch-size change. The design document lists each with
  its audience and mitigation.
- Several error codes stop being emitted (`pipeline.shared_destination_poisoned`, `pipeline.split_run_straddles_fanout`);
  they remain registered through the deprecation window. `pipeline.ack_protocol_violation` is added.
- The straddling-split-run failure that main still reports under destination fan-out disappears, because a stage
  completes its batch before emitting it. The defer-the-fan-out mechanism in
  [20260801-archv2-run-join](20260801-archv2-run-join.md) is not implemented on main and is not needed; the requirement
  it protects is met another way.
- The graduation decision stops being a single midpoint go/no-go and becomes a bar with an explicit no-go outcome. The
  benchi regression gate (not live today) becomes a precondition of the flip.
- This ADR does not supersede the other arch-v2 ADRs, and they stay in force until their code is deleted. Specifically
  20260731-archv2-fanout-ack-model (the per-position unanimity tally the new ledger generalises),
  20261007-stop-requested-never-recovers, 20260704-single-node-engine, 20260704-local-state-only and
  20260823-columnar-record-representation-scoped-to-archv2 all stand. When v0.23 deletes the funnel's stop-and-wait code,
  the design documents that describe mechanisms removed with it (`sharedMu` and poisoning in
  20260801-archv2-multiconnector-nsource, defer-the-fan-out in 20260801-archv2-run-join-defer-fanout) are recorded as
  retired in the implementing change, since immutable documents cannot be edited.
- Cost accepted: a rewrite of the Tier 1 core of the engine, reviewed by one human. The mitigations are the differential
  test against v1, the extended chaos and upgrade suites, the invariant-by-invariant argument, and the graduation bar.
- Open questions that block implementation slices are listed in the design document (credit defaults, graceful stop
  protocol, the inspect API carrier, persister-failure semantics with the EV lane). None block this decision.

## Related

- [20261009-staged-batch-engine](../design-documents/20261009-staged-batch-engine.md): the design this ADR adopts.
- Superseded: [20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md),
  [20261006-archv2-graduation-gate](20261006-archv2-graduation-gate.md).
- [20260731-archv2-fanout-ack-model](20260731-archv2-fanout-ack-model.md),
  [20260801-archv2-run-join](20260801-archv2-run-join.md),
  [20261007-stop-requested-never-recovers](20261007-stop-requested-never-recovers.md),
  [20260704-single-node-engine](20260704-single-node-engine.md),
  [20260704-local-state-only](20260704-local-state-only.md),
  [20260823-columnar-record-representation-scoped-to-archv2](20260823-columnar-record-representation-scoped-to-archv2.md).
- `benchi/METHODOLOGY.md`; #2748 (retraction), #2754, #2946, #2956 (cross-engine harness), #2929, #2945 (roadmap).
