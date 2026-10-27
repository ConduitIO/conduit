# Funnel destination write coalescing (arch-v2)

## Summary

When a destination's writer is free, send every submission already queued for it as one `Write`: no
timer, no added wait, bounded by a record cap and a byte cap. For a single-destination pipeline (1x1,
Nx1), the worker also stops waiting for each write. It enqueues and reads on, and the destination
acks the source in queue order once a write is durably confirmed. The default path is stop-and-wait
today: read, write, wait for the ack, ack the source, read again. This doc turns it into a pipelined
path whose per-`Write` cost is shared by whatever queued while the previous `Write` was in flight.
When the destination keeps up, the queue is empty and each read goes out alone, as it does today.

There is one stream owner and one coalescing site. The per-destination writer and AckReader come from
`docs/design-documents/20261027-funnel-shared-destination-concurrency.md` (#3292). This doc adds three
things on top of that component:

- a **take policy**, which is how the writer picks queued submissions under the caps, and is the only
  place coalescing happens in either mode;
- a **read-ahead completion mode** for single-destination pipelines, which absorbs today's #3210
  flusher;
- the byte cap and size estimator.

This doc owns #3292's "phase 2: fold the flusher into the writer".

**Hard gate.** Default source batching
(`docs/design-documents/20261027-default-source-batching.md`) lands first. This design is implemented
only if benchi then shows a gain on top of it (see "Benchmark plan").

Risk tier: Tier 1. It changes the default data path of every single-destination arch-v2 pipeline.
The source ack moves off the worker goroutine. A source batch can be acked after later batches have
been read. One `Write` can carry several source batches, from several sources on a shared destination.

## Context and problem

An exploratory profile of `main` at `a23efb64` (generator to file, 1 source and 1 destination,
connector defaults, host mode on a c7i.4xlarge; not the A/A-controlled harness) shows the pipeline is
not CPU-bound. It uses about 1.4 of 16 cores. The worker goroutine is blocked 41.9 s of a ~50 s run:
21.5 s in `Source.Read` and 20.3 s in `Destination.Ack`. Each hop is a goroutine wakeup, and nothing
overlaps.

Main overlaps them only behind an opt-in. Destination-write batching
(`20261017-funnel-destination-batching.md`) runs only when the destination sets `sdk.batch.size` or
`sdk.batch.delay`; the 3000-row default from #3203 is the threshold for delay-only batching, not a
switch. Read-ahead (`20261019-funnel-read-ahead-pipelining.md`, #3210) runs only with batching on and
one destination. With no `sdk.batch.*` on the destination, which is the default, the pipeline takes
the synchronous `DestinationTask.Do`.

A local prototype on `a23efb64` added the #2978 async destination machinery, then writer-side
coalescing on top. Exploratory records/s at the sink, 3 interleaved rounds, round-to-round spread at
most 3.5%:

| Shape | `main` | #2978 rebased | + coalescing |
| --- | --- | --- | --- |
| 1x1, source batch 1 (default) | 54.5k | 61.3k | 90.8k |
| 1x1, source batch 10 | 137k to 150k | 182k | 179k |
| 1x1, source batch 100 | 221k to 235k | 220k | 221k |
| 4x4, source batch 1, per sink | 29.9k | 47.2k | 152.9k |

These motivate the work; they are not a claim. Source batching at 100 reaches 221k to 235k with no
engine change, more than double the best coalescing figure at batch 1. That is why source batching is
a hard gate and not a preference.

### Existing defects this depends on

Reading the opt-in batching path for this design turned up defects that pipelining by default would
spread to every pipeline. They are fixed on `main` as separate PRs, and every one of them is a merge
condition for this design's implementation PR. This design names them and does not redesign them.

- **#3231** (fix: #3295). A partial batch has no flush timer. On graceful stop, `Worker.Stop` tears down the source
  before `DestinationTask.Close` drains the flusher, so the trailing source acks fail with
  `plugin.ErrPluginNotRunning` and the records replay. `tests/chaos/pipelined_flusher_sigkill_test.go`
  works around it: `pipelinedFlusherRestart` turns batching off for the restart.
- **H1, out-of-order source acks** (#3294). An all-filtered batch (`doBuffered`, and the worker's
  `!b.HasActiveRecords()` shortcut) and a processor-nacked sub-batch (`acker.Nack` to `Worker.Nack` to
  `Source.Ack(positions[:n])`) ack the source immediately, on the worker goroutine. Earlier records of
  the same read, or earlier reads, may still be buffered or in flight. `Source.Ack` persists the last
  position unconditionally, so a crash in that window resumes past records that were never written.
  That violates invariants 1, 2 and 3. Until #3294 merges, opt-in batching is exposed, and that
  includes batched fan-out (NxM with `sdk.batch.*`), which buffers synchronously but still has the
  bypass.
- **H2, unsynchronized `splitRun` voting** (#3294). A split run's members can be voted by the flusher (members
  written) and by the worker (members filtered or nacked) at once. `splitRun`'s fields are not
  synchronized, so a lost or a double release is possible.
- **Accumulator byte cap.** The accumulator cuts on record count only. For a standalone destination,
  3000 records can exceed the plugin's 4 MiB receive limit. This ships as its own `fix:` PR (#3296) with a
  regression test that fails on `main` (requirement R9 below).

## Constraints

1. **Invariant 1.** A source position is acked only after every record at or before it in that
   source's queue order has been written and acked by the destination, or has a terminal outcome
   (filtered, or routed to the DLQ) that does not need a write.
2. **Invariant 2.** Each source's acks reach `Source.Ack` in ascending order, from one goroutine.
   Nothing after a failed submission is released.
3. **Invariant 3.** Every position in a submission is either released (acked or DLQ'd) or replays. No
   position is released twice, and none waits forever.
4. **Invariant 4.** Within a source, records reach the destination in read order. Interleaving between
   sources is allowed.
5. **Invariant 6.** A per-record nack in a coalesced `Write` is attributed to exactly that record, in
   the submission that owns it, and follows the DLQ policy.
6. **Invariant 7.** SIGTERM drains the queue before the source is torn down. A graceful stop of a
   default pipeline produces no duplicates, which is today's behaviour. `kill -9` replays and never
   loses.
7. **One stream owner.** One writer calls `Write`, and one AckReader calls `Ack`, per destination
   (#3292, constraint 7).
8. **Message size.** A standalone plugin rejects a request over `connectors.max-receive-record-size`
   (default 4 MiB). `connector.Destination.Write` sends a whole `Write` as one `DestinationRunRequest`.
9. **Bounded memory and replay.** The records and bytes held unacked are bounded, and the bound is
   published, because it is also the crash replay window.
10. **No added latency.** The writer never waits for more work.

## Alternatives considered

### A. Take policy on the #3292 writer, plus a read-ahead completion mode (chosen)

Each `doBuffered` call becomes a submission. Without `sdk.batch.*`, that is one call per source read
or per sub-batch of a tainted read. With `sdk.batch.*`, it is one accumulator flush. The writer takes
the queue head plus every following submission that fits under the caps and writes them as one
`Write`. The AckReader completes writes in FIFO order. In read-ahead mode, completion runs the
existing fan-back: `markSpans`, then `ackPending` in queue order.

- **Pros:** one stream owner and one coalescing site for every mode. The fan-back already exists. No
  timer. It needs nothing from the connector, so it works with old SDKs too.
- **Cons:** every source ack in read-ahead mode has to go through the queue (H1). The replay window
  gets larger (M2, quantified below).

### B. Default timer-based batching

Give every destination a default `sdk.batch.delay`.

- **Lost:** it adds the delay to every record. The deadline is checked only when the next record
  arrives (#3231), so a sparse source's tail waits without bound. A ticker that acks the source was
  rejected in #3201.

### C. Default source batching only

- **The gate, not a rival.** It reaches the plateau on generator to file. But it needs a new enough
  SDK, adds up to the delay for sparse sources, and has to be checked per connector. Coalescing goes
  ahead only if it adds measurably on top (benchi arm 3).

### D. Several `Write`s in flight in read-ahead mode

The AckReader from #3292 makes this a parameter, not a new component. An unverified destination (see
the single-source guard) is held at one `Write` in flight, permanently; that is #3292's rule, not a
phase. A verified destination starts at one coalesced `Write` in flight in read-ahead mode. That is
today's flusher, and the simplest ordering argument. The rule for raising the verified limit is
pre-registered in benchi arm 2.

### E. A second coalescing site: #3201's flusher, or #3292's inbox carry slot

- **Lost:** two places to get the caps, the look-ahead and the completion rules right. The flusher is
  folded into the writer, and #3292's former R4 is this doc's R2.

## Decision

### Ownership per shape

After #3292 and this doc have both landed. #3292 is signed off at `d95843d2`, which carries the same table.

| Shape | Stream owner | Worker waits for its write? | Coalescing | Owning doc |
| --- | --- | --- | --- | --- |
| 1x1, any batching | writer and AckReader, read-ahead mode | no | yes | this doc |
| Nx1, any batching, bare or with processors | writer and AckReader, read-ahead mode; `sharedMu` (processor roots only) held for the processor pass and the enqueue | no | yes: across sources for a verified destination, one source per `Write` otherwise (see the single-source guard) | this doc |
| 1xM, M of 2 or more | direct `Do` (unchanged) | yes | no | neither |
| NxM, M and N of 2 or more, bare, no batching | writer and AckReader, #3292 concurrent mode | yes | per this doc's take policy, single-source guard included | #3292 |
| NxM with batching, or with processor roots | synchronous, under `sharedMu` (unchanged) | yes | no | neither; needs the #2978 ledger |

The predicate is simple: one destination means read-ahead mode; two or more means #3292's rules.
**Landing order.** The order is: the prerequisite fixes on `main`; then #3292's
component and concurrent mode, which build the writer, the AckReader, the completion hook and R1 to
R3; then this doc's take policy and read-ahead mode. This matches #3292's open question 2. #3292 at
`d95843d2` narrows concurrent mode to two or more sources, two or more bare destinations and no
batching. It moves its former "phase 2" and its former R4 to this doc.

**Who completes, in read-ahead mode.** #3292's rule is that the AckReader never touches the worker's
batch, and that workers ack their own source. Read-ahead mode reverses that rule, for this mode only.
The AckReader is the single completion goroutine. It attaches through the completion hook #3292
defines for this purpose. When a write's acks are all in, it runs
`markSpans` and then the group's entries in queue order. A data entry runs `ackPending`, which goes
through `runAckNacker` to `Worker.Ack` or `Worker.Nack`, and so to `Source.Ack` and `DLQ.Nack`. A
barrier entry runs the call it recorded. The writer hands a barrier that has nothing in flight ahead
of it straight to the AckReader's FIFO, so there is never a second completer. #3292's concurrent mode
keeps its rule unchanged. Ownership of a read's batch, its records and its split-run state passes to
the AckReader completely at enqueue. The staging rule below is what makes "completely" true.

Earlier drafts said fan-out coalescing would wait on an ordered cross-batch source-ack release from #3292.
It does not provide one, and does not need one, because its workers wait for their own
completion. Fan-out read-ahead, where the worker does not wait, still needs the #2978 ledger and is
out of scope for both docs.

### Take policy (the one coalescing site)

- **Submission.** One `doBuffered` call: its `*Batch`, its `ackNacker`, its records, and an estimated
  byte size. A submission is never split across `Write`s.
- **Record cap.** An explicit `sdk.batch.size` is the cap. Without one, the cap is `512` records, the
  prototype's measured setting, not 3000. That keeps the added replay small (see "Credit and replay
  window"). Delay-only batching keeps its 3000 threshold for the accumulator, and coalescing is
  capped at 3000 too.
- **Byte cap.** This applies to standalone destinations only, and to the accumulator as well (R9):
  `maxWriteBytes = connectors.max-receive-record-size × 7/8`, which is 3.5 MiB by default. It is
  derived from wire size; see the estimator. Builtin destinations have no message limit and are
  capped by records only. Nothing in `funnel` knows today whether a destination is standalone. The
  connector plugin service already knows, because the dispenser comes from either `builtinReg` or
  `standaloneReg` (`plugin/connector/service.go`). The implementation exposes that as a `Standalone()`
  method on the dispensed connector, next to #3292's verified flag (builtin, or `orderedAcks`).
  `buildDestinationTasks` passes both to `NewBatchingDestinationTask`. `maxWriteBytes` is the computed
  cap for a standalone destination and 0 (no byte cap) for a builtin one. The task never inspects
  plugin names.
- **Single-source guard (#3292, H2).** A destination is _verified_ if it is builtin, or if it is
  standalone and declares `orderedAcks` (#3292's capability gate; old plugins read as false). For an
  unverified destination, one `Write` carries records from one source only, and the in-flight limit is
  1. The writer takes the longest prefix of the queue whose data entries all come from the head's
  source. Barriers carry no records and no acks, so they never break the prefix. Without this, a
  dropped ack on a stream that mixes sources with byte-identical positions would ack one source for
  another source's record. That is loss, not a duplicate. The guard therefore also applies to the
  opt-in accumulator on Nx1. For an unverified destination the accumulator keeps one buffer per
  source, which closes `main`'s current Nx1-with-batching exposure for standalone destinations.
  Builtin destinations, and every 1x1, are unaffected.
- **Look-ahead.** The queue is a mutex-guarded slice. The writer peeks before it takes, and takes the
  longest prefix that stays under both caps. A head submission that is over a cap on its own goes
  alone, and is subject to the oversize error below.
- **No waiting.** The writer blocks only while the queue is empty, or while the in-flight limit is
  reached. That limit is always 1 for an unverified destination, and 1 for a verified destination
  unless benchi arm 2 raises it.

### Size estimator (from wire size)

The cap has to hold against the size that goes over the wire. The JSON-size walk the bytes metric uses
(`metrics.RecordBytesHistogram.SizeOf`) is far below that for structured data. Measured with
`proto.Size` on `toproto.DestinationRunRequest` (protocol v0.11.1) for one record each, against the
JSON walk of key and payload:

| Record | JSON walk | Wire | Ratio |
| --- | --- | --- | --- |
| raw, 1 KiB | 1029 | 1105 | 1.07 |
| struct, 20 one-character keys, single-digit numbers | 126 | 401 | 3.18 |
| struct, a list of 100 single-digit numbers | 212 | 1193 | 5.63 |
| struct, 20 keys, 7-digit numbers | 386 | 541 | 1.40 |
| struct, 10 keys, 64-byte strings | 746 | 841 | 1.13 |

`google.protobuf.Struct` spends 11 bytes on each number in a list (tag, length, an 8-byte double), and
about 15 bytes plus the key on each map entry with a numeric value. No fixed headroom over JSON is
safe.

So `estimateWireBytes(r)` walks the record and adds up the protobuf encoding size directly. Raw data
is its exact length. For structured data, each `Struct` field, `Value` kind and list element is counted
with its tag and varint length prefix; numbers are 9 bytes, strings are a tag, a varint and the bytes.
Position, metadata entries, operation and the record's own framing are counted the same way. It
allocates nothing. Requirement R8 is a rapid property test asserting the estimate is at least
`proto.Size` of the request, for every generated record, and within 5% of it. The 1/8 margin under the
limit covers request-level framing and varint rounding.

The walk runs on the worker goroutine at enqueue, only for standalone destinations. Its cost is a new
benchi arm (arm 6). If one standalone submission is over `maxWriteBytes`, the writer logs the estimate
and the limit, and sends it alone. A plugin rejection with gRPC `ResourceExhausted` is wrapped as
`pipeline.destination_write_exceeds_message_limit`, naming `connectors.max-receive-record-size` and
the source's `sdk.batch.size` as the fixes.

### Credit and replay window

The queue credit counts records and bytes that have been submitted but not completed: queued plus in
flight. A pass enqueues its whole staged list in one step, so credit is checked per list, not per
entry. When the queue is empty, it admits a whole staged list even if the list is over the credit;
otherwise one large read could never be enqueued. A list that does not fit waits as a unit. It is
never split, because splitting it would let a barrier be completed ahead of an entry staged before it.
Enqueue unblocks on completion, writer exit, close or cancel.

With the single-source guard, an unverified Nx1 destination keeps one accumulator buffer per source.
The credit counts every one of those buffers, as well as the queue and the `Write` in flight. In that
case the credit is (N + 2) × S, not 2 × S. Otherwise N partly filled buffers could hold all of it
while none of them is full, and nothing could flush. The replay window below counts the same way:
for N sources it includes N buffers of up to S each, not one.

A source ack does not make a position durable on its own. `Source.Ack` hands the position to the
connector persister, which flushes after `DefaultPersisterDelayThreshold` (1 s) or
`DefaultPersisterBundleCountThreshold` (10 000) acks, whichever comes first
(`connector/persister.go`, wired in `conduit/runtime.go`). So a SIGKILL today already replays
everything acked since the last flush: up to 1 s of data or 10 000 acks. At source batch 1 that is
up to 10 000 records. The default-source-batching design (#3293) changes the persister to count
positions instead of `Ack` calls, and to trigger on reaching the threshold (`>=`). After that change,
the count bound is 10 000 positions whatever the source batch size.

Read-ahead adds the queue credit on top of that window: records submitted but not yet released.

| Destination settings | Record cap C | Credit | Added replay (queued plus in flight) | SIGKILL replay window, total |
| --- | --- | --- | --- | --- |
| none (default) | 512 | 2 × C | 1024, plus one source read | persister window (10 000 records at most) + 1024 + one read |
| `sdk.batch.size: S` | S | 2 × S, as today | up to 4 × S, as #3210 documents; on unverified Nx1, (N + 3) × S | persister window + the added replay |
| `sdk.batch.delay` only | 3000 | 6000 | up to 12 000, as today; on unverified Nx1, (N + 3) × 3000 | persister window + the added replay |

For a default pipeline, the change goes from "up to 10 000 records" to "up to about 11 000". That sits
inside a window that is already there, and adds about 10%. Duplicates only, never loss. The release
note says: "arch-v2 single-destination pipelines now write while they read. After a crash, up to 1024
more records than before can be redelivered, on top of the up to 1 s or 10 000 records the position
persister already allows." The runbook entry in `docs/operations/archv2-destination-batching.md` gives
the same formula. Graceful stop does not grow: R6 drains to zero, and the persister flushes on
teardown.

### Every source ack goes through the queue (H1)

In read-ahead mode the worker never calls the source's ack or nack itself, and it does not enqueue
anything while a pass is still running.

**Staging.** For each pass of `Worker.Do`, which is one source read through every task, the worker
keeps a staging list. The pass's top-level acker is a stager. Every `Ack` or `Nack` that any task in
the pass would make is appended to the list as a barrier entry, instead of being run. That covers the
first task's shortcut, a processor's nack, and the tainted loop's sub-batches. The read-ahead
destination task's `doBuffered` appends its data entry to the same list. In read-ahead mode it
receives the list, not a queue. The tainted loop visits sub-batches in index order, so list order is
read order. When the pass returns, the worker enqueues the whole list in one critical section on the
destination's queue mutex. With `sdk.batch.*`, the accumulator absorbs the data entries. A barrier
is never completed ahead of a data entry staged before it from the same source. The rule is enforced
per buffer: a barrier that arrives while its source's buffer holds data goes into that buffer, behind
the data, and leaves with that buffer's flush. Only a barrier whose source buffer is empty passes
straight to the queue. With one buffer per source (unverified Nx1), a barrier waits only for its own
source's buffer, never for another source's. R11 includes a test where source A's barrier arrives
while A's buffer and B's buffer both hold data: A's barrier must complete after A's flush and must
not wait for B's.

The handle is the worker's, not a processor's. For M = 1, `buildRunnablePipeline` gives each worker a
pointer to the single destination task, and on Nx1 every worker gets the same pointer. Processors
never see the queue; they only call the acker they are already given.

On a shared Nx1 queue, a barrier from source A that lands behind source B's in-flight write waits for
that write. This head-of-line wait is bounded by one `Write` plus its acks. It costs latency, not
correctness; per-source order is all that invariant 2 needs.

Every terminal outcome
reached before the destination becomes a **barrier submission**: a submission with no records, which
carries the `*Batch` and acker call it would have made. That covers an all-filtered batch, the first
task's no-active-records shortcut, and a processor-nacked sub-batch, whose DLQ write is deferred with
it. The writer passes a barrier through without a `Write`. The AckReader completes it in FIFO order,
after every earlier submission. The completion goroutine is the only goroutine that calls the
source's ack, `Worker.Nack` or `DLQ.Nack` for that destination. `TestDestinationTask_DoBuffered_AllFilteredAcksImmediately`
asserts the old bypass, and is replaced.

**DLQ writes block the AckReader.** A barrier or `ackPending` sub-batch that nacks runs `DLQ.Nack` on
the AckReader, synchronously. While a DLQ write is in progress, the AckReader reads no destination acks
and completes nothing. The writer keeps sending until the in-flight limit is reached, and then credit
fills and the source stops reading. That is deliberate: the DLQ write is part of releasing that
position, so a later position cannot be released before it. The cost is that a slow DLQ throttles the
whole pipeline, which is what happens today with the synchronous path. A DLQ that fails makes the
release fail. The error is sticky, as in the completion model. `conduit_funnel_destination_enqueue_blocked_seconds_total`
shows the stall, and the runbook entry says to check DLQ latency when it rises without the destination
slowing down.

This depends on the H1 fix on `main` for opt-in batching, which may take a different route. Whatever
that fix chooses, R11 is the invariant both have to pass.

### Split-run state has one owner at a time (H2)

Moving votes to the completion goroutine is not enough on its own. `Batch.SplitRecord` grows
`run.total` on the worker goroutine (`batch.go`). `splitRun.complete` reads `total` and
`terminalCount` (`run_ledger.go`). If sub-batch A of a read were enqueued and voted while the worker was
still splitting sub-batch B of the same run, for example on a retry or a downstream split, that would
race on `total`.

Staging closes it. Nothing from a pass is enqueued until the pass has returned. By then every
processor and every retry recursion for that read has run, so no further `SplitRecord` can happen on
its runs, and `total` is final. After the enqueue, only the AckReader touches the read's `Batch` and
`splitRun` values. That is a happens-before through the queue mutex, which is a single handoff, not
shared access.

The H2 fix on `main` (synchronizing `splitRun`, #3294) stays required for read-ahead mode too, as
defence in depth. It is not the mechanism read-ahead relies on. R12 has two parts:

- **The race.** A processor that calls `SplitRecord` on sub-batch B while a destination with instant
  acks would complete sub-batch A, run under `-race -count=200`. With staging it is race-free and
  releases the run once. With staging disabled, behind a test-only switch, the race detector flags it,
  which proves the test reaches the window.
- **Divergent members.** A split run with some members written, some filtered and some nacked: one
  release, with nack-wins.

### Completion model (total)

`ackPending` releases a submission as a sequence of sub-batches, each one acked or nacked by flag. One
completion function per submission runs once, guarded by a `completed` flag and the per-worker
outstanding counter:

- **Released in full:** every sub-batch acked or nacked.
- **Released prefix k:** the first k positions of the submission released, then a release failed
  (`Source.Ack` error, DLQ write failure, or nack window exceeded). Positions after k replay. The
  error is sticky and fails the run, so no later submission releases anything. k counts records, not
  sub-batches. `Worker.Nack` acks `positions[:n]` for the n records the DLQ accepted, even when
  `DLQ.Nack` returns an error for the rest, so a release can stop partway through a sub-batch. A
  replay can therefore write a record to the DLQ a second time. That is accepted: the DLQ is
  at-least-once, like the destination.
- **Not released (k = 0):** a `Write` error, an `Ack` error or mismatch, a run-context cancel, or close
  with the submission still queued. Every position replays.

Each submission ends in exactly one of these, defined by k (0 to its record count), so the model is
total. Persisted positions
are then always a prefix of queue order per source (invariant 2), whatever state the run ends in. This
is a flag and a counter, not a state machine. That is the least machinery that makes "completes
exactly once" checkable in tests.

### Close and stop

- **Graceful stop (R6, includes #3231).** `Worker.Stop` sets `w.stop`, flushes any accumulator it
  feeds, and waits for its outstanding counter to reach zero, bounded by the context passed to `Stop`.
  Only then does it call `tearDownSource`. The completion path does not take `processingLock`, and a
  batch read after `stop` is armed is discarded (`worker.go`), so nothing is enqueued after the wait
  sees zero. If the deadline passes, teardown goes ahead, and late releases fail and replay. The
  comment in `connector/source.go` that claims no `Ack` can start once `Teardown` begins is corrected
  in the same PR. R6 also deletes the `pipelinedFlusherRestart` workaround, so the chaos restart runs
  batching again.
- **`DestinationTask.Close`** follows #3292 R2 and R3: close and enqueue share the queue mutex, writes
  already sent complete through their real acks, and the join completes whatever is left.
- **Fatal error or force-stop:** cancel. Everything not yet released replays.

## Requirements and tests

R1, R3 and R4 extend #3292's R1, R2 and R3 (its numbering at `d95843d2`) to coalesced groups and
barriers. R2 is #3292's former R4. Each test is written to fail without
its fix.

| # | Requirement | Test |
| --- | --- | --- |
| R1 | Group completion (#3292 R1): a failure or cancel completes every submission in the taken group and in the queue exactly once, with k computed per the model | `TestWriter_WriteErrorCompletesGroup`, `TestWriter_CancelCompletesQueue`; outstanding counters reach zero |
| R2 | Cap look-ahead (#3292's former R4): never take past either cap, never lose or reorder at the cut | `TestTakePolicy_RecordCap`, `TestTakePolicy_ByteCap`, and a rapid property: the `Write`s concatenated equal the submissions in order, each `Write` is under the caps unless it holds a single submission, and each submission completes once |
| R3 | Enqueue versus close (#3292 R2) | `TestDestinationTask_EnqueueRacesClose`, `-race -count=500` |
| R4 | A sent `Write` completes only through its real acks or a stream end (#3292 R3) | `TestDestinationTask_CloseWaitsForSentWriteAcks` |
| R5 | Nack fan-back in a coalesced `Write` | `TestCoalescedWrite_NackMapsToOwningSubmission`, with the nack first, middle and last, and across ack chunks |
| R6 | Graceful drain before source teardown (#3231) | `TestWorker_StopDrainsBeforeSourceTeardown`; chaos SIGTERM with zero duplicates; `pipelinedFlusherRestart` workaround removed |
| R7 | Per-source order on Nx1 | `worker_nsource_test.go`: 4 sources, 1 default destination |
| R8 | The estimator bounds wire size | `TestEstimateWireBytes_BoundsProtoSize` (rapid): at least `proto.Size`, within 5% |
| R9 | Accumulator byte cap (its own `fix:` PR first) | `TestDoBuffered_CutsOnByteCap`, and a standalone test over 4 MiB that fails on `main` |
| R10 | Existing suites pass in the new default | the `pkg/lifecycle-poc/funnel` and `tests/chaos` suites, unchanged |
| R11 | Every source ack and nack goes through the queue in order (H1) | `TestReadAhead_FilteredBatchWaitsForEarlierWrite`, `TestReadAhead_ProcessorNackWaitsForEarlierRecords`, and a chaos test that SIGKILLs between a processor nack and the write of records ahead of it; asserts no persisted position passes an undelivered record |
| R12 | Split-run state has one owner at a time (H2): nothing from a pass is enqueued before the pass ends | `TestReadAhead_SplitRecordRacesCompletionVote` (`SplitRecord` on sub-batch B against an instant completion of sub-batch A, `-race -count=200`, flagged with staging switched off), and `TestReadAhead_SplitRunDivergentMembers` |
| R13 | Total completion model | `TestAckPending_NackFailsMidSubmission`: sub-batch 1 acked, sub-batch 2's DLQ write fails; the persisted position is the end of sub-batch 1, the run fails, and restart replays from there |
| R14 | Single-source guard for unverified destinations | `TestTakePolicy_UnverifiedNeverMixesSources`: 2 sources with byte-identical positions into 1 unverified destination; every `Write` holds one source's records, barriers interleave freely, and the in-flight limit is 1. Plus `TestDoBuffered_UnverifiedAccumulatorPerSource` |

### Chaos

`TestSIGKILL_CoalescedWrite_CrashMidWrite`, modelled on `pipelined_flusher_sigkill_test.go`, uses a
default destination with a paced `Write`, so groups of two or more submissions are in flight. A test
hook asserts that at least one group of two or more was in flight at the kill; otherwise the test
proves nothing. It reuses `assertRecoveryInvariantsAtKill` (invariant 1) and
`assertRecoveryInvariantsAfterRestart` (invariant 3), and also asserts that the duplicates delivered
are no more than the persister window the child is configured with, plus the queue credit, plus one
read. There are variants with 4 sources into 1
destination, and with a filtering processor (for R11). All run in the required `tests/chaos (race, x3)`.

## Failure modes

- **SIGKILL with a queue or a coalesced `Write` in flight.** Nothing past the persisted prefix was
  released, so it replays, within the published bound.
- **`Write` or `Ack` failure, or an ack mismatch.** Sticky. Every submission completes with k = 0, the
  run fails, and every worker's next enqueue gets the error (#3292).
- **Release fails partway through a submission.** Prefix k is released and the rest replays (R13).
- **A filter or processor nack ahead of an unwritten record.** It waits in the queue as a barrier
  (R11).
- **Oversized standalone submission.** Sent alone. Rejected with
  `pipeline.destination_write_exceeds_message_limit`. Nothing is released.
- **Estimator below the wire size.** R8 exists to prevent it. If it happens anyway, it is the same
  rejection, and nothing is lost.
- **Slow destination.** Credit fills, enqueue blocks, and the source stops reading.
- **Sparse source.** The queue is empty, so each read is written at once with no added latency (benchi
  arm 5).
- **Wedged destination at stop.** The bounded `Stop` deadline, then cancel.

## Upgrade and rollback

No serialized format changes: positions, state, protocol and config schema are untouched, so N and N+1
read each other's state. Behaviour changes, and the release notes say so. One `Write` can carry
several reads and sources. The SIGKILL replay window of a default pipeline grows by up to 1024 records, on top of the
persister's existing 1 s or 10 000 record window.
Source acks for filtered and processor-nacked records can lag behind later reads. Rollback is a revert.
The per-pipeline escape hatch is an explicit `sdk.batch.size: 1` on the destination (see Decisions).

## Observability

Labelled like the `conduit_connector_*` family, so no new cardinality:

- `conduit_funnel_destination_write_submissions` (histogram): submissions per `Write`, barriers
  excluded.
- `conduit_funnel_destination_write_records` (histogram): records per `Write`.
- `conduit_funnel_destination_write_bytes` (histogram): estimated wire bytes per `Write`, standalone
  destinations only.
- `conduit_funnel_destination_cap_cuts_total{reason="records|bytes"}` (counter).
- `conduit_funnel_destination_unacked_records` (gauge): records submitted but not completed, the
  live replay window.
- `conduit_funnel_destination_enqueue_blocked_seconds_total` (counter): time blocked on credit,
  meaning the destination is the bottleneck.

The `inflight_writes` and `submit_wait_seconds` metrics from #3292 cover its mode. The oversize error and the replay
window get runbook text in `docs/operations/archv2-destination-batching.md`. `llms.txt` and the
`conduit.io` arch-v2 docs change in the implementation PR.

## Benchmark plan

Nothing here is a claim until benchi says so. Configs and results go under
`benchi/archv2-write-coalescing/`, following `benchi/METHODOLOGY.md`: base, head and base again,
interleaved over 5 rounds, medians with spread, and the A/A noise floor cited. Each decision below is
pre-registered and is valid only when the floor is under 5%.

1. **The gate (arm 3 first).** CDC parity (`benchi/v021-cdc-parity`) and generator to file at 1x1,
   both with default source batching already on `main`. Coalescing proceeds only if it improves either
   shape by more than twice the floor, and regresses neither by more than the floor.
2. **In-flight writes.** 1x1 generator to file, source batch 1, this design against the prototype's
   coalescing arm. If the median is under 79k records/s (two-thirds of the prototype's +36k gain over
   54.5k), raise the verified destinations' in-flight limit to a records window, as in Alternative D,
   and re-run. Unverified destinations stay at 1.
3. **Source batch 1, 10, 100** at 1x1: no regression above the floor at 100.
4. **Nx1** (`benchi/archv2-nxm` with M = 1): 4 sources into 1 destination.
5. **Latency:** a paced generator at 100 and 1000 records/s, p50 and p99 from read to ack. No p99
   regression above the floor.
6. **Standalone destination:** 2 KiB raw records and numeric-heavy structured records. This measures
   the estimator's cost at 1x1, batch 1, and proves the byte cap holds.
7. **Credit 1x against 2x.** 1x1 and Nx1, source batch 1, default destination. If 1x credit is within
   the A/A floor of 2x on both shapes, the default becomes 1x, which halves the added replay to 512.
8. **`make bench-engine`:** add `BenchmarkDestinationTask_Coalesced` so the per-PR gate covers the
   path.

## Decisions

DeVaris decided the open questions on 2026-10-27.

1. **Escape hatch.** An explicit `sdk.batch.size: 1` on a destination means stop-and-wait, as before:
   no read-ahead and no coalescing. `config.SDKBatchSettings` is changed to tell unset apart from an
   explicit 1, which it cannot do today (`size > 1 || delay > 0`). Without the setting, read-ahead and
   coalescing are on. No new config key and no flag.
2. **Credit.** 2x the record cap, so up to about 1024 records of added replay for a default pipeline.
   The default switches to 1x if pre-registered benchi arm 7 shows 1x matches 2x throughput.
3. **Ownership.** One destination means this doc's read-ahead mode. Two or more destinations means
   #3292's concurrent mode, under the conditions #3292 sets. This is the table in "Ownership per
   shape".

No open questions remain.

## Related

- `docs/design-documents/20261027-funnel-shared-destination-concurrency.md` (#3292, checked at
  `d95843d2`): the writer, the AckReader, the completion hook, its R1 to R3, the verified-destination gate
  and the single-source guard, and concurrent mode for NxM. A prerequisite.
- `docs/design-documents/20261027-default-source-batching.md`: the hard gate.
- `docs/design-documents/20261017-funnel-destination-batching.md` and
  `docs/design-documents/20261019-funnel-read-ahead-pipelining.md`: the accumulator, the fan-back, and
  the flusher this folds in.
- #3231: partial batch, and stop before drain.
- Prerequisite fixes on `main`: #3294 (H1), #3294 (H2), #3296 (accumulator byte cap), #3295
  (#3231). See "Existing defects this depends on".
- #3293 (default source batching, which also changes the persister to count positions).
- #2978: the prototype and its review findings.
- Roadmap: v0.21 engine, "Adaptive batching by default" and "Destination ack reader". v0.22, "Default
  engine flip".
