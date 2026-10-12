# staged-engine local session: shape 2x2

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 40274 | 0.8 | 39683 | 40679 | 2.17 | +/-1.2% | 0.9% | -0.3%, -0.7%, -1.2%, -0.9%, +1.2% |
| v2-main | 10 | 41023 | 2.6 | 38085 | 41933 | 2.10 | +/-7.5% | 3.4% | +1.7%, -1.3%, -3.4%, -4.0%, -7.5% |
| v2-proto | 10 | 57249 | 0.9 | 56558 | 58380 | 2.76 | +/-2.6% | 1.1% | -2.0%, +2.6%, +0.6%, +0.2%, -1.1% |

## Comparisons

- **v2-main vs v1**: median-to-median +1.9%; per-round +1.5%, +1.7%, +3.1%, +3.0%, -1.7%
- **v2-proto vs v1**: median-to-median +42.1%; per-round +43.1%, +41.3%, +43.6%, +43.6%, +41.6%
- **v2-proto vs v2-main**: median-to-median +39.6%; per-round +41.0%, +38.9%, +39.2%, +39.3%, +44.0%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
