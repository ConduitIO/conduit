# CLI: `conduit pipelines start | stop` + matching MCP tools

## Summary

Give humans and agents a first-class verb for the pipeline lifecycle transitions
the engine **already performs** but only exposes over raw HTTP/gRPC:
`conduit pipelines start <id>` and `conduit pipelines stop <id> [--force]`, plus
`start`/`stop` MCP tools. These act **by pipeline ID against a running Conduit
server** — they do not touch config files. The server-side `StartPipeline` /
`StopPipeline` RPCs, their HTTP handlers, the orchestrator methods, and the
lifecycle Start/Stop (including graceful drain) **all exist today**; this work is
CLI/MCP wiring plus one client-interface extension, not new engine behaviour.

Advances **Phase 1 execution plan §3 (CLI verb parity)** — "the status enum
already has User/System-Stopped; give humans and agents verbs for the
transitions that already exist"
(`docs/design-documents/20260704-phase-1-execution-plan.md:221`).

**Risk tier: Tier 2 (feature/CLI).** It adds no engine data-path code: it calls
existing, already-Tier-1-reviewed lifecycle methods over the existing RPC. The
one data-path-adjacent guarantee it depends on — that a graceful `stop` drains
in-flight records before returning — is enforced entirely server-side in code
this PR does not modify (`pkg/lifecycle/service.go` `stopGraceful`). The PR
description will state exactly that and cite the enforcement site rather than
claim a Tier-1 review it didn't perform.

## Context — the server half already ships

Verified against `origin/main` (commit at branch point):

| Piece | Where | Status |
| --- | --- | --- |
| Proto RPCs | `proto/api/v1/api.proto:441` `StartPipeline`, `:471` `StopPipeline` | **exist** |
| Request messages | `api.proto:623` `StartPipelineRequest{id}`, `:628` `StopPipelineRequest{id, force}` | **exist** |
| HTTP routes | `POST /v1/pipelines/{id}/start`, `POST /v1/pipelines/{id}/stop` (`api.proto:442,472`) | **exist** |
| HTTP/gRPC handlers | `pkg/http/api/pipeline_v1.go:195` `StartPipeline`, `:207` `StopPipeline` | **exist** |
| Orchestrator | `pkg/orchestrator/pipelines.go:29` `Start(ctx,id)`, `:34` `Stop(ctx,id,force)` | **exist** |
| Lifecycle | `pkg/lifecycle/service.go:180` `Start`, `:277` `Stop` (→ `stopGraceful`/`stopForceful`) | **exist** |
| Transition guards + codes | Start on running → `CodePipelineRunning` (`service.go:189-199`); stop on not-running → `CodePipelineNotRunning` (`service.go:279-303`) | **exist** |
| Graceful drain | `stopGraceful` stops pub nodes so in-flight messages finish (`service.go:314`); `--force` → `stopForceful` (`service.go:336`) | **exist** |

What does **not** exist yet:

1. The two CLI commands (`cmd/conduit/root/pipelines/start.go`, `stop.go`).
2. The two MCP tools (`cmd/conduit/internal/mcp/tools_lifecycle.go`).
3. `StartPipeline`/`StopPipeline` on the CLI's client interface —
   `cmd/conduit/api/services.go:36` `PipelineService` currently wires only
   `ListPipelines`, `GetPipeline`, `GetDLQ`, `PlanPipeline`, `ApplyPipeline`.
   The two lifecycle methods must be added and the generated mock regenerated.

So the plan is: **wire two verbs and two tools to RPCs that already work.**

## Decision

### 1. CLI surface

```text
conduit pipelines start <PIPELINE_ID>            # transition to Running
conduit pipelines stop  <PIPELINE_ID> [--force]  # graceful drain (default) or immediate
```

Both mirror the existing `inspect` command's shape
(`cmd/conduit/root/pipelines/inspect.go`): one required positional ID, a live
gRPC client, `--json` for free via the result decorator. Flags:

| Flag | Verb | Meaning |
| --- | --- | --- |
| `--force` | `stop` | Skip graceful drain; forceful stop (`StopPipelineRequest.force=true`). Documented as: may leave in-flight records un-drained; safe because positions are crash-safe (invariant 2) and delivery is at-least-once (invariant 3), so a forced stop behaves like a crash — recoverable, not lossy. |
| `--json` | both | Structured output (provided by the decorator, not per-command). |

