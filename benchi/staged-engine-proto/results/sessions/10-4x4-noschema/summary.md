# staged-engine local session: shape 4x4-noschema

3 rounds, 6s warmup discarded, 15s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 35378 | 3.7 | 33548 | 37373 | 2.62 | +/-3.4% | 0.8% | +0.8%, -0.7%, +3.4% |
| v2-main | 6 | 56612 | 9.7 | 46675 | 59328 | 3.37 | +/-6.3% | 2.6% | +2.6%, -6.3%, +1.3% |
| v2-proto | 6 | 63130 | 4.2 | 58192 | 66416 | 3.40 | +/-6.7% | 6.3% | +1.7%, -6.7%, -6.3% |

## Comparisons

- **v2-main vs v1**: median-to-median +60.0%; per-round +56.6%, +62.6%, +37.7%
- **v2-proto vs v1**: median-to-median +78.4%; per-round +73.8%, +81.7%, +76.1%
- **v2-proto vs v2-main**: median-to-median +11.5%; per-round +11.0%, +11.8%, +27.9%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
