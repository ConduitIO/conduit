# `conduit quickstart` — the 5-minute wow

## Summary

A single command that scaffolds an ephemeral demo pipeline (built-in generator →
built-in log) and runs it in-process, so a brand-new user sees records flowing in
under a minute with **zero flags and zero footprint**. It leaves `conduit init` and
`conduit pipelines init` untouched. Tier 2 (additive CLI surface; no data-path or
protocol change).

## Context

The Phase-1 execution plan (§1.2) calls for a "5-minute wow": a first-run
experience where records visibly flow with no configuration. The plan's original
shape merged this into `conduit init` (deprecating `conduit pipelines init`); that
was reconsidered in #2535, which kept `init` (workspace) and `pipelines init`
(pipeline scaffold) separate. Rather than reverse that split, `quickstart` is a
dedicated, purely additive command: it delivers the wow without changing either
existing command.

## Decision

Add `conduit quickstart`. It:

1. Creates an **ephemeral** workspace — a temp directory for the pipeline config and
   an **in-memory** state store (`db.type=inmemory`). Nothing is written to the
   user's working directory; the temp dir is removed on graceful exit.
2. Writes a demo pipeline: **`builtin:generator`** (structured sample records at
   1/s) → **`builtin:log`** (records printed to the console). Both are compiled-in,
   so there is nothing to install.
3. Runs it in the foreground via the **same `Entrypoint.Serve` path as
   `conduit run`** — no divergent run logic. SIGINT/SIGTERM drains and exits
   gracefully (invariant 7); a second signal hard-exits.
4. Enables the HTTP/gRPC API on the default addresses and prints the URLs, so the
   user can immediately explore `/readyz`, `/metrics`, and `/openapi`.
5. Supports `--json`, which sets the log format to JSON so the streamed records and
   logs are machine-consumable.

The demo generator settings are the same known-good ones `conduit pipelines init`
uses for its no-flag demo (a flight-record shape), so the two stay consistent.

## Alternatives

- **Merge into `conduit init` (the plan's original shape).** Rejected: reverses the
  #2535 decision and deprecates `pipelines init` — more churn and user-facing
  breakage than a new additive command, for no extra capability.
- **`conduit pipelines init --run`.** Rejected: overloads a scaffolding command with
  run-and-block semantics, and couples the wow to the workspace layout. Quickstart's
  ephemeral, zero-footprint nature is a distinct concern.

## Failure modes

- **Port collision** (8080/8084 already bound, e.g. another Conduit running).
  `NewRuntime`/`Serve` surfaces an actionable bind error and exits non-zero. v1 uses
  the default ports for predictability; automatic free-port selection is a noted
  future enhancement.
- **`kill -9` / second ctrl-C.** `Serve` calls `os.Exit`, skipping temp cleanup — the
  temp dir leaks into the OS temp location (reclaimed by the OS). The in-memory store
  means there is no state to corrupt. A single, graceful ctrl-C returns from `Serve`
  and cleans up.
- **Missing plugins.** generator and log are built-in — always present.
- **Existing `conduit.yaml` in the cwd.** Irrelevant: quickstart uses its own temp
  workspace and never reads cwd configuration.

## Rollback / compatibility

Purely additive: a new command package plus one entry in the root command's
subcommand list. No serialized formats, no protocol, no changes to existing
commands. Rollback is deleting the package and the registration.

## Observability

Records are visible in the console via the log destination. The API exposes
`/readyz`, `/metrics`, and `/openapi`. `--json` makes the stream machine-readable.

## Related

- Execution plan §1.2 (the 5-minute wow).
- Reuses `builtin:generator`, `builtin:log`, and `pkg/conduit` `Entrypoint.Serve`.
- Bundled-template-manifest schema (§1.2, registry) is a separate follow-up; the
  quickstart demo is a self-contained embedded config for now.
