# staged-engine local session: shape 1x1

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 65774 | 4.1 | 58399 | 68229 | 1.91 | +/-10.8% | 3.5% | -5.8%, +10.8%, -3.5%, -0.8%, -1.4% |
| v2-main | 10 | 51680 | 4.4 | 46343 | 53874 | 1.34 | +/-10.7% | 1.0% | -4.7%, +10.7%, +1.0%, -0.3%, -0.6% |
| v2-proto | 10 | 71508 | 6.3 | 59186 | 72566 | 1.92 | +/-6.8% | 0.3% | +0.3%, +6.8%, +1.8%, -0.2%, -0.2% |

## Comparisons

- **v2-main vs v1**: median-to-median -21.4%; per-round -21.4%, -20.7%, -23.1%, -19.5%, -18.8%
- **v2-proto vs v1**: median-to-median +8.7%; per-round +8.1%, -0.8%, +6.7%, +8.8%, +9.6%
- **v2-proto vs v2-main**: median-to-median +38.4%; per-round +37.6%, +25.1%, +38.8%, +35.2%, +35.0%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
