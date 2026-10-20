# Classic engine: default destination batching

## Symptom

A classic-engine pipeline with a built-in `postgres` or `kafka` destination, upgraded to v0.21,
shows (a) higher throughput, (b) up to 100 ms extra latency for a record that arrives alone, or
(c) after a crash, up to 1000 replayed records: duplicate rows in a keyless Postgres table, or
duplicate Kafka messages.

## Diagnosis

The log has one INFO line per start: `sdk.batch.size and sdk.batch.delay are not set: applying the
classic engine's default destination batching`, with the connector ID. If it is absent, nothing was
injected: you set `sdk.batch.size` or `sdk.batch.delay` yourself, the plugin is not a built-in
`postgres`/`kafka`, the Kafka destination is in `mirror` mode, the destination is a dead-letter
queue, or the engine is arch-v2 (which batches in the funnel from your own settings).

The effective values do not appear in `conduit pipelines validate`, `doctor` or the stored connector
config.

## Remediation

- Keep one record per write: set `sdk.batch.size: "1"` on the destination (leave `sdk.batch.delay`
  unset).
- Use other values: set both `sdk.batch.size` and `sdk.batch.delay`. Setting either key turns the
  default off.
- Keyless Postgres tables: give the table a primary key and make the records carry it, so a replay
  upserts instead of appending.

## Related

- `docs/design-documents/20261020-classic-default-destination-batching.md`
- `docs/operations/archv2-destination-batching.md`
