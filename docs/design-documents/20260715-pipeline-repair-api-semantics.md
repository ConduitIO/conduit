# Pipeline repair API semantics

## Summary

Recommendation, up front: **do not build a new store/runtime "repair" primitive that mutates a
pipeline's positions, state, or DLQ for v0.18.** The operator need behind a UI "repair" button — get
a degraded pipeline running again — is already served by the existing **`StartPipeline`** RPC, which
runs a pipeline from any non-running status (including `StatusDegraded`). Pair it with the
already-exposed error cause (`Pipeline.State.error`, and the structured `ConduitError` on the
mutating-RPC failure path) so the operator can see why it degraded, fix the config, and re-apply or
restart.

Two things commonly conflated under "repair" are separated here:

- The existing `conduit pipelines repair` command is a **config-file** repair (it edits a YAML file
  to clear validation findings). It never touches the store, a running pipeline, positions,
  checkpoints, or the DLQ. Exposing it over HTTP is a possible, safe Tier-2 addition — but it is
  file-scoped, not the "fix my degraded pipeline" action.
- A genuinely new **store/runtime** repair (reset position, clear/rebuild state, recreate the DLQ)
  lands squarely on data-integrity invariants 2, 3, and 5. `reset-position` in particular is a
  data-loss foot-gun. Any such capability is Tier-1 and requires its own design doc, invariant
  analysis, and human sign-off. It is out of scope here, and deferred pending a concrete,
  sign-off-backed use case.

Like the pause decision (`20260715-pipeline-pause-semantics.md`), this overturns an assumption in the
greenfield-UI doc that `repair` is prerequisite engine work. **UI "repair" is unblocked with no new
engine primitive.**

## Context

### What operators want from "repair"

A pipeline is `StatusDegraded` (stopped with a fatal error). The operator wants to get it running
again after addressing the cause. In an ops tool that reads "repair."

### What already exists (verified against the tree)

- **`StartPipeline` runs a degraded pipeline.** `Service.Start` rejects only when the status is
  `StatusRunning` (`pkg/lifecycle/service.go:185+`, the `StatusRunning` guard); from `Degraded`,
  `UserStopped`, `SystemStopped`, or `Recovering` it rebuilds the nodes (`buildRunnablePipeline`,
  which does not reset positions) and runs from the last committed position. A manual restart gets a
  **fresh** backoff — the backoff/recovery-counter carryover (`service.go:215-218`) only applies
  while the pipeline is still in `runningPipelines`, and a degraded pipeline was already removed from
  it during cleanup (`service.go:928`); carryover is therefore an auto-recovery-loop behavior, not a
  manual-restart one. So "restart this degraded pipeline" is already a first-class, exposed operation
  (`StartPipeline`, `proto/api/v1/api.proto`).
- **The error cause is already observable.** `Pipeline.State.error` carries the degradation message
  (surfaced by the fleet and detail views), and mutating-RPC failures return a structured
  `ConduitError` (code / configPath / suggestion) on the `google.rpc.Status` path.
- **`conduit pipelines repair` is file-only.** It collects machine-appliable validation fixes for a
  config file and writes the safe subset back atomically. Its own invariant:
  "repair edits the config file only — it never touches the pipeline store or a running pipeline"
  (`cmd/conduit/root/pipelines/repair.go`; design doc `20260712-repair-command.md` §4.3). There is
  no repair RPC or endpoint anywhere today (`grep -rni repair proto/ pkg/http/api/` → nothing).
- **Auto-recovery already handles transient errors.** A transient failure drives `recoverPipeline` /
  `StartWithBackoff` → `StatusRecovering` (`pkg/lifecycle/service.go`); `StatusDegraded` is the
  "backoff exhausted / fatal — needs manual intervention" terminal. Manual intervention today =
  fix + `StartPipeline`.

### The gap, precisely

There is no server-side operation that "repairs" a degraded pipeline by mutating its persisted
position/state/DLQ — and there should not be a casual one, because that is exactly where data loss
lives. What operators actually need (restart after fixing the cause) already exists.

## Constraints

- **Invariants 1–7 are non-negotiable.** A repair that reset positions, cleared checkpoints, or
  rebuilt the DLQ could skip records (2), drop data (3), or tear a checkpoint (5). These are the
  most dangerous mutations in the system.
- **Errors are API.** Whatever surface is chosen returns stable error codes, the failing config
  path, and a suggested fix (agents consume them).
- **No speculative generality; state-layer discipline.** A store-mutating repair subsystem with no
  concrete, signed-off use case is a YAGNI and a data-safety risk.
- **`--escalate` is human-only.** The CLI keeps escalation unreachable from MCP; any API must use the
  same process-set gate pattern as `allowLiveRestartApply` (a flag with no request field an agent can
  set), not a request parameter.

## Alternatives

### Alternative A — new store/runtime repair primitive (reset-position / clear-state / rebuild-DLQ) — declined for v0.18

**Sketch.** A `RepairPipeline` RPC/handler that mutates persisted pipeline state: reset a source
position, clear or rebuild connector state, recreate the DLQ, force-clear a degraded status.

**Why it loses (and is declined, not merely deferred).** Every one of these mutations is a
Tier-1 data-path change that can lose or duplicate data. `reset-position` re-reads from an arbitrary
point — at-least-once becomes "maybe none." Clearing state discards dedup/checkpoint progress. There
is no engine method to bind such a handler to today (`PipelineOrchestrator` is CRUD + Start/Stop),
so it is entirely new surface. Building it speculatively, before a concrete operator workflow that
demonstrably needs it and has explicit sign-off, violates both the data-integrity invariants and
"no speculative generality." If a real need appears (e.g. a documented recovery runbook that
requires a controlled position reset), it gets its own design doc, its own invariant analysis, an
upgrade/rollback story, and a human sign-off — not a v0.18 UI convenience.

