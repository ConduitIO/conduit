# archv2-gate: shape 4x4-destbatch, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 14015 | 4.0 | 12714 | 14480 | ±10.4% | ±6.0% |
| v2 | 10 | 85 | 0.6 | 84 | 86 | ±1.9% | ±0.8% |
| proto | 10 | 41490 | 5.6 | 39220 | 46201 | ±11.4% | ±4.4% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -99.4% | -99.4%, -99.4%, -99.4%, -99.4%, -99.4% | ±10.4% | resolved |
| proto vs v1 | +196.0% | +221.7%, +206.0%, +205.3%, +186.1%, +200.4% | ±11.4% | resolved |
| proto vs v2 | +48454.1% | +51338.2%, +50260.8%, +50473.0%, +46845.8%, +47117.5% | ±11.4% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
