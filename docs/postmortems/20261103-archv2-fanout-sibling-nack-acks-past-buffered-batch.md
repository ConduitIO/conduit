# Postmortem: arch-v2 fan-out acked the source past a batch a sibling destination still buffered

This is a blameless postmortem for #3323. Fix: the PR that adds this file.

## Summary

In the arch-v2 engine, when one source fans out to several destinations and one of them batches
(`sdk.batch.size > 1` or `sdk.batch.delay > 0`), a nack in another destination for a later batch was sent to
the DLQ and acked to the source straight away, while the batching destination still held an earlier batch in
memory, unwritten. `Source.Ack` persists the last position it is given, so a crash (or `kill -9`) at that
moment resumed after the nacked record and the batching destination never received the earlier batch.
Invariants 1 and 3.

## Impact

- Engine: arch-v2 only (`--preview.pipeline-arch-v2`). The classic engine orders nack handling with a
  per-message semaphore and is not affected.
- Releases: v0.21.0, v0.21.1, v0.21.2 and every v0.22.0 nightly before the fix.
- Needs all of: arch-v2, at least two destinations, batching enabled on a destination other than the one that
  nacks (it is off unless configured), and a DLQ that accepts nacks (`window-nack-threshold > 0` or
  `window-size 0`). With the default DLQ (window 1, threshold 0) the nack fails the pipeline without acking,
  which is safe.
- Loss also needs a crash before the batching destination flushes. A graceful stop drains it first (#3231).
- No user report. We know of no affected deployment.

## How it was detected

A targeted audit of the fan-out ack path after the ordering fixes in #3294/#3300. It produced a unit test that
fails deterministically on `main` (0aeba0e5) and on v0.21.2.

## Timeline (UTC)

- 2026-10-18: #3201 adds engine-level destination batching. Fan-out keeps the synchronous flush (design doc
  20261019, "Fan-out stays synchronous").
- #3294, #3300: ordered source acks for records that bypass the destinations (filtered, or nacked above the
  fan-out). Destination-level nacks inside a branch are not covered.
- 2026-11-03: #3323 filed with a reproduction; fix opened the same day.

## Why it wasn't caught

- The nack-wins rule (ADR 20260731) was written before batching existed. Then, every branch had voted for
  batch N before the worker read batch N+1, so releasing a nack on the first vote could not overtake anything.
  Batching changed that premise and the ADR was not revisited.
- `multiAckNacker` tests check order within one batch. Nothing tested order across batches when a branch
  buffers.
- The fan-out chaos test (`fanout_sigkill_test.go`) never nacks and does not batch.

## Fix

A nack still decides the outcome, but the position is released only after every branch has voted for it,
the same rule as an ack. Every branch votes in source order, so batch N is always released before batch N+1.
See ADR `20261103-archv2-fanout-nack-waits-for-every-vote`.

## New automated checks

- `pkg/lifecycle-poc/funnel/worker_fanout_nack_order_test.go`: the reproduction plus nack in the batching
  destination, nack in both, three destinations, drain on stop, flush failure after a sibling nack, and the
  default DLQ.
- `tests/chaos/fanout_nack_order_sigkill_test.go`: SIGKILL with a batching destination and a nacking sibling,
  restart, no gap. It fails 5 of 5 runs without the fix.

## Follow-ups

- Release notes for the next v0.21.x patch and v0.22.0 should name the affected configuration.
