# archv2-gate: shape 2x2, session aa-v1

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v1-a | 5 | 41886 | 0.7 | 41472 | 42273 |
| v1-b | 5 | 41932 | 1.2 | 41390 | 42781 |

## A/A control (v1-b vs v1-a)

- per-round deltas: -0.2%, +2.1%, -0.8%, +0.4%, -0.2%
- median-to-median delta: +0.1%
- **A/A floor (largest per-round |delta|): ±2.1%**

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
