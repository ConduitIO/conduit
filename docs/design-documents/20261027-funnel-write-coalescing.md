# Funnel destination write coalescing (arch-v2)

## Summary

When a destination's writer is free, send every submission already queued for it as one `Write`: no
timer, no added wait, bounded by a record cap and a byte cap. This turns the arch-v2 funnel's default
single-destination path, which today is stop-and-wait (read, write, wait for the ack, ack the source,
read again), into a pipelined path whose per-`Write` cost is shared by whatever queued up while the
previous `Write` was in flight. When the destination keeps up, the queue is empty and each source read
goes out alone, as it does today.

This is not a second batching mechanism. It generalizes the read-ahead flusher that destination-write
batching already uses (#3201, #3203, #3210): every single-destination pipeline gets the flusher, the
flusher's bounded channel becomes a record- and byte-bounded queue of submissions, and the flusher takes
every queued submission that fits under the caps instead of one. The explicit `sdk.batch.*` accumulator
stays, as an optional stage in front of the queue.

Risk tier: Tier 1. It changes the default data path of every single-destination arch-v2 pipeline: the
source ack moves off the worker goroutine, a source batch can be acked after later batches were read,
and one `Write` can carry several source batches (from several sources, on a shared destination).

## Context and problem

An exploratory profile of `main` at `a23efb64` (generator to file, 1 source and 1 destination,
connector defaults, host mode on a c7i.4xlarge; not the A/A-controlled harness) shows the pipeline is
not CPU-bound. It uses about 1.4 of 16 cores. The worker goroutine is blocked 41.9 s of a ~50 s run:
21.5 s in `Source.Read`, waiting on the source plugin goroutine, and 20.3 s in `Destination.Ack`,
waiting on the destination's ack. Each hop of read, write, ack, source ack is a goroutine wakeup, and
nothing overlaps.

Main already has the fix for this shape, but only behind an opt-in:

- Destination-write batching (`docs/design-documents/20261017-funnel-destination-batching.md`) runs
  only when the destination sets `sdk.batch.size` or `sdk.batch.delay`. The 3000-row default from
  #3203 is the threshold used when only a delay is set; it does not turn batching on.
- Read-ahead (`docs/design-documents/20261019-funnel-read-ahead-pipelining.md`) moves the flush onto a
  flusher goroutine, but only when batching is on and the pipeline has one destination.

A pipeline with no `sdk.batch.*` settings on its destination, which is the default, takes the
synchronous `DestinationTask.Do` path: one source read, one `Write`, every ack, then the next read.

A local prototype on `a23efb64` added the #2978 async destination machinery (async acks, read-ahead
credits, ledger prefix release, no `sharedMu`) and then writer-side coalescing on top. Exploratory
numbers, records/s at the sink, 3 interleaved rounds, round-to-round spread at most 3.5%:

| Shape | `main` | #2978 rebased | + coalescing |
| --- | --- | --- | --- |
| 1x1, source batch 1 (default) | 54.5k | 61.3k | 90.8k |
| 1x1, source batch 10 | 137k to 150k | 182k | 179k |
| 1x1, source batch 100 | 221k to 235k | 220k | 221k |
| 4x4, source batch 1, per sink | 29.9k | 47.2k | 152.9k |

These are a reason to do the work, not a performance claim. Two things limit what they say about this
design. First, the prototype arm includes more than coalescing: its 1x1 gain sits on top of async
acks, which this design does not adopt (Alternative D). Second, the 4x4 gain needs the shared
destination lock gone, which is the sibling doc's job, not this one's. Once the source batches 100
records or more, nothing helps; the connectors' per-record SDK work dominates.

## Constraints

1. **Invariant 1.** A source batch is acked only after the coalesced `Write` that carried it has been
   sent and every one of its records' acks has been read from the destination.
2. **Invariant 2.** Source acks go out in source order, per source. Coalescing must not let submission
   N+1's ack overtake submission N's, and a failure must not ack anything after the failed submission.
