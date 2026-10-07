# arch-v2 graduation: a go/no-go at the v0.21 midpoint against a gate fixed before measuring

## Summary

Making arch-v2 (`pkg/lifecycle-poc`) the default pipeline engine is a **go/no-go decision at the
v0.21 midpoint**, not a committed flip. The decision is taken against the gate written down here,
before any of the measurements it depends on exist. A no-go is a legitimate outcome: arch-v2 then
stays opt-in behind `--preview.pipeline-arch-v2`, remains the only engine that runs record fan-out
pipelines, and graduation moves to the next release.

This ADR refines the graduation criteria of
[20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md). It does not replace
them: every criterion there still applies. It makes the performance criterion measurable, because
as written it cannot be met with the evidence and tooling that exist today.

Decided by DeVaris, 2026-10-06.

## Context

### What the 20260704 ADR requires

arch-v2 stays behind the preview flag, with v1 (`pkg/lifecycle`) as the default, until all of these
hold: multi-source and multi-destination parity with v1, error-recovery parity, a chaos-test bar
(SIGKILL/SIGTERM mid-batch and mid-checkpoint, invariants 1-7 verified on recovery), a committed
benchi throughput comparison confirming the win end to end on a reference pipeline, and Tier-1
human sign-off.

The v0.20 planning moved the _schedule_ for the flip to v0.21. That was a scheduling decision, not a
finding that any criterion had been met. The performance criterion in particular is unmet, for the
reasons below.

### Every v1-vs-v2 throughput number has been retracted

PR #2748 (`fc812a52`, documented in [`benchi/METHODOLOGY.md`](../../benchi/METHODOLOGY.md)) withdrew
every engine-comparison figure produced from the benchi configs in `benchi/`: 1x1 default +6.9%,
2x2 default -3.4%, 2x2 batched +62.1%, and the later 1x1 -28.8% and 2x2 batched +196%. Two
independent defects, either one sufficient:

1. **The metric is not comparable between engines.** benchi's rate comes from Conduit's own
   metrics. v1 observes once per record in an ack handler (`pkg/lifecycle/stream/metrics.go`).
   arch-v2 observes in both `SourceTask.Do` (on read) and `DestinationTask.Do` (on ack)
   (`pkg/lifecycle-poc/funnel/connector_metrics.go`). Checked against ground truth, records
   actually written to a file, the same pipeline produced 6,342 rec/s where benchi reported
   29,537: about 5x off, with the v1-vs-v2 delta off by about 5x as well.
2. **No A/A control was run.** When v1 was finally measured against itself, 30-second runs
   reported +13.0% and -5.0%. Every claimed delta at that duration sat inside the harness's own
   error against a known-zero difference. At 60 seconds the A/A floor dropped to about ±3%.

The best measurement that followed (ground truth, 60s runs, warmup discarded, alternating single
runs, n=6 per engine, on a laptop under Docker Desktop) put arch-v2 at a -25.2% median delta with
p = 0.16, arch-v2's run-to-run spread double v1's and apparently bimodal, and v1's own spread rising
from about 3% to 9.8% over the session. It is not evidence in either direction, and #2748 says so.

### The allocation figure does not describe the default configuration

The 20260704 ADR's headline, about 6.3x fewer allocations and 3.3x less memory per record, came from
`BenchmarkStreamNew`, whose mocked source returns 1000-record batches. The shipped default is
`sdk.batch.size=0`, under which the SDK calls `readFn(ctx, 1)` and arch-v2 reads batches of one. The
figure is a property of batched input, not of what a pipeline on default config does.

### What is valid: arch-v2's own per-pass and fan-out costs

PR #2754 (`0ef64e19`, `pkg/lifecycle-poc/funnel/bench_engine_test.go`) measured arch-v2 in process,
with `b.N` and wall time as the measurement and no engine metrics consulted:

| Benchmark | ns/rec | allocs/rec |
| --- | --- | --- |
| `BenchmarkEnginePass`, batch 1 | 1,847 | 14.0 |
| `BenchmarkEnginePass`, batch 1000 | 654 | 5.0 |
| `BenchmarkEngineFanOut`, 1 destination | 818 | 5.1 |
| `BenchmarkEngineFanOut`, 2 destinations | 4,290 | 16.6 |
| `BenchmarkEngineFanOut`, 4 destinations | 7,289 | 26.9 |

- Each pass carries about 1,190 ns and 9 allocations of fixed cost. At batch size one, every record
  pays all of it.