**Decorator: `CommandWithExecuteWithClientResult`** (`cmd/conduit/cecdysis/decorators.go:86`)
— the same decorator `inspect` uses. It supplies a connected `*api.Client`,
renders the result as text or `--json`, and maps gRPC status codes to process
exit codes (NotFound → 2 validation, Unavailable → 3 environment; see
`inspect.go:118-127`). This is preferred over `CommandWithResult` because
start/stop **require** a live server (see §3) — there is no offline path to fall
back to, so the command should always take a client.

**Result shape** (rendered by both text and `--json`):

```go
type LifecycleResult struct {
    PipelineID string `json:"pipeline_id"`
    Action     string `json:"action"`  // "start" | "stop"
    Force      bool   `json:"force,omitempty"` // stop only
    Status     string `json:"status"`  // post-op status string from the 5-value enum
}
```

`Status` is read back via `GetPipeline` after the transition RPC returns, and
rendered through the existing status display helper
(`display.PrintStatusFromProtoString`, used at `inspect.go:167`) so CLI/MCP/UI
share one status vocabulary (the 5-value enum:
`Running, SystemStopped, UserStopped, Degraded, Recovering`). No new status
strings are introduced.

> **Graceful `stop` and the read-back.** `StopPipeline` returns after
> `stopGraceful` has asked the pub nodes to stop; the drain then completes
> asynchronously. The read-back `GetPipeline` therefore reports `UserStopped`
> once the transition has settled. If the status is still transitional when read,
> the command reports whatever the engine returns rather than blocking/polling —
> `stop` is not a "wait until fully drained" command in this iteration (a
> `--wait` flag is listed under Deferred). This is stated in the command's help
> so neither a human nor an agent assumes `stop` returning means the drain is
> 100% complete.

### 2. MCP surface

Two new tools in `cmd/conduit/internal/mcp/`, following the `apply`
mutating-tool pattern exactly (`tools_deploy.go`, registration in `server.go`):

| Tool | Args | Engine |
| --- | --- | --- |
| `start` | `{ "pipeline_id": string }` | `client.StartPipeline` RPC |
| `stop`  | `{ "pipeline_id": string, "force": bool }` | `client.StopPipeline` RPC |

Tool name constants added next to the existing ones
(`server.go:30-36`): `ToolStart = "start"`, `ToolStop = "stop"`. These are the
**same verbs the CLI uses** (shared-vocabulary rule).