3. **Invariant 3.** Every submission reaches exactly one terminal outcome: acked, acked with
   per-record nacks routed to the DLQ, or abandoned. Abandoned means never acked, the run fails or
   stops, and the records replay. No path may complete a submission twice or never.
4. **Invariant 4.** Within a source, records reach the destination in read order. Interleaving
   between sources in one `Write` is allowed.
5. **Invariant 6.** A per-record nack inside a coalesced `Write` is attributed to exactly the record
   that failed, in exactly the submission that owns it, and follows the DLQ policy.
6. **Invariant 7.** SIGTERM drains queued submissions before the source is torn down, so a graceful
   stop of a default pipeline produces no more duplicates than it does today, which is none.
   `kill -9` at any instant replays and never loses.
7. **Single writer.** `Destination.Write` and `Destination.Ack` stay on one goroutine per destination,
   in FIFO order (read-ahead doc, constraint 6).
8. **Message size.** A standalone plugin rejects a gRPC message over `connectors.max-receive-record-size`
   (default 4 MiB, `pconnector/server.DefaultMaxReceiveRecordSize`). `connector.Destination.Write`
   sends all of a `Write`'s records as one `DestinationRunRequest`, so a coalesced `Write` must fit.
9. **Bounded memory.** The bound must be no worse than read-ahead's `(maxInFlightFlushes + 2) × S`
   records, and it must also be bounded in bytes.
10. **No added latency.** The writer never waits for more work. A sparse source's record is written
    as soon as the writer is free.

## Alternatives considered

### A. Natural coalescing inside the existing flusher (chosen)

Each source read becomes a submission. With `sdk.batch.*` set, one submission is a whole accumulated
buffer. Submissions go on a bounded FIFO queue. The flusher, the only goroutine that calls
`Write`/`Ack`, takes the queue head plus every following submission that fits under the caps, writes
them as one `Write`, drains the acks, and acks each submission in order through the existing
`flushPending`/`markSpans`/`ackPending` code.

- **Pros:** one mechanism instead of two. The ack fan-back already exists and is tested
  (`flushSpan`, `markSpans`, `ackPending`). No timer and no added latency. Works for every destination
  and every SDK version, because it needs nothing from the connector.
- **Cons:** makes read-ahead the default for every single-destination pipeline, which moves the
  source ack off the worker goroutine and needs a drain step before source teardown (constraint 6).
  With one `Write` in flight, it shares the per-`Write` cost but cannot overlap two `Write`s.

### B. Turn on timer-based batching by default

Give every destination a default `sdk.batch.delay` (say 5 ms), so #3201's accumulator always runs.

