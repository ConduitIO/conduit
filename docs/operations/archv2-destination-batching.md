# Arch-v2 refuses destinations with SDK batching

On the preview pipeline engine (`--preview.pipeline-arch-v2`), a pipeline whose destination connector, or
dead-letter-queue connector, sets `sdk.batch.size` above 1 does not start. The default engine is not affected.

## Symptom

`conduit run`, a pipeline start through the API, or provisioning fails with the code
`pipeline.archv2_destination_batching_unsupported` (FailedPrecondition):

```text
can't start pipeline <pipeline-id> on pipeline architecture v2: destination connector "<connector-id>" sets
sdk.batch.size=100 (/connectors/<connector-id>/settings/sdk.batch.size); v2 waits for every ack before the next
read, and an SDK-batching destination only acks when its batch flushes, so the pipeline would stall
(sdk.batch.delay 0) or run more than 99% slower
```

The error carries the config path of the first offending setting and the suggested fix. When the dead-letter queue
is the offender the path is `/dead-letter-queue/settings/sdk.batch.size`. Every offender is named in the message.

The pipeline is not started. Nothing is read from the source and nothing is acknowledged upstream.

Before this check (v0.20.0) the same configuration started and then either delivered nothing while reporting
**running** (`sdk.batch.delay` left at 0), or ran at under 1% of the default engine's speed (a non-zero delay).

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

Only `sdk.batch.size` above 1 is refused. The defaults, an explicit 0 or 1, and `sdk.batch.delay` on its own are
accepted. A value that does not parse as an integer is not refused here; the connector rejects it itself.

## Remediation

Pick one:

- Remove `sdk.batch.size` and `sdk.batch.delay` from the destination (or DLQ) settings. This is the supported
  configuration on arch-v2. Larger source batches (`sdk.batch.size` on the **source**) are fine and are what raises
  arch-v2 throughput.
- If the pipeline does not need record fan-out (a processor that splits one record into several, such as `ai.chunk`),
  run without `--preview.pipeline-arch-v2` and keep the destination batching.

A pipeline that was running slowly with a non-zero delay now refuses to start with this error until one of the above
is applied. No data is at risk: the pipeline never started, and its stored position is unchanged.

## Related

- Issue #2979
- Error code reference: `pipeline.archv2_destination_batching_unsupported` in `llms-full.txt`
