# CLI: `conduit pipeline deploy | apply <file>` + provisioning preview/diff

## Summary

Diff-first pipeline deployment: `deploy <file>` shows exactly what would change
(create/update/delete per pipeline/connector/processor, each classified
**in-place vs restart**) with **no side effects**; `apply <file>` executes that
plan, gated by a plan hash so the diff an operator/agent approved is provably the
diff applied. Both are thin renderers over a new cobra-free **preview engine**
that reuses the provisioning reconcile that already exists
(`import.go` → `actionsBuilder.Build` → `executeActions`/`rollbackActions`). This
is the keystone §2 (MCP `deploy_pipeline`) and §4 (hot-reload) both build on.
**Tier 1** — it changes how pipelines are created/updated (data path).

## Context — the reconcile already exists

`pkg/provisioning` already computes and applies a diff; we are exposing it, not
inventing it:

| Piece | Where | What it does |
| --- | --- | --- |
| Reconcile entry | `import.go:39` `importPipeline` | `Export(current)` → `Build(old,new)` → `executeActions` → `rollbackActions` on failure |
| Diff builder | `import.go:113` `actionsBuilder.Build(old,new)` | emits ordered `[]action` (create/update/delete × pipeline/connector/processor) by comparing old vs new |
| Actions | `import_actions.go` | each action implements `Do(ctx)` + `Rollback(ctx)` (create `:56/114/173`, update `:217/325/385`, delete `:419/433/447`) |
| Ordered rollback | `import.go:49-58` | on failure, reverses the executed prefix and rolls back — the crash-safety spine we inherit |
| Field classification | `config/parser.go:56-65` | `PipelineMutableFields`, `PipelineIgnoredFields=[Status]`, `ConnectorImmutableFields=[Type]`, `ConnectorMutableFields=[Name,Settings,Processors,Plugin]` — the seed of in-place-vs-restart |

What does NOT exist yet: (1) a way to compute the diff **without executing it**
(preview), (2) a stable **describe** of each action for human/`--json` rendering,
(3) the **in-place-vs-restart** classification surfaced on each change, (4) an
**apply token** binding an approved plan to its execution, (5) the CLI verbs.

## Decision

### 1. A cobra-free preview engine in `pkg/provisioning`

```
Plan(ctx, desired config.Pipeline) (Diff, error)   // Export(current)+Build(old,new), DESCRIBE actions, never Do()
Diff{ PipelineID string; Changes []Change; Hash string }   // Hash = stable digest of the ordered Changes
Change{ Resource (pipeline|connector|processor); ID; Action (create|update|delete);
        Effect (in_place|restart); ConfigPaths []string; From, To any; Code string }
```

