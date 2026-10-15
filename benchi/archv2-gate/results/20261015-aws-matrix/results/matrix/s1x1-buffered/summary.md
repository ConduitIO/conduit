# archv2-gate: shape 1x1, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 64777 | 1.2 | 63707 | 66225 | ±1.3% | ±1.1% |
| v2 | 10 | 61803 | 0.3 | 61482 | 62107 | ±0.3% | ±0.3% |
| proto | 10 | 42008 | 0.3 | 41851 | 42240 | ±0.8% | ±0.6% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -4.6% | -5.7%, -4.8%, -6.1%, -3.2%, -4.0% | ±1.3% | resolved |
| proto vs v1 | -35.1% | -36.2%, -35.2%, -35.9%, -34.3%, -34.6% | ±1.3% | resolved |
| proto vs v2 | -32.0% | -32.3%, -31.9%, -31.8%, -32.1%, -31.9% | ±0.8% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
