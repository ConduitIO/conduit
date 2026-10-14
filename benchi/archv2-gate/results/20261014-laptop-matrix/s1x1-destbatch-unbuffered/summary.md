# archv2-gate: shape 1x1-destbatch, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 46269 | 29.6 | 28724 | 60419 | ±41.1% | ±4.2% |
| v2 | 10 | 90 | 2.1 | 85 | 92 | ±3.5% | ±1.2% |
| proto | 10 | 63348 | 17.1 | 33994 | 71103 | ±46.6% | ±7.7% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -99.8% | -99.8%, -99.7%, -99.8%, -99.9%, -99.8% | ±41.1% | resolved |
| proto vs v1 | +36.9% | +58.9%, +110.4%, +14.8%, +18.3%, +8.1% | ±46.6% | suggestive |
| proto vs v2 | +70404.1% | +64266.6%, +66527.1%, +74100.1%, +79212.0%, +54132.2% | ±46.6% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
