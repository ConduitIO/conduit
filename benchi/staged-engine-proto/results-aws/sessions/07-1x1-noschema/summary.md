# staged-engine session: shape 1x1-noschema

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 49008 | 0.7 | 48533 | 49506 | 1.45 | +/-1.9% | 0.8% | +1.2%, -1.9%, +0.4%, +0.6%, +0.8% |
| v2-main | 10 | 79851 | 0.5 | 78619 | 80069 | 1.41 | +/-1.2% | 0.3% | +0.0%, +0.8%, +0.1%, +1.2%, +0.3% |
| v2-proto | 10 | 74666 | 0.4 | 74408 | 75417 | 2.04 | +/-1.3% | 0.3% | -0.1%, +1.1%, +1.3%, +0.3%, -0.2% |

## Comparisons

- **v2-main vs v1**: median-to-median +62.9%; per-round +62.4%, +61.9%, +62.4%, +61.5%, +64.1%
- **v2-proto vs v1**: median-to-median +52.4%; per-round +51.8%, +52.8%, +52.3%, +52.2%, +53.4%
- **v2-proto vs v2-main**: median-to-median -6.5%; per-round -6.5%, -5.6%, -6.3%, -5.7%, -6.5%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
