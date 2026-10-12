# staged-engine session: shape 1x1

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

Env: `CONDUIT_BENCH_STREAM_BUFFER=64`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 73801 | 0.2 | 73444 | 74004 | 3.28 | +/-0.5% | 0.3% | -0.5%, +0.3%, +0.0%, +0.2%, -0.5% |
| v2-main | 10 | 77770 | 0.3 | 77553 | 78306 | 2.40 | +/-1.0% | 0.5% | -1.0%, -0.5%, +0.5%, +0.3%, -0.2% |
| v2-proto | 10 | 72928 | 0.5 | 72189 | 73281 | 2.62 | +/-0.6% | 0.4% | +0.2%, +0.4%, +0.4%, +0.0%, +0.6% |

## Comparisons

- **v2-main vs v1**: median-to-median +5.4%; per-round +5.7%, +5.3%, +5.9%, +5.5%, +5.4%
- **v2-proto vs v1**: median-to-median -1.2%; per-round -1.0%, -1.0%, -1.7%, -1.1%, -1.5%
- **v2-proto vs v2-main**: median-to-median -6.2%; per-round -6.4%, -6.0%, -7.2%, -6.2%, -6.6%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
