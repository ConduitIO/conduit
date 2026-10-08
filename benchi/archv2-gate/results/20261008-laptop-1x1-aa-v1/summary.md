# archv2-gate: shape 1x1, session aa-v1

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v1-a | 5 | 63756 | 25.3 | 32295 | 68075 |
| v1-b | 5 | 66208 | 1.0 | 65994 | 67560 |

## A/A control (v1-b vs v1-a)

- per-round deltas: +0.7%, +104.6%, +10.3%, -3.1%, +3.8%
- median-to-median delta: +3.8%
- **A/A floor (largest per-round |delta|): ±104.6%**

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
