# staged-engine session: shape 1x1-srcbatch100

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 51428 | 1.0 | 50572 | 52232 | 2.05 | +/-2.8% | 1.4% | -1.4%, +0.1%, -0.6%, -2.4%, -2.8% |
| v2-main | 10 | 80338 | 0.4 | 79562 | 80561 | 1.86 | +/-0.8% | 0.4% | +0.4%, +0.8%, +0.1%, -0.3%, -0.7% |
| v2-proto | 10 | 79710 | 0.6 | 78952 | 80398 | 1.88 | +/-1.8% | 0.6% | -0.3%, +0.4%, +1.8%, +0.6%, -1.2% |

## Comparisons

- **v2-main vs v1**: median-to-median +56.2%; per-round +55.4%, +55.9%, +55.5%, +55.9%, +56.3%
- **v2-proto vs v1**: median-to-median +55.0%; per-round +54.0%, +55.9%, +54.0%, +54.9%, +55.5%
- **v2-proto vs v2-main**: median-to-median -0.8%; per-round -0.9%, +0.0%, -1.0%, -0.7%, -0.5%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
