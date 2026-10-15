# archv2-gate: shape 1x1-destbatch, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 57907 | 0.7 | 57155 | 58588 | ±2.1% | ±0.8% |
| proto | 10 | 49351 | 0.2 | 49096 | 49463 | ±0.5% | ±0.2% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| proto vs v1 | -14.8% | -14.9%, -14.8%, -15.1%, -15.0%, -14.6% | ±2.1% | resolved |

Arms that produced no output in the probe and were not run, with Conduit's error lines:

- v2:

  ```text
  sinks [/sink/sink-1.jsonl] not written within 45s; conduit errors: 2026-10-15T05:27:57+00:00 ERR provisioning failed error="pipeline \"archv2-gate\", error while provisioning: could not start the pipeline \"archv2-gate\": can't start pipeline archv2-gate on pipeline architecture v2: destination connector \"sink-1\" (name \"sink-1\") sets sdk.batch.size=100 (/connectors/1/settings/sdk.batch.size); v2 waits for every ack before the next read, and a destination batch larger than the records per wri...
  ```

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
