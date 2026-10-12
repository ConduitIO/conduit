# staged-engine local session: shape 1x1-srcbatch100

3 rounds, 6s warmup discarded, 15s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1ms`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 79247 | 5.1 | 73292 | 83883 | 2.46 | +/-5.5% | 2.0% | +5.5%, +1.3%, +2.0% |
| v2-main | 6 | 66036 | 4.9 | 63672 | 73609 | 1.12 | +/-10.8% | 4.4% | +3.6%, +10.8%, -4.4% |
| v2-proto | 6 | 98208 | 0.8 | 97153 | 99544 | 1.69 | +/-1.5% | 1.2% | -1.5%, +1.0%, -1.2% |

## Comparisons

- **v2-main vs v1**: median-to-median -16.7%; per-round -18.2%, -5.3%, -21.0%
- **v2-proto vs v1**: median-to-median +23.9%; per-round +24.7%, +33.1%, +17.7%
- **v2-proto vs v2-main**: median-to-median +48.7%; per-round +52.4%, +40.6%, +49.0%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
