# archv2-gate: shape 2x2, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 35604 | 46.4 | 8878 | 38123 | ±30.3% | ±7.5% |
| v2 | 10 | 31417 | 32.5 | 9229 | 36688 | ±262.0% | ±30.5% |
| proto | 10 | 51575 | 28.5 | 18607 | 55851 | ±185.9% | ±3.6% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -11.8% | +37.7%, +130.7%, -9.2%, -7.9%, -18.6% | ±262.0% | not resolved |
| proto vs v1 | +44.9% | +93.5%, +288.6%, +48.1%, +36.0%, +45.2% | ±185.9% | suggestive |
| proto vs v2 | +64.2% | +40.5%, +68.4%, +63.1%, +47.7%, +78.3% | ±262.0% | suggestive |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
