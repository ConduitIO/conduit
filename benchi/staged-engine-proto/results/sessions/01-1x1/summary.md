# staged-engine local session: shape 1x1

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 64670 | 13.6 | 44086 | 66850 | 1.89 | +/-38.6% | 2.8% | -35.9%, +38.6%, -1.5%, +0.5%, +2.8% |
| v2-main | 10 | 53708 | 11.6 | 33732 | 54413 | 1.31 | +/-46.9% | 0.2% | +0.5%, +46.9%, -0.2%, -0.0%, -0.0% |
| v2-proto | 10 | 70596 | 10.7 | 46457 | 71497 | 1.92 | +/-42.5% | 1.1% | -1.1%, +42.5%, -0.1%, +7.2%, +1.1% |

## Comparisons

- **v2-main vs v1**: median-to-median -17.0%; per-round -2.3%, -19.3%, -18.9%, -16.3%, -17.6%
- **v2-proto vs v1**: median-to-median +9.2%; per-round +28.3%, +8.0%, +6.7%, +5.4%, +9.5%
- **v2-proto vs v2-main**: median-to-median +31.4%; per-round +31.3%, +33.8%, +31.6%, +26.0%, +32.9%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
