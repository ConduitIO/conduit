# archv2-gate: shape 4x4, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 25638 | 24.7 | 11595 | 28848 | ±36.2% | ±19.4% |
| v2 | 10 | 36758 | 40.9 | 13934 | 41730 | ±66.6% | ±20.6% |
| proto | 10 | 53132 | 29.9 | 22057 | 56933 | ±29.8% | ±6.6% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +43.4% | +40.8%, +21.7%, +54.7%, +86.8%, -35.7% | ±66.6% | not resolved |
| proto vs v1 | +107.2% | +103.1%, +79.8%, +86.1%, +279.3%, +1.5% | ±36.2% | resolved |
| proto vs v2 | +44.5% | +44.3%, +47.7%, +20.3%, +103.1%, +57.9% | ±66.6% | suggestive |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
