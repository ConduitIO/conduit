# Graceful shutdown on SIGTERM

## Problem

Conduit does not handle `SIGTERM`. `pkg/conduit/entrypoint.go` registers only
`os.Interrupt` (SIGINT):

```go
signal.Notify(signalChan, os.Interrupt)
```

`docker stop`, `kubectl delete pod`, and `systemctl stop` all deliver **SIGTERM**.
Because Conduit never installs a handler for it, SIGTERM gets Go's default
disposition — the process terminates immediately, running **no** shutdown code.
In-flight records are not drained and pipelines are not checkpointed on exit.

This violates data-integrity **invariant 7** ("Shutdown is graceful by default.
SIGTERM drains in-flight records and checkpoints before exit."). It is not silent
data loss — at-least-once delivery (invariant 3) plus crash-safe positions
(invariant 2) mean an abrupt SIGTERM is recovered on restart the same way a crash
is — but it produces duplicate re-delivery and unclean checkpoints on **every**
Kubernetes pod recycle or `docker stop`, and it contradicts a documented
guarantee. The bug is pre-existing (not introduced by v0.15.0).

## Constraints

- Must not change the SIGINT (Ctrl-C) behavior operators already rely on.
- Must reuse the existing graceful-shutdown path, not introduce a second one.
- `SIGKILL`/`SIGSTOP` cannot be caught (OS guarantee) — out of scope; recovery
  from a hard kill is already covered by invariants 2 and 3.
- Patch-sized: this ships as v0.15.1, so the change must be minimal and low-risk.

## Decision

Register `SIGTERM` alongside `SIGINT` in `CancelOnInterrupt`, so SIGTERM drives
the **same** graceful-shutdown path SIGINT already drives:

```go
signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
```

The existing semantics are preserved for both signals:

- **First signal** → cancel the root context → the runtime tomb starts dying →
  `registerCleanup`/`registerCleanupV2` runs `StopAll` (graceful), waits up to
  `exitTimeout` (30s), escalating to a forced stop only in the final interval,
  then closes the store.
- **Second signal** → hard `os.Exit` (the deliberate "stop now" escape hatch).

After this change, SIGTERM behaves exactly as SIGINT does today.

## Alternatives considered

1. **A dedicated SIGTERM handler with its own drain logic.** Rejected: it would
   create a second shutdown path to keep correct, and there is no requirement for
   SIGTERM to behave differently from SIGINT. Matching the existing path is
   simpler and strictly safer.
2. **A configurable termination grace period tied to `exitTimeout` surfaced as a
   flag/env var.** Deferred to the v0.16 12-factor work. `exitTimeout` (30s) is a
   sane default and already bounds the drain; exposing it is an enhancement, not
   part of fixing the invariant violation.

## Scope boundary (explicitly out of this change)

The forced-stop escalation in `registerCleanupV2` (`runtime.go:499-519`) stops
pipelines forcefully in the final `exitTimeout/6` interval regardless of whether
a checkpoint has completed, and `ForceStop` cancels the connector context without
asserting that no un-acked record was already forwarded downstream (an
invariant-1 adjacency). **This is a pre-existing property of the SIGINT path and
is not made worse by registering SIGTERM** — SIGTERM now simply reaches the same
code SIGINT already reached. Tightening force-stop ack-safety is a deeper
lifecycle change that needs its own analysis and a chaos harness; it is tracked
as a follow-up (see #2519, "related gaps") and slated for the v0.16 lifecycle
work, not this patch. Keeping v0.15.1 to the signal registration keeps it a small,
reviewable, obviously-correct fix.

## Failure modes

- **Drain exceeds `exitTimeout`.** The existing wait escalates to a forced stop and
  then exits; un-drained work is recovered on restart via at-least-once + crash-safe
  positions. Unchanged from today's SIGINT behavior.
- **SIGKILL during drain.** Cannot be caught; recovery is via invariants 2/3.
  Unchanged.
- **Second SIGTERM during drain.** Hard `os.Exit` — the intended escape hatch,
  identical to a second SIGINT.
- **Signal delivered before the handler goroutine is installed.** `Serve` installs
  the handler (via `CancelOnInterrupt`) before `runtime.Run`; a SIGTERM arriving in
  the narrow window before `signal.Notify` still gets the default disposition, same
  as any program — acceptable and unchanged.

## Upgrade / rollback

Behavior change: SIGTERM now drains instead of killing immediately. This is
strictly more graceful and backward-compatible — no config, API, or serialized
format changes. Rollback is reverting the one-line signal registration. No state
migration.

## Observability

The graceful path already logs shutdown progress ("waiting for pipelines to stop
running (time left: …)", "all pipelines stopped gracefully" / "some pipelines did
not stop in time"). After this change those logs appear for SIGTERM-initiated
shutdowns too, which previously produced no logs at all. No new metrics required
for the fix; a shutdown-duration metric is a candidate for the v0.16 12-factor work.

## Testing

- Regression test (`entrypoint_test.go`): `CancelOnInterrupt` returns a context
  that is canceled when the process receives SIGTERM (and, for safety, SIGINT) —
  proving the signal is caught and drives cancellation rather than terminating the
  process. Verified to fail (the test process is killed) without the registration.
- The broader SIGKILL/SIGTERM-mid-checkpoint chaos coverage that seeds
  `tests/chaos` is tracked with the force-stop follow-up above; it is not required
  to land this signal-registration fix.

## Related

- Issue #2519
- Phase 1 execution plan (`docs/design-documents/20260704-phase-1-execution-plan.md`), §0.1
- Data-integrity invariants 1, 2, 3, 7 (`CLAUDE.md`)
