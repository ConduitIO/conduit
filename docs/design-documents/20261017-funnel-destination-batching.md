# Funnel destination-write batching (arch-v2)

## Summary

Teach the arch-v2 funnel (`pkg/lifecycle-poc/funnel`) to accumulate records destined for a
destination connector into a bounded batch and flush them with a single multi-record `Write`, instead
of writing one record per `Write` call whenever the source does not batch. Batching is enabled by the
destination's existing `sdk.batch.size` / `sdk.batch.delay` settings, which arch-v2 today _refuses_
(`pipeline.archv2_destination_batching_unsupported`, #2979). Flushing always precedes acking
(Invariant 1), per-partition order is preserved, and memory is bounded by a maximum batch size plus a
flush on shutdown.

## Context and problem

The v0.21 CDC parity benchmark (`benchi/v021-cdc-parity`, `builtin:postgres` → `builtin:file`,
50k ops/s) measures Conduit ~6% behind Kafka Connect + Debezium. A pprof of the Conduit arm shows the
file sink doing one unbuffered `write(2)` syscall plus one JSON marshal per record, ~28.5% cumulative
CPU. The file-sink buffering fix (`conduit-connector-file` v0.10.10, a 64 KiB `bufio.Writer` flushed
once per `Write`) only amortizes the syscall when the sink's `Write` method is handed several records
at once — it cannot help when it is handed one record per call.

Arch-v2 does not hand the sink several records per call unless the _source_ batches:

- `funnel.DestinationTask.Do` calls `connector.Destination.Write(ctx, batch.ActiveRecords())` with
  exactly what the source read. With the source at its default (`sdk.batch.size=0`), each read returns
  one record, so each `Write` is one record.
- Arch-v2 cannot use the connector SDK's own destination batching: it refuses `sdk.batch.size > 1` on
  a destination because the funnel is stop-and-wait — it waits for every ack of a write before the
  next read, and an SDK destination batch larger than the write withholds its acks until it flushes,
  which stalls the pipeline (see `docs/operations/archv2-destination-batching.md`).

The classic engine overlaps reads, writes and acks on separate goroutines, so its destination can use
the SDK's batching; arch-v2 cannot. This gap is the binding constraint behind the ~6% delta.

The fix is to move destination batching into the funnel itself: the funnel accumulates records across
source reads and flushes multi-record `Write` calls, and because _it_ controls both the flush and the
ack, it can uphold ack-only-after-durable-write without stalling.

## Constraints

1. **Invariant 1 — ack only after the destination confirms a durable write.** A record must never be
   acknowledged to the source before the destination has written it (and the destination's ack has
   been read). Any batching must therefore flush-before-ack.
2. **Invariant 4 — per-partition ordering.** Records from one source partition must reach the
   destination in source order. Batching must not reorder records within a partition.
3. **At-least-once (Invariant 3) is the floor.** A crash between "record buffered" and "record
   flushed" must replay the record, never drop it. Buffered records are held un-acked, so their
   positions are not persisted; a crash replays them from the last persisted position.
4. **Invariant 7 — graceful shutdown.** SIGTERM drains in-flight records and checkpoints before exit.
   A partial batch must be flushed (and, where possible, acked) before the source is torn down;
   `kill -9` at any instant must remain recoverable without loss.
5. **Bounded memory.** The buffer must have a hard maximum; an unbounded accumulation across reads
   would grow without limit on a fast source.

## Alternatives considered

### A. Buffer in the funnel destination task (chosen)

Accumulate records inside `funnel.DestinationTask`, which already owns the write/ack pairing. Each
source read appends its active records to a bounded buffer together with a reference to the source
`*Batch` and its `ackNacker`. When the buffer reaches `sdk.batch.size` (or a flush deadline passes, or
the pipeline stops), the task flushes: one `Write` of all buffered records, then `Ack` reads until all
are accounted for, then each buffered source batch is acked/nacked in order. The source ack is
therefore deferred until the flush that durably wrote the record.

- **Pros:** engine-side, so it works for _any_ destination (built-in, standalone, non-SDK); the
  flush/ack ordering is explicit and local; reuses the `sdk.batch.*` settings operators already know.
- **Cons:** deferring the source ack across reads is a change to the funnel's stop-and-wait ack flow;
  the shared-destination (N-source) case must serialize buffer access and the flush.

### B. Buffer in the destination adapter (`pkg/connector`)

Put the same accumulation inside `connector.Destination.Write` (or a wrapper), before the stream send.

- **Pros:** closest to the "destination" semantics; no change to the funnel task graph.
- **Cons:** `DestinationTask.Do` is written as "one `Write` then drain `Ack` until all records are
  accounted for". A buffering `Write` that returns before flushing has no acks for `Ack` to drain, so
  `Do` would spin or block; the adapter would need to expose "0 acks = still buffering" and the task
  would still have to defer the source ack. It pushes the same deferral problem one layer down while
  splitting the ack bookkeeping between two packages. Rejected for moving the hard part without
  removing it.

### C. Flush-cadence ticker in the funnel

Run a dedicated goroutine per destination that flushes the buffer on a wall-clock tick, decoupled from
the worker.

- **Pros:** faithful to a "deadline" even when no record arrives to trigger a check.
- **Cons:** the flush would ack source records from a non-worker goroutine, concurrently with that
  worker's own `Source.Read`/`Ack`. The funnel's source ack path is deliberately single-goroutine
  (`Source.Read`/`Ack` are not designed for concurrent entry; see the deferred-ack goroutine in
  `pkg/connector/source.go`, which only _sends_ plugin acks, it never re-enters `Ack`). A ticker
  goroutine calling `Worker.Ack` is a new concurrency surface on the hottest invariants. Rejected: the
  deadline is instead checked synchronously at each record arrival, which a continuously-streaming
  source drives frequently, and any trailing partial batch is flushed on shutdown (see Failure modes).

## Decision

Destination-write batching lives in `funnel.DestinationTask` (Alternative A), enabled by the
destination's `sdk.batch.size` / `sdk.batch.delay` settings (read by the service and _stripped_ from
the settings handed to the SDK plugin, so the SDK does not also batch and re-introduce the stall).

