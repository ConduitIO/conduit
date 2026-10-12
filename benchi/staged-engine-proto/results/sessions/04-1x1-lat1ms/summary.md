# staged-engine local session: shape 1x1

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1ms`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 66442 | 0.5 | 65809 | 67054 | 2.14 | +/-1.9% | 0.7% | -0.7%, -1.9%, +0.2%, -1.0%, -0.7% |
| v2-main | 10 | 592 | 12.1 | 578 | 838 | 0.15 | +/-34.4% | 1.5% | -1.5%, +34.4%, +0.3%, -0.2%, +2.4% |
| v2-proto | 10 | 56564 | 2.7 | 51626 | 57131 | 1.63 | +/-10.1% | 0.5% | +0.5%, -0.5%, +0.5%, +10.1%, +0.6% |

## Comparisons

- **v2-main vs v1**: median-to-median -99.1%; per-round -99.1%, -98.9%, -99.1%, -99.1%, -99.1%
- **v2-proto vs v1**: median-to-median -14.9%; per-round -14.9%, -14.9%, -14.6%, -18.1%, -15.2%
- **v2-proto vs v2-main**: median-to-median +9454.7%; per-round +9517.6%, +7809.8%, +9383.3%, +9108.9%, +9537.1%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
