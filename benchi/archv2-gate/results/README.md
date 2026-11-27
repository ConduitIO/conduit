# archv2-gate results

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
