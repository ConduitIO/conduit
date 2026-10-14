# archv2-gate: shape 2x2, session matrix

5 rounds, 8s warmup discarded, 25s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 42060 | 16.7 | 29071 | 49792 | ±34.5% | ±11.9% |
| v2 | 10 | 30264 | 25.5 | 21120 | 39740 | ±69.4% | ±16.9% |
| proto | 10 | 50160 | 21.7 | 36439 | 64355 | ±25.0% | ±4.8% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | -28.0% | -15.8%, -28.1%, -9.3%, -37.0%, -35.5% | ±69.4% | not resolved |
| proto vs v1 | +19.3% | -16.3%, +38.3%, +41.3%, +19.8%, +31.8% | ±34.5% | not resolved |
| proto vs v2 | +65.7% | -0.6%, +92.5%, +55.8%, +90.2%, +104.4% | ±69.4% | not resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
