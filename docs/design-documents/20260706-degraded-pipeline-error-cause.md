# Correct error cause for a degraded pipeline (#1659)

## Summary

When a source connector errors, a running pipeline can degrade with a useless
`io.EOF` ("failed to forward ack to source connector") instead of the connector's
actual error, because the error the user sees is decided by a race between two
nodes. This is a real observability defect on the data path — the API, UI, and
logs report the wrong cause — and it is why `TestServiceLifecycle_PipelineError`
is skipped in both `pkg/lifecycle` and `pkg/lifecycle-poc`. This doc proposes
making the source connector's real error authoritative, with a fallback, and
enumerates the failure modes. Tier 1 (data-path error semantics); needs sign-off.

## Context — how a pipeline's degraded error is chosen

Each node runs in the pipeline tomb: `rp.t.Go(func() error { … node.Run(ctx) … })`
(`pkg/lifecycle/service.go` `runPipeline`). **`tomb.v2` records only the _first_
non-nil error as the death reason** (`rp.t.Err()`), and the degraded status stores
exactly that: `UpdateStatus(…, StatusDegraded, fmt.Sprintf("%+v", rp.t.Err()))`.
So the pipeline's reported cause = whichever node returns an error first.

## Problem (the race)

When a source plugin's `Run` returns an error, the bidirectional plugin stream
closes. The stream is asymmetric by design: `Recv` (used by `Source.Read`) returns
the server's real `Close(reason)` value, while `Send` (used to forward acks to the
source) returns a bare `io.EOF` (`ErrStreamNotOpen = io.EOF`,
`pkg/plugin/connector/errors.go:22`; `builtin/stream.go` `Send`). Two tomb jobs
then fail, in a nondeterministic order:

- **`SourceNode.Run`** — its next `Source.Read` returns the connector's **real
  error** → returns `"error reading from source: <real error>"` (source.go:88-90).
- **`DestinationAckerNode.Run`** — this is the subtle part. `SourceAckerNode.Run`
  does **not** call `Source.Ack` in its own goroutine; it only _registers_ ack/nack
  handler closures on each message (`source_acker.go` `registerAckHandler`/
  `registerNackHandler`). Those closures run later, when `msg.Ack()`/`msg.Nack()`
  is actually called — for a normal record that call site is
  `DestinationAckerNode.worker` → `handleAck` → `msg.Ack()`
  (`destination_acker.go:189`), which runs the registered closure that calls
  `Source.Ack` → gets `io.EOF`. The error propagates out of
  `DestinationAckerNode.Run`, so the tomb job that fails is the **destination
  acker**, and the reported string is
  `"node <destination-acker-id> stopped with error: … failed to forward ack to
  source connector: io.EOF"`.

If the destination-acker job wins the race to the tomb, the degraded pipeline
reports `io.EOF` — telling the user only that the stream closed, not _why_. The
real cause is logged but appears nowhere the user looks (API/UI). Reproduced by
`TestServiceLifecycle_PipelineError` (skipped, #1659), whose assertion expects the
tomb error to come from the **source** node — i.e. the fixed state has only
`SourceNode` producing a tomb error.

The `io.EOF` is always a **consequence** of the source stopping — it carries no
independent information — yet it can mask the root cause. Note the fix therefore
lives in the ack/nack handler closures in `source_acker.go` (which call
`Source.Ack`), regardless of which goroutine ends up executing them.

## Constraints

1. The degraded cause must be the connector's real error whenever that error
   exists, deterministically.
2. Do not change graceful-stop (`ErrGracefulShutdown` → `StatusUserStopped`) or
   force-stop (`ErrForceStop` → `StatusDegraded`) outcomes.
3. At-least-once (invariant 3) must hold: suppressing the acker's stream-closed
   error must not drop a record or skip a needed nack.
4. No connector-protocol change (breaking-change territory) if avoidable.

## Alternatives

### Option A — Aggregate all node errors, pick the root cause (rejected as primary)

Stop relying on the tomb's first-error; have each node goroutine record its error
into a shared collection, and select the "most meaningful" one (drop `io.EOF` /
`context.Canceled` derived errors in favor of a real cause).

- **Why it loses (as primary):** it replaces the core tomb-based error mechanism on
  the data path with a custom aggregator + a ranking heuristic — a large, risky
  change for a targeted defect, and the heuristic ("which error is the real one")
  is exactly the ambiguity we're trying to remove. Keep as a fallback idea only.

### Option B — Capture the source error out-of-band and substitute it (rejected)

Record the source connector's error (from `Source.Errors()`) separately and, at
degradation, if the tomb reason is a stream-closed error, swap in the captured
source error.

- **Why it loses:** introduces a second error-tracking path and a substitution
  step with its own races (was the source error captured yet when we substitute?);
  more state and ordering to get right than Option C.

### Option C — The derived node suppresses its stream-closed error (recommended)

The code that _knows_ its error is derived suppresses it. The `Source.Ack` call
lives in the ack/nack handler closures registered in `source_acker.go`
(`registerAckHandler` **and** `registerNackHandler` — both must be handled). When
that `Source.Ack` fails with the stream-closed sentinel (`errors.Is(err, io.EOF)`;
`ErrStreamNotOpen` is that same value), the closure does **not** propagate it as
the pipeline cause — it treats the ack-forward as a no-op (the record is already
durably handled downstream; see failure modes) so the error never reaches the tomb
via `DestinationAckerNode.Run`. The `SourceNode`'s real `Read` error then becomes
the tomb's death reason deterministically: with the derived `io.EOF` removed as an
independent death source, the only remaining first-death in this scenario is the
source's real error.

**Scope the suppression narrowly:** only the stream-closed sentinel from the
`Source.Ack` forwarding call is suppressed — never a different `Source.Ack` error,
and never `base.Send`/downstream errors. In particular `SourceAckerNode.Run`'s own
inline `base.Send(...)` → `msg.Nack(...)` path (source_acker.go:72-75) runs the
nack closure synchronously in `SourceAckerNode`'s goroutine; a genuine `base.Send`
failure there must still return a non-nil error even though the nack-forward may
also hit `io.EOF` (see failure modes).

- **Why it wins:** minimal, local, principled — the acker's `io.EOF` genuinely
  carries no cause; the source's error is authoritative by construction; no change
  to the tomb mechanism, graceful/force-stop paths, or the protocol.
- **Fallback:** if the connector died so abruptly that even `Source.Read` only
  yields `io.EOF` (no real error on `Errors()`), the pipeline still degrades with
  `io.EOF` — unavoidable, since the connector gave no better reason. We report the
  best available cause, never worse than today.

## Decision

Adopt **Option C**. Suppress the `SourceAckerNode`'s stream-closed (`io.EOF` /
plugin "stream closed") error from the pipeline cause, letting the `SourceNode`'s
real error win, with the `io.EOF` fallback when no real error exists. Apply the
same fix to the `pkg/lifecycle-poc` mirror. Un-skip
`TestServiceLifecycle_PipelineError` in both.

