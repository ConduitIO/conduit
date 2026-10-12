# staged-engine local session: shape 4x4

5 rounds, 8s warmup discarded, 25s window, records counted at the sink,
every arm twice per round (palindrome).

**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).

| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 28418 | 2.1 | 27257 | 28897 | 3.00 | +/-5.7% | 3.1% | +3.1%, +4.1%, +5.7%, +0.1%, -0.0% |
| v2-main | 10 | 42934 | 1.4 | 41120 | 43205 | 3.69 | +/-4.6% | 0.5% | +0.3%, +0.1%, +0.9%, +0.5%, +4.6% |
| v2-proto | 10 | 49101 | 1.0 | 48394 | 49922 | 3.95 | +/-1.6% | 1.4% | +1.4%, -1.2%, -1.6%, -0.5%, +1.4% |

## Comparisons

- **v2-main vs v1**: median-to-median +51.1%; per-round +54.0%, +53.6%, +53.3%, +47.2%, +48.1%
- **v2-proto vs v1**: median-to-median +72.8%; per-round +74.9%, +74.9%, +76.5%, +67.9%, +73.9%
- **v2-proto vs v2-main**: median-to-median +14.4%; per-round +13.6%, +13.8%, +15.2%, +14.1%, +17.4%

Read every delta against the arms' A/A floors above; a delta inside the floor is not
resolved. Rates from different sessions are not comparable.
