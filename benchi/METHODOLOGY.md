# Benchmarking arch-v2 vs v1: methodology, and a retraction

This document exists because the first several rounds of arch-v2 vs v1 numbers
produced in this repo were **wrong**, in more than one way, and the corrections
are more useful than the numbers were.

## Retraction

Every engine-comparison figure previously reported from the benchi configs in
this directory is withdrawn:

| claim | status |
| --- | --- |
| 1×1 default: v2 **+6.9%** faster | withdrawn |
| 2×2 default: v2 **−3.4%** slower | withdrawn |
| 2×2 batched: v2 **+62.1%** faster | withdrawn |
| 1×1 default: v2 **−28.8%** slower | withdrawn |
| 2×2 batched: v2 **+196%** faster | withdrawn |

Two independent defects, either one sufficient to invalidate all of them.

### Defect 1: the metric is not comparable between engines

benchi's `msg-rate-per-second` is derived from Conduit's own metrics, and the
two engines do not instrument the same event.

- **v1** observes its histogram inside an **ack handler**
  (`pkg/lifecycle/stream/metrics.go`) — one observation per acked record.
- **arch-v2** calls `ConnectorMetricsImpl.Observe(records, start)` from **both**
  `SourceTask.Do` (on read) and `DestinationTask.Do` (on ack)
  (`pkg/lifecycle-poc/funnel/connector_metrics.go`).

Measured against ground truth (records actually written to a file) on the same
pipeline: ground truth reported **6,342 rec/s** where benchi reported
**29,537** — a ~5× discrepancy, and the v1-vs-v2 delta differed by ~5× as well.
**Any comparison built on that metric measures instrumentation, not throughput.**

### Defect 2: the effect was smaller than the noise floor

No A/A control was ever run. When one finally was — v1 measured against
**itself** — 30-second runs reported deltas of **+13.0%** and **−5.0%**. Every
v1-vs-v2 delta claimed at that duration was smaller than the harness's own
error against a known-zero difference.

## What actually works

**Measure ground truth, not engine metrics.** Write to a real file and count
records. It is engine-independent by construction and cannot be skewed by where
either engine chooses to instrument.

**Run for 60s, not 30s.** The A/A noise floor drops from ±13% to **±3%** — the
difference between "cannot resolve a 10% gate" and "can".

**Always run an A/A control alongside the A/B.** It is the only thing that
tells you whether an observed difference means anything. It is cheap and it
would have caught all of this immediately.

**Alternate single runs; never compare blocks.** Absolute throughput drifted
steadily upward across a session (303k → 348k) as the machine warmed, and in one
round the entire A/B block ran cold while the A/A block ran warm — which
manufactured a +16.5% outlier out of a slow v1. Discard warmup runs and
alternate v1/v2/v1/v2 so drift lands on both arms equally.

**Do not run other work concurrently.** Earlier rounds had `go test -race
-count=40` sweeps running on the same laptop as the benchmark.

## Current state: still unresolved, and honestly so

Best available measurement — ground truth, 60s runs, warmup discarded,
alternating singles, n=6 per engine:

| | median rec/s | sd | range |
| --- | --- | --- | --- |
| v1 | 338,200 | 9.8% | 269k–371k |
| arch-v2 | 252,954 | **20.2%** | 207k–358k |

Median delta **−25.2%**, paired deltas −28.0%, −29.5%, −23.2%, **+12.5%**,
−23.2%, −6.0%. Permutation test: **p = 0.16** — with n=6 this is **not
statistically established**, despite the large effect size.

Two things worth noting rather than glossing:

1. **arch-v2's run-to-run variance is double v1's**, and it looks bimodal —
   mostly ~250k, but reaching 358k (v1 territory) in one run. That is a finding
   in its own right and wants explaining before any throughput conclusion is
   drawn.
2. **The environment degraded over the session.** v1's own sd rose from ~3%
   (A/A block) to 9.8% here, consistent with thermal drift on a laptop across a
   long run.

## Recommendation

Do not settle the v0.20 throughput gate on this machine. A >10% gate needs a
platform whose A/A floor is comfortably under 10% _for the whole run_, and a
laptop under Docker Desktop drifts too much across a long session. Either:

- run this on dedicated/CI hardware with an A/A control in the same session, or
- replace the Docker harness with an in-process Go benchmark that excludes
  container and destination I/O entirely.

Whatever is chosen, the gate decision should cite an A/A floor next to the
result. A number without one is not evidence.
