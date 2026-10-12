# staged-engine local session: shape 1x1

3 rounds, 6s warmup discarded, 20s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_STREAM_BUFFER=64`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 99939 | 0.4 | 99238 | 100434 | 2.50 | +/-0.8% | 0.6% | -0.6%, -0.8%, +0.1% |
| v2-main | 6 | 98715 | 0.5 | 97608 | 99158 | 2.09 | +/-1.2% | 1.1% | +0.1%, +1.2%, +1.1% |
| v2-proto | 6 | 97478 | 0.4 | 96969 | 98109 | 2.12 | +/-0.9% | 0.8% | -0.8%, -0.3%, +0.9% |

## Comparisons

- **v2-main vs v1**: median-to-median -1.2%; per-round -1.4%, -1.5%, -1.3%
- **v2-proto vs v1**: median-to-median -2.5%; per-round -2.8%, -2.2%, -2.3%
- **v2-proto vs v2-main**: median-to-median -1.3%; per-round -1.4%, -0.7%, -0.9%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
