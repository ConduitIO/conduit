# archv2-gate: shape 4x4, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 33716 | 5.9 | 30649 | 37546 | ±19.6% | ±8.5% |
| v2 | 10 | 35610 | 0.5 | 35270 | 35891 | ±1.1% | ±0.7% |
| proto | 10 | 55321 | 1.4 | 54261 | 56400 | ±3.0% | ±1.9% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +5.6% | +11.3%, +5.5%, +3.2%, +5.2%, +2.8% | ±19.6% | not resolved |
| proto vs v1 | +64.1% | +70.2%, +65.2%, +62.0%, +62.7%, +61.4% | ±19.6% | resolved |
| proto vs v2 | +55.4% | +53.0%, +56.5%, +56.9%, +54.6%, +57.0% | ±3.0% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
