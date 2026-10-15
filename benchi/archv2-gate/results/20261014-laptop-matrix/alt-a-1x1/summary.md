# archv2-gate: shape 1x1, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 32422 | 42.7 | 13052 | 57816 | ±211.6% | ±58.1% |
| v2 | 10 | 28814 | 36.1 | 15082 | 48920 | ±128.9% | ±29.8% |
| proto | 10 | 46592 | 27.2 | 32163 | 67757 | ±52.4% | ±34.6% |
| v2-alta | 10 | 86664 | 19.6 | 52224 | 99570 | ±67.0% | ±21.4% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -11.1% | +17.6%, -29.1%, -45.5%, +32.4%, -23.5% | ±211.6% | not resolved |
| proto vs v1 | +43.7% | +70.4%, +5.9%, +34.5%, +55.9%, +78.6% | ±211.6% | not resolved |
| v2-alta vs v1 | +167.3% | +180.7%, +72.6%, +158.4%, +220.5%, +115.0% | ±211.6% | suggestive |
| proto vs v2 | +61.7% | +44.9%, +49.4%, +146.6%, +17.8%, +133.5% | ±128.9% | not resolved |
| v2-alta vs v2 | +200.8% | +138.7%, +143.5%, +373.7%, +142.1%, +181.0% | ±128.9% | resolved |
| v2-alta vs proto | +86.0% | +64.7%, +62.9%, +92.1%, +105.6%, +20.4% | ±67.0% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
