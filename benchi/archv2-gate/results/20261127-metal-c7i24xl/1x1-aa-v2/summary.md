# archv2-gate: shape 1x1, session aa-v2

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v2-a | 5 | 124870 | 0.2 | 124615 | 125073 |
| v2-b | 5 | 124944 | 0.1 | 124763 | 124994 |

## A/A control (v2-b vs v2-a)

- per-round deltas: +0.3%, -0.1%, -0.2%, +0.1%, +0.3%
- median-to-median delta: +0.1%
- **A/A floor (largest per-round |delta|): ±0.3%**

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