- **Accumulation.** `doBuffered` appends each source read's active records to a buffer, paired with
  its source `*Batch` and `ackNacker`. Filtered records are not written but are still acked with their
  batch (they were already "handled" by filtering).
- **Flush triggers.** (a) buffered record count reaches `sdk.batch.size` (a hard maximum; if unset, a
  default cap bounds memory), or (b) `sdk.batch.delay` has elapsed since the first record of the
  current buffer (checked at the next record arrival), or (c) shutdown (`DestinationTask.Close`
  flushes before `Teardown`).
- **Ack/flush ordering.** A flush is strictly: `Write(all records)` → drain `Ack` until every record
  is accounted for → mark per-record nacks on their source batches → ack/nack each source batch via
  its stored `ackNacker`, in source order. The source ack can therefore only happen after the
  destination's own ack, upholding Invariant 1. No buffered record is ever acked before its flush.
- **Ordering.** Records are appended in source-read order and flushed in that same order inside one
  `Write`; the destination's `Write` processes the slice in order, so per-partition order holds.
  Cross-partition interleaving (multiple sources feeding one shared destination) is allowed — there is
  no ordering requirement _between_ partitions — and never reorders within one.
- **Shutdown.** `DestinationTask.Close` flushes the trailing partial batch before `Teardown`, so
  buffered records are written durably rather than dropped. Because the shared sink is closed only
  after every worker has exited (and thus after each source is torn down), a trailing batch's _source
  ack_ may fail against a torn-down source; that is surfaced as an error and is at-least-once safe
  (the record replays on restart), not a loss. The flush deadline is deliberately checked
  synchronously on record arrival rather than by a ticker (Alternative C rejected above).

## Failure modes

- **Crash between accumulate and flush (SIGKILL).** Buffered records are held un-acked; their
  positions were never persisted. Restart replays them from the last persisted position. At-least-once,
  no loss. A kill-mid-batch test covers this.
- **Partial batch on shutdown.** Flushed by `DestinationTask.Close` before `Teardown` (durable). The
  trailing source ack may fail (source already torn down) → replayed on restart. At-least-once.
- **Ordering across partitions.** Multiple sources appending to one shared buffer interleave at the
  batch level only; within a partition, append order == flush order == write order. No reordering.
- **Memory bound.** The buffer never exceeds the configured max size (or the default cap). A slow or
  stopped destination cannot grow it unboundedly: a full buffer flushes synchronously before the next
  record is accepted.
- **Shared destination (N sources).** Buffer access and the flush are serialized (the shared-subtree
  lock already guards entry, and the buffer has its own mutex). A flush triggered by one worker writes
  and acks the buffered records of all workers, in append order; each worker's own acker is invoked
  for its own records. `connector.Source.Ack` is lock-guarded and defers the plugin send to a
  dedicated goroutine, so this is safe under `-race`; the N-source test exercises it.
- **Destination write fails mid-flush.** The `Write` returns an error with no acks; the buffered
  records are nacked (or the error fails the pipeline) and are replayed on restart. No partial ack.

## Rollback

The change is gated on the destination's `sdk.batch.size`/`sdk.batch.delay`: a destination without
those settings keeps the existing one-write-per-read path byte-for-byte (the non-batching `Do` path is
unchanged). Rolling back is therefore removing those settings, or reverting the commit — no config or
state format changes.

## Related

- Issue #2979 (the refusal this replaces); `docs/operations/archv2-destination-batching.md`.
- Roadmap: v0.21 engine — "adaptive batching by default", "destination ack reader".
- `docs/design-documents/20261009-staged-batch-engine.md` (the eventual single-engine replacement;
  this is a targeted stop-gap for the CDC parity gap, not that redesign).
