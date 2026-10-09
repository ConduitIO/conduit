# staged-engine local session: shape 2x2

3 rounds, 6s warmup discarded, 20s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 57840 | 2.0 | 55211 | 58691 | 3.35 | +/-4.1% | 1.2% | +4.1%, -1.1%, -1.2% |
| v2-main | 6 | 42176 | 0.9 | 41701 | 42803 | 2.12 | +/-1.6% | 1.6% | +1.6%, +1.0%, -1.6% |
| v2-proto | 6 | 71782 | 1.2 | 71391 | 73673 | 3.42 | +/-2.9% | 1.3% | -1.3%, +2.9%, -0.1% |

## Comparisons

- **v2-main vs v1**: median-to-median -27.1%; per-round -25.4%, -28.0%, -26.7%
- **v2-proto vs v1**: median-to-median +24.1%; per-round +28.6%, +24.4%, +23.2%
- **v2-proto vs v2-main**: median-to-median +70.2%; per-round +72.4%, +72.8%, +68.2%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
