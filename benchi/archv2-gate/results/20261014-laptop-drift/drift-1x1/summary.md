# archv2-gate: shape 1x1, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1-base | 10 | 65074 | 19.7 | 27382 | 66121 | ±136.7% | ±1.9% |
| v1-main | 10 | 66975 | 20.3 | 26784 | 68973 | ±154.8% | ±3.1% |
| v2-base | 10 | 52732 | 20.6 | 20559 | 53474 | ±155.6% | ±1.1% |
| v2-main | 10 | 92682 | 21.2 | 34526 | 94083 | ±171.2% | ±0.9% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v1-main vs v1-base | +2.9% | +3.1%, +1.7%, +1.7%, +11.8%, +2.3% | ±154.8% | not resolved |
| v2-base vs v1-base | -19.0% | -20.7%, -20.0%, -18.8%, -15.1%, -18.8% | ±155.6% | suggestive |
| v2-main vs v1-base | +42.4% | +39.0%, +39.9%, +41.5%, +54.0%, +43.0% | ±171.2% | suggestive |
| v2-base vs v1-main | -21.3% | -23.1%, -21.3%, -20.1%, -24.0%, -20.6% | ±155.6% | suggestive |
| v2-main vs v1-main | +38.4% | +34.9%, +37.6%, +39.2%, +37.8%, +39.8% | ±171.2% | suggestive |
| v2-main vs v2-base | +75.8% | +75.3%, +74.9%, +74.2%, +81.3%, +76.0% | ±171.2% | suggestive |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