## Failure modes

- **Source reports no real error (only `io.EOF`).** Degrade with `io.EOF` — the
  documented fallback; no worse than today.
- **At-least-once (invariant 3) — confirmed safe by trace.** `connector.Source.Ack`
  (`connector/source.go:207-235`) calls `s.stream.Send(...)` and **returns on its
  error before it mutates `s.Instance.State` or calls `persister.Persist`**. So a
  suppressed `io.EOF` from that `Send` cannot advance or persist the checkpoint past
  an unacked record — the position simply doesn't move for that record. And the ack
  only fires _after_ the destination durably wrote (`destination_acker.go` worker →
  `handleAck`), so the record was already delivered. On restart the source resumes
  from its last persisted position and re-reads (at-least-once, possible
  duplicates) — the floor, not a violation. `DestinationAckerNode.teardown` still
  nacks genuinely-in-flight messages and is untouched by this change. **Still a hard
  gate: this must be verified in a kill/chaos test, not assumed — and since
  `tests/chaos` is not live yet (process-maturity table), that test is written and
  run manually as a precondition for calling this done.**
- **Inline `SourceAckerNode.Run` `base.Send` → `Nack` path (source_acker.go:72-75).**
  Here the nack closure runs synchronously in `SourceAckerNode`'s own goroutine. If
  `base.Send` genuinely fails (e.g. ctx canceled forwarding downstream) and the
  nack-forward to the source _also_ hits `io.EOF`, a blanket suppression would make
  `msg.Nack(err, n.ID())` return `nil` and hide the real `base.Send` failure. The
  suppression must not swallow this: `SourceAckerNode.Run` must still return non-nil
  when `base.Send` itself failed. Cover with a dedicated test that fails `base.Send`
  concurrently with a stream-closed nack and asserts a non-nil return.
- **Destination-side error masked the same way.** Out of scope here (this fix is
  the source/acker race), but note whether a destination `io.EOF` can similarly
  mask a real destination error; if so, file a follow-up — do not silently widen
  Option C to the destination without its own analysis.
- **Graceful stop.** `ErrGracefulShutdown` is already special-cased to `nil` before
  the tomb; unaffected. Confirm suppressing acker `io.EOF` doesn't turn a real
  error into a graceful-looking `nil` (it can't — the source error still kills the
  tomb).
- **Force-stop.** `ErrForceStop` is a fatal tomb kill issued directly by
  `stopForceful`; it already wins. Unaffected.
- **Suppression hides a genuine acker bug.** If `Source.Ack` fails for a reason
  other than stream-closed (e.g. a real ack protocol error), that is NOT `io.EOF`
  and must still surface. Scope the suppression narrowly to the stream-closed
  sentinels, not all `Source.Ack` errors.

## Test / verification strategy

- Un-skip `TestServiceLifecycle_PipelineError` (`pkg/lifecycle` and
  `pkg/lifecycle-poc`); it must assert the degraded cause is the real
  `"source connector error"`, not `io.EOF`, and pass under `-race -count=200`
  (the race is the whole point — determinism is the bar).
- A focused test that forces the acker to lose/win the race (inject an `io.EOF` on
  `Source.Ack` while the source returns a real error) and asserts the real error
  wins in both orderings — covering both the ack and nack handler closures.
- A test for the inline path (failure mode above): fail `base.Send` in
  `SourceAckerNode.Run` concurrently with a stream-closed nack, and assert
  `SourceAckerNode.Run` still returns a non-nil error (the suppression must not hide
  a genuine `base.Send` failure).
- A kill/chaos test confirming no record is dropped when the acker's stream-closed
  error is suppressed (invariant 3) — a hard precondition, run manually since
  `tests/chaos` is not yet live.

## Rollback / compatibility

No serialized formats, no protocol, no public API changes — only which error
string a degraded pipeline reports (strictly more accurate). Rollback is reverting
the diff and re-skipping the test.

## Related

- #1659 — the bug and the two skipped tests.
- `20260706-forceful-stop-test-determinism.md`, `20260706-...` (sibling lifecycle
  correctness work this session).
- Invariant 3 (at-least-once) governs the acker-suppression failure mode.
