# Connector refuses to start: `connector.delete_hook_ran`

Issue: [#3137](https://github.com/ConduitIO/conduit/issues/3137).

## Symptom

Starting a pipeline fails with the error code `connector.delete_hook_ran` (gRPC
`FailedPrecondition`). The message names the connector, its plugin and pipeline. Earlier, the log
has a warning: "connector restored after a failed delete whose delete hook had already run".

## Diagnosis

The connector was deleted, and a later step of that delete (or of a provisioning apply) failed, so
the delete was rolled back. By then the connector's plugin had already received the "deleted"
lifecycle event and may have released resources that its saved position depends on. The Postgres
connector drops its replication slot and publication there by default. Resuming from the saved
position could skip records, and so could starting without one: a Kafka source with no position
starts at the end of the topic. Conduit therefore keeps the position unchanged and refuses to
start the connector until you decide.

## Remediation

Choose one:

- **Start over.** Delete the connector and create it again. It starts with no
  position, so what it reads first depends on the connector's own settings. Check them first, for
  example the Postgres source's snapshot mode or the Kafka source's `readFromBeginning`. A
  connector provisioned from a pipeline config file cannot be deleted through the API: change or
  remove it in the config file instead; Conduit applies the change when it next loads its pipeline
  configs.
- **Resume from the saved position.** First check that what the position depends on still exists.
  For Postgres, check both the replication slot and the publication:
  `SELECT slot_name, confirmed_flush_lsn FROM pg_replication_slots WHERE slot_name = '<slot>';`
  and `SELECT pubname FROM pg_publication WHERE pubname = '<publication>';`
  If both exist, restart Conduit. The refusal is kept in memory only, so after the restart the
  connector loads with its saved position and starts normally.

If Conduit restarts before you have checked, the connector resumes from its saved position without
this refusal. Conduit v0.21+ bundles conduit-connector-postgres v0.17.3, which fails with
`postgres.source.slot_wal_gap` when its slot had to be recreated and its saved position can no
longer be reached; older builds do not detect this. Other connectors behave as they did before this
check existed.
