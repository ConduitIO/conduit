# Deterministic CLI exit codes

## Summary

Every `conduit` process exit — one-shot CLI commands and the long-running `conduit run` — goes
through a single classifier (`pkg/conduit/exitcode`) that maps an error to one of four
process exit codes: `0` success, `1` runtime/unclassified, `2` validation, `3` environment. A
forced double-signal kill of `conduit run` now reports the POSIX `128+signum` convention
(`SIGINT` → `130`, `SIGTERM` → `143`) instead of a fixed value, which **changes that exit code
from `2` to `130`/`143`** — a breaking change, documented below.

## Context

Before this change, `conduit`'s exit codes were accidental, not designed:

- `cmd/conduit/cli/cli.go` exited `1` for any `cmd.Execute()` error and `0` otherwise — every
  failure, from "pipeline not found" to "the server is unreachable," looked identical to a
  script.
- `pkg/conduit/entrypoint.go` exited `1` (`exitCodeErr`) for any `conduit run` setup or runtime
  failure, and `2` (`exitCodeInterrupt`) for a **second** termination signal (a forced kill after
  a graceful shutdown was already in progress) — an exit code chosen arbitrarily, not from any
  convention.
- Errors carried no stable classification a CLI-boundary function could switch on for most of
  the codebase. That gap is being closed independently by the `conduiterr` package (see
  `docs/design-documents/20260705-conduit-error-and-structured-output.md`), which gives some
  errors a stable `Code` with an explicit gRPC category, but most call sites are not migrated yet.

Phase 1 execution plan §1.1 (`docs/design-documents/20260704-phase-1-execution-plan.md`) commits
to "documented deterministic exit codes (0 ok · 1 runtime · 2 config/validation · 3
environment)" as one of the structured-output acceptance criteria. Landing that classifier
requires deciding, concretely:

- What does "2 config/validation" mean in terms of gRPC status categories, given most errors
  aren't `conduiterr`-tagged yet?
- Where does `3 environment` get its signal, given almost nothing is tagged as an environment
  failure today?
- The existing "second signal" hard-exit already uses `2`. Freeing `2` for the
  config/validation bucket means the hard-exit code must move — to what, and is that safe to
  change?
- One-shot commands (talk to a running server over gRPC) and `conduit run` (the process that
  starts the server) fail in different shapes. Can they share one classifier without one of them
  becoming a lie?

## Decision

### One classifier, `pkg/conduit/exitcode.ExitCode(err error) int`

A new leaf package, `pkg/conduit/exitcode`, is the single source of truth. It is imported by
both `cmd/conduit/cli` (one-shot commands) and `pkg/conduit` (the `run` entrypoint), so the two
surfaces cannot drift onto different conventions for the same kind of error. It has no
dependency on either caller, only on `pkg/foundation/cerrors` and
`pkg/foundation/cerrors/conduiterr`, so importing it introduces no cycle.

Classification order:

1. `err == nil` or `errors.Is(err, context.Canceled)` → `0` (OK). Canceled covers the graceful
   `conduit run` shutdown path: the tomb's context is canceled on the first SIGINT/SIGTERM, and
   `Runtime.Run` returning `context.Canceled` is success, not failure.
2. A `*conduiterr.ConduitError` anywhere in `err`'s chain (found via `conduiterr.Get`, which uses
   `errors.As` and so sees through any number of `cerrors.Errorf("...: %w", err)` wraps) →
   classified by its registered gRPC category.
3. Otherwise, a raw gRPC client error (`google.golang.org/grpc/status.FromError`) → classified
   the same way. This step exists because a `ConduitError` a server raises crosses the gRPC
   boundary with its category set on the **top-level** status code
   (`conduiterr.ToStatus`/`status.New(e.Code.GRPCCode(), ...)`), but no CLI call site decodes the
   `ErrorInfo` detail back into a `*ConduitError` today (`conduiterr.FromStatus` is unused outside
   its own tests) — so most server-raised errors reach the CLI as a plain `*status.Error`, not a
   `ConduitError`. Skipping this step would silently misclassify nearly every `NotFound`/
   `FailedPrecondition` a real command returns.
4. Otherwise, a narrow OS-level sentinel check (`syscall.ECONNREFUSED`, `syscall.EADDRINUSE`) →
   `3` (environment). A deliberately small safety net, not a general network-error classifier —
   see "Coverage is intentionally partial" below.
5. Otherwise → `1` (runtime), the catch-all.

gRPC category → bucket mapping (`fromGRPCCode`):

| Bucket             | Code | gRPC categories                                                                 |
| ------------------ | ---- | -------------------------------------------------------------------------------- |
| OK                 | `0`  | `OK`, `Canceled`                                                                  |
| Runtime            | `1`  | `Internal`, `Unknown`, `DataLoss`, `Aborted`, `Unimplemented`                     |
| Validation         | `2`  | `InvalidArgument`, `NotFound`, `AlreadyExists`, `FailedPrecondition`, `OutOfRange` |
| Environment        | `3`  | `Unavailable`, `DeadlineExceeded`, `ResourceExhausted`, `Unauthenticated`, `PermissionDenied` |

