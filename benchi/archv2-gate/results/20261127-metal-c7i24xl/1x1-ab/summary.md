# archv2-gate: shape 1x1, session ab

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v1-a | 5 | 63055 | 0.9 | 62614 | 64063 |
| v2 | 5 | 124607 | 0.2 | 124522 | 124971 |
| v1-b | 5 | 63188 | 1.2 | 62637 | 64279 |

## A/A control (v1-b vs v1-a)

- per-round deltas: -1.6%, +2.2%, +1.5%, -1.4%, +0.1%
- median-to-median delta: +0.2%
- **A/A floor (largest per-round |delta|): ±2.2%**

## A/B (v2 vs v1)

- per-round deltas, v2 against the mean of the v1 runs bracketing it: +97.3%, +95.9%, +96.7%, +96.4%, +98.8%
- median-to-median delta, v2 vs all v1 runs: +97.4%
- A/A floor from this session, quoted beside it: ±2.2%

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
