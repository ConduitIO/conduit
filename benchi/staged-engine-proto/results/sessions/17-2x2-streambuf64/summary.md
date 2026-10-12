# staged-engine local session: shape 2x2

3 rounds, 6s warmup discarded, 20s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

Env: `CONDUIT_BENCH_STREAM_BUFFER=64`

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 6 | 61042 | 3.1 | 60422 | 65237 | 3.34 | +/-6.1% | 5.9% | -5.9%, +6.1%, +0.3% |
| v2-main | 6 | 37577 | 0.4 | 37426 | 37838 | 2.04 | +/-0.5% | 0.5% | +0.5%, -0.5%, +0.1% |
| v2-proto | 6 | 133510 | 3.9 | 130366 | 143474 | 4.98 | +/-3.0% | 0.9% | -0.5%, +3.0%, +0.9% |

## Comparisons

- **v2-main vs v1**: median-to-median -38.4%; per-round -39.6%, -40.6%, -38.1%
- **v2-proto vs v1**: median-to-median +118.7%; per-round +109.0%, +110.8%, +136.1%
- **v2-proto vs v2-main**: median-to-median +255.3%; per-round +246.3%, +255.1%, +281.5%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
