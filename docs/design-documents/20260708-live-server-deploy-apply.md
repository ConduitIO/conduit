# Live-server deploy/apply — apply to a running pipeline (issue #2588)

## Summary

Give `deploy`/`apply` (CLI and MCP) a path through a **running** Conduit server so
they can apply changes to a **live** pipeline — the server gracefully
stops-drains-restarts it via its lifecycle service — instead of the Wave-2
standalone path that refuses any running pipeline and only works against a
stopped BadgerDB store. This lifts the Badger-only default-deny for the
server-reachable case and completes the MCP north-star (agent → _running_
pipeline in one session) and the CLI apply-to-running story, both from the **same**
new API surface. **Tier 1** — it mutates live pipelines (stop-drain-restart on
the data path) and adds two API RPCs.

## Context — what exists vs what's missing

| Piece | State |
| --- | --- |
| `provisioning.Service.Plan` / `ApplyPlan` | exist (Wave 2, `plan.go`); `ApplyPlan` **refuses** a running pipeline (`isRunningStatus`, `plan.go:26`) |
| `provisioning.Service` holds `lifecycleService` | yes (`service.go:40,62`), and already calls `Start` (`service.go:272`) |
| `lifecycleService` = `Start(ctx,id)` / `Stop(ctx,id,force)` / `Init` | yes (`runtime.go:113`) — `Stop(...,false)` is the graceful (SIGTERM-style) drain |
| `runtime.ProvisionService` (the live server's, with lifecycle) | yes (`runtime.go:90,282`) |
| API `PipelineService` RPCs | CreatePipeline/UpdatePipeline/Delete/Start/Stop (`api.proto:302`) — **no Plan/Apply** |
| CLI `deploy`/`apply`, MCP `deploy`/`apply` | standalone-only (`NewLocalService`, Badger, stopped) |

**Missing:** (1) API `PlanPipeline` + `ApplyPipeline` RPCs; (2) an apply path that
**stop-drains-restarts** a running pipeline instead of refusing; (3) CLI/MCP
client logic to prefer the live server when reachable.

## Decision

### 1. Two additive API RPCs (no breaking change)

`PipelineService.PlanPipeline(config) → Diff{changes, hash}` (read-only) and
`ApplyPipeline(config, hash) → Diff+result` (mutating). Additive to `api.proto` —
existing RPCs untouched, so N/N+1 compatible; the proto change is flagged +
regenerated (`make proto-generate`). Server handlers call the live
`runtime.ProvisionService` (which has the lifecycle) — the same `Plan`/apply
engine the CLI standalone path uses, so the diff/hash/rollback semantics are
identical; only the _reachability_ (live server vs local store) differs.

### 2. Apply-to-running = graceful stop-drain-restart (the Tier-1 core)

Add `ProvisionService.ApplyPlanLive(ctx, desired, hash)` (or a mode on
`ApplyPlan`) used only by the server handler, which HAS a running lifecycle:

- Recompute + verify the hash (stale → `plan_stale`, as now).
- If the target pipeline is **running** and the diff's effect is `restart`:
  `lifecycleService.Stop(ctx, id, false)` — **graceful drain** (invariant 7: in-flight
  records finish + checkpoint before teardown) → wait for stopped → `importPipeline`
  (apply the actions, inheriting the reverse-order rollback) → `lifecycleService.Start(ctx, id)`
  (resumes from the checkpointed position — invariant 2, at-least-once).
- If the diff is `in_place`-only (no restart-class change): apply without a
  stop (future optimization; Wave-of-#2588 may still stop-restart for safety and
  defer true in-place to §4 hot-reload — decide in review).
- On any failure after Stop, the pipeline is left **stopped** with the rollback
  applied and a clear error — never half-mutated-and-running. A crash between
  Stop and Start is recoverable: the pipeline is stopped + the store is
  consistent (importPipeline is transactional-per-action), and a restart
  re-resumes from the last checkpoint.

The standalone `ApplyPlan` keeps refusing running pipelines (it has no lifecycle
to drive) — unchanged.

### 3. CLI + MCP: prefer the live server, fall back to standalone

`deploy`/`apply` (and the MCP tools) try the API first (a server is reachable) →
use `PlanPipeline`/`ApplyPipeline` (works against running pipelines, any store).
If no server is reachable → the Wave-2 standalone path (Badger, stopped). The
verb's behavior is identical; only the transport differs. MCP inherits this by
wrapping the same client logic.

### 4. Enforced human-sign-off on data-path apply-to-running

Now that apply _can_ mutate a running pipeline, the MCP review's open item
becomes real: an `ApplyPipeline` whose diff touches ack/position/checkpoint-adjacent
config on a **running** pipeline must not proceed autonomously. Enforce at the
API layer: such an apply requires an explicit operator-authorization token/flag
(server-side `--allow-live-data-path-apply` or a per-call signed authorization),
NOT an agent-passable arg. Absent it → refuse with a coded error directing to the
human path. This is the enforced gate the MCP design deferred to #2588.

## Failure modes (Tier 1 — enumerate)

- **Drain incompleteness:** `Stop(...,false)` must fully drain + checkpoint before
  importPipeline; if Stop returns before drain, in-flight records could be lost
  (invariant 3). Verify Stop's drain semantics; test with in-flight load.
- **Crash between Stop and Start:** pipeline left stopped, store consistent,
  re-resumes from checkpoint on next start — kill-mid-apply recovery test.
- **Rollback of a running-pipeline apply:** if importPipeline fails mid-sequence,
  reverse rollback runs while the pipeline is stopped; then it stays stopped
  (not auto-restarted into a rolled-back state) with a clear error.
- **Stale hash across the stop window:** recompute+verify the hash after Stop but
  before importPipeline (state can't change while stopped + single-writer).
- **Concurrent applies / apply-during-manual-stop:** per-pipeline provisioning
  lock; second apply sees a changed hash or the lock.
- **Restart failure:** apply succeeded but Start fails → pipeline stopped with the
  new (valid) config + a surfaced error; operator/agent can Start.

## Acceptance criteria

1. `PlanPipeline` RPC returns the same `Diff`+hash as the CLI standalone `deploy` for the same config; no mutation.
2. `ApplyPipeline` against a **stopped** pipeline applies (like standalone) and the store reflects it.
3. `ApplyPipeline` against a **running** pipeline with a `restart` diff: gracefully stops (drains — asserted no
   in-flight record loss), applies, restarts; the pipeline is running the new config, resumed from checkpoint.
4. Stale hash → `plan_stale`, no mutation, pipeline still running unchanged.
5. Partial-apply failure on a running pipeline → rollback, pipeline left stopped, coded error, no data loss.
6. **kill-mid-apply** (SIGKILL between Stop and Start / mid-import) → recoverable: store consistent, restart resumes
   from checkpoint, no records lost/duplicated beyond at-least-once.
7. Data-path diff on a running pipeline without operator authorization → refused (enforced gate), not applied.
8. CLI `apply` uses the API when a server is reachable, standalone when not — same result shape; asserted both paths.
9. MCP `apply` (with `--allow-mutations`) against a running server applies to a running pipeline (the north-star E2E).
10. `api.proto` change is additive; generated code regenerates clean; existing RPCs unaffected.

## Risk tier & tests

**Tier 1** (mutates live pipelines on the data path + API contract). Requires:
the kill-mid-apply recovery test (AC-6), the drain-no-loss test (AC-3), rollback
(AC-5), stale-hash (AC-4), the enforced-gate test (AC-7), human sign-off, and a
failure-mode analysis in the PR. Chaos suite: add the stop-drain-restart-under-load
- kill-mid-apply case.

## Scope boundary

Delivers: the two API RPCs, server-side stop-drain-restart apply, CLI/MCP
live-server preference with standalone fallback, and the enforced data-path gate.
Deferred: true in-place live hot-swap (no stop) → §4 hot-reload; multi-pipeline
transactional apply; the `repair` tool.

## Related

- Issue #2588; the Wave-2 deploy/apply design (`20260708-cli-pipeline-deploy-apply.md`)
  and the MCP design (`20260708-mcp-server.md`, whose deferred data-path gate lands here).
- Execution plan §2 (MCP `deploy_pipeline`), §4 (hot-reload).
- `provisioning.Service.Plan`/`ApplyPlan`, the `lifecycleService`, `api.proto`.
