# archv2-gate: shape 2x2, session aa-v1

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink. Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v1-a | 5 | 40563 | 2.5 | 38937 | 41607 |
| v1-b | 5 | 40118 | 3.7 | 38541 | 41834 |

## A/A control (v1-b vs v1-a)

- per-round deltas: +4.3%, -5.0%, -1.5%, +7.4%, -7.0%
- median-to-median delta: -1.1%
- **A/A floor (largest per-round |delta|): ±7.4%**

Read this against the A/A floor, not on its own. A difference inside the floor is not a difference this session can resolve. Rates from different sessions are not comparable.
