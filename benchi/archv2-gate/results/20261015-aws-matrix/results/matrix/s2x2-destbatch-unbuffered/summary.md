# archv2-gate: shape 2x2-destbatch, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 39800 | 1.2 | 38991 | 40470 | ±1.9% | ±0.4% |
| proto | 10 | 90126 | 0.6 | 89606 | 91296 | ±1.5% | ±0.5% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| proto vs v1 | +126.4% | +125.0%, +124.7%, +130.6%, +131.2%, +126.1% | ±1.9% | resolved |

Arms that produced no output in the probe and were not run, with Conduit's error lines:

- v2:

  ```text
  sinks [/sink/sink-1.jsonl /sink/sink-2.jsonl] not written within 45s; conduit errors: 2026-10-15T05:58:20+00:00 ERR provisioning failed error="pipeline \"archv2-gate\", error while provisioning: could not start the pipeline \"archv2-gate\": can't start pipeline archv2-gate on pipeline architecture v2: destination connector \"sink-1\" (name \"sink-1\") sets sdk.batch.size=100 (/connectors/2/settings/sdk.batch.size); destination connector \"sink-2\" (name \"sink-2\") sets sdk.batch.size=100 (/conn...
  ```

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
