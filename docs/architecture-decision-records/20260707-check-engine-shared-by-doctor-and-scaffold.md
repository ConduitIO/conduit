# Shared check engine for `doctor` and scaffolding preflight

## Summary

`conduit doctor` and the `connector|processor new` toolchain preflight both run a
set of pass/warn/fail diagnostics. They will share the check **engine and types**,
not their concrete checks. A neutral `pkg/conduit/check` package owns the `Check`
interface, `CheckResult`, the panic-isolating runner, and a few generic check
constructors; `doctor` and `scaffold` each supply their own check sets.

## Context

The v0.17 design docs for `doctor` and scaffolding both claimed the scaffold
preflight would "consume `pkg/conduit/doctor`'s toolchain checks as a subset." The
technical review proved that relationship does not exist and is incoherent as
written:

- `doctor`'s job is "would `conduit run` succeed here?" → its checks are
  config-resolve/validate, store reachability, API address bind, plugin-dir scan,
  builtin registry, engine reachability. **None are toolchain checks** — Conduit
  itself never needs Go/git/docker installed to run.
- The scaffold preflight's job is "can I build a new connector here?" → its checks
  are Go on PATH + min version, `GOPATH/bin` writable, git, docker. **None overlap
  with doctor's set.**

The two check _sets_ are disjoint by design. What is genuinely shareable is the
mechanism: the result type, the interface, the `recover()`-wrapped runner, the
human/JSON rendering, and the exit-code aggregation. Leaving this as "reconcile at
implementation" (both docs' hedge) would produce exactly the two-parallel-`doctor`-
implementations risk both docs claimed to avoid.

## Decision

1. A neutral package **`pkg/conduit/check`** owns the shared mechanism:
   - `Check` interface, `CheckResult` (fields per the CLI output conventions:
     `Name, Status, Message, Suggestion, Code, ConfigPath, Category`), `Status`
     (`pass|warn|fail`).
   - `Run(ctx, checks []Check) Report` with **per-check `recover()`** so a
     panicking check becomes a `fail` (`internal.error`), never a process crash.
   - `Report.ExitCode()` — aggregates N results to one exit code by synthesizing a
     `*conduiterr.ConduitError` per failing result and taking the **max** bucket via
     `exitcode.ExitCode` (environment 3 > validation 2 > runtime 1). This is new
     aggregation logic, owned here, not free reuse of the single-error classifier.
   - Generic, reusable check constructors that any caller can compose, e.g.
     `BinaryOnPath(name, minVersion)`, `DirWritable(path, configKey)`,
     `AddrBindable(addr, configKey)`.
   - The render/JSON helpers (via the shared `internal/ui` + the `--json` envelope).
2. **`doctor`** (`cmd/conduit/root/doctor` + its check set) imports `check` and
   supplies its runtime checks (config/store/network/plugins/engine).
3. **Scaffolding** (`pkg/scaffold`) imports `check` and supplies its toolchain
   checks (Go/git/docker), reusing `BinaryOnPath`/`DirWritable`.
4. A future **MCP `doctor` tool** wraps `doctor`'s check set via the same engine
   (the §2/§3 1:1 requirement).

### Sequencing

`pkg/conduit/check` ships **first** (it is a small, dependency-free package). Then
`doctor` and scaffolding are both consumers and can be built **in parallel** — no
"which command ships first" dependency remains, because neither depends on the
other, only on `check`.

## Consequences

- No duplicated runner/result/exit-code logic; one panic-isolation guarantee; one
  exit-code aggregation.
- `go-plugin`-dispensing checks (doctor's `--deep` standalone-compat) can panic in
  the library's own background goroutines, which a caller `recover()` cannot catch.
  The runner documents this limit; such checks run with a subprocess timeout and
  are opt-in, and the runner treats an abnormal child exit as a `fail` rather than
  relying solely on `recover()`.
- The `check` package must not import cobra/ecdysis or call `os.Exit` (so MCP and
  tests reuse it) — same discipline as `pkg/conduit/doctor` originally specified.

## Related

- `20260707-cli-doctor.md`, `20260707-connector-processor-scaffolding.md`
  (the consumers).
- `20260707-cli-output-conventions.md` (the result shape + exit-code rule).
- `pkg/conduit/exitcode`, `pkg/foundation/cerrors/conduiterr`.
