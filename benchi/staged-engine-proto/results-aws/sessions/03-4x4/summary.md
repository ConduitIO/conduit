# staged-engine session: shape 4x4

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 22114 | 1.3 | 21543 | 22485 | 3.62 | +/-2.8% | 2.4% | -0.8%, +0.3%, +2.4%, +2.8%, +2.7% |
| v2-main | 10 | 31638 | 0.5 | 31414 | 31896 | 4.17 | +/-1.1% | 1.0% | -1.1%, +0.9%, +0.5%, +1.0%, -1.1% |
| v2-proto | 10 | 44600 | 1.1 | 43641 | 45219 | 5.46 | +/-2.8% | 2.1% | -2.8%, +2.1%, +2.0%, -0.6%, -2.1% |

## Comparisons

- **v2-main vs v1**: median-to-median +43.1%; per-round +41.2%, +43.1%, +43.7%, +44.8%, +42.9%
- **v2-proto vs v1**: median-to-median +101.7%; per-round +99.5%, +101.8%, +101.2%, +104.9%, +98.8%
- **v2-proto vs v2-main**: median-to-median +41.0%; per-round +41.2%, +41.0%, +40.0%, +41.5%, +39.0%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
