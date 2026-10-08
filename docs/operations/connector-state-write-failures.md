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
failed to store connector batch, transaction discarded: connector "<id>": ...
failed to create transaction for connector batch: ...
failed to commit connector batch: ...
```

A stopping connector wraps it as `failed to persist source connector position during teardown: ...`
or `failed to persist destination connector state: ...`.

The Conduit log always has the error line
`failed to persist connector batch; nothing in it was committed and every connector in it is notified`.
Both pipeline architectures (the default one and arch-v2, `--preview.pipeline-arch-v2`) treat it
the same way:

- A running pipeline fails on the first failed write of one of its connectors. The error is not
  fatal, so the pipeline goes through error recovery: status **recovering**, then a restart from
  the last stored position after the backoff. If the store is back by then, the pipeline runs
  again and nothing else is needed.
- If the store keeps failing, every restart fails the same way. With the default
  `pipelines.error-recovery.max-retries` (-1, unlimited) the pipeline keeps cycling between
  recovering and running; with a limit it goes **degraded** once the limit is spent, with an error
  saying it could not recover.
- A pipeline that was stopping reports the error as its stop error and is not restarted.

Under arch-v2 the error the run failed with reads
`worker for source <id> stopped with error: connector <id> reported an error: ...`, followed by
the message above. Before v0.21 arch-v2 did not read these errors: the pipeline kept reporting
running, and the only signs were the log line and upstream retention that kept growing (#2929).

## Diagnosis

The batch is all-or-nothing. When one connector's write fails, nothing in that batch is committed
and every connector in it gets the error, so pipelines other than the one with the bad connector
can fail at the same moment. The connector ID in the message is the one whose write failed. The
others were failed because they shared its batch.

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
  separate Conduit instances until #2930 lands.
- If the same connector fails on every restart, its state cannot be written at all. Stop that
  pipeline so it stops failing the batches it shares with others, and report it with the error
  message.