### Alternative B — expose the existing config-file repair over HTTP — available, deferred

**Sketch.** A `PipelineService`/provisioning endpoint that runs the offline validate+fix engine over
a submitted config (config-in → fixed-config-out, with the same default-deny + `--escalate`-style
gate), so the UI/agents can get machine-fixes for a config without the CLI.

**Why deferred.** It is genuinely safe (no data path — it only rewrites config bytes, and the only
route into a live engine remains `deploy`/`apply` with its existing guard). But it is **file/config
scoped**, not the "fix my degraded pipeline" action operators reach for, and the UI is observe +
operate, not authoring. Worth doing when config-authoring-over-API demand is real; not needed to
unblock UI operate.

### Alternative C — "repair" = restart via the existing `StartPipeline`, plus surface the cause — recommended for v0.18

The UI's "repair"/"restart" action on a degraded pipeline maps to the existing `StartPipeline` RPC
(verified to run from `Degraded`). The detail view already shows `State.error`; the operate action
surfaces the structured `ConduitError` (code / path / suggestion) on failure so the operator can fix
the config and re-`apply`, then restart. No new engine primitive, no store mutation, no data-path
risk.

**Caveat.** Restart re-runs from the last committed position (at-least-once; the un-flushed tail may
replay) — identical to any restart. If the degradation cause is unfixed, the pipeline will degrade
again; the UI reflects that via the polled status rather than pretending a restart "repaired" it.
Honest copy: this is "restart," and the cause must be addressed in config.

## Recommendation

Adopt **Alternative C** for v0.18: UI "repair" is restart via `StartPipeline` plus clear surfacing of
the degradation cause. **Decline A** (store-mutating repair) until a concrete, signed-off use case
exists. Keep **B** (config-file repair over HTTP) available as an independent, safe Tier-2 addition
if config-authoring-over-API demand materializes. This unblocks the UI's operate surface with zero
data-path risk.

Open decision for the maintainer: whether to also schedule **B** now, or defer it with A. The
recommendation is to defer both and ship C.

## Failure modes

For the recommended path (restart via `StartPipeline`):

- **Restart from a non-running status** is the supported path (`Start` rejects only `StatusRunning`).
  A double "repair" on an already-running pipeline returns the existing `CodePipelineRunning` error
  with its suggestion — surfaced, not swallowed.
- **Cause unfixed → re-degrades.** Restart does not fix configuration; the pipeline degrades again
  and the UI shows it. The UI must not imply "repaired"; it shows "restarting…" then the real polled
  status.
- **Unservable persisted position — the honest gap in C.** Some degradations are not fixable by
  restart _or_ config: a pipeline whose committed source position is no longer servable (Kafka
  retention expired past the offset, a dropped CDC replication slot) will re-degrade on every
  restart, and for some connectors there is no config knob to move past it. Under path C, **this
  class has no in-product remedy in v0.18** — recovery requires deleting/recreating the pipeline
  (losing its state) or an out-of-band store edit. This is exactly the narrow, genuinely-needed case
  a future position-reset primitive (Alternative A) would serve — and exactly why that primitive
  must be built deliberately, behind a signed-off design with audit + data-loss warnings, rather
  than as a casual "repair" button. We accept this gap for v0.18 and surface the cause clearly so
  the operator understands why restart won't help.
- **Restart replays the un-flushed tail** (at-least-once) — identical to any restart/crash recovery;
  no new semantics, invariants 2/3/7 unchanged.
- **Concurrent operators.** Start/Stop idempotency and the running-guard are the RPCs' existing
  contract; the UI disables the control during a transition.

If Alternative A is ever pursued, its failure modes (position-reset data loss, torn state on crash
mid-repair, DLQ rebuild semantics, `kill -9` during repair) must be fully enumerated in that doc
before any code — they are the reason it is declined here.

## Upgrade / rollback

- **Recommended path (C):** no new API, no serialized-format change, no migration — it uses existing
  RPCs. Rollback is UI-only.
- **If B is added later:** config-in/config-out with no persisted-state change; still no data-path
  migration. **If A is ever added:** any persisted-state or position mutation requires a versioned
  migration and an upgrade/downgrade test, and is a release-gating concern — a reason it is not a
  casual addition.

## Observability

- **Recommended path (C):** reuse the existing pipeline status surface and the drop/error metrics; a
  restart is visible as a status transition. No new metric.
- **If A is ever built:** every state-mutating repair must emit an audit event (who repaired what,
  which positions/state changed) and a runbook entry — a silent data mutation is unacceptable on a
  data pipeline.

## Related

- `20260715-pipeline-pause-semantics.md` — the parallel decision (pause = stop/start, no new
  primitive); same "the capability already exists" finding.
- `20260712-repair-command.md` — the existing config-file repair command this doc distinguishes
  from a runtime repair.
- `20240812-recover-from-pipeline-errors.md` — auto-recovery vs. the degraded terminal that manual
  restart addresses.
- `20260713-greenfield-built-in-ui.md` — the epic that assumed `repair` was prerequisite engine
  work; this doc revises that. UI operate proceeds against existing `Start`/`Stop`.
- ROADMAP v0.18 (P5). If accepted, the "no store-mutating repair primitive" decision is a candidate
  to promote to an immutable ADR.
