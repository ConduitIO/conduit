# Deleting a connector whose settings no longer resolve

When a connector is deleted, Conduit tells its plugin ("deleted" lifecycle event) so it can clean up
what it created on the other side. The Postgres source drops its replication slot; a slot that is left
behind keeps retaining WAL on the database until someone drops it, and a full disk follows.

The plugin needs the connector's settings for that, resolved the way they were when it ran. If a
setting no longer resolves, the hook cannot run:

- a `${VAR}` that left `env.allow` (or was removed from the environment),
- a path outside `connectors.file.allowed-dirs` / `secrets.allowed-dirs`,
- a target refused by `connectors.egress.allow`.

## What happens

The delete is **refused**, and nothing is removed:

```text
connector.delete_hook_unresolvable
connector <id> cannot be deleted: settings.url no longer resolves (...), so the plugin's deletion hook
cannot run and what it cleans up (for example a Postgres replication slot, which keeps retaining WAL)
would stay behind
config path: settings.url
```

The error names the setting and the fix. The environment values that were substituted are never in the
message (they are replaced by `${NAME}`).

## Fix

1. **Restore the setting** (add the variable back to `env.allow` and restart, fix the path) and delete
   again. The hook runs and cleans up.
2. **Or delete with `force`** if the thing to clean up is already gone or you will do it yourself:
   - HTTP: `DELETE /v1/connectors/{id}?force=true`
   - gRPC: `DeleteConnectorRequest{id, force: true}`

   The connector is deleted and the hook is skipped. The response carries `warnings` that say what was
   skipped, the log has a `WRN` with the setting path, and
   `conduit_connector_force_deletes_total{type="source"}` goes up. Then clean up by hand, for example
   `SELECT pg_drop_replication_slot('<slot>')`; the slot name is in the connector's `logrepl.slotName`
   setting (default `conduit_<publication>`).

There is no CLI or MCP command that deletes a single connector; the API is the only path, so it is the
only place `force` exists. Pipelines removed by deleting their YAML file are not forced: an unresolvable
setting fails the removal with the same code until you fix it.

Alert on `conduit_connector_force_deletes_total` increasing.

## When the hook runs but does not succeed

The settings resolving is only the precondition. The hook itself still has to run and succeed before
anything is removed: the connector's stored record is the only handle Conduit has on the connector, so
if the record were removed first and the hook then failed, the replication slot (or whatever the plugin
created) would be orphaned with no way to retry the cleanup through Conduit.

So nothing is removed until the hook has succeeded, and a hook that did not succeed refuses the delete:

```text
connector.delete_hook_failed
connector <id> was not deleted: the Source plugin's deletion hook did not succeed (...), so what it
cleans up (for example a Postgres replication slot, which keeps retaining WAL) would stay behind
```

The message names the cause. The three it can be:

| Cause | Code | Symptom in the message | Remediation |
| --- | --- | --- | --- |
| The plugin could not be dispensed | `connector.delete_hook_failed` | `plugin not found`, or a failure starting the binary | Put the connector's plugin back where `connectors.path` looks for it (the same plugin and version the connector ran with), then delete again |
| The connector is still running | `connector.running` | `connector is running` | Stop the pipeline (`conduit pipelines stop <id>`), wait for it to stop, then delete again |
| The plugin's hook returned an error | `connector.delete_hook_failed` | the plugin's own message, for example a Postgres error dropping the slot | Fix what the plugin reported (the database is unreachable, the role lacks `REPLICATION`, the slot is still active on another connection), then delete again |

The connector is untouched in all three cases — it is still in `conduit connectors list` and still in the
store, so a restart sees it — and the delete can simply be retried. A plugin's `deleted` handler may
have run in part before failing, so it has to tolerate being called twice; the plugins Conduit ships do.

If the cleanup cannot be made to work and you will do it by hand, delete with `force` (above): force
skips the hook and removes the connector regardless of which of the three causes it was.

One case is the reverse: the hook succeeded and the store write then failed. The connector is still
listed, but the plugin has already released what its saved position depends on. It refuses to start with
`connector.delete_hook_ran` (see [connector-delete-hook-ran.md](connector-delete-hook-ran.md)) until it
is deleted — retry the delete, which is the intended remedy.
