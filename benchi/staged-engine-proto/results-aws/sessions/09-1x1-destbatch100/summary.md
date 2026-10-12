# staged-engine session: shape 1x1-destbatch

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 37994 | 0.6 | 37407 | 38337 | 1.71 | +/-1.6% | 0.7% | -0.3%, +1.1%, -0.7%, -0.5%, +1.6% |
| v2-main | 10 | 95 | 0.4 | 94 | 95 | 0.03 | +/-1.1% | 0.0% | +0.0%, -1.1%, +0.0%, -1.1%, +0.0% |
| v2-proto | 10 | 47757 | 0.3 | 47664 | 48069 | 1.64 | +/-0.7% | 0.3% | +0.7%, -0.3%, +0.3%, -0.4%, +0.0% |

## Comparisons

- **v2-main vs v1**: median-to-median -99.7%; per-round -99.8%, -99.8%, -99.8%, -99.8%, -99.7%
- **v2-proto vs v1**: median-to-median +25.7%; per-round +26.0%, +25.6%, +25.0%, +26.3%, +26.6%
- **v2-proto vs v2-main**: median-to-median +50170.5%; per-round +50314.2%, +50422.2%, +50146.8%, +50542.3%, +50158.4%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
