# Postmortem: arch-v2 fan-out acked the source past a batch a sibling destination still buffered

This is a blameless postmortem for #3323. Fix: #3324, the PR that adds this file.

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
- Releases (dates UTC): v0.21.0 and v0.21.1 (tagged 2026-10-26), v0.21.2 (tagged 2026-10-27), every v0.21.0
  nightly from `v0.21.0-nightly.20261020` (tagged 2026-10-20, the first to contain c73fb0cb from #3201) and every
  v0.22.0 nightly before the fix.
  Exposure window: on `main` from 2026-10-18 (#3201), in published nightly artifacts from 2026-10-20, in
  releases from 2026-10-26, until #3324 merged on 2026-11-03.
- Needs all of: arch-v2, at least two destinations, batching enabled on a destination other than the one that
  nacks (it is off unless configured), and a DLQ that accepts nacks (`window-nack-threshold > 0` or
  `window-size 0`). With the default DLQ (window 1, threshold 0) the nack fails the pipeline without acking,
  which is safe.
- Loss also needs a crash (or `kill -9`) before the batching destination flushes. A graceful stop does not lose
  the batch, by a different route on each line:
  - On `main`, Stop drains the destinations before it tears the source down (#3295, 2026-11-02), so the batch
    is written and acked.
  - On v0.21.0-v0.21.2, which do not have #3295, Stop tears the source down first and the drain runs in the
    destination's `Close`. The batch is still written to the batching destination, but its acks are refused;
    records the source had not been acked past are delivered again after the restart (duplicates, not loss).
- No user report. We know of no affected deployment.

## How it was detected

A targeted audit of the fan-out ack path after the ordering fixes in #3294/#3300. It produced a unit test that
fails deterministically on `main` (0aeba0e5) and on v0.21.2.

## Timeline (UTC)

- 2026-10-18: #3201 (c73fb0cb) merges engine-level destination batching and introduces the bug. Fan-out keeps
  the synchronous flush (design doc 20261019, "Fan-out stays synchronous").
- 2026-10-20: `v0.21.0-nightly.20261020` is tagged, the first published artifact with the bug.
- 2026-10-26: v0.21.0 and v0.21.1 are tagged. v0.21.0 is the first release with the bug.
- 2026-10-27: v0.21.2 is tagged.
- 2026-11-02: #3294 and #3300 order source acks for records that bypass the destinations (filtered, or nacked
  above the fan-out). Destination-level nacks inside a branch are not covered. #3295 drains destinations
  before source teardown on stop.
- 2026-11-03: found by audit; #3323 filed with a reproduction. Fix #3324 opened the same day.

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

Two hardening changes from review of the fix, in the same PR:

- A release to the parent that fails is final. Before, a sibling branch still running could vote on a later
  position and retry the failed `parent.Nack`: a second DLQ entry (when the DLQ write had succeeded and
  `Source.Ack` failed) and a second count in the nack window.
- Each branch votes through its own view, and a second vote from the same branch is refused as a bug, before
  every branch has voted as well as after. Counting votes alone let one branch's two votes release a position
  without its sibling's vote. No code path does this today.

Cost: the nack is now handled when the batching destination flushes. With the default DLQ the pipeline fails
then, not when the nack happens, and with `sdk.batch.size` and no `sdk.batch.delay` on a quiet source that wait
has no bound until #3231. See `docs/operations/archv2-destination-batching.md`.

## New automated checks

- `pkg/lifecycle-poc/funnel/worker_fanout_nack_order_test.go`: the reproduction plus nack in the batching
  destination, nack in both, three destinations, drain on stop, flush failure after a sibling nack, and the
  default DLQ.
- `pkg/lifecycle-poc/funnel/multi_ack_nacker_sticky_test.go`: a failed release is not retried by a later vote,
  and a double vote before every branch has voted is refused.
- `tests/chaos/fanout_nack_order_sigkill_test.go`: SIGKILL with a batching destination and a nacking sibling,
  restart, no gap. It fails 5 of 5 runs without the fix. The restart run must commit every position, or the
  test fails as a harness failure, so a reported gap is never one the run did not reach.

## Follow-ups

- Release notes for the next v0.21.x patch and v0.22.0 should name the affected configuration and the
  delayed nack handling with the default DLQ.
- #3231: a flush timer bounds the delay.
