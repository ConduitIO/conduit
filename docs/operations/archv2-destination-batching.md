# Arch-v2 destination batching

On the preview pipeline engine (`--preview.pipeline-arch-v2`) the engine batches destination writes itself and
configures the destination connector not to batch. This page covers what that means for operators, the one
batching configuration arch-v2 still refuses (on the dead-letter queue), and the failure modes around it. The
default engine is not affected: there the connector SDK batches, as it always has.

## How arch-v2 batches a destination

For each destination the engine resolves the effective `sdk.batch.size` and `sdk.batch.delay`, per key:

1. the value in the pipeline config, if it is set and parses;
2. otherwise the default the connector declares in its specification;
3. otherwise 0 (off).

It batches with the result (size, byte cap, the `sdk.batch.delay` timer, a stop), and hands the connector `0` for
each of those keys whose value would otherwise turn the connector's own batcher on. Without that, the connector
would hold every acknowledgement back behind its own batch, and arch-v2, which waits for the acknowledgements of a
write before it reads more, would slow to one record per connector delay.

Consequences:

- A connector that batches by default, such as the S3 destination (1000 records / 1 s), is batched by the engine
  at that default even when the pipeline sets nothing. It writes fewer, larger objects than v0.21 arch-v2 did, the
  same layout the default engine produces.
- Setting `sdk.batch.*` on such a destination no longer leaves the connector batching a second time behind the
  engine.
- A value that does not parse, or is negative, is passed to the connector unchanged, which rejects it at start.
- The start logs `destination batching taken over from connector defaults` (INFO) for a destination whose batching
  came at least partly from the connector's defaults, with the resolved size and delay and where each came from
  (`pipeline_config` or `connector_default`).
- `conduit_funnel_destination_plugin_batching_neutralised_total` counts each start where at least one key was
  handed to the connector as `0`. File, Kafka, log and Postgres destinations declare `0` defaults and are not
  counted unless the pipeline turns batching on.

## A destination is slow behind a 1-record source

Applies to arch-v2 from the release that contains #3321 (design
`docs/design-documents/20261103-archv2-destination-batch-timing.md`, Decision 2).

### Symptom

