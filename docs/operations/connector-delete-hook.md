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
