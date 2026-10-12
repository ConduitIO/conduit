# staged-engine session: shape 2x2

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 25402 | 1.3 | 24554 | 25822 | 2.38 | +/-5.0% | 1.0% | +5.0%, -0.3%, -0.4%, +1.0%, +1.5% |
| v2-main | 10 | 24094 | 1.2 | 23270 | 24240 | 2.04 | +/-4.1% | 0.5% | +4.1%, +2.1%, -0.0%, -0.5%, -0.3% |
| v2-proto | 10 | 46782 | 1.6 | 45306 | 47594 | 3.56 | +/-3.5% | 1.5% | -1.5%, +3.5%, +1.5%, +0.9%, +0.9% |

## Comparisons

- **v2-main vs v1**: median-to-median -5.1%; per-round -5.7%, -5.7%, -5.4%, -5.4%, -5.7%
- **v2-proto vs v1**: median-to-median +84.2%; per-round +81.2%, +82.2%, +82.9%, +84.5%, +85.8%
- **v2-proto vs v2-main**: median-to-median +94.2%; per-round +92.1%, +93.4%, +93.3%, +95.0%, +96.9%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
