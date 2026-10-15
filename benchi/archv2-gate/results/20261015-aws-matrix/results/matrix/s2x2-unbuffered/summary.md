# archv2-gate: shape 2x2, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 27440 | 0.5 | 27257 | 27730 | ±1.2% | ±0.7% |
| v2 | 10 | 28052 | 0.3 | 27951 | 28158 | ±0.2% | ±0.0% |
| proto | 10 | 50304 | 0.7 | 49817 | 51056 | ±1.7% | ±0.2% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +2.2% | +2.8%, +2.3%, +1.8%, +1.3%, +2.6% | ±1.2% | resolved |
| proto vs v1 | +83.3% | +83.7%, +82.3%, +84.0%, +82.0%, +84.5% | ±1.7% | resolved |
| proto vs v2 | +79.3% | +78.7%, +78.2%, +80.8%, +79.5%, +79.9% | ±1.7% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
