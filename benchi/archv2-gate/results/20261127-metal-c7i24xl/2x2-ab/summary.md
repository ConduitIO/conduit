# archv2-gate: shape 2x2, session ab

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max |
| --- | --- | --- | --- | --- | --- |
| v1-a | 5 | 41770 | 1.1 | 40957 | 42065 |
| v2 | 5 | 59392 | 0.1 | 59319 | 59430 |
| v1-b | 5 | 41516 | 0.9 | 41337 | 42098 |

## A/A control (v1-b vs v1-a)

- per-round deltas: +0.2%, +0.9%, -1.6%, +0.1%, +0.8%
- median-to-median delta: -0.6%
- **A/A floor (largest per-round |delta|): ±1.6%**

## A/B (v2 vs v1)

- per-round deltas, v2 against the mean of the v1 runs bracketing it: +43.2%, +44.3%, +42.4%, +41.3%, +41.7%
- median-to-median delta, v2 vs all v1 runs: +42.6%
- A/A floor from this session, quoted beside it: ±1.6%

Read this against the A/A floor, not on its own. A difference inside the floor is not
a difference this session can resolve. Rates from different sessions are not comparable.
