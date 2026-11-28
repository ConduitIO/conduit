# archv2-gate: shape 1x1, session aa-v1

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v1-a | 5 | 63545 | 0.6 | 62742 | 63828 |
| v1-b | 5 | 63580 | 0.7 | 62594 | 63664 |

## A/A control (v1-b vs v1-a)

- per-round deltas: -0.3%, +1.5%, -0.4%, +0.3%, -1.5%
- median-to-median delta: +0.1%
- **A/A floor (largest per-round |delta|): ±1.5%**

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
