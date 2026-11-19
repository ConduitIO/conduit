# Connector force delete and the orphan ledger

## Summary

Run the plugin's deletion hook before the connector record is removed, refuse the delete when the hook
fails, and widen the existing `force` flag to cover that refusal as well as the one it covers today. Every
forced delete writes a durable orphan entry under its own store prefix, so "what did we abandon, and what
is still out there?" has an answer after the request is over. The entry is inspectable and acknowledgeable
through two new RPCs and a `conduit connector orphans` CLI surface, not through fields on
`DeleteConnectorRequest`.

The delete response keeps `repeated string warnings = 1`, marked deprecated, and gains
`repeated Warning coded_warnings = 2`. Retyping the shipped field is not an option: both forms are
protobuf wire type 2, so an old client misdecodes a submessage as a string instead of failing.

This closes #3058. It supersedes the approach in #3129 (hook-first plus `acknowledge_orphan = 3` plus a
retyped `warnings = 1`), which is being closed as stale.

## Context

### What ships today

`DeleteConnectorRequest` has carried `bool force = 2` since v0.21.0, unchanged in v0.21.2
(`proto/api/v1/api.proto`, lines 1053-1064):

```protobuf
message DeleteConnectorRequest {
  string id = 1;
  // Delete although the connector's stored settings no longer resolve, so the
  // plugin's deletion hook (for example dropping a Postgres replication slot)
  // cannot run and is skipped. Without it such a delete is refused with
  // connector.delete_hook_unresolvable.
  bool force = 2;
}
message DeleteConnectorResponse {
  // What a forced delete skipped; clean up by hand.
  repeated string warnings = 1;
}
```

`force` means exactly one thing: the connector's `LastActiveConfig.Settings` no longer resolve, so the
plugin cannot be dispensed and configured, so the hook cannot run at all. `Instance.checkDeleteHook`
(`pkg/connector/delete_force.go`, line 80) resolves the stored settings as a preflight. Without `force`,
a resolution failure is refused with `CodeConnectorDeleteHookUnresolvable`, naming the failing setting and
the fix. With `force` it logs a warning, calls `addDeleteWarning`, increments
`conduit_connector_force_deletes_total` and lets the delete proceed.

### The two gaps

**The hook is not actually gated on anything.** `Service.Delete` (`pkg/connector/service.go`, line 217)
runs the preflight, then `s.store.Delete`, then `instance.Close`, and `Close` is where `conn.OnDelete`
is called. The real hook therefore runs _after_ the record is already removed, and its error is only
logged:

```go
err = instance.Close(ctx, dispenserFetcher)
if err != nil {
    // connector is already deleted, only log the error coming from the
    // cleanup function, use the instance logger to attach connector ID
    instance.logger.Err(ctx, err).Msg("could not close connector instance")
}
```

So a hook that _runs and fails_ — Postgres unreachable, `pg_drop_replication_slot` refused, the plugin
binary no longer dispensable — deletes the connector anyway, returns success, puts no warning in the
response, and increments no counter. That is #3058, still open: the replication slot stays, Postgres keeps
retaining WAL for it, and the only trace is one log line. The preflight shipped in v0.21 is #3058's
suggested direction 1 (predict what can fail); direction 2 (do not remove the record until the hook
succeeds) was never done.

**Warnings do not outlive the request.** `DeleteWarnings` is a `sync.Mutex` plus a `[]string` held in a
context value (`WithDeleteWarnings`). When the request returns, the collector is garbage. The connector
record is gone, the external resource survives, and nothing in Conduit can be asked later what was
abandoned. An operator who missed the response body has a log line, and a log line is not an inventory.

### Where the transaction boundary already is

`ConnectorOrchestrator.Delete` (`pkg/orchestrator/connectors.go`, line 127) opens a database transaction,
puts it on the context, and only then calls `connectors.Delete`; `pipelines.RemoveConnector` follows and
`txn.Commit()` closes it. So the record removal and the pipeline's connector-list update are already one
atomic commit, and anything else written through the same context joins that commit. The hook is the one
step in the sequence that is outside it, because it is an external side effect that cannot be rolled back.

