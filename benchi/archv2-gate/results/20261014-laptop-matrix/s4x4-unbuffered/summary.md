# archv2-gate: shape 4x4, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 21265 | 21.6 | 14627 | 28857 | ±38.0% | ±17.3% |
| v2 | 10 | 39256 | 26.9 | 19730 | 43102 | ±43.0% | ±7.3% |
| proto | 10 | 44995 | 21.9 | 26013 | 50919 | ±23.7% | ±17.6% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +84.6% | +35.6%, +85.5%, +44.4%, +110.7%, +42.3% | ±43.0% | resolved |
| proto vs v1 | +111.6% | +81.7%, +105.5%, +82.8%, +153.4%, +73.7% | ±38.0% | resolved |
| proto vs v2 | +14.6% | +34.0%, +10.7%, +26.5%, +20.3%, +22.0% | ±43.0% | not resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
