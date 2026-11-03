# Arch-v2 refuses destinations with SDK batching

On the preview pipeline engine (`--preview.pipeline-arch-v2`), a pipeline whose destination connector, or
dead-letter-queue connector, sets `sdk.batch.size` above 1 does not start. The default engine is not affected.

## Symptom

`conduit run`, a pipeline start through the API, or provisioning fails with the code
`pipeline.archv2_destination_batching_unsupported` (FailedPrecondition). A live apply (`pipelines deploy` against a
running server) that adds the setting to a running pipeline is refused with the same code before the pipeline is
stopped, so the pipeline keeps running with its old config:

```text
can't start pipeline <pipeline-id> on pipeline architecture v2: destination connector "<connector-id>" (name "<name>")
sets sdk.batch.size=100 (/connectors/1/settings/sdk.batch.size); v2 waits for every ack before the next read, and a
destination batch larger than the records per write withholds its acks until it flushes, so the pipeline can stall
(sdk.batch.delay 0) or slow sharply
```

The error names every offending destination in the message. Its config path and structured fix point at the first
one, as a JSON pointer into the pipeline config document: `/connectors/<index>/settings/sdk.batch.size`, where
`<index>` is the connector's position in `connectors:`, or `/dead-letter-queue/settings/sdk.batch.size` when the
dead-letter queue is the offender. Fix it and re-run to see the next one, if any.

A pipeline created through the API (rather than a pipeline config file) has no config document to point into, so for
it the error carries no config path and no structured fix. The message still names the connector id and name and the
setting; remove `sdk.batch.size` from that connector's settings through the API.

The pipeline is not started. Nothing is read from the source and nothing is acknowledged upstream.

Before this check (v0.20.0) the same configuration started and then, depending on write size and delay, either
delivered nothing while reporting **running** (`sdk.batch.delay` left at 0), or ran far slower than the default
engine (measured at 83 rec/s against 56.7k rec/s with one-record reads and a 10ms delay).

## Diagnosis

The arch-v2 destination task writes a batch and then waits for every ack of it before the source is read again. With
`sdk.batch.size` above 1 the connector SDK's destination only queues a write, and sends the acks when the batch
flushes: when it is full, or `sdk.batch.delay` after its first record. With the default delay of 0 a partial batch is
never flushed by time, so the ack never comes. The default engine reads destination acks on their own goroutine and
keeps sending records, so it is not affected.

Check which settings are set on each destination (and the DLQ):

```bash
grep -n -B2 -A6 "sdk.batch" pipeline.yaml
```

The check is conservative: any `sdk.batch.size` above 1 is refused, including a configuration where the write size
happens to line up with the batch and would have worked. Size 0 or 1, and `sdk.batch.delay` on its own, are safe and
accepted (the SDK flushes synchronously when the batch reaches its size). The SDK trims whitespace around keys and
values, so the check does too. A value that does not parse as an integer is not refused here; the connector rejects
it itself.

A destination that is not built on the connector SDK, or a standalone connector that buffers internally, is not
detected by this check and can still stall arch-v2.

## Remediation

Pick one:

- Remove `sdk.batch.size` and `sdk.batch.delay` from the destination (or DLQ) settings. This is the supported
  configuration on arch-v2. Larger source batches (`sdk.batch.size` on the **source**) are fine and are what raises
  arch-v2 throughput.
- If the pipeline does not need record fan-out (a processor that splits one record into several, such as `ai.chunk`),
  run without `--preview.pipeline-arch-v2` and keep the destination batching.

A pipeline that was running slowly with a non-zero delay now refuses to start with this error until one of the above
is applied. No data is at risk: the pipeline never started, and its stored position is unchanged.

## Fan-out: a nack waits for the batching destination

Applies from the release that contains #3324. A pipeline with one source and several destinations, where one
destination batches (`sdk.batch.size` above 1 or `sdk.batch.delay` above 0) and another nacks a record.

### Symptom

- The nacking destination logs the failed write, but the record reaches the DLQ later, and
  `conduit_dlq_records_total` lags that log line.
- With the default DLQ (`window-size: 1`, `window-nack-threshold: 0`) the pipeline does not fail when the
  destination nacks. It keeps reporting **running** and fails later, when the batching destination flushes.
- In the meantime the other destinations keep writing later records. When the pipeline fails they are not acked
  to the source, so they are delivered again after a restart (duplicates, not loss).

### Diagnosis

A nacked position is handled (DLQ write, then source ack, or the pipeline failure with the default DLQ) only
once every destination has voted for it. A batching destination votes when its batch flushes: when it is full,
or `sdk.batch.delay` after its first record. Handling the nack earlier let the source position move past records
the batching destination still held unwritten, and a crash then lost them (#3323, postmortem
`docs/postmortems/20261103-archv2-fanout-sibling-nack-acks-past-buffered-batch.md`).

With `sdk.batch.size` set and no `sdk.batch.delay`, on a source that goes quiet, the batch may not fill for a
long time, and nothing bounds the wait until the source sends more records or the pipeline is stopped. #3231
tracks a flush timer for this case.

### Remediation

- Set `sdk.batch.delay` on the batching destination (for example `1s`). The nack is then handled within that
  delay.
- Or stop the pipeline: a graceful stop flushes the batch and the nack is handled then.

No data is at risk from the wait itself: nothing past the nacked record is acked to the source until it is
handled.

## Related

- Issue #2979
- Issue #3323, PR #3324: a fan-out nack waits for every destination's vote
- Issue #3231: flush timer for destination batches
- Error code reference: `pipeline.archv2_destination_batching_unsupported` in `llms-full.txt`
