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
failure-mode analysis in the PR. Chaos suite: add a stop-drain-restart-under-load
and kill-mid-apply case.

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

## Review outcome & required rework (2026-07-08) — NEEDS-REWORK → reworked

A Tier-1 design review verified the plan against the code and found **two
data-integrity blockers** in the original Decision above. Both are folded in
here; the sections above are superseded where they conflict.

### Blocker 1 (fixed here): `Stop(ctx,id,false)` does NOT drain before returning

Traced through `pkg/lifecycle/service.go`: graceful `Stop` injects a
"stop-here" control message into the source node and returns immediately — it
does **not** wait for the message to propagate, the destination to ack, or the
connector position to durably flush (the `connector.Persister` is an async
batched writer; only `Persister.Wait()` blocks for durability). Drain completion
is observable only via `Service.WaitPipeline(id)` (`service.go:418`), which is
**not on the `LifecycleService` interface** (`interfaces.go:74-77`). So the
original §2 — `Stop` then immediately `importPipeline` — races an in-flight drain
and can tear down a connector while old goroutines are still mid-flight
(invariant-1/3 loss). **Rework:**

- Add **`StopAndWait(ctx, id) error`** to the lifecycle service (and its
  `LifecycleService`/`lifecycleService` interfaces). It does: `Stop(ctx,id,false)`
  → `WaitPipeline(id)` (all node goroutines exited) → **await the connector
  persister's durable flush** (the lifecycle service reaches the persister via
  the connector service it already holds — expose a "wait for pending position
  writes" on `connector.Service`/`Persister` rather than giving provisioning a
  persister handle). Only after all three is the pipeline safe to mutate.
- `ApplyPlanLive` calls `lifecycleService.StopAndWait(ctx, id)` (not `Stop`)
  before `importPipeline`, then `Start`. AC-3's "drains — asserted no in-flight
  record loss" now has a real primitive to rest on.

### Blocker 2 (already landed): apply must be transactional

The review found `ApplyPlan`'s `importPipeline` wasn't wrapped in a DB
transaction (unlike `provisionPipeline`), so a crash mid-apply left partial
state — AC-6 (kill-mid-apply) would have failed. **Fixed and merged as #2595**
(the standalone crash-safety fix). `ApplyPlanLive` inherits it; the doc's earlier
"transactional-per-action" phrasing was wrong (the guarantee is the enclosing
transaction, now present).

### Item 6 (reworked conservative): the data-path gate

There is no existing taxonomy mapping a `Change.ConfigPath` to
"ack/position/checkpoint-adjacent," and `EffectRestart` is a broad bucket. Do
**not** invent a fine-grained classifier that could misfire toward silently
allowing a data-path change. Wave-of-#2588 gates **every `EffectRestart` apply
against a running pipeline** behind the operator authorization flag (conservative
default); a finer classifier is future work.

### Open parity item

The drain analysis is for `pkg/lifecycle` (the default). `pkg/lifecycle-poc`
(`Preview.PipelineArchV2`, `runtime.go:259`) has its own Stop semantics — audit
its `StopAndWait` equivalent before enabling live apply under that arch, or gate
live apply off when the preview arch is active.

### Restart-from-checkpoint (confirmed)

`connector.Source.Open` resumes from the persisted `SourceState.Position`
(`source.go:64,227,285`), and `Start` rebuilds from the stored instance — so
post-apply `Start` resumes at-least-once (invariant 2), provided StopAndWait
awaited the durable flush (blocker 1).

## PR2 status (issue #2588)

PR2 delivers the API surface + CLI/MCP wiring on top of PR1's
`StopAndWait`/`ApplyPlanLive`:

- **Per-pipeline provisioning lock** (`pkg/provisioning/lock.go`,
  `pipelineLocks`): `ApplyPlan` and `ApplyPlanLive` each hold the target
  pipeline's lock for their entire body, closing the "Concurrent applies /
  apply-during-manual-stop" gap flagged as a KNOWN GAP in PR1's
  `ApplyPlanLive`. Verified with `-race -count=20`
  (`TestPipelineLocks_SerializesSameID`).
- **TOCTOU close inside `ApplyPlanLive`**: the running-check is repeated
  immediately before the mutating write (not just once, early); if the
  pipeline became running in between (e.g. an external `Start` call not
  routed through this lock), the method falls through to the StopAndWait
  branch instead of mutating a live pipeline directly
  (`TestApplyPlanLive_TOCTOU_BecameRunningBetweenChecks_Drains`).
- **Two additive API RPCs**: `PipelineService.PlanPipeline` (read-only) and
  `ApplyPipeline` (mutating), in `proto/api/v1/api.proto`. Handlers in
  `pkg/http/api/pipeline_v1.go` call the live `runtime.ProvisionService.Plan`
  / `ApplyPlanLive`, reusing the standalone path's config-enrich/validate
  step (`config.Enrich` + `config.Validate`) so the two surfaces can't drift
  on what a valid desired config is. Additive only — every existing RPC is
  unchanged; `buf generate` re-run twice produces no incremental diff.
- **Enforced data-path gate**: `ApplyPipeline` refuses any `EffectRestart`
  change against a running pipeline unless the server was started with
  `--api.allow-live-restart-apply` (`conduit.Config.API.AllowLiveRestartApply`,
  a process-level flag with no `ApplyPipelineRequest` field — not
  agent-passable). Coded `provisioning.live_apply_unauthorized`. See
  `docs/operations/live-restart-apply.md`.
- **CLI/MCP**: `deploy`/`apply` (CLI) and the MCP `deploy`/`apply` tools now
  prefer a live server (`cmd/conduit/internal/deploy.NewService`, health-
  checked with a bounded probe) and fall back to the standalone
  `NewLocalService` path when none is reachable — same `PlanApplier`
  interface, same `--json`/result shape either way
  (`TestNewService_PrefersLiveServer` /
  `TestNewService_FallsBackToStandalone_WhenNoServerReachable`).

Inherited from PR1, not re-tested here: AC-3's real drain-no-loss guarantee
(`TestServiceLifecycle_StopAndWait_DrainsAndPersists`,
`pkg/lifecycle`) and the DB-transaction crash-safety of `importPipeline`
(#2595). PR2 does not touch either.

Still deferred (unchanged from PR1's scope boundary): true in-place hot-reload
(§4), multi-pipeline transactional apply, the `repair` tool, and the
`pkg/lifecycle-poc` `StopAndWait` parity audit (see "Open parity item" above)
— `ApplyPlanLive` still has no arch-v2-specific branch; a server running with
`preview.pipeline-arch-v2` gets whatever `lifecycle_v2.Service.StopAndWait`
does today (always refuses, per its own doc), so `ApplyPipeline` against a
running pipeline under that preview architecture fails closed rather than
silently skipping the drain.
