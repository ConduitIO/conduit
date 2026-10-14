# archv2-gate: shape 1x1, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 57896 | 11.5 | 42420 | 63702 | ±27.3% | ±5.1% |
| v2 | 10 | 37693 | 22.7 | 22745 | 47685 | ±42.5% | ±18.7% |
| proto | 10 | 52955 | 19.7 | 27277 | 54951 | ±21.7% | ±4.6% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -34.9% | -44.2%, -25.8%, -36.6%, -26.6%, -35.9% | ±42.5% | not resolved |
| proto vs v1 | -8.5% | -41.5%, -14.7%, +11.3%, -8.3%, -14.1% | ±27.3% | not resolved |
| proto vs v2 | +40.5% | +4.8%, +14.9%, +75.7%, +24.9%, +34.0% | ±42.5% | suggestive |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
