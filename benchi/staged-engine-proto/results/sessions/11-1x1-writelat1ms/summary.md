# staged-engine local session: shape 1x1

3 rounds, 6s warmup discarded, 15s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_WRITE_LATENCY=1ms`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 563 | 2.3 | 542 | 586 | 0.21 | +/-3.3% | 2.9% | +2.9%, +0.0%, +3.3% |
| v2-main | 6 | 580 | 0.4 | 575 | 583 | 0.20 | +/-0.9% | 0.7% | -0.7%, +0.2%, -0.9% |
| v2-proto | 6 | 586 | 1.4 | 573 | 600 | 0.21 | +/-4.6% | 1.5% | +1.0%, -4.6%, +1.5% |

## Comparisons

- **v2-main vs v1**: median-to-median +3.0%; per-round +5.6%, +3.1%, +0.2%
- **v2-proto vs v1**: median-to-median +4.1%; per-round +6.5%, +4.2%, +1.6%
- **v2-proto vs v2-main**: median-to-median +1.0%; per-round +0.9%, +1.0%, +1.4%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
