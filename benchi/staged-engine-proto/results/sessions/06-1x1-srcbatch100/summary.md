# staged-engine local session: shape 1x1-srcbatch100

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 75928 | 0.7 | 74850 | 76527 | 1.85 | +/-1.7% | 0.8% | +1.4%, -0.7%, -0.7%, +0.8%, -1.7% |
| v2-main | 10 | 99388 | 1.0 | 96605 | 100085 | 1.62 | +/-2.5% | 0.7% | -0.0%, +0.9%, -0.7%, -0.3%, -2.5% |
| v2-proto | 10 | 99470 | 0.4 | 99071 | 100362 | 1.63 | +/-0.9% | 0.3% | -0.3%, +0.9%, +0.2%, +0.3%, -0.6% |

## Comparisons

- **v2-main vs v1**: median-to-median +30.9%; per-round +32.0%, +31.5%, +29.4%, +31.1%, +29.6%
- **v2-proto vs v1**: median-to-median +31.0%; per-round +31.2%, +32.1%, +31.1%, +30.1%, +32.2%
- **v2-proto vs v2-main**: median-to-median +0.1%; per-round -0.6%, +0.4%, +1.3%, -0.7%, +2.0%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
