# staged-engine local session: shape 1x1

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=10ms`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 66065 | 0.4 | 65584 | 66339 | 2.15 | +/-0.5% | 0.2% | -0.0%, +0.5%, -0.2%, +0.1%, -0.3% |
| v2-main | 10 | 94 | 31.0 | 85 | 176 | 0.06 | +/-67.7% | 55.9% | -11.9%, +65.7%, +35.5%, -67.7%, +55.9% |
| v2-proto | 10 | 56360 | 0.5 | 55991 | 56863 | 1.66 | +/-1.5% | 1.1% | +1.1%, +1.1%, -0.4%, -0.5%, -1.5% |

## Comparisons

- **v2-main vs v1**: median-to-median -99.9%; per-round -99.9%, -99.8%, -99.8%, -99.8%, -99.8%
- **v2-proto vs v1**: median-to-median -14.7%; per-round -14.7%, -14.4%, -14.0%, -15.2%, -14.6%
- **v2-proto vs v2-main**: median-to-median +60178.6%; per-round +60907.0%, +42423.8%, +52678.5%, +42654.0%, +47719.5%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
