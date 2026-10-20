# Funnel destination read-ahead pipelining (arch-v2)

## Summary

Overlap the arch-v2 funnel's synchronous destination flush (`Write` + ack drain) with the next source
read by moving the flush onto a dedicated "flusher" goroutine per destination, fed by a bounded channel
whose capacity is the read-ahead credit. The worker (which owns the source read and the processor
chain) keeps reading and preparing batch N+1 while batch N's `Write`+ack is in flight, so the source
and the destination run concurrently instead of serially. Ack correctness is unchanged: a source batch
is still acknowledged only after the destination confirms a durable write for that batch, and acks are
still issued in source order. Pipelining is enabled only for single-destination pipelines (the CDC
parity case this closes); destination fan-out keeps the existing synchronous flush, where its
cross-batch source-ack ordering is already guaranteed by the worker's sequential passes.

## Context and problem

The v0.21 CDC parity benchmark (`benchi/v021-cdc-parity`, Postgres `logrepl` source → destination,
50k ops/s loader) measures Conduit ~20% behind Kafka Connect + Debezium (~40k vs ~50k events/s). The
logrepl source is not the limiter: in isolation it sustains >450k/s. The limiter is the funnel's
synchronous destination flush.

Destination-write batching (#3201, `destination_batch.go`) already accumulates records across source
reads into one multi-record `Write` and flushes before acking (Invariant 1). But the flush itself is
still **synchronous and on the worker goroutine**: when the buffer fills,
`DestinationTask.doBuffered` calls `flush`, which blocks draining the destination's acks
(`Destination.Ack` = `stream.Recv`) before the worker can read the next batch. In steady state the
worker alternates between "read/prepare a batch" and "block on the destination ack", so the source is
idle for the entire duration of each destination write. That serialization — not the destination's own
throughput — is the gap. Debezium's producer is fire-and-forget (background batching, async ack), which
is why it sustains 50k while Conduit caps at ~40k.

This is the credit/flow-control work that was parked pending the batching result; batching is now
merged (#3201, #3203), so it is unblocked.

## Constraints

1. **Invariant 1 — ack only after the destination confirms a durable write.** Pipelining must never
   turn into an early ack. A source batch is acked only after its flush has both sent the `Write` and
   drained the destination's acks for exactly those records.
2. **Invariant 3 — at-least-once on crash.** A batch that is in flight (enqueued or mid-`Write`/ack)
   when the process dies must be replayed, never lost. It is held un-acked, so its source position is
   never persisted; a restart re-reads from the last persisted position.
3. **Invariant 4 — per-partition ordering.** Records within a partition must reach the destination in
   source order, and source acks must be issued in source order (the persisted position is monotonic).
   Pipelining must not let batch N+1's ack overtake batch N's ack.
4. **Invariant 7 — graceful shutdown.** SIGTERM drains in-flight records (flushes the trailing partial
   batch and waits for the flusher) before the destination is torn down. `kill -9` at any instant must
   remain recoverable without loss.
5. **Bounded memory.** The read-ahead window must have a hard maximum; a fast source must not grow
   buffered-but-unacked state without limit.
6. **Single-writer FIFO on the destination stream.** `Destination.Write`/`Ack` pair on a single gRPC
   stream and are unsafe for concurrent `Send`/`Recv` from multiple goroutines (see
   `Worker.doTask`'s comment). Any pipelining must keep every `Write` and `Ack` on one goroutine, in
   order.

## Alternatives considered

### A. Credit/read-ahead count, flush still on the worker goroutine

Track an in-flight count of destination batches; the worker stops reading when it is out of credit,
and the flush still runs synchronously on the worker when credit is free.

- **Pros:** bounds memory; a small change to `doBuffered`.
- **Cons:** does not actually overlap anything. The flush still blocks the worker, so the source still
  stalls for the entire duration of each `Write`+ack drain. It is the current behavior plus a counter.
  **Rejected:** it does not close the gap it exists to close.

### B. Decouple read from write via a bounded channel + a flusher goroutine (chosen)

Move the flush onto a dedicated goroutine per destination. The worker appends source reads to the
buffer; when the buffer fills it enqueues the pending batches onto a bounded channel (capacity = the
credit) and immediately returns to read the next batch. The flusher goroutine is the _only_ caller of
`Write`/`Ack`: it takes flushes off the channel in FIFO order, does `Write` → drain acks → ack each
source batch, then takes the next. A full channel blocks the worker's enqueue, which is the correct
backpressure when the destination is slower than the source.

- **Pros:** real overlap — the source reads while the destination writes; the channel capacity is the
  memory/credit bound; the single flusher preserves the single-writer FIFO invariant on the destination
  stream; ack ordering falls out of the channel's FIFO order for a single destination.
- **Cons:** the source ack now runs on the flusher goroutine instead of the worker goroutine (a small
  thread-safety surface, addressed below); fan-out must stay synchronous (see Failure modes / Decision).

### C. Do nothing and accept the gap

Leave the ~20% gap and document Conduit as slower than Kafka Connect + Debezium out of the box.

- **Pros:** zero risk.
- **Cons:** fails the stated goal; the gap is structural (stop-and-wait), not a tuning issue.
  **Rejected** for the same reason the task exists.

## Decision

Pipelining is implemented as Alternative B, scoped to **single-destination pipelines**.

- **Flusher goroutine.** `DestinationTask.Open` starts a flusher goroutine when the task batches
  (`batchingEnabled`). The flusher is the sole caller of `Destination.Write`/`Ack`, preserving the
  single-writer FIFO invariant (constraint 6). It processes flushes strictly in enqueue order.
- **Bounded channel = credit.** The channel capacity is `maxInFlightFlushes = 2`. `doBuffered`
  enqueues a full buffer with a `select` on the channel, the flusher's done signal, and `ctx`, so a
  full channel blocks (backpressure) and a dead/errored flusher or a cancelled run unblocks the
  enqueuer instead of hanging.
- **Ack ordering.** For a single destination there is exactly one flusher, so `Write`s and source acks
  are issued in FIFO order — batch N fully acked before batch N+1 (Invariant 4). Within a flush,
  `ackPending` acks each buffered source batch in source order exactly as today.
- **Fan-out stays synchronous.** With M>1 destinations, M flushers would race on the single Worker's
  source ack across batches: each multiAckNacker guarantees _within_-batch prefix ordering, but the
  final hop to `connector.Source.Ack` has no cross-batch happens-before, so batch N+1's ack could reach
  the source before batch N's (a monotonic-position violation). The current synchronous flush
  serializes this naturally (a pass completes, including acks, before the next pass). `buildSharedTail`
  sets `pipelined = (len(destTasks) == 1)`, so fan-out keeps the pre-existing synchronous path
  byte-for-byte. The N-source / single-destination case pipelines: there is one shared destination and
  therefore one flusher, so each source's acks are FIFO and cross-source interleaving (allowed) is
  preserved.
- **Thread safety.** `connector.Source.Ack` and `DLQ.Ack/Nack` are already lock-guarded and safe from a
  non-worker goroutine; `multiAckNacker`/`runAckNacker` state is either mutex-guarded or owned
  exclusively by the flusher after hand-off. The one new race is `Worker.lastReadAt` (written by the
  worker on each read, read by `updateTimer` on the flusher's ack path); it becomes an `atomic.Int64`.

### Memory / credit bound

With `maxInFlightFlushes = 2` and flush size `S` (`effectiveBatchSize`, default 3000), the records read
from the source but not yet acked are bounded by:

- the accumulating buffer in `doBuffered`: ≤ `S`,
- flushes queued on the channel: ≤ `maxInFlightFlushes × S`,
- the flush the flusher is currently writing/acking: ≤ `S`,

for a total of `(maxInFlightFlushes + 2) × S = 4 × 3000 = 12 000` records with defaults (~12 MB at
1 KB/record). `maxInFlightFlushes` is a package constant, not a config surface; the bound scales with
an operator's explicit `sdk.batch.size` exactly as the batching work already documents.

## Failure modes

- **Crash mid-window (SIGKILL).** Enqueued or mid-`Write`/ack flushes are held un-acked; their source
  positions are never persisted. Restart replays them from the last persisted position — at-least-once,
  no loss. A chaos test kills the process with flushes in flight and asserts the destination count
  never exceeds what the source actually acked (re-delivery, never loss).
- **Flusher `Write`/`Ack` error.** The flusher records the error and exits. The next `doBuffered`
  returns it, which propagates through `doTask` (poisoning a shared destination) and fails the run as
  today. Flushes still queued behind the failure are abandoned, never acked, and replay on restart.
- **Slow destination (backpressure).** A full channel blocks the worker's enqueue, so the source
  naturally stops reading — the pipeline runs at the destination's rate, never ahead of it by more than
  the bound above.
- **Ack reordering.** Cannot happen on the pipelined (single-destination) path: one flusher, FIFO
  channel, in-order `ackPending`. Fan-out is not pipelined, so its existing serialization is unchanged.
- **Graceful shutdown.** `DestinationTask.Close` enqueues the trailing partial batch, closes the
  channel, waits for the flusher to drain, then tears down the destination. The source is already torn
  down by then, so the trailing source ack may fail (logged, records replay) — at-least-once, no loss,
  exactly as the batching design already documents. A force-stop/fatal error cancels the run context,
  which unblocks a wedged flusher and leaves in-flight records un-acked (replay).
- **Flusher hangs on a wedged destination.** The flusher blocks in `Ack` until the run context is
  cancelled (force-stop/fatal) or the destination returns — the same bound the synchronous flush had
  during `Close`; no new hang surface.

## Rollback

Pipelining is gated on `batchingEnabled` (destination `sdk.batch.*`) _and_ on single-destination
pipelines. A pipeline without those settings, or with multiple destinations, keeps the existing path
unchanged. Rolling back is reverting the commit — no config, state, or wire-format change.

## Observability

- The existing `conduit_connector_*` throughput/latency/error metrics are unchanged and remain correct:
  `flush`'s `Observe` still measures destination write+ack latency, and the worker's end-to-end timer
  still measures source-read → source-ack latency (now reflecting the pipelined path).
- `DestinationTask` logs at debug that the flusher started (with `maxInFlightFlushes`), and at error
  when a flush fails before teardown.
- A benchmark (`BenchmarkDestinationTask_DoBuffered_Pipelined` vs a serial baseline) demonstrates the
  read-ahead benefit at a 50k-class destination rate.

## Related

- `docs/design-documents/20261017-funnel-destination-batching.md` (the batching this builds on).
- `docs/design-documents/20260731-archv2-multiconnector.md` (fan-out ack model — why fan-out stays
  synchronous here).
- Roadmap: v0.21 engine — "adaptive batching by default", "destination ack reader".