- Fan-out is superlinear: two destinations cost 5.2x the per-record cost of one, four cost 8.9x.
  The cost is in `doNextTask`'s clone and `pool.Go` path. The shared-boundary mutex was measured
  and is not the cost.

These results compare arch-v2 with itself. v1 has no equivalent entry point, so they say nothing
about v1 vs v2. **No valid cross-engine comparison exists today.**

### Why the gate is written now

A gate chosen after the numbers are in can be fitted to them. #2748's history shows how that
happens in practice: a first run was reported as the answer before repeats contradicted it, and the
metric itself was wrong for several rounds before anyone checked it against ground truth. The
cheapest protection is to fix the method and the pass condition first.

## Decision

1. **Build a cross-engine harness before measuring.** It must:
   - count records at the sink (ground truth), never engine metrics;
   - use 60-second runs, discard warmup, and alternate v1 and v2 single runs so drift lands on
     both arms;
   - run an A/A control (v1 against v1) in the same session and report its floor beside every A/B
     result;
   - run on quiesced hardware with nothing else on the machine;
   - commit its configs and results, so the run is reproducible.
2. **Fix the superlinear fan-out cost first.** The 2x2 shape is part of the gate, and measuring it
   against a known per-record cost of 5.2x at two destinations would measure that defect, not the
   engine. The fix is a data-path change and follows the Tier-1 process. It is done when
   `BenchmarkEngineFanOut` shows per-record cost growing roughly in proportion to destination
   count.
3. **The gate.** arch-v2 graduates only if all of the following hold:
   - on 1x1 and 2x2 pipelines at default config, arch-v2's median throughput is not lower than
     v1's by more than the A/A noise floor measured in the same session;
   - recovery parity with v1;
   - the chaos suite is green;
   - Tier-1 human sign-off;
   - every remaining criterion of [20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md).

   A result the platform cannot resolve, because the A/A floor is too wide to separate the engines,
   is not a pass.
4. **Decided at the v0.21 midpoint.** If the gate is met, the flip lands in v0.21. If not, or if
   the harness and the fan-out fix are not ready by then, the answer is no-go: arch-v2 stays opt-in,
   stays required for record fan-out pipelines (v1 cannot run them and stops with
   `pipeline.fanout_requires_arch_v2`), and graduation moves to the next release. No-go is a
   legitimate outcome, not a failure to plan.
5. **Optional follow-up, own design doc.** Conduit could select arch-v2 automatically only for
   pipelines that need record fan-out, independent of the default. That changes engine selection,
   which is a data-path decision, and is out of scope here.
6. **No performance claims about arch-v2 until the harness exists.** Not in release notes, docs,
   or the README, in either direction. Performance claims require reproducible benchi results
   committed to the repo; until a cross-engine harness meeting item 1 exists, none can be made.

## Consequences

- The `postgres-pgvector-rag` template, and any pipeline with a record-splitting processor
  (`ai.chunk`, `split`, `clone`), still requires `--preview.pipeline-arch-v2` in v0.20.
- The v0.20 release notes make no performance claim about arch-v2. They state that graduation is
  decided at the v0.21 midpoint against this gate.
- The benchi regression gate, not yet live and targeted at v0.21, becomes load-bearing for the
  flip. A default engine change without a standing regression check would leave the first
  regression to be found by users.
- The 20260704 ADR's 6.3x figure stays in that ADR, because ADRs are immutable, but should not be
  quoted as what users get. This ADR is the reference for arch-v2 performance until the harness
  produces a result.
- v1 and arch-v2 dual maintenance continues at least through the v0.21 decision, bounded by the
  portability rule in the 20260704 ADR. A no-go extends it by one release.
- If the measured gap is real and large, the outcome may be a new ADR reconsidering arch-v2's
  scope, as the 20260704 ADR already anticipates. This gate does not pre-decide that.

## Related

- [20260704-pipeline-architecture-v2](20260704-pipeline-architecture-v2.md): graduation criteria
  this ADR refines
- [20260731-archv2-fanout-ack-model](20260731-archv2-fanout-ack-model.md)
- [20260801-archv2-run-join](20260801-archv2-run-join.md)
- [20260801-archv2-run-join-defer-fanout](../design-documents/20260801-archv2-run-join-defer-fanout.md)
- [`benchi/METHODOLOGY.md`](../../benchi/METHODOLOGY.md): benchmark methodology and the retraction
- #2748: retraction of the v1-vs-v2 numbers
- #2754: in-process per-pass and fan-out costs (`pkg/lifecycle-poc/funnel/bench_engine_test.go`)
- #2273: arch-v2 feature finalization
