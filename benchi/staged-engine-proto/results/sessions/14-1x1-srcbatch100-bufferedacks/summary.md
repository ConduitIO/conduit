# staged-engine local session: shape 1x1-srcbatch100

3 rounds, 6s warmup discarded, 15s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 81233 | 1.9 | 77115 | 81457 | 2.29 | +/-5.3% | 1.6% | -5.3%, +0.1%, -1.6% |
| v2-main | 6 | 99592 | 0.4 | 98854 | 100073 | 1.61 | +/-0.4% | 0.2% | +0.1%, -0.2%, -0.4% |
| v2-proto | 6 | 99570 | 0.4 | 99315 | 100207 | 1.63 | +/-0.8% | 0.6% | -0.2%, -0.8%, +0.6% |

## Comparisons

- **v2-main vs v1**: median-to-median +22.6%; per-round +25.7%, +21.8%, +23.6%
- **v2-proto vs v1**: median-to-median +22.6%; per-round +25.5%, +22.9%, +23.6%
- **v2-proto vs v2-main**: median-to-median -0.0%; per-round -0.2%, +0.8%, +0.0%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
