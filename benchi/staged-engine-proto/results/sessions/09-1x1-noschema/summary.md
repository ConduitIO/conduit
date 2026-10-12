# staged-engine local session: shape 1x1-noschema

3 rounds, 6s warmup discarded, 15s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 82376 | 1.1 | 81644 | 84336 | 1.36 | +/-2.0% | 1.0% | +1.0%, -0.7%, -2.0% |
| v2-main | 6 | 122238 | 0.2 | 121946 | 122558 | 1.37 | +/-0.3% | 0.2% | -0.1%, +0.3%, -0.2% |
| v2-proto | 6 | 122370 | 0.9 | 120061 | 122731 | 1.82 | +/-2.2% | 1.1% | +1.1%, +0.1%, +2.2% |

## Comparisons

- **v2-main vs v1**: median-to-median +48.4%; per-round +49.1%, +49.3%, +46.2%
- **v2-proto vs v1**: median-to-median +48.6%; per-round +48.0%, +49.7%, +45.4%
- **v2-proto vs v2-main**: median-to-median +0.1%; per-round -0.7%, +0.3%, -0.6%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
