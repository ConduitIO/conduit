# archv2-gate: shape 2x2, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 46456 | 2.1 | 44468 | 47702 | ±2.6% | ±2.3% |
| v2 | 10 | 26611 | 0.3 | 26512 | 26774 | ±0.8% | ±0.5% |
| proto | 10 | 60710 | 1.5 | 58996 | 61570 | ±2.1% | ±1.4% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -42.7% | -42.2%, -43.1%, -41.9%, -40.8%, -43.5% | ±2.6% | resolved |
| proto vs v1 | +30.7% | +30.8%, +28.2%, +33.8%, +35.4%, +25.0% | ±2.6% | resolved |
| proto vs v2 | +128.1% | +126.4%, +125.4%, +130.3%, +128.8%, +121.2% | ±2.1% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
