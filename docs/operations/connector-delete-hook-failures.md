# Connector delete fails because its delete hook failed

When a connector is deleted, Conduit first calls the plugin's delete hook, which releases what the connector created
outside Conduit: the Postgres source drops its replication slot and publication there, for example. The connector is
removed only after the hook succeeds (#3058). Before v0.21 the connector was removed first and a failing hook was only
logged, which could leave a replication slot behind that keeps WAL until the source database's disk fills.

## Symptom

`conduit connectors delete <id>` or `DELETE /v1/connectors/{id}` fails with `connector.delete_hook_failed`
(FailedPrecondition, exit code 2):

```text
could not delete connector "orders-src" (source, plugin "builtin:postgres", pipeline "orders"): its delete hook
failed, so resources it created outside Conduit may not have been released: failed to drop replication slot
"conduit_orders": dial ***: connection refused
```

The connector is still there and still attached to its pipeline. Nothing else changed.

For a pipeline removed from the pipeline config files, the same error appears in the startup log, as part of
`failed to delete a pipeline provisioned by a config file`. The pipeline stays in `conduit pipelines list` and Conduit
tries again at every start.

Values of settings that look secret (passwords, tokens, URLs) are shown as `***`.

## Diagnosis

The text after the colon is the plugin's own error. It names what the plugin could not do and usually what it was
trying to release. Common causes:

- The external system is unreachable (network, the database is down or was moved).
- The credentials in the connector's settings were rotated.
- The plugin is no longer installed (`connector.plugin_not_found` or a dispense error in the message).

For a Postgres source, check what is still there:

```sql
SELECT slot_name, active, pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained
FROM pg_replication_slots;
SELECT pubname FROM pg_publication;
```

## Remediation

Pick one:

- Fix the cause and delete again. The hook is safe to run more than once. For a config-provisioned pipeline, restart
  Conduit after fixing it.
- If the hook can never succeed (the database was decommissioned, the plugin is gone for good), delete with force:

  ```bash
  conduit connectors delete orders-src --force
  ```

  The connector is deleted and the command prints a `connector.orphaned_resources` warning that repeats the plugin's
  error. Remove the resources by hand, for Postgres:

  ```sql
  SELECT pg_drop_replication_slot('conduit_orders');
  DROP PUBLICATION conduit_orders;
  ```

  `conduit doctor` reports every forced delete under `connectors.orphaned_resources` until you acknowledge it. Doctor
  reads the store directly, so with BadgerDB or SQLite stop the server first. Once the resources are gone:

  ```bash
  conduit connectors delete orders-src --acknowledge-orphan
  ```

`--force` is not available for connectors created from a pipeline config file. Fix the cause and restart Conduit.

## Related

- Issue #3058, design note `docs/design-documents/20261015-connector-delete-hook-first.md`
- Error codes `connector.delete_hook_failed`, `connector.orphaned_resources` and `connector.orphan_not_found` in
  `llms-full.txt`
