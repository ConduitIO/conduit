# staged-engine local session: shape 1x1-destbatch

3 rounds, 6s warmup discarded, 15s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 57767 | 0.5 | 57544 | 58457 | 1.64 | +/-1.6% | 0.3% | +1.6%, +0.2%, +0.3% |
| v2-main | 6 | 86 | 0.9 | 85 | 87 | 0.06 | +/-1.2% | 1.2% | +0.0%, -1.2%, -1.2% |
| v2-proto | 6 | 67594 | 0.4 | 67002 | 67766 | 1.56 | +/-0.8% | 0.3% | +0.3%, -0.8%, -0.1% |

## Comparisons

- **v2-main vs v1**: median-to-median -99.9%; per-round -99.9%, -99.9%, -99.9%
- **v2-proto vs v1**: median-to-median +17.0%; per-round +16.7%, +16.5%, +17.0%
- **v2-proto vs v2-main**: median-to-median +78956.7%; per-round +79501.2%, +77686.7%, +79039.2%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
