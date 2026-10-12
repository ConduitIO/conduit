# staged-engine session: shape 2x2

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

Env: `CONDUIT_BENCH_STREAM_BUFFER=64`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 51414 | 2.0 | 49978 | 54282 | 4.77 | +/-5.6% | 1.5% | +2.9%, -0.3%, -1.5%, -1.4%, +5.6% |
| v2-main | 10 | 21756 | 0.3 | 21583 | 21850 | 1.85 | +/-1.2% | 0.3% | +0.4%, -0.1%, -0.3%, -1.2%, +0.0% |
| v2-proto | 10 | 103692 | 1.1 | 102538 | 106783 | 5.95 | +/-2.2% | 0.7% | +0.5%, -0.7%, +0.2%, +2.2%, -0.9% |

## Comparisons

- **v2-main vs v1**: median-to-median -57.7%; per-round -57.2%, -57.9%, -57.7%, -57.5%, -58.8%
- **v2-proto vs v1**: median-to-median +101.7%; per-round +102.8%, +100.5%, +101.6%, +106.9%, +95.7%
- **v2-proto vs v2-main**: median-to-median +376.6%; per-round +373.6%, +376.2%, +376.5%, +386.3%, +374.8%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