That is why #3137 exists: if the commit fails after the hook ran, the rollback reinstates the same
instance in memory and marks it so its next start is refused with `connector.delete_hook_ran`. The mark
lives on the in-memory `Instance`. It does not survive the process.

### What #3129 proposed

A different `force`: the hook runs first, and if it fails the delete proceeds _anyway_ and writes a
durable `Orphan` entry — plus `bool acknowledge_orphan = 3` on the request, and `warnings = 1` retyped
from `repeated string` to `repeated Warning`. The hook-first reordering is right and is kept here. The
other two parts are not: a retyped shipped field is a breaking change to a released contract, and
acknowledgement does not belong on the delete request.

## Constraints

- **`force` is a released API field.** Its meaning can widen but its wire form, tag and type cannot move.
  Two releases carry both response fields before the old one goes.
- **The hook is an external, irreversible side effect.** It cannot be inside the database transaction, so
  the design has to be correct across a crash in the window around it (invariant 7).
- **The orphan entry is a new persisted store key.** Serialized formats need a versioned migration path
  and an upgrade test (project rule); the entry has to survive the deletion of the record it describes,
  which is the whole point, so it cannot hang off the connector key.
- **Deletion hooks are idempotent by contract** (#3058). Redelivering one on a retry is acceptable, which
  is what makes refuse-and-retry viable at all.
- **The API is unauthenticated by default** (`20261021-deny-by-default-settings-and-outbound-access`).
  Anything the ledger records about _who_ acted is caller-supplied and advisory, not an identity claim.
- **No new concepts on the hot path.** None of this touches record flow; it is connector lifecycle.

## Alternatives considered

### A. One widened `force`, acknowledgement in its own RPCs (chosen)

`force` means "delete even though the delete hook did not succeed", for both causes. Acknowledgement moves
to `ListOrphans` / `AcknowledgeOrphan`, so `DeleteConnectorRequest` stays at two fields and the ledger is
inspectable without a delete.

### B. A second flag, `force_hook_failure`

Keep `force` as "settings do not resolve" and add a second bool for "the hook ran and failed".

Rejected. The operator has to work out which failure mode they are in before they can choose a flag, and
from the API response they frequently cannot: an unresolvable setting and a plugin that will not dispense
both surface as "the plugin could not be reached". Two bools also describe four states, and
`force=false, force_hook_failure=true` is meaningless — it would have to be refused, which is a validation
rule existing only to paper over the modelling. Both rows are one decision: _I accept that cleanup will
not happen; remove it anyway._ Worth revisiting only if operators actually ask to permit one cause and not
the other.

### C. A new message version or a `DeleteConnectorV2` RPC

Rejected. It duplicates the delete surface across gRPC, HTTP, OpenAPI and the UI in order to change the
predicate behind one boolean, and the v1 RPC still needs defined behavior afterwards, so both semantics
ship forever. A v2 earns its keep when the _shape_ of a call changes. Here only the condition widens.

### D. Do nothing: keep two separate concepts

Leave `force` as it is and leave a failing hook as it is.

Rejected. That is #3058 unfixed: a failing hook silently abandons a replication slot and reports success.
Documenting it and deferring is not available for a resource-abandonment gap.

### E. Retry the hook in the background instead of refusing

Rejected for now. After the record is gone there is no owner for the retry, no bound on it, and no place to
report it. The ledger plus an explicit operator action is the honest version of the same idea. A
`RetryOrphanCleanup` RPC is a plausible later addition _because_ the ledger exists; it is not in scope and
nothing here is built speculatively for it.

## Decision

### Semantics

| Cause | Without `force` | With `force` |
| --- | --- | --- |
| Stored settings no longer resolve, so the hook cannot run | refuse, `connector.delete_hook_unresolvable` | delete, write orphan entry, warn, count |
| The hook ran and returned an error | refuse, `connector.delete_hook_failed` (new) | delete, write orphan entry, warn, count |

The second row's "without `force`" column is a behavior change in its own right: that delete succeeds
today. The two changes cannot be split across releases. Making a hook failure refuse, without widening
`force` in the same release, would leave a connector whose hook cannot succeed permanently undeletable —
which is precisely the trap the widening exists to prevent. They ship together or not at all.

### Ordering, and what is atomic

1. **Preflight** (unchanged). Resolve the stored settings. Unresolvable and not forced: refuse with
   `connector.delete_hook_unresolvable`, nothing written. Unresolvable and forced: skip to step 2 with the
   cause recorded as `hook_unresolvable`; no hook call is possible.
2. **Write-ahead the intent.** Commit an orphan entry in state `pending`, in its own transaction, _before_
   the hook is called. This is the only committed write that precedes an irreversible external call, and
   it is what makes the hook window recoverable.
3. **Run the hook** (`conn.OnDelete`), outside any transaction. Success: continue to step 4 with the
   cause cleared. Failure and not forced: refuse with `connector.delete_hook_failed`, naming the plugin
   and the redacted hook error; the record is untouched and the existing provisioning retry
   (`deleteOldPipelines`) applies on the next run. Failure and forced: continue with cause `hook_failed`.
4. **Finalize in one transaction**, the one the orchestrator already opens: remove the connector record,
   update the pipeline's connector list, and either delete the `pending` entry (the hook succeeded) or
   rewrite it as `abandoned` with its cause and cleanup hint. One commit, so the entry and the removal are
   never out of step (invariant 5).

The ledger write is therefore atomic with the record removal by construction, not by a reconciliation
loop. The crash window that remains is between the step-2 commit and the step-4 commit, and step 2 is what
leaves a durable description of it.

### Startup reconciliation

A `pending` entry means the process died inside the hook window. On load:

- The connector record still exists: the hook may or may not have run, and it may have released what the
  stored position points into. Mark the instance unstartable with the existing
  `connector.delete_hook_ran`, keep the entry visible, and log a warning naming both. The next delete
  re-runs the idempotent hook, which is the recovery path.
- The connector record is gone: step 4 should have finalized the entry, so this is a bug. Promote it to
  `abandoned` with cause `unknown` rather than deleting it, because an entry nobody can explain is still
  better than a resource nobody knows about.

Reconciliation runs before the engine starts pipelines, and only rewrites orphan keys.

### The entry and its store key

Prefix `connector:orphan:`, key `connector:orphan:<ulid>`, JSON-encoded like the other stores
(`pkg/connector/store.go`). The ULID gives time-ordered listing and means a connector ID that is deleted,
recreated and deleted again gets distinct entries instead of shadowing its own history.

The prefix choice is load-bearing for downgrades. The stores enumerate with
`db.GetKeys(ctx, "connector:instance:")`, `"pipeline:instance:"` and `"processor:instance:"`, so a key
under `connector:orphan:` is invisible to every existing reader. A key under `connector:instance:orphan:`
would be enumerated as a connector instance and would break the load. That is a test, not a comment.

Fields: `version` (int, explicit, refused on read with a coded error if it is newer than the binary
understands rather than decoded on a guess), the ULID, connector ID, connector type, plugin name and
version, pipeline ID, `deleted_at`, state (`pending` / `abandoned`), cause (`hook_unresolvable` /
`hook_failed` / `unknown`), the redacted failure reason, the failing config path where there is one, a
best-effort cleanup hint, and `acknowledged_at` / `acknowledged_by` once acknowledged.

Every string field is capped (the reason at 2 KiB, paths and names at 512 bytes) so one pathological error
message cannot make the transaction that carries it fail. No settings values, no record data: the reason
goes through the same `redactError` path the refusal message already uses.

The cleanup hint is best effort and the design does not pretend otherwise. For `hook_failed` the settings
resolved, so the hint can name the concrete resource — for the Postgres source, `logrepl.slotName`
(default `conduit_<publication>`). For `hook_unresolvable` the settings did _not_ resolve, which is the
whole problem, so the hint can only name the plugin and the setting that failed.

Deleting a pipeline does not delete its orphan entries. Entries outliving their records is the feature.

### Response shape

```protobuf
message DeleteConnectorResponse {
  // Deprecated: use coded_warnings. Removed no earlier than v0.24.
  repeated string warnings = 1 [deprecated = true];
  repeated Warning coded_warnings = 2;
}

message Warning {
  string code = 1;
  string message = 2;
  string config_path = 3;
  string suggestion = 4;
  string orphan_id = 5;
}
```

Both fields are populated for two minor versions. Tag 2 is free on that message, and adding
`[deprecated = true]` to a shipped field changes generated-code annotations only — it is not a wire
change, so no client breaks on it.

Retyping `warnings = 1` instead, as #3129 proposed, is the failure worth spelling out. `repeated string`
and `repeated Warning` are both protobuf wire type 2 (length-delimited), so an old client decoding a new
server's response does not get a type error. It gets the serialized `Warning` submessage handed to it as
the contents of a string: binary garbage in an operator's terminal, or a UTF-8 validation error in a
strict runtime, depending on the language. A silent misdecode is worse than a clean break, and a new tag
avoids both.

### Inspection and acknowledgement

```protobuf
rpc ListOrphans(ListOrphansRequest) returns (ListOrphansResponse);
rpc AcknowledgeOrphan(AcknowledgeOrphanRequest) returns (AcknowledgeOrphanResponse);
```

`ListOrphans` filters by pipeline ID, connector ID and state. `AcknowledgeOrphan` takes the ULID and an
optional free-text note; it is the only way an entry leaves the store. Acknowledging an already
acknowledged entry is refused with `connector.orphan_already_acknowledged` rather than being a silent
no-op, so a script cannot mistake "already handled by someone else" for "handled by me".
`acknowledged_by` is caller-supplied and advisory: the API has no authentication by default, so presenting
it as an audit trail would be a lie.

CLI, under `cmd/conduit/root/connectors`, with `--json` from the first commit:

```text
conduit connector orphans list [--pipeline <id>] [--state pending|abandoned] [--json]
conduit connector orphans ack <orphan-id> [--note <text>]
```

There is still no CLI or MCP command that deletes a single connector, so `force` stays API-only
(`docs/operations/connector-delete-hook.md`). The orphans surface is read plus acknowledge, which does not
change that.

### Error codes and metrics

New codes, registered in `pkg/connector/codes.go` next to the existing two, each with a config path where
one applies and a suggestion naming the next action:

- `connector.delete_hook_failed` (`FailedPrecondition`) — the hook ran and failed, `force` not set.
- `connector.orphan_not_found` (`NotFound`).
- `connector.orphan_already_acknowledged` (`FailedPrecondition`).
- `connector.orphan_version_unsupported` (`FailedPrecondition`) — an entry written by a newer Conduit.

`conduit_connector_force_deletes_total{type}` is left exactly as it is. Adding a `cause` label would
change the series identity and break existing dashboards and recording rules for a cosmetic gain. Two new
series instead:

- `conduit_connector_orphans_total{type,cause}` — counter, incremented when an entry is finalized as
  `abandoned`.
- `conduit_connector_orphans{state}` — gauge, the current ledger size by state, set at startup and
  maintained on write.

## Failure modes

- **The ledger write fails after the record was removed.** Not reachable as designed: the finalize write
  and the record removal are in the same transaction, so either both are durable at commit or neither is.
  A commit that cannot succeed at all (disk full) fails the delete and is reported; the hook may already
  have run, so the instance is marked `connector.delete_hook_ran` by the existing #3137 rollback and its
  next start is refused. Bounded entry fields keep a long error message from being the reason the commit
  fails.
- **SIGKILL in the hook window.** Before the step-2 commit, nothing happened and the connector is intact.
  Between step 2 and step 4 there is a durable `pending` entry naming the connector and what might be
  orphaned; startup reconciliation marks the instance unstartable and surfaces the entry, and the next
  delete re-runs the idempotent hook. After the step-4 commit, the removal and the entry are both durable.
  Without step 2 this window is unrecoverable, because the only marker today
  (`connector.delete_hook_ran`) is in-memory and dies with the process — which would let the restarted
  binary start a connector whose replication slot was already dropped.
- **SIGKILL during the finalize commit.** The storage engine's transaction either applies or does not; a
  torn finalize is not representable. If it did not apply, the state is the `pending` case above.
- **The ledger grows unbounded and nobody acknowledges.** Accepted, deliberately. There is no TTL and no
  eviction: an entry is the only durable record that something was abandoned, and expiring it would
  reintroduce exactly the silent loss this design exists to remove. Growth is bounded in practice by the
  number of forced deletes, and entries are a few hundred bytes. What the design adds instead is pressure:
  the `conduit_connector_orphans{state}` gauge, a startup warning naming the unacknowledged count, and a
  runbook. A forced delete is _not_ refused when the ledger is large — making the escape hatch
  unavailable when an operator most needs it is a worse failure than a big ledger.
- **Downgrade to v0.21 against a store holding orphan keys.** The v0.21 stores enumerate by their own
  prefixes, so `connector:orphan:` keys are never read: pipelines and connectors load unchanged and the
  entries sit inert until a newer binary runs again. The operator loses the inspection surface, which is a
  degraded view rather than a load failure. Two honest caveats. First, a v0.21 binary does no
  reconciliation, so a downgrade performed while a `pending` entry exists can start a connector whose hook
  already ran; the window is narrow but real and belongs in the release note. Second, a v0.21 binary
  deletes a connector whose hook fails without writing anything, so entries stop appearing while it runs.
- **Ordering between the hook, the removal and the ledger.** Fixed by the four steps above: intent
  committed before the hook, hook outside any transaction, removal and finalize in one transaction. The
  hook can never run after the record is gone, which is the inversion #3058 reported.
- **Concurrent deletes of the same connector.** Serialized by the orchestrator's mutation mark and the
  database transaction, as today. The loser sees not-found or a transaction conflict; it cannot produce a
  second entry for the same removal.
- **A connector ID deleted, recreated and deleted again.** Distinct ULIDs, so the second entry does not
  overwrite or resurrect the first. Listings carry the connector ID as a field, not as the key.
- **Force used when the settings do not resolve.** No hook call happens, so nothing was attempted and
  nothing was half-done. The entry says so (`cause: hook_unresolvable`) and the cleanup hint is limited to
  the plugin and the failing setting path. This is the v0.21 behavior, now with a durable record.

## Upgrade and rollback

- **Upgrade.** No migration is needed: the ledger starts empty and the first entry appears on the first
  forced delete. Existing connector, pipeline and processor records are untouched, and no existing key's
  encoding changes.
- **Behavior change on upgrade.** A v0.21 client that sends `force=true` gets a delete today only when the
  settings fail to resolve. Afterwards it also gets one when the hook ran and failed. A client that sends
  `force=false` and relies on a delete succeeding despite a failing hook now gets
  `connector.delete_hook_failed` instead of success. Both go in the release note, with the second called
  out as the one that can break automation.
- **Rollback.** Covered under failure modes: a previous binary ignores the new prefix and keeps working,
  with reconciliation and ledger writes absent while it runs. Rolling forward again picks the ledger back
  up, including any `pending` entry the older binary left alone.

## Versioning and deprecation plan

- `warnings = 1` is deprecated on arrival and populated identically to `coded_warnings`. It is removed no
  earlier than v0.24, which satisfies announce, warn, remove across a minimum of two minor versions.
- The entry's `version` field starts at 1. A reader that meets a higher version refuses the entry with
  `connector.orphan_version_unsupported` and leaves it in place; it does not guess, and it does not drop
  it.
- `force`'s widened meaning is documented in the proto comment, the OpenAPI description,
  `docs/operations/connector-delete-hook.md` and the release notes. The flag itself does not change
  version.

## Observability

- Every forced delete returns a coded warning carrying the orphan ULID, so the response is enough to find
  the entry later.
- `conduit_connector_orphans{state}` is the alertable signal: any non-zero `pending` value outside a
  delete is a crash in the hook window, and a growing `abandoned` value is uncleaned external state.
  `conduit_connector_force_deletes_total` keeps its existing alert.
- The existing WRN on a forced delete gains the ULID. A refused delete logs nothing extra — the coded
  error is the signal.
- Runbooks: `docs/operations/connector-delete-hook.md` gains the hook-failure cause and the orphans
  commands; a new `docs/operations/connector-orphans.md` covers symptom (gauge non-zero), diagnosis
  (`conduit connector orphans list --json`) and remediation (the per-plugin cleanup, then `ack`).
- `docs/metrics.md` and `llms.txt` move in the same PR as the code.

## Test plan

The first item is a gate on implementation, not a follow-up. `git grep -l orphan tests/upgrade/` returns
nothing today.

- **Serialized-format gate (blocking).** The orphan entry is a new persisted key, so the project rule
  applies: a versioned migration path and an upgrade test, before the feature merges.
  - A prefix-isolation test asserting that `connector:orphan:` keys are not returned by any existing
    `GetKeys` prefix and that the three instance stores load unchanged with entries present.
  - `tests/upgrade/storecompat` fixtures regenerated to contain orphan entries, so the forward direction
    (old libraries wrote, current build reads) covers them.
  - A new case in `tests/upgrade/storecompat/downgrade` proving a store holding orphan keys still opens
    and decodes. Note what that module actually pins: old badger and SQLite library versions against
    current repository code, not a previous Conduit binary. It therefore proves that prefix enumeration
    ignores unknown keys under the old storage libraries. There is no release-to-release binary harness in
    the tree (`tests/upgrade/resume` is arch v1 against arch v2), so the remaining risk — that a real
    v0.21 binary behaves differently — is covered by a manual check recorded in the PR, or by building
    that harness. The gap is stated rather than assumed away.
  - A version-field test: an entry at version 2 is refused with `connector.orphan_version_unsupported`
    and left in the store.
- **Chaos (`tests/chaos`).** SIGKILL at four points: before the intent commit, after the intent commit and
  before the hook, after the hook and before the finalize commit, and during the finalize commit. Each
  must come back to a state reconciliation can explain, and the two hook-window kills must leave the
  connector unstartable.
- **Regression test for #3058.** A plugin whose `OnDelete` returns an error. Without the reorder,
  `Service.Delete` returns nil and the record is gone; with it, the delete is refused and the record
  survives. The test fails on current `main`.
- **Unit and integration.** Force with unresolvable settings and force with a failing hook, each producing
  one entry with the right cause; both response fields populated identically; refusal codes and their
  config paths; acknowledge idempotency and the already-acknowledged refusal; pipeline deletion not
  cascading over entries; distinct entries for a recreated connector ID; capped fields truncated rather
  than rejected.
- **CLI.** `--json` schema covered by a golden, and the error codes surfaced rather than flattened to
  exit 1.

## Open questions

1. Which release. The reorder and the widening cannot be split, and v0.22 freezes Tier 1 merges on
   2026-11-21, so this is v0.23 at the earliest. DeVaris to confirm v0.23 against v0.24.
2. Do `ListOrphans` and `AcknowledgeOrphan` belong on the existing connector service or on their own? The
   entries outlive connectors, which argues for their own; discoverability argues for the connector
   service.
3. Should the MCP server expose the ledger read-only in the same release? It mirrors CLI verbs, and there
   is no connector-delete tool for it to mirror, so this is additive and can wait.

## Related

- #3058 (the issue), #3129 (the superseded hook-first PR), #3137 (the delete rollback and
  `connector.delete_hook_ran`)
- [`20261118-unified-connector-force-delete-semantics`](../architecture-decision-records/20261118-unified-connector-force-delete-semantics.md)
- [`20261021-deny-by-default-settings-and-outbound-access`](../architecture-decision-records/20261021-deny-by-default-settings-and-outbound-access.md)
  — why settings stop resolving in the first place
- [`20261016-secrets-at-rest`](20261016-secrets-at-rest.md) — the `${VAR}` resolution this preflight checks
- `docs/operations/connector-delete-hook.md`, `docs/operations/connector-delete-hook-ran.md`
- `pkg/connector/delete_force.go`, `pkg/connector/service.go` (`Service.Delete`, `Restore`),
  `pkg/orchestrator/connectors.go` (`ConnectorOrchestrator.Delete`)