- **Lost because** it adds up to the delay to every record at low rates. Worse, the deadline is
  checked only when the next record arrives (#3201 rejected a ticker, its Alternative C). With a
  sparse source, the last records before a lull sit in the buffer until the next record or until
  shutdown, an unbounded latency. Fixing that means a ticker goroutine that acks the source, which
  #3201 rejected for good reason.

### C. Default source batching only

Ship `sdk.batch.size=100`, `sdk.batch.delay=10ms` as the source default
(`docs/design-documents/20261027-default-source-batching.md`). On generator to file this reaches the
plateau, about 4x the default.

- **Not rejected; it is the other half.** It cannot replace coalescing for three reasons. It needs an
  SDK that knows the setting, because standalone connectors on old SDKs reject unknown `sdk.*`
  parameters. It adds up to 10 ms to sparse sources. And it has to be re-checked per connector against
  that connector's semantics: CDC transaction boundaries, Kafka offsets. Coalescing needs none of
  that. Where source batching is on, coalescing measured no gain, so the two overlap rather than
  stack. Open question 1 asks whether we need both.

### D. Separate ack reader, several `Write`s in flight (#2978 shape)

Split the flusher into a writer and an ack reader, so `Write` N+1 is sent before `Write` N's acks
return, bounded by an unacked-records window. This is the shape the prototype measured.

- **Deferred, not rejected.** It is the roadmap's "Destination ack reader" item. It is also where all
  four bugs from the prototype review lived, because completion becomes a cross-goroutine handshake.
  It is a strictly larger change. If benchi shows Alternative A recovers most of the prototype's 1x1
  gain, D is not worth its surface. If it does not, D becomes its own design doc on top of this one.
  The queue, caps, completion rules and tests below carry over unchanged.

### E. Coalesce inside the `pkg/connector` destination adapter

- **Lost for the reason #3201's Alternative B lost.** The adapter does not own the source ack, so the
  ack deferral and fan-back would be split across two packages.

## Decision

### One mechanism

`DestinationTask` gets a submission queue in place of `flushCh`. Every destination task in a
single-destination pipeline is pipelined (`SetPipelined` no longer requires `batchingEnabled`). The
DLQ's task and fan-out pipelines (M > 1 destinations) keep the synchronous path until phase 2.

```text
worker: read -> processors -> [accumulator, only if sdk.batch.* set] -> enqueue submission
flusher: take head + every queued submission under the caps -> one Write -> drain acks
         -> markSpans -> ackPending(submission 1), ackPending(submission 2), ... in queue order
```

- **Submission.** One `pendingBatch` group: a single source read without `sdk.batch.*`, or a whole
  accumulator flush with it. A submission is never split across `Write`s, so a split run
  (`run_ledger.go`), which lives inside one source batch, never straddles two `Write`s.
- **Record cap.** `effectiveBatchSize()`: an explicit `sdk.batch.size`, otherwise 3000. One knob
  across the accumulator and coalescing, so an operator who set `sdk.batch.size: 500` for a database's
  bind-parameter limit never gets a 1000-record `Write`.
- **Byte cap.** `maxWriteBytes = connectors.max-receive-record-size / 2` (2 MiB by default). It
  applies to the accumulator as well as to coalescing. The accumulator has no byte cap today, so by
  reading the code, 3000 records over about 1.4 KB each already exceed the 4 MiB limit of a standalone
  destination with `sdk.batch.delay` set. This is not reproduced; the implementation PR adds the test
  that shows it either way. Builtin destinations get the same cap, so one path also bounds per-`Write`
  memory (open question 3).
- **Taking from the queue.** The queue is a mutex-guarded slice, not a channel. The flusher peeks
  before it takes, so it takes the longest prefix whose record and byte totals stay under the caps. A
  submission that would cross a cap stays at the head for the next `Write`. A head submission that is
  over a cap on its own is taken and written alone (today's behaviour for that read).
- **Queue credit.** Enqueue blocks while the queue holds `maxInFlightFlushes × S` records or
  `maxInFlightFlushes × maxWriteBytes` estimated bytes. An empty queue always admits one submission,
  so an oversized submission cannot deadlock. Enqueue unblocks on dequeue, flusher exit, close, or
  run-context cancel. The memory bound stays `(maxInFlightFlushes + 2) × S` records, plus one source
  read of overshoot, and is now also bounded in bytes.
- **No waiting.** The flusher blocks only while the queue is empty. It never waits for the queue to
  fill.

### Size estimator

`estimateRecordBytes(r)` adds `metrics.RecordBytesHistogram.SizeOf(r)` (key, before and after, the
JSON-size walk from #2268 that never calls `Bytes()`), the position length, metadata key and value
lengths, and a fixed 64 bytes of per-record framing. It runs once per record at enqueue, on the worker
goroutine, so the flusher only adds integers. The estimate is reused for the
`conduit_connector_bytes` observation instead of being recomputed after the ack.

The JSON walk is not the protobuf wire size. `google.protobuf.Struct` adds framing per field and
encodes every number as an 8-byte double, so a record of many small numeric fields can encode larger
than its JSON. The 2x headroom in `maxWriteBytes` covers that. A rapid property test checks the
headroom: for generated records, raw and structured, numeric-heavy and string-heavy, the
`DestinationRunRequest` that `connector.Destination.Write` would send must be at most twice the
estimate. If the test finds a counterexample, the estimator changes; the headroom does not.

When a single submission's estimate is over `maxWriteBytes` and the destination is standalone, the
flusher logs a warning with the estimate and the limit before sending it. If the plugin then rejects
it with gRPC `ResourceExhausted`, the error is wrapped as
`pipeline.destination_write_exceeds_message_limit`, with the config path and a fix: raise
`connectors.max-receive-record-size`, or lower the source's `sdk.batch.size`. Today that failure is an
opaque stream error, and likely a restart loop, because the replay rebuilds a similar oversized `Write`.

### Ack fan-back, nacks and the DLQ

`flushPending` already concatenates several `pendingBatch` values into one `Write`, remembers each
one's span, validates acks by position in order, marks per-record nacks with `markSpans` against the
owning batch, and only after every ack is in, acks or nacks each batch in order with `ackPending`.
Coalescing feeds it several submissions instead of one accumulator buffer. That code path is reused
and not rewritten.

- A nack in submission 2 of 3 marks exactly that record on submission 2's `*Batch`. `ackPending`
  splits submission 2 by flag and sends the nacked sub-batch to `acker.Nack`, then the DLQ through
  `Worker.Nack`. Submissions 1 and 3 ack wholesale. Source acks go out 1, 2, 3.
- If the DLQ write fails or the nack window trips, `ackPending` returns an error. Submissions after
  that one are abandoned, never acked; the flush error is recorded and the run fails. Positions stay
  monotonic, because nothing after the failure is acked.
- If the stream errors partway through the ack drain, nothing in the group is acked, including
  submissions whose acks had all arrived. Releasing that prefix would save at most one `Write`'s worth
  of duplicates, at the cost of a second completion path. It is not worth it.

### Exactly-once completion

Each submission gets a `state` field moved by compare-and-swap, from `queued` to `taken` to one of
`acked` or `abandoned`. A second move into a terminal state is a bug: tests panic, production returns
`(bug)` and fails the run. Every exit path names its submissions explicitly:

- A `Write` error, an ack error or an `ackPending` error abandons every submission in the taken group
  that did not reach `acked`.
- The flusher exiting on a run-context cancel abandons the taken group and everything still queued.
- `Close` waits for the flusher (below) and then abandons whatever is left.

Each worker keeps an outstanding-submission counter. It goes up at enqueue and down on the terminal
transition, whichever one it is. That counter is what the graceful-stop drain waits on, so a missed
completion becomes a test failure on a stop deadline, not a silent hang.

### Close and stop

- **Graceful stop (Invariant 7).** `Worker.Stop` sets `w.stop`, then, before `tearDownSource`, asks
  each destination it feeds to flush its accumulator and waits for its own outstanding counter to reach
  zero, bounded by the context passed to `Stop` (bounded stop is #2990). The flusher's ack path does not take `processingLock`,
  so the wait cannot deadlock against `Stop` holding it. If the deadline passes, `Stop` tears down the
  source anyway. Late acks then hit a closed source stream (`isClosedSourceStream`) and the records
  replay. This also fixes a duplicate-on-stop that opt-in batching has today: the read-ahead doc
  accepts that the trailing source acks fail on graceful stop.
- **`DestinationTask.Close`.** Under the queue mutex it sets `closed`, then wakes the flusher, which
  drains the queue and exits. Close then waits for the flusher, then tears down the destination.
  Close never fails a `Write` that has already been sent; its acks are still read, and a real ack wins
  over teardown.
- **Fatal error or force-stop.** The run context is cancelled. The flusher stops after its current
  `Ack` call returns or fails, and abandons everything it holds. Records replay.

### Shared destinations and fan-out

- **Phase 1: one destination (1x1 and Nx1).** Nx1 is already pipelined when batching is on, with one
  flusher for the shared destination. Coalescing needs only three things: one flusher owns the stream;
  enqueue order matches each source's order; and a flush error is sticky, so every worker's next
  enqueue gets it, which is the same effect as `poisoned`. It does not need `sharedMu`. While
  `sharedMu` exists, a worker holds it only for the enqueue, not for `Write` plus the ack wait.
- **Phase 2: fan-out (M > 1).** Fan-out stays synchronous for the reason the read-ahead doc gives: M
  flushers would race on the worker's cross-batch source ack. Coalescing under fan-out depends on
  `docs/design-documents/20261027-funnel-shared-destination-concurrency.md`, which is being written
  in parallel and is expected to remove `sharedMu` and give fan-out an ordered cross-batch source-ack
  release. The 4x4 number above needs both docs. Phase 2 is a follow-up PR against that design. This
  doc does not change the fan-out path.

## Requirements and tests

Each requirement below comes with the test that proves it, written to fail without the fix. The first
four are the bugs the security review found in the prototype. Each one has a counterpart on `main`'s
code or would be introduced by coalescing.

| # | Requirement | Test |
| --- | --- | --- |
| R1 | Taken-group completion. A `Write`, ack or `ackPending` error, or a context cancel, moves every submission in the taken group and in the queue to exactly one terminal state. Prototype bug: the writer returned on an error or on close without completing the batch it took. | `TestFlusher_WriteErrorCompletesTakenGroup`, `TestFlusher_CancelCompletesQueue`: outstanding counters reach zero, no source ack, the run fails |
| R2 | Cap look-ahead. The flusher never takes a submission that would cross either cap, and no submission is lost or reordered at the cut. Prototype bug: the drain took one item past the cap. | `TestFlusher_CoalesceRespectsRecordCap`, `TestFlusher_CoalesceRespectsByteCap`, and a rapid property: for random submission sizes and caps, every `Write` is under the caps unless it holds one submission, the concatenated `Write`s equal the submissions in order, and each submission completes once |
| R3 | Enqueue versus close. Enqueue and `closed` share one mutex. An enqueue after close fails without acking, and the submission replays. Prototype bug: enqueue could land behind close's drain. Main avoids it only through ordering (the sink closes after every worker exits), which phase 2 may change. | `TestDestinationTask_EnqueueRacesClose`, `-race -count=500`: never panics, never hangs, every submission is completed or rejected |
| R4 | No orphaned `Write`s. A `Write` already sent completes only through its real acks, or through a stream error or context cancel; close never fails it early. Prototype bug: the ack reader exited on close, so sent `Write`s never completed. | `TestDestinationTask_CloseWaitsForSentWriteAcks` (real ack wins), `TestDestinationTask_StreamErrorAbandonsSentWrite` |
| R5 | Nack fan-back. A per-record nack in a coalesced `Write` reaches the DLQ for that record only. Other submissions ack, in order. | `TestCoalescedWrite_NackMapsToOwningSubmission`, with the nack in the first, middle and last submission and across ack chunks |
| R6 | Graceful drain. `Stop` waits for this worker's submissions before source teardown. | `TestWorker_StopDrainsBeforeSourceTeardown`, plus a chaos SIGTERM test: a default destination with a slow `Write`, stop mid-stream, zero duplicates on restart |
| R7 | Per-source order on Nx1. Coalesced `Write`s interleave sources and never reorder one. | Extend `worker_nsource_test.go`: 4 sources, 1 default destination, assert per-source order and ascending position acks |
| R8 | Estimator soundness. The encoded request is at most twice the estimate. | `TestEstimateRecordBytes_BoundsWireSize` (rapid) |
| R9 | Accumulator byte cap. Opt-in batching cuts on bytes too. | `TestDoBuffered_CutsOnByteCap`, and a standalone test over 4 MiB that fails on `main` |
| R10 | Existing suites in the new default. The fan-out-share, deep-clone, retry-bound, poison and ack-order tests pass with the single-destination default now pipelined. | Run the existing `pkg/lifecycle-poc/funnel` and `tests/chaos` suites unchanged |

### Chaos

`TestSIGKILL_CoalescedWrite_CrashMidWrite` in `tests/chaos`, modelled on
`pipelined_flusher_sigkill_test.go`, uses a destination with no `sdk.batch.*` settings and a paced
`Write`, so the queue builds and coalesced groups of two or more submissions are in flight. The child
SIGKILLs itself while a coalesced `Write` is sent but not yet acked. The test also asserts, through a
test hook, that at least one group of two or more was in flight at the kill; otherwise it proves
nothing. Assertions are the existing ones: at the kill, every position the upstream ledger committed
was durably delivered (Invariant 1); after restart, the upstream ledger reaches the total and every
position was delivered at least once (Invariant 3). It runs in `tests/chaos (race, x3)`, which is
required. A second variant runs 4 sources into 1 destination.

## Failure modes

- **SIGKILL with submissions queued or a coalesced `Write` in flight.** Nothing in the queue or the
  group was acked, so no position was persisted. Replay from the last persisted position, with
  duplicates bounded by the queue credit plus one `Write`. Covered by the chaos test.
- **`Write` fails.** The group and the queue are abandoned (R1), the flush error is sticky, and the
  run fails. On a shared destination, every worker gets the error on its next enqueue.
- **Ack stream fails partway through a group.** Nothing in the group is acked. Replay.
- **Destination returns a per-record error.** That record goes to the DLQ (R5). A DLQ failure fails
  the run and abandons the rest.
- **Oversized submission on a standalone destination.** Written alone. If the plugin rejects it, the
  run fails with `pipeline.destination_write_exceeds_message_limit`, not an opaque stream error.
- **Estimator underestimates past the headroom.** The plugin rejects the `Write` with the same error
  and the run fails. Nothing is acked, so nothing is lost. R8 exists to find this before users do.
- **Slow destination.** The queue fills to its credit, enqueue blocks, and the source stops reading.
  The pipeline runs at the destination's rate, as with read-ahead today.
- **Sparse source.** The queue is empty, each read is written immediately, and latency equals today's
  stop-and-wait. The benchi latency arm checks this.
- **Wedged destination at shutdown.** The flusher blocks in `Ack` until the stop deadline cancels the
  run context. That is the same bound the synchronous path has.
- **Close racing enqueue.** Impossible by construction (R3).
- **Processor mutates a record after enqueue.** The worker builds a fresh `Batch` each pass. The
  record ownership rules from #3201 and the fan-out deep-clone rules already apply to buffered batches,
  and R10 runs those tests in the new default.

## Upgrade and rollback

No serialized format changes: positions, checkpoints, connector state and the connector protocol are
untouched, so N+1 reads N's state and N reads N+1's. Behaviour does change: one `Write` can carry
records from several source reads. A destination that assumed one `Write` per source read would see
that already under opt-in batching, and connectors must handle multi-record `Write`s under the SDK
contract anyway. The release notes call it out.

Rollback is reverting the PR. The per-pipeline escape hatch is open question 2. No new config surface
is proposed beyond reusing `connectors.max-receive-record-size`.

## Observability

New metrics, labelled the same as the `conduit_connector_*` family (pipeline, plugin, type,
`component_id`), so no new cardinality:

- `conduit_funnel_destination_write_submissions`: histogram of submissions per `Write`. A value of 1
  everywhere means the destination keeps up and coalescing is idle.
- `conduit_funnel_destination_write_records`: histogram of records per `Write`.
- `conduit_funnel_destination_write_bytes`: histogram of estimated bytes per `Write`, with buckets up
  to `maxWriteBytes`.
- `conduit_funnel_destination_cap_cuts_total{reason="records|bytes"}`: counter of `Write`s cut short by
  a cap. A high byte-cut rate means records are large relative to the message limit.
- `conduit_funnel_destination_queue_records`: gauge of queued, not-yet-written records.
- `conduit_funnel_destination_enqueue_blocked_seconds_total`: counter of time workers spent blocked on
  queue credit. It is the direct "destination is the bottleneck" signal.

At debug, the flusher logs its caps on start. The oversized-submission warning and the new error code
get a runbook entry in `docs/operations/archv2-destination-batching.md`, which the implementation PR
extends to cover coalescing. `llms.txt` and the arch-v2 docs on `conduit.io` change in the same PR.

## Benchmark plan

Nothing here is a claim until benchi says so. The implementation PR commits configs and results under
`benchi/archv2-write-coalescing/`, using the A/A-controlled methodology in `benchi/METHODOLOGY.md`
(interleaved base and head, 5 rounds, medians with spread, cited noise floor).

1. **1x1 generator to file, source batch 1, 10, 100.** The exploratory table, reproduced. Pass: a
   gain at batch 1 well above the noise floor, and no regression above the noise floor at batch 100.
2. **Prototype comparison.** The same shape, this design against the prototype's coalescing arm, to
   answer Alternative D. If this design recovers less than about two-thirds of the prototype's 1x1
   gain, write the ack-reader doc.
3. **CDC parity** (`benchi/v021-cdc-parity`, Postgres `logrepl` to a destination). The real-connector
   shape the v0.22 engine flip is gated on, with and without the default-source-batching change, to
   answer open question 1.
4. **Nx1** (`benchi/archv2-nxm` with M = 1): 4 sources into 1 destination.
5. **Standalone destination, 2 KB and 16 KB records.** Proves the byte cap holds and measures its
   cost.
6. **Latency.** A paced generator at 100 and 1000 records/s, reporting p50 and p99 source-read-to-ack
   latency against `main`. Pass: no p99 regression above the noise floor.
7. **`make bench-engine`.** Add `BenchmarkDestinationTask_Coalesced` so the per-PR gate covers the
   path from now on.

4x4 is measured in phase 2, with the shared-destination doc.

## Open questions for DeVaris

1. **Both, or one?** Default source batching reaches the plateau on generator to file, and where it
   is on, coalescing adds nothing. Coalescing adds no latency and works for old SDKs and non-batching
   sources. My recommendation: land default source batching first, because it is the smaller change.
   Then ship coalescing only if benchi arm 3 shows a gain on CDC parity on top of it, or if source
   batching cannot be the default for enough connectors. Do you agree with that gate?
2. **Escape hatch.** Making read-ahead and coalescing the default for every single-destination
   pipeline is a big behaviour change for one PR. Options: (a) no switch, revert to roll back; (b) an
   explicit destination `sdk.batch.size: 1` means "stop-and-wait, as before", which needs
   `SDKBatchSettings` to tell unset apart from 1; (c) a temporary engine flag that follows the
   deprecation policy. I lean to (b): it reuses an existing knob and is per pipeline.
3. **Byte cap for builtin destinations.** The same 2 MiB cap keeps one code path and bounds memory per
   `Write`, but builtins have no message limit and may lose throughput on large records. Same cap, or
   records only?
4. **Accumulator byte cap as a separate fix.** R9 fixes what looks like a live bug in opt-in batching
   for standalone destinations. Should it go out first as its own `fix:` PR, with a regression test,
   ahead of this design?
5. **Phase 2 sequencing.** Is it acceptable that the 4x4 gain waits for the shared-destination doc,
   or do you want fan-out coalescing designed here once that doc settles?

## Related

- `docs/design-documents/20261017-funnel-destination-batching.md`: the accumulator and `flushPending`
  fan-back this reuses.
- `docs/design-documents/20261019-funnel-read-ahead-pipelining.md`: the flusher this generalizes.
- `docs/design-documents/20261027-funnel-shared-destination-concurrency.md`: `sharedMu` and fan-out,
  a prerequisite for phase 2. Being written in parallel.
- `docs/design-documents/20261027-default-source-batching.md`: the complementary source-side default.
- `docs/design-documents/20260801-archv2-multiconnector-nsource.md`: shared destination, poisoning.
- #2978: the staged batch engine prototype, and the async machinery the review findings come from.
- Roadmap: v0.21 engine, "Adaptive batching by default" and "Destination ack reader". v0.22, "Default
  engine flip", gated on parity-or-faster at shipped defaults.
