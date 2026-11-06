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
or `sdk.batch.delay` after its first record, which a timer enforces even when no further record arrives.
Handling the nack earlier let the source position move past records the batching destination still held
unwritten, and a crash then lost them (#3323, postmortem
`docs/postmortems/20261103-archv2-fanout-sibling-nack-acks-past-buffered-batch.md`).

With `sdk.batch.delay` set, the wait is bounded by that delay (plus the time the flush itself takes, and on
a fan-out any pass in flight). With `sdk.batch.size` set and no `sdk.batch.delay`, on a source that goes
quiet, the batch may not fill for a long time, and nothing bounds the wait until the source sends more
records or the pipeline is stopped. The start logs `pipeline.archv2_destination_batch_unbounded_wait` for
such a destination (see below).

### Remediation

- Set `sdk.batch.delay` on the batching destination (for example `1s`). The nack is then handled within that
  delay, by the destination's flush timer.
- Or stop the pipeline: a graceful stop flushes the batch and the nack is handled then.

No data is at risk from the wait itself: nothing past the nacked record is acked to the source until it is
handled.

## A batched write fails while the source is idle

Applies from the release that contains the fix for this case (design
`docs/design-documents/20261103-archv2-destination-batch-timing.md`, rollout step 1). A destination that the
engine batches (`sdk.batch.size` above 1 or `sdk.batch.delay` above 0) and that is the only destination of its
pipeline, so its writes run on a background flusher while the source reads ahead.

### Symptom

- The pipeline leaves **running** soon after the destination fails a write: it goes to recovering, or to
  degraded when the error is fatal. Its error reads
  `destination <connector-id> failed to flush buffered records: ...`, followed by the write, ack or DLQ error.
- Before the fix the same failure left the pipeline reporting **running** while it wrote nothing, until the
  source sent another record or the pipeline was stopped. The stop then reported
  `pipeline.flush_on_close_failed`.

### Diagnosis

The worker hands a full batch to the flusher and goes back to the source. When the source has nothing more, the
worker waits in the source read, and only a record arriving or a stop used to look at the flusher's error. The
flusher now reports its own failure to the run, which ends the run and unblocks the read. The error keeps its
classification: a failed DLQ write or an exceeded nack threshold is fatal and degrades the pipeline; a plain
write error goes through recovery like any other transient failure. The cause is in the wrapped error and in the
destination connector's logs.

These do not fail the run, because they are the expected cases where the records replay: the run's own
cancellation, a source that was already torn down when the flush acked it, and, during a graceful stop, a drain
that could not reach the flusher, or whose flush failed because the stop deadline ended. A genuine destination
failure during a graceful stop (for example a write the flusher already had in flight) is still reported.

### Remediation

Fix the destination (the wrapped error names the cause) and let recovery restart the pipeline, or restart it
yourself. No data is at risk: the records in the failed flush, and everything queued behind it, were never
acknowledged to the source and are delivered again after the restart.

## A partial batch waits longer than `sdk.batch.delay`

Applies from the release that contains the flush timer (#3231, design
`docs/design-documents/20261103-archv2-destination-batch-timing.md`). A destination the engine batches with
`sdk.batch.delay` set.

### Symptom

- The last records before the source goes quiet show up at the destination, and the source position moves,
  later than `sdk.batch.delay` after they were read.
- `conduit_funnel_destination_flush_linger_seconds` for the destination has a p99 above twice
  `sdk.batch.delay`.

### Diagnosis

`sdk.batch.delay` bounds how long a record waits in the destination's buffer before the buffer is taken for
writing, counted from the buffer's first record. A timer takes it at that deadline if nothing else did. What
comes after the take is not covered by the delay: the write and its acks, the flushes queued ahead of it (at
most two on a single-destination pipeline), and on a pipeline with several destinations any pass that is in
flight, which the timer waits for. Compare the triggers in `conduit_funnel_destination_flushes_total`:
`deadline_timer` counts partial batches the timer flushed. If linger is high while `deadline_timer` stays flat,
the timer is waiting behind something, usually a slow destination (check
`conduit_connector_execution_duration_seconds` for it) or, on fan-out, a slow sibling destination.

### Remediation

Fix or scale the slow destination. Lowering `sdk.batch.delay` shortens the wait in the buffer but not the
write. No data is at risk: nothing is acknowledged to the source before it is written.

## Size-only batching: `pipeline.archv2_destination_batch_unbounded_wait`

### Symptom

The start of a pipeline on `--preview.pipeline-arch-v2` logs a warning with the code
`pipeline.archv2_destination_batch_unbounded_wait`, naming a destination connector that sets
`sdk.batch.size` above 1 and no `sdk.batch.delay`. The pipeline starts and runs.

### Diagnosis

With size-only batching nothing bounds how long a partial batch waits: on a source that goes quiet, the last
records are not written, and not acknowledged upstream, until more records arrive or the pipeline stops. The
connector SDK behaves the same way, so this is not a change from the default engine. It pins the source
position (on Postgres, WAL is retained), and on a pipeline with several destinations a record another
destination nacks reaches the DLQ, or with the default DLQ fails the pipeline, only once this destination has
flushed.

### Remediation

Set `sdk.batch.delay` on the destination (for example `1s`), or accept the wait if the source never goes quiet.

## Related

- Issue #2979
- Issue #3323, PR #3324: a fan-out nack waits for every destination's vote
- Issue #3231: flush timer for destination batches (design
  `docs/design-documents/20261103-archv2-destination-batch-timing.md`)
- Error code reference: `pipeline.archv2_destination_batching_unsupported` in `llms-full.txt`
