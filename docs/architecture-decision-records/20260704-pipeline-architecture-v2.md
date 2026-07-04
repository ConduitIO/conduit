# Pipeline architecture v2: adopt as target, graduate behind explicit criteria

## Summary

Adopt the v2 pipeline architecture (`pkg/lifecycle-poc`, a batch-oriented
"funnel") as the target replacement for v1 (`pkg/lifecycle`, a record-at-a-time
node graph). The decision is driven by a measured **~6.3× reduction in
allocations and ~3.3× reduction in memory per record** in the data path. v2 stays
behind the `-preview.pipeline-arch-v2` flag with v1 as the default until it meets
the graduation criteria below. We do **not** delete v2 (the win is proven) and do
**not** make it the default prematurely (it is incomplete and lacks recovery
parity).

## Context

Two pipeline lifecycle implementations exist in the tree:

- **v1** — `pkg/lifecycle`, the default. A node graph
  (`SourceNode → SourceAckerNode → DestinationNode → DestinationAckerNode`)
  that moves records one at a time across per-node goroutines and channels, with
  per-record acking.
- **v2** — `pkg/lifecycle-poc`, opt-in via `-preview.pipeline-arch-v2`. A
  `funnel` of tasks driven by a worker that processes records in batches.

v2 was introduced as a preview in #1913 with **no ADR, no completion criteria,
and no committed benchmark**. That left it an open question that silently taxes
every data-path change: the metrics fix (#2268), the SIGTERM graceful-shutdown
work, and the force-stop ack-safety follow-up (#2519) each have to be reasoned
about in both implementations. v2 is also **incomplete** — its own flag help
states it "currently supports only 1 source and 1 destination per pipeline."

To decide its fate on evidence rather than assertion, we benchmarked both
architectures on the same workload.

### Benchmark

Committed, reproducible microbenchmarks isolating data-path orchestration
(no-op mocked connectors, no real I/O), each processing exactly 100,000 records
generator → printer:

- v1: `pkg/lifecycle/stream/benchmark_test.go` `BenchmarkStreamOld`
- v2: `pkg/lifecycle-poc/funnel/funnel_test.go` `BenchmarkStreamNew`

| Metric (per 100k records) | v1 (record-at-a-time) | v2 (1000-record batches) | v2 advantage |
| --- | --- | --- | --- |
| Allocations | ~6,905,000 | ~1,104,000 | **~6.3× fewer** |
| Memory | ~250 MB | ~76 MB | **~3.3× less** |
| Per record | ~69 allocs / ~2497 B | ~11 allocs / ~764 B | — |

The driver is batching: v2's source returns 1000-record batches per read and the
worker moves them as a unit, amortizing the per-record channel hops, function
calls, and acking that v1 pays on every single record across four nodes.

Allocation and GC pressure are primary drivers of sustained throughput and tail
latency under load, so this efficiency gap implies a material throughput
advantage for the data-path orchestration itself. A full end-to-end records/sec
figure via benchi on a reference pipeline is **not yet measured** and is a
graduation criterion below — the win is proven at the allocation level, and the
end-to-end magnitude (which is also bounded by connector I/O in real pipelines)
must be confirmed before v1 is removed.

## Decision

1. **Adopt v2 as the target architecture.** v1 is legacy and will be removed once
   v2 graduates. The ~6× allocation win makes deleting v2 (discarding a proven
   efficiency architecture and the work already done) the wrong call.
2. **v2 remains behind `-preview.pipeline-arch-v2` with v1 as default** until it
   meets all graduation criteria:
   - Multi-source and multi-destination support (v1 feature parity).
   - Error-recovery parity with the behavior in
     [20240812-recover-from-pipeline-errors](20240812-recover-from-pipeline-errors.md).
   - A chaos-test correctness bar: SIGKILL/SIGTERM mid-batch, mid-checkpoint;
     verify data-integrity invariants 1–7 hold on recovery. This seeds
     `tests/chaos` (which does not exist yet).
   - A committed benchi throughput comparison confirming the win end-to-end on a
     reference pipeline.
   - Human Tier-1 sign-off. Per the process-maturity table, the chaos suite and a
     second maintainer are Phase 2 gates — so realistically v2 graduates in
     Phase 2, not Phase 1.
3. **Bound the dual-maintenance tax until graduation.** New data-path behavior is
   designed to be portable to both implementations. Where a change can only land
   in one, the v2 gap is tracked explicitly (an issue) so graduation is never
   blocked by silent drift. We do not add v1-only data-path features that widen
   the parity gap without recording them.
4. **Do not rush graduation.** Making an incomplete, unrecovered architecture the
   default would trade a data-integrity guarantee for throughput — forbidden
   without the design-doc-and-sign-off path.

## Consequences

- The fork is now a **documented, criteria-gated decision** instead of an open
  question. Every future data-path PR knows the target and the finish line.
- The dual-maintenance tax continues until graduation, but is bounded by the
  portability rule and the explicit criteria. Prioritizing v2 completion shortens
  the window; letting it drift lengthens it.
- Graduation depends on a second maintainer for the Tier-1 completion and review,
  consistent with the process-maturity table. This ADR does not manufacture that
  capacity — it commits the direction so the work is ready when the capacity
  exists.
- Phase 1 data-path items (the #2519 force-stop follow-up, v0.17 hot-reload) must
  be reasoned about in both implementations; this ADR makes that explicit rather
  than incidental.
- If a future benchi run fails to confirm a meaningful end-to-end throughput win
  (e.g. connector I/O dominates on all realistic reference pipelines), this ADR
  is superseded by a new one that reconsiders deletion — the allocation win alone
  justifies keeping v2, but not necessarily finishing it if end-to-end shows no
  benefit.

## Related

- #1913 — Pipeline architecture v2 (preview)
- #2268 — metrics change carried into both implementations
- #2519 — SIGTERM graceful shutdown; force-stop ack-safety follow-up spans both
- [20240812-recover-from-pipeline-errors](20240812-recover-from-pipeline-errors.md)
- Benchmarks: `pkg/lifecycle/stream/benchmark_test.go`,
  `pkg/lifecycle-poc/funnel/funnel_test.go`
- Phase 1 execution plan: `docs/design-documents/20260704-phase-1-execution-plan.md`
