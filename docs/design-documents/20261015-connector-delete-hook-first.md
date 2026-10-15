# Connector delete runs the delete hook before it drops the record

## Summary

Deleting a connector removes its record only after the plugin's delete hook (`OnDelete`, the "deleted" lifecycle
event) has succeeded. When the hook fails, the record stays, and the caller gets a coded error,
`connector.delete_hook_failed`, that names the connector, its plugin and the plugin's own error. Retrying the delete
re-runs the hook. An explicit override, `--force` on the new `conduit connectors delete` command and `force: true` on
the `DeleteConnector` API, deletes the record anyway, returns a coded warning (`connector.orphaned_resources`) and
leaves an entry that `conduit doctor` reports until the operator acknowledges it.

Issue: #3058. Decided by DeVaris on 2026-10-15 (option 2 from the issue, plus the override). Roadmap: v0.21.
Risk tier: Tier 1 (connector lifecycle, external state, a new persisted record).

## Problem

`connector.Service.Delete` deletes the stored record and the in-memory entry first, then calls `Instance.Close`,
which dispenses the plugin and calls `OnDelete`. A failing hook is logged and dropped: the connector is already gone,
so nothing can retry it.

A connector's delete hook releases what the connector created outside Conduit. The Postgres source drops its
replication slot and publication there. When that call fails (the database is briefly unreachable, the credentials
were rotated, the plugin binary was removed), the slot survives, Postgres keeps WAL for it, and the source database's
disk fills. The only trace in Conduit is one log line.

Provisioning has a retry for failed deletes (`deleteOldPipelines`, run on every boot), but it only fires when the
delete returns an error, and `Service.Delete` never returns the hook's error.

## Constraints

- Delete hooks are idempotent by contract (the SDK documents `LifecycleOnDeleted` as safe to call again), so calling
  one more than once is acceptable. Losing a call is not.
- A connector that never started (`LastActiveConfig.Settings == nil`) created nothing externally, and its hook is
  skipped today. That stays.
- Invariant 5: store writes for one delete are atomic. A crash at any point must leave either the record or a
  complete deletion, never a half-written orphan entry.
- No change to `conduit-connector-protocol`. The hook's error is a plain string; we cannot get a structured list of
  resources from the plugin without a protocol change, which needs its own versioning discussion.
- Error text must not leak secrets. Plugin errors are free-form and can quote connection strings.
- The operator needs a way out when the hook can never succeed (the plugin is gone, the database was decommissioned).

## Alternatives

### 1. Preflight, then delete as today (rejected)

Check what can be checked before the store delete (the plugin dispenses, the config resolves), return a coded error
while the record still exists, then keep today's order.

It covers predictable failures only. The failure that matters, the hook itself failing against the external system,
cannot be preflighted without running the hook. Postgres being unreachable at delete time would still orphan the
slot silently. It also adds a second code path that has to stay in sync with what `OnDelete` needs.

### 2. Hook first, record second, with an explicit override (chosen)

Run the hook, and remove the record only when it returns nil. A failure returns a coded error and leaves everything
as it was, so any retry (the operator's, or provisioning's next boot) runs the hook again. `--force` covers the case
where the hook can never succeed: the record is deleted, and the possible orphan is recorded where `conduit doctor`
will report it.

### 3. Tombstone the connector and retry the hook in the background (rejected)

Mark the record "deleting", hide it from the API, and retry the hook on a schedule. It never blocks the caller, but it
adds a state to the connector lifecycle, a retry loop with its own backoff and failure reporting, and a record the user
cannot see but that still holds the ID. It is a lot of machinery for a path that runs rarely, and it moves the failure
away from the person who can act on it.

## Decision

### Ordering

`connector.Service.Delete(ctx, id, fetcher)`:

1. Fetch the instance (unchanged).
2. Run the delete hook (`Instance.Close` → `OnDelete`). Skipped, as today, when the connector never started.
3. On hook failure, return `connector.delete_hook_failed`. The store and the in-memory map are untouched.
4. On success, delete the record from the store, then from the in-memory map. If the store delete fails, the record
   stays and the error is returned; a retry re-runs the hook, which is idempotent.

Plugin dispense failures (the plugin is not installed any more) count as hook failures: the hook did not run, so the
resources were not released.

### The error

