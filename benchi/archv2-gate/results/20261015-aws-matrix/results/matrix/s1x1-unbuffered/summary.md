# archv2-gate: shape 1x1, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 44329 | 0.8 | 43587 | 44854 | ±2.3% | ±0.7% |
| v2 | 10 | 64292 | 0.3 | 63951 | 64689 | ±0.8% | ±0.2% |
| proto | 10 | 51013 | 0.5 | 50609 | 51273 | ±0.6% | ±0.5% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +45.0% | +44.1%, +45.4%, +46.1%, +44.5%, +45.0% | ±2.3% | resolved |
| proto vs v1 | +15.1% | +14.2%, +15.8%, +15.4%, +15.2%, +14.5% | ±2.3% | resolved |
| proto vs v2 | -20.7% | -20.8%, -20.3%, -21.0%, -20.3%, -21.1% | ±0.8% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
