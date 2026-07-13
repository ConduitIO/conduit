# `repair` edits the config file, never the store or a running pipeline

## Summary

`conduit pipelines repair` and the MCP `repair`/`repair_apply` tools apply a structured,
machine-appliable `conduiterr.Fix` to a pipeline configuration by editing an in-memory YAML node
tree and returning (or writing back) the repaired bytes. `repair` never opens the provisioning
store, never dials a running Conduit server, and never mutates a running pipeline — the only path
from a repaired file into a live engine remains `conduit pipelines deploy`/`apply`, unchanged.
This is an architectural boundary, not an incidental implementation detail, and future work on
`repair` (widening the v1 fix set, adding a store-aware fix class) must not cross it without a new
ADR.

## Context

The execution plan (`docs/design-documents/20260704-phase-1-execution-plan.md` §2, §3) calls for a
`repair` verb that applies a `ConduitError`'s structured `Fix` field, "sharing MCP `repair`'s
engine so the human isn't a second-class citizen to the agent," and explicitly requires that "agent
`repair` touching ack/position/checkpoint-adjacent config still requires the human Tier-1 sign-off
path" (§2). `docs/design-documents/20260712-repair-command.md` is the design doc that scoped the
feature; §11 ("Alternatives considered") records the store-vs-file decision made there and flags
that it should be promoted to an ADR once built. This is that ADR.

Two shapes were on the table for what "apply a fix" means:

1. **Apply directly to the pipeline store** (or a running pipeline via the API), the same way
   `deploy`/`apply` do — `repair` would call into `pkg/provisioning.Service` and inherit its
   plan-hash + running-pipeline guard machinery directly.
2. **Apply to the config file only**, as a text edit — `repair` never touches the store; getting a
   repaired file into a running engine stays `deploy`/`apply`'s job, which already owns that gate.

## Decision

`repair` is a **config file editor**, never a store/pipeline mutator (option 2). Concretely:

- `cmd/conduit/internal/repair.Collect`/`CollectContent` are pure reads: they run the existing
  offline `validate`/`lint` engine (`cmd/conduit/internal/validate`) and parse the file's own bytes
  into a `*yaml.Node` tree purely to render a diff. Neither function imports
  `pkg/provisioning`, `pkg/pipeline`, or any store/API client package — this is enforced
  structurally by the import graph, not by convention or a runtime check.
- `repair.Apply` performs its edits on that same in-memory node tree and returns the repaired
  bytes. It does not write to disk itself (design doc §4.1: "Writing to disk is the caller's
  choice"): the CLI command atomically rewrites the source file (temp + rename) only after `Apply`
  returns success; the MCP `repair_apply` tool returns the repaired content directly and writes
  nothing anywhere.
- The classifier (`classify`, `cmd/conduit/internal/repair/classify.go`) is default-deny over
  ack/position/checkpoint-adjacent `ConfigPath`s (connector `settings`, a connector's own
  `plugin`/`type`, DLQ config, any `id` field) — but this classification governs whether a fix is
  _offered for auto-apply to the file at all_, not whether it may reach a running pipeline. Even a
  `FixClassSafe`-classified fix, once written to the file, still has to pass through
  `deploy`/`apply`'s own plan-hash and Invariant-7 running-pipeline guard
  (`provisioning.CodePlanStale`, `provisioning.CodePipelineRunning`) before it affects anything
  live. `repair` does not shortcut, bypass, or duplicate that gate.

## Consequences

- **`repair` cannot, by construction, corrupt or reorder a record, or violate invariants 1-7**: it
  has no code path that reaches the data path at all. The worst a bad fix can do is write an
  incorrect config file, which `deploy`/`apply`'s own re-validation and running-pipeline refusal
  catch before anything live is affected — and `repair.Apply` itself re-validates the repaired
  bytes before returning success (AC-10 in the design doc), so a bad producer is visible in CI, not
  in a user's pipeline.
- **A repaired file is not itself a deployed change.** A human or agent that runs `repair --apply`
  still has to run `deploy`/`apply` (or the MCP `deploy`/`apply` tools) to get the change into a
  running Conduit — this is a deliberate two-step flow, not a missing feature. `repair` never grew
  a `--deploy`-after-apply convenience flag in v1; if that gap proves painful, it is a separate,
  explicit follow-up (composing two already-gated verbs, not a new gate).
- **This bounds what a future wider fix set can do without a new ADR.** A fix class that could only
  be applied correctly with knowledge of the live/stored pipeline state (e.g. the design doc's
  deferred connector-ID-rename fix, which needs to know whether a pipeline is new or already has a
  stored position) cannot be added to `repair` as currently architected without either (a) still
  routing through `deploy`/`apply`'s existing store access, unchanged, or (b) revisiting this
  decision explicitly. Silently reaching into the store from a future `repair` producer or the
  engine itself would violate the boundary this ADR records.

## Related

- `docs/design-documents/20260712-repair-command.md` — the design doc this ADR promotes §11's
  decision from; §4.3 ("What repair mutates, and what it does not") and §9 (failure mode 2) reason
  through the same boundary in more detail.
- `docs/design-documents/20260704-phase-1-execution-plan.md` §2, §3 — the source requirement.
- `docs/design-documents/20260708-cli-pipeline-deploy-apply.md` /
  `20260708-live-server-deploy-apply.md` — the plan-hash + running-pipeline gate `repair`
  deliberately does not duplicate.
- `cmd/conduit/internal/repair` — the implementation; see its package `doc.go` for the same
  invariant stated at the code level.
