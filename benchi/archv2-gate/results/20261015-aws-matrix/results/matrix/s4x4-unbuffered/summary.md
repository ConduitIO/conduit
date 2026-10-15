# archv2-gate: shape 4x4, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 23143 | 1.4 | 22736 | 23772 | ±2.8% | ±1.9% |
| v2 | 10 | 36639 | 0.5 | 36456 | 37015 | ±1.5% | ±0.7% |
| proto | 10 | 47897 | 0.8 | 47206 | 48313 | ±1.8% | ±1.4% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +58.3% | +57.7%, +56.9%, +59.9%, +60.9%, +56.3% | ±2.8% | resolved |
| proto vs v1 | +107.0% | +106.1%, +104.0%, +107.4%, +108.6%, +104.6% | ±2.8% | resolved |
| proto vs v2 | +30.7% | +30.6%, +30.0%, +29.7%, +29.6%, +30.9% | ±1.8% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