`connector.delete_hook_failed`, gRPC `FailedPrecondition` (exit code 2). The message names the connector ID, type,
pipeline and plugin, and quotes the plugin's error, which is the only place the plugin can say what it failed to
release (the Postgres connector's error names the slot or publication). Values of settings whose keys look secret
(`password`, `secret`, `token`, `url`, and the rest of the redaction heuristic in `pkg/foundation/log`), and passwords
embedded in URL-shaped settings, are replaced with `***` before the text goes into the message.

Suggestion: fix the cause in the plugin's error and delete again, or delete with `--force` and clean up by hand.

### The override

`DeleteConnectorRequest.force` (API), `conduit connectors delete ID --force` (CLI).

- The hook still runs first. If it succeeds, a forced delete is an ordinary delete and returns no warning.
- If it fails, an orphan entry is written and the record deleted, in one store transaction (the orchestrator already
  wraps a connector delete in one). The response carries a warning with code `connector.orphaned_resources`, the
  same text as the hook error would have had, and a suggestion to remove the resources by hand. The same warning is
  logged.

The orphan entry is a new key, `connector:orphan:<connector-id>`, JSON with a `version` field (1), the connector ID,
type, pipeline ID, plugin, the scrubbed hook error and the time. Nothing else reads this prefix, so it does not change
any existing format; it is versioned so a later change can migrate it.

`conduit doctor` gets a check, `connectors.orphaned_resources`, that lists the entries as warnings (warnings never
fail doctor). Doctor reads the store offline like `store.reachable`; with BadgerDB that means the server must be
stopped, which is how doctor already works.

The finding is cleared with `conduit connectors delete ID --acknowledge-orphan` (`acknowledge_orphan: true`), once the
operator has removed the resources. It only applies when the connector record no longer exists; on an existing
connector it is refused with `common.invalid_argument`. It is a separate flag on purpose: re-running a forced delete
must not quietly erase the only record of a disk-fill hazard.

### Pipeline delete and provisioning

The API pipeline delete does not cascade: it refuses while connectors are attached
(`orchestrator.pipeline_has_connectors_attached`), so the override lives on the connector delete only, and an API
pipeline is deleted by deleting its connectors (with `--force` where needed) and then the pipeline.

The cascade exists in provisioning, when a pipeline disappears from the config files (`deleteOldPipelines` on boot) or
a connector is removed by `pipelines apply`/`deploy`. There:

- A failing hook fails the pipeline delete. That connector's record stays, and the pipeline record stays.
- Provisioning has no `--force`. `deleteOldPipelines` retries on the next boot and logs the coded error each time.
- Each connector is atomic: hook, then record. Connectors already deleted before the failing one stay deleted.
- So the retry can see a partly deleted pipeline: the pipeline still lists connector and processor IDs that are gone.
  Today `Export` fails on a missing child, which would make the retry fail forever. The delete path now exports with
  missing children skipped (only for deletion; the export API keeps failing on a missing child, where it signals real
  damage).
- `pipelines apply` and `deploy` return the coded error for the failing connector, and the plan's own rollback runs as
  it does for any failed action.

A config-provisioned connector cannot be force-deleted through the API (`orchestrator.immutable_provisioned_by_config`,
unchanged). An operator whose removed config pipeline has a hook that can never succeed has no override today; see
_Open questions_.

## Failure modes

| Failure | Result |
| --- | --- |
| Hook fails (system unreachable, credentials rotated) | Record kept, `connector.delete_hook_failed`, retry re-runs the hook. Nothing orphaned silently. |
| Plugin not installed any more | Same code, the dispense error is quoted. Fix by reinstalling, or `--force`. |
| Hook succeeds, store delete fails | Record kept, store error returned, retry re-runs the (idempotent) hook. |
| Crash after the hook, before the store commit | Same as above on the next attempt: the record is still there. |
| Crash during a forced delete | Orphan entry and record deletion are one transaction: either both or neither. |
| Hook keeps failing for a config-provisioned pipeline | Retried and logged every boot; the record stays visible in `pipelines list`. |
| Hook hangs | Bounded by the caller's context, as today. The API request times out and the record stays. |
| Plugin error contains a secret | Values of secret-looking settings and URL passwords are scrubbed. A secret the plugin prints that is not in the settings is not caught; this is the same exposure as a pipeline error message today. |
| `--force` on a connector whose hook works | Ordinary delete, no orphan entry, no warning. |
| Never-started connector | Hook skipped, delete as today. |

Data-path invariants: no record flow, ack or position logic changes. Deleting a connector already requires its
pipeline to be stopped. Invariant 5 holds for the forced delete through the orchestrator's transaction. Nothing here
can drop a record in flight.

## Upgrade and rollback

- Upgrade: no migration. Existing records are read as before. The first forced delete writes the first
  `connector:orphan:` key.
- Behaviour change users will notice: a delete that used to "succeed" while the hook failed now fails. The release note
  says so and names `--force`.
- Rollback to v0.20: the old binary ignores `connector:orphan:` keys (nothing scans that prefix), so they are inert. Its
  deletes go back to dropping hook errors.
- API: `force` and `acknowledge_orphan` are new optional request fields and `warnings` a new response field, so old
  clients and old servers interoperate (an old server ignores `force`, which means a plain delete with the old,
  silent behaviour).

## Observability

- The coded error on the API, the CLI (`--json` prints the code) and in the logs, with connector and
  pipeline IDs as log fields.
- The `connector.orphaned_resources` warning in the forced-delete response and the log.
- The `connectors.orphaned_resources` doctor check, which stays until acknowledged.
- `deleteOldPipelines` logs the coded error on every boot it fails.
- Runbook: `docs/operations/connector-delete-hook-failures.md` (symptom, diagnosis, remediation, including the Postgres
  slot query).

## Open questions

- An override for provisioning. Candidates: allow `--force` on a config-provisioned connector whose pipeline is no
  longer in the config files, or a one-shot config flag. Neither is in this change; until one is, the remedy is to fix
  what the hook needs.
- MCP: there is no connector delete tool. A destructive tool for agents is a separate decision.

## Related

- Issue #3058; surfaced while reviewing the secrets-at-rest design (#3055).
- `pkg/connector/service.go` (`Delete`), `pkg/connector/instance.go` (`Close`), `pkg/connector/source.go` and
  `destination.go` (`OnDelete`), `pkg/provisioning/service.go` (`deleteOldPipelines`, `Delete`).
- `20260707-cli-output-conventions.md` (the `--json` envelope the new command uses), `20260707-cli-doctor.md`.
