# archv2-gate: shape 1x1-destbatch, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 24454 | 1.6 | 23934 | 25120 | ±2.4% | ±1.7% |
| v2 | 10 | 85 | 0.5 | 85 | 86 | ±0.5% | ±0.4% |
| proto | 10 | 27702 | 1.8 | 26699 | 28413 | ±4.8% | ±1.7% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -99.7% | -99.7%, -99.7%, -99.6%, -99.7%, -99.7% | ±2.4% | resolved |
| proto vs v1 | +13.3% | +14.2%, +11.4%, +13.4%, +14.3%, +12.6% | ±4.8% | resolved |
| proto vs v2 | +32414.0% | +32720.8%, +32357.6%, +32174.8%, +32849.1%, +32120.5% | ±4.8% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
