# archv2-gate: shape 4x4-destbatch, session matrix

5 rounds, 20s warmup discarded, 1m0s window, records counted at the sink.
Environment in env.json, every run in runs.csv.

Bench knobs: `CONDUIT_BENCH_DEST_ACK_LATENCY=1us`

| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1 | 10 | 26858 | 2.0 | 25878 | 27424 | ±5.4% | ±3.2% |
| proto | 10 | 89295 | 0.7 | 88762 | 91071 | ±1.7% | ±0.3% |

| comparison | median delta | per-round deltas | A/A floor | read |
| --- | --- | --- | --- | --- |
| proto vs v1 | +232.5% | +235.3%, +235.8%, +230.5%, +232.3%, +237.6% | ±5.4% | resolved |

Arms that produced no output in the probe and were not run, with Conduit's error lines:

- v2:

  ```text
  sinks [/sink/sink-1.jsonl /sink/sink-2.jsonl /sink/sink-3.jsonl /sink/sink-4.jsonl] not written within 45s; conduit errors: 2026-10-15T08:14:28+00:00 ERR provisioning failed error="pipeline \"archv2-gate\", error while provisioning: could not start the pipeline \"archv2-gate\": can't start pipeline archv2-gate on pipeline architecture v2: destination connector \"sink-1\" (name \"sink-1\") sets sdk.batch.size=100 (/connectors/4/settings/sdk.batch.size); destination connector \"sink-2\" (name \"si...
  ```

Each arm ran twice per round (replicates -a and -b). Its A/A floor is the largest per-round
difference between them; "typical" is the median per-round difference. A comparison is _resolved_ only
if the median delta exceeds the larger floor and every per-round delta has the same sign. It is
_suggestive_ if not resolved, but every per-round delta has the same sign and the median delta exceeds
twice the larger typical difference: one disturbed run may be hiding a consistent effect. Suggestive is
a reason to rerun on quiet hardware, not a result. Rates from different sessions are not comparable.
