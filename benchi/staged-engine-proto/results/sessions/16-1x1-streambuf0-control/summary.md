# staged-engine local session: shape 1x1

3 rounds, 6s warmup discarded, 20s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_STREAM_BUFFER=0`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 65893 | 0.5 | 65689 | 66613 | 1.88 | +/-1.0% | 0.4% | -1.0%, +0.4%, -0.1% |
| v2-main | 6 | 53554 | 0.7 | 53133 | 54042 | 1.31 | +/-1.2% | 0.0% | -0.0%, -0.0%, +1.2% |
| v2-proto | 6 | 72746 | 0.4 | 72204 | 73160 | 1.92 | +/-0.6% | 0.5% | -0.3%, -0.5%, +0.6% |

## Comparisons

- **v2-main vs v1**: median-to-median -18.7%; per-round -19.5%, -17.9%, -18.8%
- **v2-proto vs v1**: median-to-median +10.4%; per-round +10.0%, +10.0%, +10.8%
- **v2-proto vs v2-main**: median-to-median +35.8%; per-round +36.7%, +33.9%, +36.4%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
