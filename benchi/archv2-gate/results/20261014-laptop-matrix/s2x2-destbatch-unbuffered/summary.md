# archv2-gate: shape 2x2-destbatch, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 27491 | 13.0 | 20915 | 31135 | ±27.9% | ±5.5% |
| v2 | 10 | 90 | 0.7 | 89 | 91 | ±1.1% | ±0.2% |
| proto | 10 | 59780 | 7.4 | 52662 | 64041 | ±14.9% | ±6.5% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -99.7% | -99.7%, -99.7%, -99.6%, -99.6%, -99.6% | ±27.9% | resolved |
| proto vs v1 | +117.5% | +110.7%, +98.1%, +134.6%, +133.1%, +121.1% | ±27.9% | resolved |
| proto vs v2 | +66248.7% | +68966.6%, +67499.9%, +66115.4%, +63827.3%, +58982.8% | ±14.9% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
