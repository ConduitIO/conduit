# archv2-gate: shape 4x4-destbatch, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 14660 | 8.0 | 12776 | 16341 | ±21.8% | ±4.8% |
| v2 | 10 | 89 | 1.7 | 85 | 91 | ±4.8% | ±0.7% |
| proto | 10 | 45448 | 6.8 | 40857 | 50374 | ±8.2% | ±1.7% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -99.4% | -99.4%, -99.4%, -99.4%, -99.4%, -99.4% | ±21.8% | resolved |
| proto vs v1 | +210.0% | +212.8%, +207.8%, +210.1%, +204.2%, +195.0% | ±21.8% | resolved |
| proto vs v2 | +50736.5% | +51095.3%, +49947.5%, +55265.1%, +51121.8%, +47133.4% | ±8.2% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