A pipeline whose source returns one record per read (for example a Kafka source without `sdk.batch.*`) delivers
about one record per second, or per some other fixed interval, to the destination. The pipeline reports
**running** and CPU use is low. Before the fix this was what a default S3 destination did on arch-v2 (#3321).

### Diagnosis

The destination's connector is batching inside its plugin and holding acknowledgements until its own delay
passes. Check, for that destination:

- `conduit_funnel_destination_plugin_batching_neutralised_total`. If it counts the destination, the engine already
  hands the connector `0` for its declared batch settings, and the slowness is elsewhere (check
  `conduit_connector_execution_duration_seconds`).
- If it does not count the destination, the connector either declares no batching default, or batches
  internally without declaring `sdk.batch.*` in its specification (a connector not built on the Go connector SDK,
  or one whose code default disagrees with its specification). The engine cannot see that kind of batching.

### Remediation

Set `sdk.batch.size` and `sdk.batch.delay` on the destination in the pipeline config. The engine then batches the
writes itself and reads ahead, so the connector receives larger writes. If the connector still holds
acknowledgements, report it to the connector's maintainers. No data is at risk: nothing is acknowledged to the
source before the destination acknowledges it.

## Arch-v2 refuses SDK batching on the dead-letter queue

The dead-letter queue is not batched by the engine. A pipeline whose dead-letter-queue connector sets
`sdk.batch.size` above 1, or `sdk.batch.delay` above 0, does not start on arch-v2. A dead-letter queue that batches
only by its connector's default (S3 used as a dead-letter queue) is handed `0` instead and writes each nacked record
synchronously.

### Symptom

`conduit run`, a pipeline start through the API, or provisioning fails with the code
`pipeline.archv2_destination_batching_unsupported` (FailedPrecondition). A live apply (`pipelines deploy` against a
running server) that adds the setting to a running pipeline is refused with the same code before the pipeline is
stopped, so the pipeline keeps running with its old config:

```text
can't start pipeline <pipeline-id> on pipeline architecture v2: the dead letter queue sets sdk.batch.size=50
(/dead-letter-queue/settings/sdk.batch.size); v2 waits for every ack before the next read, and a DLQ batch larger
than the records per write withholds its acks until it flushes, so the pipeline can stall (sdk.batch.delay 0) or
slow sharply
```

The config path and structured fix point at the first offending setting, as a JSON pointer into the pipeline
config document: `/dead-letter-queue/settings/sdk.batch.size` or `/dead-letter-queue/settings/sdk.batch.delay`.
Fix it and re-run to see the next one, if any.

A pipeline created through the API (rather than a pipeline config file) has no config document to point into, so
for it the error carries no config path and no structured fix. The message still names the setting; remove it
from the dead-letter queue's settings through the API.

The pipeline is not started. Nothing is read from the source and nothing is acknowledged upstream.

From the release that contains #3321, `sdk.batch.delay` above 0 on the dead-letter queue is refused too. An arch-v2
pipeline that set it started before and now does not: the SDK batches on a delay alone, with a size of its own
(1000 in SDK v0.17.2), so every nacked record waited for that delay before its acknowledgement.

### Diagnosis

The arch-v2 engine writes to the dead-letter queue and then waits for the acknowledgement before it acknowledges
the nacked record to the source. With `sdk.batch.size` above 1, or `sdk.batch.delay` above 0, the connector SDK only
queues the write and sends the acknowledgement when its batch flushes: when it is full, or `sdk.batch.delay` after
its first record. With no delay a partial batch is never flushed by time, so the acknowledgement never comes. The
default engine reads acknowledgements on their own goroutine, so it is not affected.

Check the dead-letter queue's settings:

```bash
grep -n -A6 "dead-letter-queue" pipeline.yaml
```

The check is conservative: any `sdk.batch.size` above 1 and any `sdk.batch.delay` above 0 is refused. Size 0 or 1
with no delay is accepted. The SDK trims whitespace around keys and values, so the check does too. A value that
does not parse is not refused here; the connector rejects it itself.

### Remediation

Pick one:

- Remove `sdk.batch.size` and `sdk.batch.delay` from the dead-letter queue's settings.
- If the pipeline does not need record fan-out (a processor that splits one record into several, such as
  `ai.chunk`), run without `--preview.pipeline-arch-v2` and keep the batching.

No data is at risk: the pipeline never started, and its stored position is unchanged.

## Arch-v2 refuses an environment reference in `sdk.batch.*`

On arch-v2, a destination's or the dead-letter queue's `sdk.batch.size` and `sdk.batch.delay` must be literal
values. A value that contains `$`, such as `${S3_BATCH}`, is refused.

### Symptom

`conduit run`, a pipeline start through the API, or provisioning fails with the code
`pipeline.archv2_destination_batching_not_literal` (FailedPrecondition). A live apply that adds such a value is
refused with the same code before the running pipeline is stopped. `conduit pipelines validate --preview.pipeline-arch-v2`
and `conduit doctor` (with arch-v2 enabled) report the same code offline.

```text
can't start pipeline <pipeline-id> on pipeline architecture v2: destination connector "<pipeline-id>:s3-out" sets
sdk.batch.size=${S3_BATCH} (/connectors/1/settings/sdk.batch.size); v2 batches a destination itself and configures
the connector not to batch, which it decides from the value as written, while an environment reference is
expanded only for the connector, so the connector could batch behind the engine and withhold acks
```

The config path points at the first offending setting (`/connectors/<index>/settings/<key>` or
`/dead-letter-queue/settings/<key>`). A pipeline created through the API has no config document, so the error then
carries no config path; the message still names the connector and the setting. The message shows the reference as
written, never its value.

The pipeline is not started. Nothing is read from the source and nothing is acknowledged upstream.

### Diagnosis

The engine decides how to batch a destination, and which `sdk.batch.*` keys to hand the connector as `0`, from the
settings as stored. `${VAR}` references are expanded later, only for the connector (see
[env-allowlist.md](env-allowlist.md)). To the engine `${S3_BATCH}` is not a number, so it would batch with the
connector's default (S3: 1000 / 1 s) and leave the size key alone, while the connector gets the expanded value.
With `S3_BATCH=10000` the connector would batch 10000 records with no delay behind the engine, and hold the
acknowledgements of every engine flush until records the engine never sends fill its batch: the pipeline stalls
while it reports running. A `sdk.batch.delay` reference makes the connector batch on a delay behind the engine
instead, so every write waits that much longer. On the dead-letter queue a reference would also get past the
refusal above.

Find the reference:

```bash
grep -n 'sdk.batch.*\$' pipeline.yaml
```

### Remediation

- Write the value in the pipeline config, for example `sdk.batch.size: "1000"` and `sdk.batch.delay: "1s"`. To
  vary it per environment, template the pipeline file before Conduit reads it rather than through `${VAR}`.
- Or remove the setting: the engine then uses the connector's own default.
- Or run without `--preview.pipeline-arch-v2`. The default engine hands the settings to the connector as they are,
  and the connector does all the batching.

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
`pipeline.archv2_destination_batch_unbounded_wait`, naming a destination connector whose effective
`sdk.batch.size` is above 1 with no `sdk.batch.delay`, from the pipeline config or the connector's defaults.
The pipeline starts and runs. The warning is logged again on every start, including each restart after an
error recovery, so a repeat is the same configuration, not a new problem.

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
- Issue #3321: arch-v2 owns destination batching (design
  `docs/design-documents/20261103-archv2-destination-batch-timing.md`, Decision 2)
- Issue #3323, PR #3324: a fan-out nack waits for every destination's vote
- Issue #3231: flush timer for destination batches (design
  `docs/design-documents/20261103-archv2-destination-batch-timing.md`)
- Error code reference: `pipeline.archv2_destination_batching_unsupported` in `llms-full.txt`
