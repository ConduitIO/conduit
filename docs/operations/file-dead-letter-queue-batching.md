# File dead-letter queue batching

Since `builtin:file` v0.13.0 the file destination batches by default: `sdk.batch.size` `1000` and
`sdk.batch.delay` `100ms`, because every write pays for two fsyncs. On the default engine a dead-letter queue
that uses `builtin:file` is affected. On `--preview.pipeline-arch-v2` the engine hands the dead-letter queue `0`
for both keys, so this page does not apply there.

## A pipeline that nacks many records slows to about ten records per second

### Symptom

Records reach the dead-letter queue slowly, roughly one every 100ms, and the pipeline stalls behind them. The
dead-letter file grows by about ten lines per second. Throughput was normal before upgrading to Conduit
v0.22 (file connector v0.13.0). No record is lost.

### Diagnosis

The default engine writes one dead-letter record and waits for its acknowledgement before the next. The file
destination's SDK batcher holds that record for `sdk.batch.delay` (100ms) before it writes and acknowledges it,
and the handler runs on the acknowledgement path, so every record behind a nack waits as well. Check the
dead-letter settings in the pipeline: `conduit pipelines describe <id>` or the pipeline file. If
`sdk.batch.size` and `sdk.batch.delay` are absent, the connector default applies.

### Remediation

Under `dead-letter-queue.settings` set both keys to `"0"`:

```yaml
dead-letter-queue:
  plugin: builtin:file
  settings:
    path: ./files/dlq.out
    sdk.batch.size: "0"
    sdk.batch.delay: "0"
```

Set both. With only `sdk.batch.delay` at `0` the destination still batches by size, and a partial batch waits
until it is full. Each nacked record is then written and fsynced on its own.

## Related

- [Release notes, file connector v0.13.0](../releases/v0.22.0.md)
- [Arch-v2 destination batching](archv2-destination-batching.md)
