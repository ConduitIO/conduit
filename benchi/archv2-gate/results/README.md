# archv2-gate results

## 2026-11-27, dedicated c7i.metal-24xl — the gate run

The first cross-engine measurement this harness has produced, and the first
v1-vs-arch-v2 numbers in the project that were not retracted. Six sessions:
an A/A control for each engine on each shape, then the two A/B sessions that
carry their own A/A control.

- Machine: one whole physical host, `c7i.metal-24xl` (Intel Xeon Platinum 8488C,
  96 cores, 188 GiB), Amazon Linux 2023, kernel 6.18, Docker Engine 25.0.18 —
  no hypervisor between the container and the CPU.
- **Quiesced, and measured as such:** CPU steal was 0 in all 1,301 `vmstat`
  samples, the clocksource was `tsc`, the CPU governor was pinned to
  `performance`, nothing else ran on the host, and load stayed under 3 for the
  whole session (`vmstat.log`, `loadavg.log`, `environment.txt`).
- Image: built once from `80c8e679`, the same image for every arm of every
  session; the only difference between arms is `--preview.pipeline-arch-v2`.
- 5 rounds per session, 20s warmup discarded, 60s window, records counted as
  newlines in the sink's byte range. No engine metric is read.

### A/B results

| shape | arch-v2 vs v1, median | per-round deltas | A/A floor, same session | resolvable |
| --- | --- | --- | --- | --- |
| 1x1 | **+97.4%** | +97.3%, +95.9%, +96.7%, +96.4%, +98.8% | ±2.2% | yes, 44x the floor |
| 2x2 | **+42.6%** | +43.2%, +44.3%, +42.4%, +41.3%, +41.7% | ±1.6% | yes, 26x the floor |

Median rates per sink: 1x1 v1 63,055 and arch-v2 124,607 rec/s; 2x2 v1 41,770
and arch-v2 59,392 rec/s.

### Why this is not one lucky session

The four A/A sessions were run first, before either A/B session, and they
reproduce the A/B medians to within a percent:

| shape | engine | A/A session median | same engine in the A/B session | A/A floor |
| --- | --- | --- | --- | --- |
| 1x1 | v1 | 63,545 | 63,055 | ±1.5% |
| 1x1 | arch-v2 | 124,870 | 124,607 | ±0.3% |
| 2x2 | v1 | 41,886 | 41,770 | ±2.1% |
| 2x2 | arch-v2 | 59,366 | 59,392 | ±0.3% |

Six independent sessions, one machine state, same answer. arch-v2's own
run-to-run spread is 0.1-0.2% here, against the "double v1's and apparently
bimodal" spread that #2748 saw on a laptop.

### Against the earlier laptop read

The best previous measurement put arch-v2 at **-25.2%** on 1x1 and was
explicitly not treated as evidence (#2748). This run is the opposite sign, and
the difference is not explained by the harness: the method is the same one that
document prescribed. Two candidates, neither verified here:

- The October read predates the fan-out deep-copy removal (#2946) and arch-v2's
  destination-write batching and read-ahead (#3201, #3210), all of which are in
  `80c8e679`.
- It ran under Docker Desktop in a 16-CPU, 7.7 GB VM on a shared laptop, where
  the A/A floor for the same shape reached ±104.6% (see the section below).

Separating those would need the laptop session re-run at `80c8e679`. Nothing in
the gate requires it.

### What this does and does not settle

Met: the graduation gate's throughput criterion — on 1x1 and 2x2 at default
config, arch-v2's median is not lower than v1's by more than the A/A floor
measured in the same session. It is higher by 26 to 44 times that floor.

Not settled by this run, and still required by
[the gate ADR](../../../docs/architecture-decision-records/20261006-archv2-graduation-gate.md)
and [20260704](../../../docs/architecture-decision-records/20260704-pipeline-architecture-v2.md):
recovery parity with v1, the chaos-test bar, Tier-1 human sign-off, and the
remaining 20260704 criteria. Throughput is a rate, not a correctness claim: this
harness counts records at the sink in a window and never checks that the records
are the right ones, in order, exactly once. The chaos suite is what does that,
and it runs both engines.

The 2x2-batched shape was not run; it is context, not part of the gate.

## 2026-10-08, laptop, A/A only

Four A/A sessions run once to check that the harness works and to see the
noise floor on a developer laptop. **These are not gate results and contain no
v1-vs-v2 comparison.** The gate is decided at the v0.21 midpoint on quiesced
hardware.

- Machine: Apple M3 Max (16 cores, 64 GB), Docker Desktop 29.6.2 (16 CPUs,
  7.7 GB for the VM). It was **not quiesced**: other builds and test runs shared
  it, with host load averages between about 5 and 32 during the sessions (see
  `host_load` in each `runs.csv`).
- Image: built from `ce758f96` (origin/main on the day), revision in each
  `env.json`.
- 5 rounds per session, 20s warmup discarded, 60s window, sessions run one after
  another in the order 1x1 aa-v1, 1x1 aa-v2, 2x2 aa-v1, 2x2 aa-v2.
- The 2x2-batched shape was not run.

| shape | session | per-round A/A deltas | A/A floor (largest per-round \|delta\|) |
| --- | --- | --- | --- |
| 1x1 | aa-v1 | +0.7%, +104.6%, +10.3%, -3.1%, +3.8% | ±104.6% |
| 1x1 | aa-v2 | -0.4%, +2.6%, -2.9%, -1.2%, -2.6% | ±2.9% |
| 2x2 | aa-v1 | +4.3%, -5.0%, -1.5%, +7.4%, -7.0% | ±7.4% |
| 2x2 | aa-v2 | +1.1%, -1.9%, -4.4%, -2.7%, -1.8% | ±4.4% |

The 1x1 aa-v1 floor comes from one run, round 2 `v1-a`, which delivered about
half the rate of every other v1 run in that session. The next run started at a
host load average of 32, so the machine was busy with other work during that
window. It is kept, because removing an outlier after the fact is how the
earlier numbers went wrong. Without it, the largest per-round delta in that
session would be 10.3%, which is still wider than the other three sessions.

What this says: on a shared laptop, one A/A session can range from about ±3% to
more than ±100% depending on what else is running. That is the ADR's reason for
requiring quiesced hardware, and no gate decision can rest on runs like these.

**Do not compare rates across these sessions.** Each session measured one
engine against itself. The sessions were not interleaved and ran at different
times on a machine in a different state, so a v1 rate from one session next to a
v2 rate from another is not a measurement of anything. A v1-vs-v2 number comes
only from an `ab` session, read against that session's own A/A floor.