`FailedPrecondition` and `NotFound` land in Validation (`2`), not Runtime: both mean the request
was rejected because of the state of the world the caller controls (a pipeline is already
running, a referenced instance doesn't exist), which is a validation-shaped failure from the
caller's point of view, not an internal bug or an unreachable dependency.

### Coverage is intentionally partial — tag only the high-value environment cases now

Almost nothing in the codebase is tagged as an environment error today. Rather than block this
change on migrating every relevant call site (explicitly out of scope per the incremental,
boundary-first rollout in the structured-output design doc), this change tags only the highest-
value cases, each via a new `conduiterr.CodeUnavailable` (`common.unavailable`, gRPC
`Unavailable`):

- **Server unreachable**: `cmd/conduit/api/client.go` `CheckHealth` — the error every client
  command returns when `conduit run` isn't up.
- **Bind-in-use**: `pkg/conduit/runtime.go` `serveGRPC`/`serveHTTP` — `net.ListenConfig.Listen`
  failing because the configured address is already bound.
- **Database unreachable**: `pkg/conduit/runtime.go` `NewRuntime` — `badger.New`/`postgres.New`/
  `sqlite.New` failing to open/dial the configured database. (An *invalid* DB type, a
  config/validation problem, is deliberately **not** tagged Unavailable — it returns directly as
  a plain error, unchanged from before this PR, so it still exits `1`, matching prior behavior.)

Everywhere else, an untagged error still classifies as Runtime (`1`) — the same as every
`conduit run` failure exited before this change. That is not a regression; it is the explicit,
documented starting point. Coverage grows as more boundaries adopt `conduiterr` tagging, without
this package's contract changing.

The new `conduiterr.CodeUnavailable` lives in a **new file**
(`pkg/foundation/cerrors/conduiterr/environment.go`), not an edit to the package's existing
files: a parallel change is migrating `conduiterr`'s codes and `status.go`, so keeping this
addition purely additive avoids a merge conflict with that work.

### The second-signal hard exit moves off `2`, onto POSIX `128+signum`

`pkg/conduit/entrypoint.go`'s `cancelOnSignal` used exit code `2` for a forced kill (a second
SIGINT/SIGTERM received after the first one already started a graceful shutdown). That value now
belongs to the Validation bucket, so a forced kill needed to move. It moves to the POSIX
convention shells already use for "killed by signal N" — `128+N` — rather than to another
arbitrary constant:

- `SIGINT` (2) → `130`
- `SIGTERM` (15) → `143`

This is picked over reusing `1` (indistinguishable from an ordinary runtime error) or inventing
a fifth bucket (loses the standard meaning scripts already know for `128+signum`). It also
deliberately does **not** route through `exitcode.ExitCode`: a forced double-signal kill isn't a
classified error at all, it's "the operator asked twice, so we stopped waiting" — a different
kind of thing than "the command failed for reason X."

`cancelOnSignal` is changed to capture the second signal's actual value (previously discarded;
only its arrival mattered) and derive the code from it via `syscall.Signal`, instead of an
`exitCodeInterrupt` constant.

### One-shot commands and `run` share the classifier, not the call sites

`cmd/conduit/cli/cli.go`'s `Run` and `pkg/conduit/entrypoint.go`'s `exitWithError` both call
`exitcode.ExitCode(err)` directly; neither reimplements or duplicates the classification logic.
This is what makes "validation/not-found/precondition errors exit `2` from both a one-shot
command and the `run`/provisioning path" true by construction rather than by two independently
maintained switch statements staying in sync.

## Consequences

- **Breaking change: the second-signal `conduit run` exit code changes from `2` to `130`
  (SIGINT) or `143` (SIGTERM).** Any script or supervisor (systemd, Docker healthchecks, a CI
  job) that specifically checked for exit code `2` on a forced Conduit kill must be updated. This
  is the only behavior change to an existing, documented exit code; `1` (default failure) and `0`
  (success) are unchanged for every case that produced them before.
- One-shot commands gain non-`1` exit codes for the first time (previously always `0` or `1`).
  This is additive for scripts that only checked "zero vs. nonzero," and new information for
  anything more specific.
- `pkg/conduit/exitcode` becomes the place every future CLI/`run` boundary must route errors
  through to get a correct exit code; a boundary that calls `os.Exit` directly instead would
  silently bypass the classifier. There is no static enforcement of this yet (matching the
  `conduiterr` CI-guard's own current scope — boundary-only, not repo-wide).
- The environment bucket (`3`) is under-covered by design today. A real environment failure that
  isn't one of the three tagged call sites, and doesn't hit the `ECONNREFUSED`/`EADDRINUSE`
  sentinel check, still exits `1`. This is visible and intentional, not silently wrong: `1` was
  the exit code for every `conduit run` failure before this change, so nothing regresses; it
  simply doesn't yet get the more specific `3`.

## Related

- Phase 1 execution plan §1.1 (`docs/design-documents/20260704-phase-1-execution-plan.md`) —
  "documented deterministic exit codes (0 ok · 1 runtime · 2 config/validation · 3 environment)"
  acceptance criterion this ADR satisfies.
- `docs/design-documents/20260705-conduit-error-and-structured-output.md` — the `ConduitError`/
  `conduiterr` model this classifier's first two tiers depend on, and the incremental,
  boundary-first migration this ADR's "coverage is intentionally partial" section follows.
- `pkg/conduit/exitcode` — the implementation.
- `pkg/foundation/cerrors/conduiterr/environment.go` — the new, additive `CodeUnavailable`
  registration.
- README "Exit codes" section — the user-facing mapping table.
