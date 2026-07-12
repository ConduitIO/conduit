# Live restart-class apply: `--api.allow-live-restart-apply`

Issue #2588 gave `deploy`/`apply` (CLI and MCP) a path through a **running** Conduit server:
`PlanPipeline`/`ApplyPipeline` (gRPC/HTTP RPCs on `PipelineService`) let a caller apply changes to
a pipeline the server currently has running, instead of only a stopped, directly-opened store. See
[`docs/design-documents/20260708-live-server-deploy-apply.md`](../design-documents/20260708-live-server-deploy-apply.md)
for the full design and failure-mode analysis. This doc is the operator-facing summary: what the
gate is, why it exists, and what to check when it fires.

## Symptom

An `apply` (CLI `conduit pipelines apply`, or the MCP `apply` tool) against a **running** pipeline
fails with:

```
code: provisioning.live_apply_unauthorized
message: pipeline "orders" is running and this plan includes a restart-class change; applying it
         requires operator authorization
suggestion: have an operator restart the Conduit server with --api.allow-live-restart-apply (or
            the equivalent config/env setting) to authorize live restart-class applies, or stop
            the pipeline first and apply while it's stopped
```

## Diagnosis

This is not a bug — it's the Tier-1 data-path gate. Applying a `restart`-class change (see the
`effect` field in `apply`'s `Diff` output: `in_place` vs `restart`) to a running pipeline means the
server gracefully stops it, drains and durably checkpoints it
(`lifecycle.Service.StopAndWait`), applies the change, and restarts it
(`provisioning.Service.ApplyPlanLive`). That is real, in-place mutation of live infrastructure —
exactly the kind of change this project's data-integrity invariants (see `CLAUDE.md`) require a
human decision for, not something an agent or CI job should be able to trigger unattended by
default.

The gate is deliberately coarse: it fires for **every** `EffectRestart` change against a running
pipeline, not a finer classification of which changes actually touch ack/position/checkpoint
state. See the design doc's "Item 6 (reworked conservative)" for why a narrower classifier was
explicitly rejected — a misclassified "safe" restart change is a data-loss bug waiting to happen.

## Remediation

Pick one:

1. **Authorize it.** Restart the Conduit server with the flag set, then re-run apply:

   ```console
   conduit run --api.allow-live-restart-apply
   ```

   or the config-file/env equivalent:

   ```yaml
   api:
     allow-live-restart-apply: true
   ```

   ```console
   CONDUIT_API_ALLOW_LIVE_RESTART_APPLY=true conduit run
   ```

   This is a **process-level** flag, read once at server startup
   (`conduit.Config.API.AllowLiveRestartApply`, wired into
   `pkg/http/api.PipelineAPIv1.allowLiveRestartApply`). There is no corresponding field on the
   `ApplyPipelineRequest`/`ApplyPipeline` MCP tool argument — no API caller, human or agent, can
   set or override it in a request. Only restarting the process with the flag changes the answer.
   This is what makes it "not agent-passable": an agent driving `apply` over the API has no lever
   for this gate at all.

2. **Stop the pipeline first**, then apply while it's stopped (no gate applies to a stopped
   pipeline — see `provisioning.Service.ApplyPlan`'s existing behavior, unchanged by this feature):

   ```console
   conduit pipelines stop orders
   conduit pipelines apply orders.yaml --plan-hash <hash>
   conduit pipelines start orders
   ```

## Operational notes

- **Default is off.** `AllowLiveRestartApply` defaults to `false` — a fresh `conduit run` refuses
  every restart-class apply against a running pipeline until an operator explicitly opts in.
- **Scope: the whole server, for its whole lifetime**, not per-pipeline or per-request. If you need
  to authorize a single change, prefer remediation option 2 (stop, apply, start) — enabling the
  flag broadly means *every* restart-class apply against *any* running pipeline on that server is
  now unattended-authorized until the process restarts.
- **`in_place`-only changes are never gated** — the gate only fires when the diff includes at least
  one `restart`-class change against a pipeline the server currently considers running
  (`Status.Running`/`Recovering`/`Degraded`; see `provisioning.isRunningStatus`). A no-op or
  in-place-only diff applies normally regardless of this flag.
- **This flag does not, by itself, make an apply safe** — it only removes the "did a human decide
  this" gate. Invariant 7 (graceful shutdown), invariant 2 (crash-safe resume), and invariant 5
  (atomic state writes) are enforced unconditionally by `ApplyPlanLive`/`StopAndWait`, whether or
  not this flag is set.

## Related

- Design doc: [`docs/design-documents/20260708-live-server-deploy-apply.md`](../design-documents/20260708-live-server-deploy-apply.md)
- `pkg/provisioning.CodeLiveApplyUnauthorized`, `pkg/provisioning.Service.ApplyPlanLive`
- `pkg/http/api.PipelineAPIv1.ApplyPipeline`
- `pkg/conduit.Config.API.AllowLiveRestartApply`
