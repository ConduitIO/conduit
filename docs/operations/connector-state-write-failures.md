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
or `failed to persist destination connector state: ...`. A source reports its teardown persist error
only if the write fails within the teardown flush timeout (10 seconds); a failure after that is
only logged.

The Conduit log has one of these error lines:

- `failed to persist connector state; retrying the rest of the batch without it in a new transaction`,
  with the connector ID, when one connector's write failed;
- `failed to persist connector batch; nothing in this attempt was committed and every connector in it is notified`,
  when the transaction could not be opened or committed.

Both pipeline architectures (the default one and arch-v2, `--preview.pipeline-arch-v2`) treat the
error the same way:

- A running pipeline fails on the first failed write of one of its connectors. The error is not
  fatal, so the pipeline goes through error recovery: status **recovering**, then a restart from
  the last stored position after the backoff. If the store is back by then, the pipeline runs
  again and nothing else is needed.
- If the store keeps failing, every restart fails the same way. With the default
  `pipelines.error-recovery.max-retries` (-1, unlimited) the pipeline keeps cycling between
  recovering and running; with a limit it goes **degraded** once the limit is spent, with an error
  saying it could not recover.
- A pipeline that was stopping is not restarted. It ends **user stopped** and nothing is lost,
  because the positions that could not be stored were never acknowledged. What the pipeline
  reports as the reason depends on the architecture. The default architecture reports the persist
  error as the stop error. Under arch-v2 the persist error is returned to whoever called stop, but
  the run's own error is usually the later, less useful
  `failed to tear down source: plugin is not running`, because the worker tears the source down a
  second time on close and records that failure instead (#2966). If you see that message on a
  stopped arch-v2 pipeline, look for the persist error in the log line above.

Under arch-v2 the error the run failed with reads
`worker for source <id> stopped with error: connector <id> reported an error: ...`, followed by
the message above. Before v0.21 arch-v2 did not read these errors: the pipeline kept reporting
running, and the only signs were the log line and upstream retention that kept growing (#2929).

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
