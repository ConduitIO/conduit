# arch-v2 fan-out: a nacked position is released only after every branch has voted

## Summary

Supersedes one clause of
[`20260731-archv2-fanout-ack-model`](20260731-archv2-fanout-ack-model.md): a position that a branch
nacks is no longer terminal "the instant any branch nacks it". Nack still wins (the first nack decides
the outcome and the DLQ attribution), but `multiAckNacker` releases the position to the parent
(`Worker.Nack`: DLQ write, then `Source.Ack`) only once all M branches have voted for it, exactly like an
ack. The rest of that ADR (per-position tally, unanimity for acks, in-source-order release) stands.

## Context

The 2026-07-31 decision predates engine-level destination batching (#3201, v0.21.0). Without batching,
every branch had voted for every position of batch N before the worker read batch N+1, so releasing a
nack early could not overtake anything. With batching, a branch can return from batch N with its records
buffered and no vote cast. If a sibling then nacked a record of batch N+1, nack-wins released it at once:
the DLQ write and `Source.Ack` ran while the batching destination still held batch N unwritten.
`Source.Ack` persists the last position it is given, so a crash at that point resumed after batch N+1
and the batching destination never received batch N (invariants 1 and 3, #3323).

The ack path never had this problem: an ack needs every branch's vote, every branch votes in source order
(fan-out is synchronous, and a batching destination acks its pending batches in order), so the last vote
for any position of batch N+1 comes after the last vote for every position of batch N.

## Decision

Count ack and nack votes together. A position becomes terminal when it has M votes. Its outcome is nack if
any vote was a nack, and the first nack's record, error and task ID are used for the DLQ entry.

Each branch votes through its own view of the tally, which records which branches have voted for each
position. A second vote from the same branch is refused with a `(bug)` error and not counted, whether or not
the other branches have voted yet: counting it could release a position without a sibling's vote.

A release to the parent that fails is final. Every later vote returns that error (wrapped, so the fatal
classification holds) and the parent is not called again, so a sibling still running cannot retry a
`parent.Nack` that may already have written the DLQ entry.

## Alternatives considered

- **Order the nack behind the batching siblings** (`ackBehind(batchingDestinations(fanNode))`, the
  mechanism #3294/#3300 use for records that bypass the destinations). Rejected: it has to queue on every
  batching destination of the fan-out, including the nacking one, which may be inside its own flush holding
  `syncMu` with work outstanding. That relies on the `releaseSync` re-check for liveness and adds lock
  surface to Tier 1 code for the same result.
- **Pipeline the fan-out like the single-destination path.** Out of scope, and the 2026-10-19 read-ahead
  design keeps fan-out synchronous on purpose.

## Consequences

- A sibling-nacked record reaches the DLQ only when the batching sibling flushes (batch size, `sdk.batch.delay`,
  or the drain on stop). Its source ack waits the same way, which is the point.
- With the default DLQ (window 1, threshold 0) the pipeline still fails on the first nack and moves no
  position past it, but the failure surfaces when the last branch votes rather than when the nack happens.
  With `sdk.batch.size` and no `sdk.batch.delay` on a quiet source, that wait has no bound until #3231 adds
  a flush timer; the pipeline reports running meanwhile. Records after it may be written to the
  destinations in between; none of them is acked, so they replay.
- If a branch never votes (its flush fails, the worker errors, the drain times out), the nacked position is
  never released: it is not in the DLQ and replays on restart. At-least-once, no early DLQ copy.
- The guardmut guards `funnel-multiack-{ack,nack}-after-terminal-noop` become
  `funnel-multiack-{ack,nack}-double-vote-refused`, and
  `funnel-multiack-{ack,nack}-after-failed-release-refused` are added.

## Related

- [`20260731-archv2-fanout-ack-model`](20260731-archv2-fanout-ack-model.md) (partly superseded here)
- [`docs/design-documents/20261019-funnel-read-ahead-pipelining.md`](../design-documents/20261019-funnel-read-ahead-pipelining.md),
  "Fan-out stays synchronous"
- #3323, #3324, #3294, #3300, #3201, #3231
- `pkg/lifecycle-poc/funnel/worker.go`: `multiAckNacker`
