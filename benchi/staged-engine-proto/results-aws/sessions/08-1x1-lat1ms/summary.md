# staged-engine session: shape 1x1

5 rounds, 10s warmup discarded, 40s window, records counted at the sink,
every arm twice per round (palindrome).

**NOT GATE EVIDENCE.** early, non-gating read on a virtualized instance; not published

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1ms`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 49084 | 0.6 | 48524 | 49407 | 2.62 | +/-1.5% | 1.1% | +1.5%, -0.3%, +1.1%, -0.0%, +1.5% |
| v2-main | 10 | 838 | 0.2 | 835 | 840 | 0.11 | +/-0.5% | 0.2% | -0.4%, -0.2%, -0.5%, +0.2%, +0.1% |
| v2-proto | 10 | 37416 | 0.5 | 37185 | 37836 | 1.79 | +/-1.3% | 0.4% | +0.0%, +0.4%, -1.3%, +1.1%, -0.3% |

## Comparisons

- **v2-main vs v1**: median-to-median -98.3%; per-round -98.3%, -98.3%, -98.3%, -98.3%, -98.3%
- **v2-proto vs v1**: median-to-median -23.8%; per-round -23.7%, -23.8%, -23.5%, -23.3%, -23.8%
- **v2-proto vs v2-main**: median-to-median +4367.6%; per-round +4362.6%, +4366.9%, +4375.4%, +4401.1%, +4341.2%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
