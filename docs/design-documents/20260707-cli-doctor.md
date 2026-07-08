# CLI: `conduit doctor` — preflight diagnostics

## Summary

A pre-flight, works-offline diagnostic answering "is this machine/config/toolchain
set up so `conduit run` would succeed?" — distinct from the runtime
`/readyz`+`/healthz` (which answer "is the running engine serving?",
`pkg/conduit/readyz.go`). It does NOT boot a Runtime; it factors out the exact
primitives `NewRuntime` uses (config validate, DB open+ping, address bind, plugin
scan) and runs each as an isolated, non-fatal probe. Tier 2 (low–medium risk).

## Decision — a reusable check engine (CLI + MCP share it)

Introduce a check engine in a neutral package that both the CLI command and a
future MCP `doctor` tool (§3: "rename MCP `diagnose` → `doctor`") import 1:1. The
CLI is a thin renderer over `[]CheckResult`; MCP wraps the identical results.

- `pkg/conduit/doctor/` (new) — no CLI/ecdysis imports, no `os.Exit`:
  `Check` interface, `CheckResult`, `Status`, `Run(ctx,cfg,opts) Report`, the
  concrete checks (`DefaultChecks(cfg) []Check`), and `Report.ExitCode()`.
- `cmd/conduit/root/doctor/doctor.go` — ecdysis command; wired into `root.go`.

```go
type CheckResult struct { // fields map 1:1 onto conduiterr.ConduitError (§1.1)
    Name, Message string
    Status        Status // pass | warn | fail
    Suggestion, Code, ConfigPath, Category string
}
type Check interface { Name() string; Run(ctx, cfg conduit.Config) CheckResult }
```

`Run` wraps each check in `recover()` → a panicking/erroring check becomes a `fail`
(`code: internal.error`), never a crash.

## The checks (each reuses existing machinery — no boot duplication)

| Check | Reuses | pass / warn / fail | Exit bucket |
| --- | --- | --- | --- |
| `config.resolve` | ecdysis `ParseConfig` | found+parsed / defaults-used / unparseable | 2 on fail |
| `config.validate` | `Config.Validate()` (`config.go:286`, same call `NewRuntime` makes) | valid / — / bad field (`configPath`) | 2 |
| `store.reachable` | DB-open switch (`runtime.go:132-167`) + `DB.Ping` → close | open+ping / inmemory (warn) / cannot open | **3** (env) |
| `network.grpc/http` | `net.ListenConfig.Listen` (`runtime.go:795,836`) → close | free / disabled / EADDRINUSE | **3** |
| `plugins.connectors/processors_dir` | `os.ReadDir` scan (`standalone/registry.go:96`) | readable / missing (warn) / is-a-file | — |
| `plugins.standalone_compat` (`--deep`) | dispense over go-plugin (`registry.go:168`) | valid spec / empty (warn) / handshake fail | 1 |
| `plugins.builtin` | `DefaultBuiltinConnectors/Processors` maps | non-empty / — / empty | 1 |
| `engine.reachable` | `api.Client.CheckHealth` (`client.go:61`) | SERVING / no server (warn) | 3 w/ `--require-server` |

The `store`/`network` fails inherit exit **3** from `exitcode.ExitCode` because
they carry `CodeUnavailable` — "unreachable DB = environment" falls out of the
shared classifier, not a re-decision.

## CLI UX & ergonomics

```console
$ conduit doctor
Conduit doctor — checking your environment

Config
  ✓ config.resolve      conduit.yaml found at ./conduit.yaml
  ✓ config.validate     configuration is valid
Store
  ✗ store.reachable     cannot open badger store at ./conduit.db
                        └ db.badger.path
                        → Check the directory exists and is writable, or set db.badger.path.
Network
  ✗ network.grpc        address :8084 is already in use
                        └ api.grpc.address
                        → Stop the process holding it, or set api.grpc.address to a free port.
Plugins
  ⚠ plugins.connectors  ./connectors not found — built-ins still available
  ✓ plugins.builtin     42 built-in connectors, 12 processors available

Summary: 4 passed · 2 warnings · 2 failed
2 checks failed. Fix the ✗ items above, then re-run `conduit doctor`.
```

Grouped by category; glyphs `✓`/`⚠`/`✗` (ASCII `[OK]`/`[!]`/`[X]` when not a TTY /
`--no-color`). Every `✗` carries the failing config path (`└`) + suggestion (`→`).