- `Plan` runs the exact `actionsBuilder.Build(old, new)` the apply path runs, then
  maps each `action` to a `Change` via a new `action.Describe() Change` method
  (added alongside the existing `Do`/`Rollback`/`String`) — so the **preview and
  the apply are the same action list by construction** (no drift between "what I
  showed" and "what I'll do").
- **Effect classification:** a change to a `ConnectorImmutableFields` field
  (`Type`) ⇒ delete+recreate ⇒ `restart`; a change confined to
  `ConnectorMutableFields`/`PipelineMutableFields` ⇒ `in_place` **iff** the
  service `Update` can apply it to a running pipeline (see Failure modes — this is
  the Tier-1 crux). `Status` (ignored) never appears as a change.
- `Diff.Hash` is a deterministic digest (sorted, stable-encoded Changes) — the
  apply token.

Both the CLI `deploy` and the future MCP `deploy_pipeline` call `Plan` 1:1.

### 2. `deploy` shows, `apply` executes — token-bound

- `conduit pipeline deploy <file>`: parse+enrich+validate (reuse the merged
  validate engine) → `Plan` → render the Diff (human + `--json`). **Zero side
  effects.** Emits the `hash`.
- `conduit pipeline apply <file>`: `Plan` → **require the presented hash to equal
  the freshly-computed hash** (`--plan-hash <h>`, or `deploy --apply` as a
  convenience that computes-and-applies in one call for humans) → `executeActions`
  with the existing rollback. A mismatch (config or live state changed since the
  plan) ⇒ refuse with `provisioning.plan_stale` (exit 2). This makes the safety
  gate real, not theater (§2): an agent that approved hash `H` cannot apply a
  different plan.
- Idempotent: re-applying an already-applied config ⇒ empty Diff ⇒ "nothing to
  do," exit 0.

### 3. Reuse the shared contract

Follows `20260707-cli-output-conventions.md` exactly: the `Result` envelope
(`command: pipeline.deploy|pipeline.apply`), glyphs via `internal/ui`, exits via
`pkg/conduit/exitcode`, `SilenceErrors`. `deploy` is offline-parse + a read of
current state (needs the server/store to Export current); `apply` mutates.

## CLI UX

```text
$ conduit pipeline deploy orders.yaml
Plan for pipeline "orders" (hash 9f3a2c):
  ~ update  connector  pg-source        in place   settings.table: users → orders_v2
  + create  connector  s3-sink          restart    (new destination)
  ~ update  pipeline   orders           restart    connectors changed
  - delete  processor  redact-pii       restart

3 to change, 1 to create, 1 to delete. Applying will RESTART pipeline "orders".
Run:  conduit pipeline apply orders.yaml --plan-hash 9f3a2c
```

`--json`: `{command, ok, summary:{create,update,delete,restart}, result:{pipelineID,
hash, changes:[{resource,id,action,effect,configPaths,code}]}, error}`.

Flags: `deploy` — `--json`, `--apply` (compute+apply in one call, prompts unless
`--yes`), `--quiet`. `apply` — `--plan-hash <h>` (required unless `--yes` with a
freshly recomputed identical plan), `--json`, `--yes`.

Exit codes (via `exitcode`): 0 applied / no-op; 2 validation or **stale plan**;
1 apply failed (rolled back); 3 cannot reach store/server to read current state.

## File-level plan

- `pkg/provisioning/plan.go` (new): `Plan`, `Diff`, `Change`, `Diff.Hash`;
  `action.Describe()` added to the `action` interface + each action in
  `import_actions.go`. `apply` calls the existing `importPipeline` behind a
  hash-check wrapper `ApplyPlan(ctx, desired, hash)`.
- `cmd/conduit/root/pipelines/{deploy,apply}.go` (+ tests); register in
  `pipelines.go` `SubCommands()`.
- `cmd/conduit/internal/deploy/` — render/`--json` for the Diff (shared by both
  verbs), built on `internal/ui` + the `Result` envelope.
- Reuse `cmd/conduit/internal/validate` for the parse+validate pre-step.

## Acceptance criteria

1. New pipeline (no current) → Diff is all `create`; `--json` lists every resource; exit 0, no side effects (assert
   store unchanged after `deploy`).
2. Change one connector `Settings` field → one `update`/`in_place` Change naming the exact `configPath`; `deploy`
   leaves state unchanged.
3. Change connector `Type` (immutable) → `delete`+`create` (or `update`/`restart`), Effect=`restart`.
4. Remove a resource from the file → `delete` Change; add one → `create`.
5. `deploy` output includes a stable `hash`; two `deploy`s of the same file+state produce the **same** hash; changing
   the file changes the hash.
6. `apply --plan-hash <H>` where `H` matches → executes; store reflects the new config; exit 0.
7. `apply --plan-hash <stale>` (file or current state changed since) → refused with `provisioning.plan_stale`, exit 2,
   **no mutation**.
8. Idempotent: `apply` of an already-applied config → empty Diff, "nothing to do," exit 0, no actions executed.
9. Partial apply: an action fails mid-sequence → the executed prefix is rolled back in reverse (assert via a
   forced-failure action), exit 1, final state == pre-apply state.
10. `--json` envelope conforms to the conventions schema on success and on stale/failed apply.
11. **Tier-1 data integrity:** applying a `restart` change to a RUNNING pipeline drains in-flight records before
    teardown (invariant 3/7) OR the plan/apply refuses on a running pipeline with an actionable error; verified by a
    kill-mid-apply recovery test that asserts no record loss and recoverable position state.
12. `deploy`/`apply` on an unparseable/invalid file → validation findings (reusing the validate engine), exit 2,
    before any Plan.

## Failure modes (Tier 1)

- **Apply to a running pipeline (the crux).** Today `importPipeline` is invoked at
  provision time; whether the service `Update`/delete actions are safe against a
  *running* pipeline is the open question this doc forces a decision on. Options:
  (a) `apply` requires the target pipeline **stopped** (simplest, safe; the Diff
  says "restart" and apply stops-drains-restarts around the actions); (b) true
  in-place update for `in_place` changes only. **Recommendation for Wave 2:
  option (a)** — apply performs a graceful stop-with-drain (invariant 7) →
  actions → restart, and the Diff's `restart` effect tells the operator up front.
  In-place live hot-swap is deferred to §4 hot-reload. Decide in review.
- **Partial apply + crash:** inherited `rollbackActions` covers in-process failure;
  a process crash *between* actions is the same exposure the existing provisioner
  has — call it out, and ensure apply is re-runnable (idempotent Build) so a
  re-`apply` reconciles to desired.
- **Token replay / TOCTOU:** the hash is recomputed at apply and compared; if
  current state changed between plan and apply, hash differs → refuse. (The window
  between recompute and execute is the residual TOCTOU — acceptable single-writer;
  note for the concurrent-apply case below.)
- **Concurrent applies:** two applies of the same pipeline — the second sees a
  changed hash (or should take a per-pipeline provisioning lock). Specify the lock.
- **Rollback failure:** existing code logs "state might be corrupted"; surface that
  as a non-zero, clearly-coded outcome rather than a silent warning.

## Risk tier & tests

**Tier 1** (data-path: creates/updates/deletes running pipelines). Requires: the
kill-mid-apply recovery test (AC-11), the rollback test (AC-9), the stale-token
test (AC-7), and human sign-off. Chaos/upgrade suites unaffected (no serialized
format change) — state so in the PR.

## Scope boundary

Wave 2 delivers: the preview engine (`Plan`/`Diff`/`Describe`), `deploy`+`apply`
with the hash token, restart-based apply with drain, and the in-place-vs-restart
classification **in the diff**. Deferred: true live in-place hot-swap execution
(§4 hot-reload), the MCP `deploy_pipeline` wrapper (Wave 3, wraps `Plan`/`ApplyPlan`
1:1), and `--remote`/multi-pipeline-file orchestration.

## Related

- Execution plan §2 (MCP `deploy_pipeline`), §3 (CLI verb parity), §4 (hot-reload).
- `20260707-cli-output-conventions.md`; `pkg/provisioning/import*.go` (the reused reconcile).
- Feeds MCP `deploy_pipeline` (Wave 3) and the hot-reload subsystem (§4).

## Review outcome (2026-07-08) — SOUND-WITH-CONCERNS (technical + UX, inline)

Verified against code:
- **Sound**: actions hold their config (`import_actions.go` create/update/delete structs), so
  `actionsBuilder.Build(old,new)` is a describable diff — the `Plan`/`Describe` preview is achievable without
  executing.
- **Tier-1 strengthened (confirmed gap, not just an open question):** `pipeline.Service.Update` (`service.go:155`) AND
  `Delete` (`:310`) have **no running-state guard**, and `importPipeline` does no stop/drain — the raw path would
  mutate/delete a *live* pipeline. So Wave-2 `apply` MUST stop-drain-restart (invariant 7) or refuse a running
  pipeline. **New AC-13:** `apply` never silently mutates a running pipeline — it either refuses with an actionable
  error or performs a graceful stop-with-drain → actions → restart; verified by a kill-mid-apply test asserting no
  record loss.
- **UX fixes (must-do):** the diff symbols (`+`/`~`/`-`) and the status-enum label render through the shared
  `cmd/conduit/internal/ui` helper with ASCII fallback (not hand-rolled); the "applying will RESTART pipeline X"
  warning is prominent before a destructive apply; the `--yes` confirm matches scaffold's behavior. Token model
  (recompute-at-apply, refuse on mismatch) is coherent.
