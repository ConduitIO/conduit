# One connector force-delete flag: "the delete hook did not succeed", whatever the reason

## Summary

**Status: Proposed (2026-11-18).** Needs DeVaris' sign-off; merging this file is the acceptance.

`DeleteConnectorRequest.force`, shipped in v0.21.0, means "the stored settings no longer resolve, so the
plugin's deletion hook cannot run". It widens to mean "delete even though the deletion hook did not
succeed", covering a hook that ran and failed as well as one that could not run. There is one flag, not
two, because both cases are one operator decision: _I accept that cleanup will not happen; remove it
anyway._

Alongside it, the hook moves ahead of the record removal, a hook failure is refused by default with a new
`connector.delete_hook_failed`, and every forced delete writes a durable orphan entry under its own
`connector:orphan:` store prefix. `DeleteConnectorResponse.warnings = 1` stays, deprecated, and
`repeated Warning coded_warnings = 2` is added; the field is not retyped. Acknowledgement lives in
`ListOrphans` / `AcknowledgeOrphan`, not on the delete request.

## Context

`Service.Delete` removes the connector record and _then_ calls the plugin's hook through
`instance.Close`, and logs the hook's error without acting on it. A hook that runs and fails therefore
reports a successful delete while the external resource it was supposed to release — a Postgres
replication slot, which keeps retaining WAL until someone drops it — stays behind. That is #3058, open
since the secrets-at-rest review.

v0.21 shipped the preflight half of the fix: `Instance.checkDeleteHook` resolves the stored settings
before anything is removed and refuses with `connector.delete_hook_unresolvable` unless `force` is set
(`pkg/connector/delete_force.go`). It covers the failures that can be predicted. It does nothing about a
hook that resolves fine and then fails, and its warnings live in a context value that dies with the
request, so after a forced delete nothing can be asked what was abandoned.

PR #3129 proposed the other half — hook first, orphan ledger — together with two choices this ADR
rejects: `bool acknowledge_orphan = 3` on the delete request, and `warnings = 1` retyped from
`repeated string` to `repeated Warning`. Retyping a shipped field is a breaking change to a released
contract, and the worst kind: both forms are protobuf wire type 2, so an old client hands a serialized
submessage to the application as a string instead of raising a type error.

The ordering constraint that shapes everything else: the hook is an irreversible external call, and the
orchestrator already wraps the record removal and the pipeline update in one database transaction
(`pkg/orchestrator/connectors.go`). The hook cannot join that transaction. The only in-memory marker for
"the hook may have run" (`connector.delete_hook_ran`, #3137) dies with the process.

## Decision

- **One flag, widened.** `force` permits a delete when the hook did not succeed, for either cause. Without
  it, an unresolvable setting is refused with `connector.delete_hook_unresolvable` and a failing hook with
  the new `connector.delete_hook_failed`. A second flag was rejected: it makes the operator diagnose which
  failure mode they are in before they can choose, and the API response often cannot tell them apart —
  an unresolvable setting and an undispensable plugin both read as "the plugin could not be reached".
- **Hook before removal.** The hook runs before the connector record is removed, so a failure can be
  refused while the record still exists and the existing provisioning retry applies. Hooks are idempotent
  by contract, so redelivery on a retry is acceptable.
- **The reorder and the widening ship in the same release.** Refusing on hook failure without widening
  `force` would leave a connector whose hook cannot succeed permanently undeletable. They are not
  separable.
- **Durable orphan entry, atomic with the removal.** A forced delete commits a `pending` entry _before_
  the hook is called, and the finalize — rewrite the entry as `abandoned`, remove the record, update the
  pipeline's connector list — is one transaction (invariant 5). A `pending` entry found at startup means
  the process died in the hook window: the connector is marked unstartable and the entry is surfaced
  (invariant 7). Entries live under `connector:orphan:<ulid>`, carry their own `version` field, and are
  not cascaded away when the pipeline is deleted.
- **Additive response.** `warnings = 1` keeps its type and gains `[deprecated = true]`;
  `repeated Warning coded_warnings = 2` carries the code, message, config path, suggestion and orphan ID.
  Both are populated until the string field is removed, no earlier than v0.24.
- **Acknowledgement is its own surface.** `ListOrphans` and `AcknowledgeOrphan`, with a
  `conduit connector orphans list|ack` CLI and `--json`. `DeleteConnectorRequest` stays at two fields.
  Acknowledging is the only way an entry leaves the store; there is no TTL.

The alternatives — a second flag, a v2 message or RPC, keeping the two concepts separate, retrying the
hook in the background — are set out with their costs in
[`docs/design-documents/20261118-connector-force-delete-and-orphan-ledger.md`](../design-documents/20261118-connector-force-delete-and-orphan-ledger.md).

## Consequences

- **Widening is a behavior change, not a pure addition.** A v0.21 client sending `force=true` gets a
  delete today only when the settings fail to resolve; afterwards it also gets one when the hook ran and
  failed. The sharper break is in the other direction: a caller sending `force=false` and relying on a
  delete succeeding despite a failing hook now gets `connector.delete_hook_failed`. Both need a release
  note; the second can break automation.
- `force` is no longer a complete account of what a forced delete did. The response says what was skipped
  and the ledger says what is still out there, and an operator who wants the second has to look.
- The ledger grows without bound if nobody acknowledges. That is chosen: an entry is the only durable
  record that something was abandoned, and expiring it would restore the silent loss this fixes. Pressure
  comes from `conduit_connector_orphans{state}`, a startup warning and a runbook, not from eviction. A
  forced delete is never refused because the ledger is large.
- A new persisted store key is a serialized-format change, so it is gated on a versioned entry format and
  an upgrade test before the feature merges — `tests/upgrade/storecompat` fixtures carrying orphan entries,
  a downgrade case proving an older store ignores the prefix, and a prefix-isolation test. Note the limit:
  that module pins old badger and SQLite libraries against current repository code, not a previous Conduit
  binary, so it proves prefix enumeration ignores unknown keys rather than proving v0.21 itself is happy.
  There is no release-to-release binary harness in the tree.
- A downgrade to v0.21 across the hook window can start a connector whose hook already ran, because v0.21
  does no reconciliation and the `delete_hook_ran` marker is in-memory. Narrow, real, and in the release
  note.
- `conduit_connector_force_deletes_total{type}` is left alone — adding a `cause` label would change the
  series identity and break existing dashboards. `conduit_connector_orphans_total{type,cause}` and
  `conduit_connector_orphans{state}` are new.
- Three new error codes (`connector.delete_hook_failed`, `connector.orphan_not_found`,
  `connector.orphan_already_acknowledged`) plus `connector.orphan_version_unsupported`.
- `acknowledged_by` is caller-supplied. The API is unauthenticated by default, so the ledger is an
  inventory, not an audit trail, and the docs say so.
- Tier 1 (connector lifecycle and external state). Human sign-off and a failure-mode analysis are required
  on the implementation PR.

## Related

- [`docs/design-documents/20261118-connector-force-delete-and-orphan-ledger.md`](../design-documents/20261118-connector-force-delete-and-orphan-ledger.md)
- [`20261021-deny-by-default-settings-and-outbound-access`](20261021-deny-by-default-settings-and-outbound-access.md)
  — why a stored setting stops resolving
- [`docs/design-documents/20261016-secrets-at-rest`](../design-documents/20261016-secrets-at-rest.md)
- #3058, #3129, #3137
- `pkg/connector/delete_force.go`, `pkg/connector/service.go`, `pkg/orchestrator/connectors.go`,
  `docs/operations/connector-delete-hook.md`
