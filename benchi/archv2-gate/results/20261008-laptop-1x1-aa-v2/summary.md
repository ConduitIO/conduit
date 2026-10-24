# archv2-gate: shape 1x1, session aa-v2

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v2-a | 5 | 51422 | 1.6 | 50373 | 52486 |
| v2-b | 5 | 51174 | 1.3 | 49953 | 51695 |

## A/A control (v2-b vs v2-a)

- per-round deltas: -0.4%, +2.6%, -2.9%, -1.2%, -2.6%
- median-to-median delta: -0.5%
- **A/A floor (largest per-round |delta|): ±2.9%**

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
