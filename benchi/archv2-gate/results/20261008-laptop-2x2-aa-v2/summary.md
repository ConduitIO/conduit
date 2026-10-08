# archv2-gate: shape 2x2, session aa-v2

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v2-a | 5 | 44069 | 1.8 | 42702 | 44723 |
| v2-b | 5 | 43164 | 0.5 | 42775 | 43424 |

## A/A control (v2-b vs v2-a)

- per-round deltas: +1.1%, -1.9%, -4.4%, -2.7%, -1.8%
- median-to-median delta: -2.1%
- **A/A floor (largest per-round |delta|): ±4.4%**

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
