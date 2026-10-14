# archv2-gate: shape 1x1, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 53126 | 23.8 | 31610 | 63792 | ±29.1% | ±11.4% |
| v2 | 10 | 42360 | 22.7 | 25188 | 48302 | ±18.3% | ±7.6% |
| proto | 10 | 52713 | 21.0 | 37527 | 66039 | ±26.6% | ±13.9% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -20.3% | -28.5%, -13.1%, -15.2%, -22.9%, -17.5% | ±29.1% | not resolved |
| proto vs v1 | -0.8% | +1.7%, +5.9%, +14.3%, -2.4%, +28.0% | ±29.1% | not resolved |
| proto vs v2 | +24.4% | +42.2%, +21.9%, +34.9%, +26.7%, +55.1% | ±26.6% | not resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
