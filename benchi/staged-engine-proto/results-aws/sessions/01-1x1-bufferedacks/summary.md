# staged-engine session: shape 1x1

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 48558 | 0.6 | 48274 | 49012 | 2.75 | +/-1.3% | 0.4% | +0.1%, -0.4%, +1.3%, +0.3%, -1.3% |
| v2-main | 10 | 32533 | 0.9 | 32169 | 33138 | 1.34 | +/-2.1% | 1.2% | +1.2%, -2.1%, +1.2%, +0.8%, -1.0% |
| v2-proto | 10 | 38608 | 0.4 | 38240 | 38760 | 1.93 | +/-0.7% | 0.3% | +0.3%, -0.3%, -0.2%, -0.4%, -0.7% |
| v2-altA | 10 | 76558 | 0.7 | 75538 | 77338 | 1.87 | +/-2.0% | 0.3% | -0.8%, -2.0%, -0.3%, -0.3%, +0.1% |

## Comparisons

- **v2-main vs v1**: median-to-median -33.0%; per-round -33.0%, -32.3%, -33.3%, -33.1%, -33.6%
- **v2-proto vs v1**: median-to-median -20.5%; per-round -20.9%, -20.1%, -20.6%, -21.0%, -20.8%
- **v2-proto vs v2-main**: median-to-median +18.7%; per-round +18.0%, +17.9%, +19.1%, +18.0%, +19.2%
- **v2-altA vs v1**: median-to-median +57.7%; per-round +57.5%, +57.6%, +56.9%, +58.5%, +56.2%
- **v2-altA vs v2-main**: median-to-median +135.3%; per-round +134.9%, +132.7%, +135.4%, +136.9%, +135.3%
- **v2-proto vs v2-altA**: median-to-median -49.6%; per-round -49.8%, -49.3%, -49.4%, -50.2%, -49.3%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
