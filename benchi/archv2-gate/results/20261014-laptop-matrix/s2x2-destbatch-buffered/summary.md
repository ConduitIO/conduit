# archv2-gate: shape 2x2-destbatch, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 21400 | 15.5 | 19169 | 31046 | ±23.8% | ±4.8% |
| v2 | 10 | 85 | 1.0 | 84 | 87 | ±1.6% | ±0.6% |
| proto | 10 | 44029 | 8.1 | 41390 | 53793 | ±23.1% | ±2.5% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -99.6% | -99.6%, -99.7%, -99.6%, -99.6%, -99.6% | ±23.8% | resolved |
| proto vs v1 | +105.7% | +97.0%, +70.6%, +120.8%, +118.5%, +122.2% | ±23.8% | resolved |
| proto vs v2 | +51546.6% | +49015.9%, +55145.4%, +56843.5%, +52578.2%, +50198.1% | ±23.1% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