Both tools reach the live server through the MCP server's existing API client
seam (`Config.newAPIClient` / `newAPIInspectClient`, `server.go:108`,
`inspect.go`), extended with the two lifecycle methods (same interface extension
as §1's CLI client). No divergent logic: the tool handler builds the request,
calls the RPC, reads status back, returns the same `LifecycleResult` as the CLI
via the MCP `Result[T]` wrapper (`mcp/result.go`), so an error carries `code` +
`suggestion` identical to the CLI.

### 3. Server-vs-local behaviour — start/stop **require** a running server

`deploy`/`apply` prefer a live server via a bounded gRPC probe and **fall back to
a standalone Badger-only local service** when none answers
(`cmd/conduit/internal/deploy/remote.go:192` `NewService`,
`service.go:70` `NewLocalService`). **start/stop deliberately do NOT get that
local fallback.** Rationale, grounded in the engine:

- Starting a pipeline means **running its goroutines inside a live process**.
  A standalone one-shot CLI invocation has no long-lived host: if it started the
  pipeline and then exited, the pipeline would die with it. "Start" only has
  meaning against a process that stays up — i.e. a `conduit run` server.
- Stopping is symmetric: if no server is running, there is nothing running to
  stop. The stored status is already laundered to `SystemStopped` on load
  (`pkg/pipeline/service.go:74-79`, `pkg/lifecycle/service.go:157-167` auto-start
  those on `run`), so a standalone "stop" would be a no-op on a definition, not
  an operation on a running pipeline.
- A standalone command that merely flips the **stored** status (so the next
  `conduit run` starts/skips it) is a different feature — deferred config-desired-
  state management, not a live lifecycle verb — and it would collide with the
  same concurrent-store-access hazard `NewLocalService` already refuses for
  non-Badger stores (`service.go:70-78`). Out of scope.

**Therefore:** both verbs take a live `*api.Client` unconditionally (via the
client-result decorator). When no server is reachable, the underlying RPC fails
with gRPC `Unavailable`, which the decorator maps to `common.unavailable`
(`pkg/foundation/cerrors/conduiterr/environment.go:38` `CodeUnavailable`, exit
code 3) with a "start a Conduit server first" suggestion. This is the honest
behaviour: no silent, meaningless standalone success.

### 4. The `--allow-mutations` gate (MCP)

`start` and `stop` are **mutating** — they change a running pipeline's state.
They MUST be gated exactly like `apply`/`scaffold_*`:

- Registered in `NewServer` **only inside the `if cfg.AllowMutations` block**
  (`server.go:157-172`), alongside `ToolApply`.
- `cfg.AllowMutations` is set solely from the operator/process startup flag
  `conduit mcp --allow-mutations` (`cmd/conduit/root/mcp/mcp.go:57,160`). It is
  **never a tool argument** — an agent cannot pass anything to flip it. This is
  the whole point of the gate; documented at `server.go:43-50`.
- Read tools (`validate/lint/dry_run/doctor/deploy/inspect`) stay
  unconditionally registered; the two lifecycle tools join the write set
  (`server_test.go:31` `writeToolNames`).

Consequence for the test matrix: with the server started **without**
`--allow-mutations`, `tools/list` MUST NOT contain `start`/`stop`, and a direct
`tools/call` for either MUST be refused by the MCP SDK as an unknown tool. This
is asserted (see Test plan). Unlike the deploy/apply data path, there is no
second server-side authorization flag involved here — start/stop have no
"restart a running pipeline" escalation the way live-apply does; the single
`--allow-mutations` gate is the complete MCP safety story for these verbs.

### 5. Error codes — reuse the existing registry, add nothing

Every failure path already has a registered code; this feature introduces **no
new codes**.

| Situation | Code (registry site) | gRPC / exit |
| --- | --- | --- |
| Pipeline ID not found | `pipeline.instance_not_found` (`pkg/pipeline/codes.go:28` `CodePipelineNotFound`) | NotFound / 2 |
| `start` on already-Running pipeline | `pipeline.running` (`codes.go:31` `CodePipelineRunning`) — set at `lifecycle/service.go:192`, suggestion "already running; stop it first" | FailedPrecondition / 2 |
| `stop` on not-running / stopped pipeline | `pipeline.not_running` (`codes.go:34` `CodePipelineNotRunning`) — set at `lifecycle/service.go:281,297`, suggestion "start the pipeline before trying to stop it" | FailedPrecondition / 2 |
| Empty ID (MCP arg / CLI arg) | `common.invalid_argument` (`conduiterr/conduiterr.go:103` `CodeInvalidArgument`) | InvalidArgument / 2 |
| No server reachable | `common.unavailable` (`conduiterr/environment.go:38` `CodeUnavailable`) | Unavailable / 3 |

The server handlers already wrap lifecycle errors via `status.PipelineError`
(`pkg/http/api/pipeline_v1.go:200,214`), so the coded error crosses the wire and
the CLI/MCP surface it unchanged. The CLI/MCP layers must **not** re-wrap in a
way that strips the code (the client-result decorator preserves gRPC status →
code mapping; the MCP `toolErr` helper preserves `conduiterr` codes — same path
`apply` uses at `tools_deploy.go:116-120`).

## Acceptance criteria (testable checklist)

Happy paths:

- [ ] **AC-1** `conduit pipelines start <id>` on a `SystemStopped`/`UserStopped`
      pipeline → RPC returns OK; pipeline transitions to `Running`; exit 0.
- [ ] **AC-2** `--json` on AC-1 emits exactly one JSON object
      `{"pipeline_id":"<id>","action":"start","status":"Running"}` on stdout and
      nothing else (no log lines interleaved — CLI output conventions §1).
- [ ] **AC-3** `conduit pipelines stop <id>` on a `Running` pipeline → graceful
      path (`stopGraceful`) invoked; pipeline transitions to `UserStopped`; exit 0;
      `--json` → `{"pipeline_id":"<id>","action":"stop","status":"UserStopped"}`.
- [ ] **AC-4** `conduit pipelines stop <id> --force` on a `Running` pipeline →
      forceful path (`stopForceful`); `--json` includes `"force":true`.
- [ ] **AC-5** Text (non-`--json`) output for each verb is a single human line
      naming the pipeline and its resulting status, rendered through
      `display.PrintStatusFromProtoString` (status vocabulary matches `inspect`).

Failure modes:

- [ ] **AC-6** `start` on an already-`Running` pipeline → error carries
      `pipeline.running`, message includes the running-state reason, suggestion
      "already running; stop it first", exit 2. Nothing mutated.
- [ ] **AC-7** `stop` on a `SystemStopped`/`UserStopped` pipeline → error carries
      `pipeline.not_running`, suggestion "start the pipeline before trying to stop
      it", exit 2. Nothing mutated.
- [ ] **AC-8** Either verb with an unknown ID → `pipeline.instance_not_found`,
      exit 2.
- [ ] **AC-9** Either verb with no argument → CLI arg error ("requires a pipeline
      ID"), non-zero exit, no RPC attempted (mirrors `inspect.go:107-115`).
- [ ] **AC-10** Either verb when no server is reachable at the configured gRPC
      address → `common.unavailable`, exit 3, suggestion points at starting a
      server. No partial state, no hang beyond the client dial timeout.

MCP:

- [ ] **AC-11** With `conduit mcp --allow-mutations`, `tools/list` includes
      `start` and `stop`; calling `start {pipeline_id}` transitions a stopped
      pipeline to `Running` and returns a structured result whose `code` is empty
      on success and whose payload matches the CLI `LifecycleResult` fields.
- [ ] **AC-12** Without `--allow-mutations`, `tools/list` contains neither `start`
      nor `stop`, and a direct `tools/call` for either is refused as unknown
      (parity with `apply` at `server_test.go`). This is the gate assertion.
- [ ] **AC-13** MCP `stop {pipeline_id, force:true}` calls the RPC with
      `force=true`; MCP `start`/`stop` on an invalid transition return the same
      `pipeline.running` / `pipeline.not_running` code the CLI returns (shared
      engine, no divergent logic).
- [ ] **AC-14** MCP `start`/`stop` with empty `pipeline_id` → `common.invalid_argument`
      with an actionable suggestion, before any RPC (mirrors `apply`'s empty-hash
      guard at `tools_deploy.go:96-100`).

Wiring / non-regression:

- [ ] **AC-15** `StartPipeline`/`StopPipeline` added to `api.PipelineService`
      (`cmd/conduit/api/services.go`) and the generated mock regenerated; existing
      `deploy`/`inspect` client tests still pass.
- [ ] **AC-16** No new error code registered; no proto change; no server-side
      handler/orchestrator/lifecycle change (diff is confined to `cmd/conduit/**`
      plus regenerated mock).

## Failure modes (design-level)

- **Not found** — server returns NotFound; surfaced as `pipeline.instance_not_found`,
  exit 2 (AC-8). No retry, no fallback.
- **Invalid transition** — start-on-running / stop-on-stopped are refused
  server-side with a coded, suggestion-bearing error (AC-6/7). The CLI/MCP never
  attempt to "force" a transition the engine rejected.
- **Server unreachable** — no local fallback by design (§3); `common.unavailable`,
  exit 3 (AC-10). The failure is honest and actionable, not a silent no-op.
- **Graceful-stop drain not fully settled at read-back** — `stop` reports the
  engine's current status without blocking; help text states `stop` returning is
  not a guarantee the drain is 100% complete (a `--wait` flag is Deferred). This
  avoids implying a stronger guarantee than the RPC gives.
- **Concurrent start/stop of the same pipeline** — serialization is the engine's
  responsibility (orchestrator has a `// TODO lock pipeline` at
  `pkg/orchestrator/pipelines.go:30,35`); this feature does not add locking and
  does not weaken anything — two racing CLI calls resolve to two sequential RPCs,
  the second of which hits the invalid-transition guard and is refused with a
  coded error. Called out so a reviewer knows it was considered, not missed.

## Test plan

Unit (no DB, no live server — the seams already support this):

- CLI `start`/`stop` commands tested against a **fake `api.Client`**
  (the mock regenerated in AC-15), asserting: correct RPC called with correct
  request (incl. `force`), result shape, `--json` output, and gRPC-code →
  exit-code mapping for NotFound/FailedPrecondition/Unavailable. Pattern:
  `cmd/conduit/root/pipelines/inspect_test.go`.
- MCP `start`/`stop` tools tested against a **fake lifecycle client**
  (`Config.newAPIClient` injection, as `inspect_test.go` / `tools_deploy_test.go`
  do), asserting request mapping, structured result, and coded errors.
- **Gate test:** `NewServer` with `AllowMutations:false` registers neither tool;
  with `true`, both appear — extend `server_test.go`'s `writeToolNames` set so
  the existing table test covers them (AC-12).
- Arg-validation tests: empty ID (CLI AC-9, MCP AC-14), too many args.

Integration (live engine, existing harness):

- Spin up a runtime with a generator→file pipeline, `start` it, assert `Running`
  via `GetPipeline`; `stop` it, assert `UserStopped`; `stop --force` from a fresh
  `Running`, assert forceful path taken. Reuse the integration pattern behind
  `pkg/http/api/pipeline_v1_test.go:81` (`TestPipelineAPIv1_StopPipeline`) which
  already exercises the handler — the new integration test drives it through the
  CLI/MCP layer instead of the handler directly.
- Invalid-transition integration: start-on-running and stop-on-stopped return the
  documented codes end-to-end (AC-6/7/13).

No chaos/upgrade/soak tests required: this feature changes no serialized format,
no position/checkpoint code, and no drain logic. The graceful-drain guarantee it
relies on is covered by the existing SIGTERM/force-stop tests
(`docs/design-documents/20260704-graceful-shutdown-sigterm.md`,
`20260706-forceful-stop-test-determinism.md`) — this PR will note it as
"unaffected, relied upon" rather than re-testing drain correctness.

## Docs

- `conduit.io` CLI reference: add `pipelines start` / `pipelines stop` with the
  `--force` semantics note and the "stop ≠ fully drained" caveat.
- `llms.txt` / MCP tool catalog: add the two tools (regenerated in CI per the
  execution plan, not hand-edited).
- Changelog entry (conventional-commit derived).

## Adversarial self-review

Re-reading the plan hunting for the bug, per CLAUDE.md AI-authored-code rule:

- **"Is this really Tier 2, or am I sneaking a data-path change past review?"**
  The diff is confined to `cmd/conduit/**` + a regenerated mock (AC-16). It adds
  zero lines to `pkg/lifecycle`, `pkg/orchestrator`, or `pkg/http/api`. It calls
  `Stop(ctx, id, force)` which routes to the **already-reviewed** graceful/forceful
  paths. The only integrity-relevant guarantee (drain-before-return on graceful
  stop) lives in code this PR doesn't touch. Tier 2 is correct — but the PR
  description must cite the drain enforcement site so the reviewer can confirm the
  claim rather than trust it.
- **Does `stop` returning falsely imply the drain finished?** Yes, that was the
  trap. `StopPipeline` returns after pub-nodes are asked to stop, not after full
  drain. Mitigated: the read-back reports actual status, help text states the
  caveat, and `--wait` is explicitly deferred rather than half-implemented.
- **Could the local-fallback omission be seen as a gap?** It's a deliberate,
  documented decision (§3) with an engine-grounded reason, not an oversight. A
  standalone "start" would host a pipeline in a process that immediately exits —
  worse than refusing. The alternative (flip stored status) is a different
  feature with a real concurrent-store hazard; deferred with reason.
- **Error-code drift.** The risk is the CLI/MCP layer re-wrapping and dropping the
  server's code. Mitigated by reusing the exact preservation paths `inspect`
  (client-result decorator) and `apply` (`toolErr`) already use; AC-6/7/8/13
  assert the codes survive end-to-end. No new codes invented (AC-16).
- **`--allow-mutations` theater check.** The gate is only real if it's a startup
  flag, not an agent-passable arg. Confirmed: it comes from
  `mcp.go:57,160` → `Config.AllowMutations`, read at `server.go:157`; no tool
  argument touches it. AC-12 asserts the tools are absent (not merely error-ing)
  without the flag — absence is a stronger gate than a runtime refusal.
- **Uncertainty stated, not assumed away:** whether `stop`'s post-op read-back
  reliably observes `UserStopped` vs a transient transitional status depends on
  drain timing; the design chooses "report what the engine says, don't block,"
  and the integration test will confirm the settled-state case. If flakiness
  appears there, the fix is a bounded read-back retry in the CLI layer, not an
  engine change.

## Related

- `docs/design-documents/20260704-phase-1-execution-plan.md` §3 (CLI verb parity),
  the roadmap item this advances; §9 (shared verbs across CLI/MCP/UI).
- `docs/design-documents/20260708-cli-pipeline-deploy-apply.md` — the
  mutating-verb + `PlanApplier` seam pattern reused here for command shape.
- `docs/design-documents/20260708-mcp-server.md` — the `--allow-mutations`
  read/write tool split these tools join.
- `docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md` — the
  `CommandWithExecuteWithClientResult` (live-server) command shape reused here.
- `docs/design-documents/20260704-graceful-shutdown-sigterm.md` — the graceful
  drain guarantee `stop` relies on (unaffected by this PR).
- **Deferred, not in scope:** `conduit pipelines pause`. The status enum has no
  `Paused` state and the engine has no pause primitive (only start / graceful
  stop / forceful stop exist — `pkg/lifecycle/service.go`). Pause would require a
  new engine state **and** a new lifecycle primitive (suspend consumption while
  holding position without tearing down nodes), which is a data-path change
  needing its own design doc and Tier-1 review. Inventing a fake "pause" as
  stop+start would silently drop the in-memory pipeline and re-read from the last
  committed position — different semantics from pause, and a data-integrity
  footgun. Explicitly deferred to a separate design.
- **Deferred:** `stop --wait` (block until drain fully settles) and standalone
  stored-desired-status management (offline start/stop that the next `conduit run`
  obeys).
