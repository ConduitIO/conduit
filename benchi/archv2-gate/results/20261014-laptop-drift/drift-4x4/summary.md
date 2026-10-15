# archv2-gate: shape 4x4, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1-base | 10 | 30084 | 2.3 | 29651 | 31641 | ±5.7% | ±4.3% |
| v1-main | 10 | 33557 | 2.5 | 32634 | 34641 | ±5.5% | ±4.4% |
| v2-base | 10 | 44925 | 0.6 | 44388 | 45142 | ±1.6% | ±0.2% |
| v2-main | 10 | 52363 | 1.8 | 49418 | 52616 | ±6.4% | ±0.4% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v1-main vs v1-base | +11.5% | +10.5%, +12.2%, +11.8%, +10.6%, +8.6% | ±5.7% | resolved |
| v2-base vs v1-base | +49.3% | +47.7%, +48.7%, +47.8%, +48.1%, +46.2% | ±5.7% | resolved |
| v2-main vs v1-base | +74.1% | +72.9%, +74.4%, +69.1%, +71.7%, +70.1% | ±6.4% | resolved |
| v2-base vs v1-main | +33.9% | +33.7%, +32.5%, +32.2%, +33.9%, +34.6% | ±5.5% | resolved |
| v2-main vs v1-main | +56.0% | +56.5%, +55.4%, +51.3%, +55.2%, +56.6% | ±6.4% | resolved |
| v2-main vs v2-base | +16.6% | +17.1%, +17.3%, +14.4%, +15.9%, +16.4% | ±6.4% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
