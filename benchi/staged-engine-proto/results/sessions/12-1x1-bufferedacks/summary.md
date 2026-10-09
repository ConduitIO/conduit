# staged-engine local session: shape 1x1

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 70118 | 0.5 | 69619 | 70728 | 2.12 | +/-0.7% | 0.6% | +0.7%, -0.6%, -0.7%, +0.4%, -0.4% |
| v2-main | 10 | 52440 | 0.3 | 52214 | 52692 | 1.31 | +/-0.6% | 0.3% | -0.6%, -0.5%, +0.0%, -0.1%, +0.3% |
| v2-proto | 10 | 57576 | 0.4 | 57242 | 58044 | 1.57 | +/-0.7% | 0.3% | +0.7%, +0.3%, +0.2%, -0.5%, +0.3% |

## Comparisons

- **v2-main vs v1**: median-to-median -25.2%; per-round -25.5%, -24.8%, -25.8%, -25.1%, -24.8%
- **v2-proto vs v1**: median-to-median -17.9%; per-round -17.8%, -17.6%, -18.3%, -17.8%, -17.8%
- **v2-proto vs v2-main**: median-to-median +9.8%; per-round +10.4%, +9.6%, +10.1%, +9.7%, +9.2%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
