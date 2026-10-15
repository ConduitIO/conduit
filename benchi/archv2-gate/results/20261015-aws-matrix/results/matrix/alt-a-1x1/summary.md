# archv2-gate: shape 1x1, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 44413 | 0.8 | 43772 | 45089 | ±1.4% | ±0.6% |
| v2 | 10 | 64186 | 0.5 | 63651 | 64580 | ±1.1% | ±0.7% |
| proto | 10 | 50692 | 0.5 | 50359 | 51093 | ±1.4% | ±0.5% |
| v2-alta | 10 | 231653 | 0.9 | 227215 | 233699 | ±2.8% | ±1.4% |
| proto-alta | 10 | 83726 | 0.5 | 83117 | 84105 | ±0.8% | ±0.0% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| v2 vs v1 | +44.5% | +43.9%, +42.6%, +45.7%, +44.5%, +45.2% | ±1.4% | resolved |
| proto vs v1 | +14.1% | +14.3%, +12.4%, +14.8%, +14.4%, +14.4% | ±1.4% | resolved |
| v2-alta vs v1 | +421.6% | +420.5%, +416.0%, +425.1%, +424.7%, +419.9% | ±2.8% | resolved |
| proto-alta vs v1 | +88.5% | +87.7%, +86.7%, +89.6%, +87.6%, +89.7% | ±1.4% | resolved |
| proto vs v2 | -21.0% | -20.6%, -21.2%, -21.2%, -20.8%, -21.2% | ±1.4% | resolved |
| v2-alta vs v2 | +260.9% | +261.7%, +262.0%, +260.4%, +263.0%, +258.2% | ±2.8% | resolved |
| proto-alta vs v2 | +30.4% | +30.4%, +30.9%, +30.1%, +29.8%, +30.7% | ±1.1% | resolved |
| v2-alta vs proto | +357.0% | +355.5%, +359.2%, +357.2%, +358.5%, +354.3% | ±2.8% | resolved |
| proto-alta vs proto | +65.2% | +64.3%, +66.1%, +65.1%, +63.9%, +65.8% | ±1.4% | resolved |
| proto-alta vs v2-alta | -63.9% | -63.9%, -63.8%, -63.9%, -64.2%, -63.5% | ±2.8% | resolved |

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
