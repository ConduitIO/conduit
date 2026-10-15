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

Where the refusal shows up depends on how the start was triggered:

- **A start through the API or `conduit pipelines start`** returns the error to the caller. The pipeline's stored
  status is left as it was.
- **At boot** (`conduit run` resuming a pipeline that was running when Conduit last stopped, or provisioning a
  pipeline config file with `status: running`) the pipeline is set to **degraded** and the refusal is stored as its
  error, prefixed with the code and followed by the suggestion. `conduit pipelines list` and `GET /v1/pipelines/{id}`
  show it. A degraded pipeline is not resumed at the next boot, so the same refusal is not repeated at every start.
  Before v0.21.0 the boot path only logged the refusal and the pipeline showed as stopped with no error.
- **A recovery restart** (the pipeline was running, failed, and the destination config changed before it restarted)
  ends **degraded** with the refusal as its error.

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
is applied. If the refusal happened at boot, the pipeline stays degraded until you start it again (`conduit pipelines
start <pipeline-id>`, or restart Conduit with the fixed pipeline config file); a successful start clears the error. No data is at risk: the pipeline never started, and its stored position is unchanged.

## Related

- Issues #2979, #2989
- Error code reference: `pipeline.archv2_destination_batching_unsupported` in `llms-full.txt`
