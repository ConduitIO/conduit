# Connector state write failures

Conduit stores each connector's state (for a source, its position) in the configured database
(`db.type`: badger by default, or postgres or sqlite). Writes are batched: the persister collects
the changed connectors for up to one second (or 10,000 changes) and writes them in one transaction.
A source tells its plugin to acknowledge records upstream only after the write holding their
position has committed. See
[the postmortem for #2925](../postmortems/20261007-persister-store-error-ignored.md) for why.

## Symptom

The error carries the code `connector.state_persist_failed` and a message containing one of:

```text
failed to store connector "<id>", its state was not committed: ...
failed to create transaction for connector batch: ...
failed to commit connector batch: ...
```

A stopping connector wraps it as `failed to persist source connector position during teardown: ...`
or `failed to persist destination connector state: ...`.

The Conduit log has one of these error lines:

- `failed to persist connector state; retrying the rest of the batch without it in a new transaction`,
  with the connector ID, when one connector's write failed;
- `failed to persist connector batch; nothing in this attempt was committed and every connector in it is notified`,
  when the transaction could not be opened or committed.

What else you see depends on the pipeline architecture:

- **Default architecture:** the pipeline goes degraded with the error above. A pipeline that was
  stopping reports it as its stop error.
- **Arch-v2 (`--preview.pipeline-arch-v2`):** the pipeline keeps reporting **running**. The error
  reaches it only when the pipeline stops (#2929). While it runs, the signs are the log line above
  and a source whose upstream retention keeps growing because nothing is acknowledged. For a
  Postgres source that means a replication slot whose `confirmed_flush_lsn` stops advancing and
  retained WAL that keeps growing.

## Diagnosis

When one connector's write fails, only that connector gets the error. The persister discards the
transaction and writes the rest of the batch again in a new one, so other connectors, including
those of other pipelines, are not affected (#2930). The connector ID in the message is the one
whose write failed.

When the transaction itself could not be opened or committed, the failure is not one connector's,
and every connector in that attempt gets the error, so several pipelines can fail at the same
moment.

No data is lost. The failed positions were never acknowledged upstream. After a restart the source
resumes from its last stored position and re-reads from there, so expect duplicates downstream.

Common causes:

- **badger `Txn is too big to fit into one request`:** the batch exceeded badger's transaction
  limit (about 9.6 MB of connector state in one batch, counting only values under 1 MiB). It takes
  unusually large connector state: a large position, or large connector settings, which are
  stored twice per connector.
- **badger `Value with size ... exceeded`:** a single connector's encoded state is larger than
  1 GiB.
- **postgres or sqlite errors:** connection loss, disk full, lock timeouts. The message carries the
  driver's error.
- **database closed:** the write raced Conduit shutting down. Check whether the error lines up with
  a shutdown in the log.

## Remediation

- For transient database errors, restart the pipeline once the database is healthy. It resumes from
  its last stored position.
- For badger size errors, find the connector named in the message and reduce its state: check its
  settings for large inline values (certificates, schemas) and its position size. If many large
  connectors flush together, a single oversized batch can also be avoided by running them in
  separate Conduit instances. When a batch is too big, the connectors whose writes cross the limit
  fail (the last ones by connector ID) and the rest are committed. The persister does not retry
  the failed ones. For a source the error degrades its pipeline, so its next write usually comes
  only after the pipeline is restarted.
- If the same connector fails on every restart, its state cannot be written at all. Stop that
  pipeline and report it with the error message.