`--json`: `{checks:[{name,status,message,suggestion?,code?,configPath?,category}],
summary:{pass,warn,fail}}`.

Exit codes: all pass or pass+warn → **0** (warnings never fail the process); any
fail → non-zero bucketed by the worst failing check's code via `exitcode.ExitCode`,
precedence environment(3) > validation(2) > runtime(1).

Flags: `--json`, `--check <name>` (repeatable, filters checks), `--deep`
(subprocess checks), `--require-server`. `--help` documents preflight-vs-`/readyz`
and the exit codes.

## File-level plan

- New: `pkg/conduit/doctor/{doctor,checks,report}.go`; `cmd/conduit/root/doctor/doctor.go`.
- Refactor (optional, recommended): extract the DB-open switch from
  `runtime.go:132-167` into a shared `openStore(cfg)` so C3 and `NewRuntime` share
  one path. If too invasive for this slice, replicate + `// keep in sync` TODO.
- Edit: one line in `root.go` `SubCommands()`; docs + `llms.txt`.
- Works fully offline; only `engine.reachable` consults a server, warn by default.

## Acceptance criteria

1. every check returns a defined status (pass/warn/fail); none empty.
2. bad DB path → C3 `fail`, `code=common.unavailable`, `configPath=db.badger.path`,
   `ExitCode()==3`.
3. port in use → C4 `fail` (EADDRINUSE), suggestion names the port, exit 3.
4. missing plugin dir → `warn` (not fail); if no other fails, exit 0.
5. invalid config field (`log.level=bogus`) → `fail`, `common.invalid_argument`, exit 2.
6. `--json` matches schema; counts == rendered summary.
7. all-good → exit 0.
8. `--check <name>` runs exactly one; unknown name errors listing valid names.
9. a panicking check → reported as `fail`/`internal.error`, process exits cleanly.
10. engine reusable by MCP — a test in `pkg/conduit/doctor` (no cmd/ecdysis import)
    calls `Run` and asserts `[]CheckResult`.
11. no side effects — no DB files/dirs left behind after running.

## Failure modes & risk

- self-erroring/slow/hanging check → `recover()` + per-check `context.WithTimeout`
  → `fail`, never a hang or crash.
- CI has no `./connectors` → deliberately `warn`, exit 0 (not noise).
- subprocess cost (`--deep`) gated off by default.
- **Cross-plan note:** the scaffolding plan defines a `pkg/scaffold/doctor`
  toolchain preflight — that should be a SUBSET consuming this engine's toolchain
  checks, not a parallel implementation. Reconcile at implementation.
- Risk tier low–medium; the only edit to existing code is the optional `openStore`
  extraction (medium) + one line in `root.go`.

## Optimization

Reuse `Config.Validate()`, the runtime DB-open + bind paths, the standalone scan,
the builtin maps, and `exitcode.ExitCode` — zero reimplementation of boot logic.
`CheckResult` maps 1:1 onto `conduiterr.ConduitError` so CLI/`--json`/MCP speak the
one §1.1 result model. Factor `openStore` so a future `NewRuntime` could even run
`doctor` checks as its own preflight.

## Related

- Execution plan §3. `pkg/conduit/readyz.go` (runtime counterpart).
- `pkg/conduit/exitcode`. Shared by the future MCP `doctor` tool (§2).
- Scaffolding design doc (shares the toolchain-preflight subset).

## Review outcome (2026-07-07) — SOUND-WITH-CONCERNS

Must-fix during implementation:

- **`Report.ExitCode()` (N results → 1 exit code) is NEW aggregation, not free reuse**
  — take the MAX bucket, not the first. Now owned by the shared `pkg/conduit/check`
  engine (see ADR `20260707-check-engine-shared-by-doctor-and-scaffold.md`).
- The engine/types move to neutral `pkg/conduit/check`; doctor supplies its check
  SET. Toolchain checks are NOT in doctor's set (disjoint from scaffold — see ADR).
- `openStore(cfg, logger)` — signature needs the logger (won't compile without it).
- `recover()` cannot catch go-plugin's background goroutines; gate `--deep`, use a
  subprocess timeout, treat abnormal child exit as `fail`.
- `CheckHealth` can't distinguish "no server" from "unhealthy" (both `Unavailable`)
  — binary pass/fail only for now; footnote it. Mockup counts (42/12) are
  illustrative; real defaults are 6 connectors / 27 processors.
- Adopt the shared output conventions.
