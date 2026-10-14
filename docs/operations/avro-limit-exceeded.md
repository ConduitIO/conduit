# Avro payload over the element limit (`schema.avro.limit_exceeded`)

Conduit caps every Avro array and map at 1,000,000 elements when it decodes Avro in-process:
the `avro.decode` processor and the schema handling of built-in connectors and processors. The
limit is the engine setting `schema.avro.max-elements` (flag `--schema.avro.max-elements`,
config `schema.avro.max-elements`; `0` removes it). A payload that declares more fails to
decode. It is never truncated. Standalone plugins decode in their own process with their own SDK
and are not governed by this setting.

The limit applies to decoding only. A source still encodes a record over the limit, so the
failure shows up downstream, at the pipeline's own destination or at `avro.decode`.

## Symptom

The failing record's error carries the stable code `schema.avro.limit_exceeded`, the config path
`schema.avro.max-elements`, and a message containing ``size is greater than `Config.MaxSliceAllocSize` ``
(arrays) or ``size is greater than `Config.MaxMapAllocSize` `` (maps). It appears in:

- the pipeline's `State.error` (API, `conduit pipelines ...`, UI) when the pipeline stops;
- the Conduit log, on the processor or destination node;
- DLQ records, when the DLQ is enabled and routes the record.

What the pipeline does depends on the path and the DLQ:

| Failure path | DLQ threshold 0 (default) | DLQ threshold above 0, window exceeded | DLQ threshold above 0, within window |
| --- | --- | --- | --- |
| Classic engine, `avro.decode` or any processor | **Degraded** | Degraded | record goes to the DLQ, pipeline keeps running |
| Classic engine, destination (SDK schema handling) | **Recovering**, retried forever | Degraded | whole write batch goes to the DLQ |
| Architecture v2 (`--preview.pipeline-arch-v2`), any path | **Recovering**, retried forever | Degraded | record (or whole write batch) goes to the DLQ |

A destination failure fails the whole `Write` call, so every record in that batch is nacked
together, not only the over-limit one.

Recovering means the pipeline restarts after a backoff, re-reads the same record from the last
stored position and fails again. Nothing is acknowledged and nothing is lost, but the pipeline
makes no progress. See "Poison-record loop" below.

## Diagnosis

1. Read the error. It names the code and the setting. Check whether the message says array
   (`MaxSliceAllocSize`) or map (`MaxMapAllocSize`).
2. Find which path failed from the node in the log line: a processor ID (`avro.decode` or another
   built-in processor) or the destination connector.
3. Decide whether the record is legitimate. An array or map with more than 1,000,000 elements in a
   single record is unusual. Look at the producer: a runaway batch, a bug that repeats elements,
   or a corrupt payload that declares a huge count with few bytes.
4. Compare the record's real element count with the setting. Run
   `conduit config` (or look at the startup log) for the value in effect.

## Remediation

- **The record is legitimate:** raise `schema.avro.max-elements`, or set it to `0` for no limit,
  and restart Conduit. The setting is process-wide, read once at startup, and a schema already
  cached keeps its old limit for up to 4 hours in a long-running process, so restart rather than
  expect a live change. Removing the limit lets a corrupt payload make the decoder allocate
  whatever it declares, so prefer a high finite value.
- **The record is bad and the pipeline should continue:** enable the DLQ with a threshold above 0
  (for example `dead-letter-queue.window-nack-threshold: 5`, `window-size: 20`) so the record is
  routed to the DLQ and acknowledged instead of stalling the pipeline.
- **Fix the producer** so it stops emitting oversized collections. This is the real fix when the
  record is wrong.
- A pipeline stuck in Recovering can be stopped, and the DLQ or setting changed, then started
  again; it resumes from the stored position.

## Poison-record loop

With the default DLQ (threshold 0) and the default infinite retries
(`pipelines.error-recovery.max-retries: -1`), a record that fails deterministically puts the
pipeline in an endless Recovering loop on architecture v2 (every kind of nack) and on the classic
engine for destination-side failures. It is not specific to Avro. Tracked in
[#2997](https://github.com/ConduitIO/conduit/issues/2997). Until it is fixed, configure a DLQ with
a threshold above 0 for pipelines that can see poison records, or set a finite
`pipelines.error-recovery.max-retries` so the pipeline ends in Degraded.
