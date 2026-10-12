# staged-engine session: shape 1x1

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 46722 | 0.9 | 45862 | 47178 | 2.33 | +/-1.0% | 0.3% | +0.3%, -0.1%, +1.0%, +0.1%, -0.3% |
| v2-main | 10 | 35157 | 0.5 | 35014 | 35634 | 1.34 | +/-0.5% | 0.2% | +0.2%, -0.0%, -0.3%, +0.5%, +0.2% |
| v2-proto | 10 | 48525 | 0.5 | 47802 | 48727 | 2.14 | +/-1.6% | 0.5% | -0.5%, +1.6%, +1.2%, +0.0%, -0.3% |
| v2-altA | 10 | 80453 | 0.6 | 79872 | 81443 | 1.87 | +/-1.6% | 0.4% | +0.4%, -0.8%, -0.2%, -1.6%, -0.1% |

## Comparisons

- **v2-main vs v1**: median-to-median -24.8%; per-round -25.4%, -24.7%, -25.0%, -24.0%, -23.4%
- **v2-proto vs v1**: median-to-median +3.9%; per-round +2.8%, +3.2%, +3.5%, +3.9%, +5.4%
- **v2-proto vs v2-main**: median-to-median +38.0%; per-round +37.8%, +37.1%, +38.1%, +36.7%, +37.6%
- **v2-altA vs v1**: median-to-median +72.2%; per-round +70.3%, +71.7%, +73.2%, +72.8%, +75.2%
- **v2-altA vs v2-main**: median-to-median +128.8%; per-round +128.2%, +128.2%, +131.1%, +127.4%, +128.6%
- **v2-proto vs v2-altA**: median-to-median -39.7%; per-round -39.6%, -39.9%, -40.3%, -39.9%, -39.8%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
