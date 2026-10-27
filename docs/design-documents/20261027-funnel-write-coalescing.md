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
spread to every pipeline. They are fixed on `main` as separate PRs. This design names them as
prerequisites and does not redesign them:

- **#3231.** A partial batch has no flush timer. On graceful stop, `Worker.Stop` tears down the source
  before `DestinationTask.Close` drains the flusher, so the trailing source acks fail with
  `plugin.ErrPluginNotRunning` and the records replay. `tests/chaos/pipelined_flusher_sigkill_test.go`
  works around it: `pipelinedFlusherRestart` turns batching off for the restart.
- **H1, out-of-order source acks.** An all-filtered batch (`doBuffered`, and the worker's
  `!b.HasActiveRecords()` shortcut) and a processor-nacked sub-batch (`acker.Nack` to `Worker.Nack` to
  `Source.Ack(positions[:n])`) ack the source immediately, on the worker goroutine. Earlier records of
  the same read, or earlier reads, may still be buffered or in flight. `Source.Ack` persists the last
  position unconditionally, so a crash in that window resumes past records that were never written.
  That violates invariants 1, 2 and 3.
- **H2, unsynchronized `splitRun` voting.** A split run's members can be voted by the flusher (members
  written) and by the worker (members filtered or nacked) at once. `splitRun`'s fields are not
  synchronized, so a lost or a double release is possible.
- **Accumulator byte cap.** The accumulator cuts on record count only. For a standalone destination,
  3000 records can exceed the plugin's 4 MiB receive limit. This ships as its own `fix:` PR with a
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

The AckReader from #3292 makes this a parameter, not a new component. Phase 1 starts at one coalesced
`Write` in flight per destination. That is today's flusher, and the simplest ordering argument. The
rule for raising it is pre-registered in benchi arm 2.

### E. A second coalescing site: #3201's flusher, or #3292's inbox carry slot

- **Lost:** two places to get the caps, the look-ahead and the completion rules right. The flusher is
  folded into the writer, and #3292's R4 becomes this doc's R2.

## Decision

### Ownership per shape

After #3292 and this doc have both landed:

| Shape | Stream owner | Worker waits for its write? | Coalescing | Owning doc |
| --- | --- | --- | --- | --- |
| 1x1, any batching | writer and AckReader, read-ahead mode | no | yes | this doc |
| Nx1, any batching, bare or with processors | writer and AckReader, read-ahead mode; `sharedMu` (processor roots only) held for the processor pass and the enqueue | no | yes, across sources | this doc |
| 1xM, M of 2 or more | direct `Do` (unchanged) | yes | no | neither |
| NxM, M and N of 2 or more, bare, no batching | writer and AckReader, #3292 concurrent mode | yes | yes, across workers (N or fewer queued) | #3292 |
| NxM with batching, or with processor roots | synchronous, under `sharedMu` (unchanged) | yes | no | neither; needs the #2978 ledger |

The predicate is simple: one destination means read-ahead mode; two or more means #3292's rules.
Phase 1 of #3292 currently gates concurrent mode on `batchingEnabled() == false` and two or more
workers, which includes Nx1. I am asking its author to narrow that gate to M of 2 or more, so Nx1 has
one owner and never runs a mode that lives for only one release. If #3292 lands first with Nx1
included, this doc's PR moves Nx1 to read-ahead mode and deletes the gate branch.

Earlier drafts said fan-out coalescing would wait on an ordered cross-batch source-ack release from #3292.
It does not provide one, and does not need one, because its workers wait for their own
completion. Fan-out read-ahead, where the worker does not wait, still needs the #2978 ledger and is
out of scope for both docs.

### Take policy (the one coalescing site)

- **Submission.** One `doBuffered` call: its `*Batch`, its `ackNacker`, its records, and an estimated
  byte size. A submission is never split across `Write`s.
- **Record cap.** An explicit `sdk.batch.size` is the cap. Without one, the cap is `512` records, the
  prototype's measured setting, not 3000. That keeps the replay window small (see "Credit and replay
  window"). Delay-only batching keeps its 3000 threshold for the accumulator, and coalescing is
  capped at 3000 too.
- **Byte cap.** This applies to standalone destinations only, and to the accumulator as well (R9):
  `maxWriteBytes = connectors.max-receive-record-size × 7/8`, which is 3.5 MiB by default. It is
  derived from wire size; see the estimator. Builtin destinations have no message limit and are
  capped by records only.
- **Look-ahead.** The queue is a mutex-guarded slice. The writer peeks before it takes, and takes the
  longest prefix that stays under both caps. A head submission that is over a cap on its own goes
  alone, and is subject to the oversize error below.
- **No waiting.** The writer blocks only while the queue is empty, or while the in-flight limit is
  reached (1 in phase 1).

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
flight. An empty queue always admits one submission. Enqueue unblocks on completion, writer exit,
close or cancel.

| Destination settings | Record cap C | Credit | Records unacked at most (the SIGKILL replay window) |
| --- | --- | --- | --- |
| none (default) | 512 | 2 × C | 1024, plus one source read in progress |
| `sdk.batch.size: S` | S | 2 × S, as today | 4 × S, as #3210 documents today |
| `sdk.batch.delay` only | 3000 | 6000 | 12 000, as today |

For a default pipeline, a SIGKILL today replays at most the one batch in flight, which is one record at
source batch 1. After this change it replays up to about 1024 records, which is about 11 ms of data at
90k records/s. Duplicates only, never loss. But that is a visible change for consumers that are not
idempotent. It goes in the release notes and in `docs/operations/archv2-destination-batching.md`.
Graceful stop does not grow: R6 drains to zero.

### Every source ack goes through the queue (H1)

In read-ahead mode the worker never calls the source's ack or nack itself. Every terminal outcome
reached before the destination becomes a **barrier submission**: a submission with no records, which
carries the `*Batch` and acker call it would have made. That covers an all-filtered batch, the first
task's no-active-records shortcut, and a processor-nacked sub-batch, whose DLQ write is deferred with
it. The writer passes a barrier through without a `Write`. The AckReader completes it in FIFO order,
after every earlier submission. The completion goroutine is the only goroutine that calls the
source's ack, `Worker.Nack` or `DLQ.Nack` for that destination. `TestDestinationTask_DoBuffered_AllFilteredAcksImmediately`
asserts the old bypass, and is replaced.

This depends on the H1 fix on `main` for opt-in batching, which may take a different route. Whatever
that fix chooses, R11 is the invariant both have to pass.

### Split-run votes on one goroutine (H2)

With H1's routing, every vote for a source batch's split runs in read-ahead mode is cast on the
completion goroutine, so the votes are serialized by construction. The H2 fix on `main` (synchronizing
`splitRun`) remains the defence for the other modes. R12 is a `-race` test with a split run whose
members diverge: some written, some filtered, some nacked.

### Completion model (total)

`ackPending` releases a submission as a sequence of sub-batches, each one acked or nacked by flag. One
completion function per submission runs once, guarded by a `completed` flag and the per-worker
outstanding counter:

- **Released in full:** every sub-batch acked or nacked.
- **Released prefix k:** sub-batches 1 to k released, then the release of sub-batch k+1 failed
  (`Source.Ack` error, DLQ write failure, or nack window exceeded). Positions after sub-batch k replay.
  The error is sticky and fails the run, so no later submission releases anything.
- **Not released (k = 0):** a `Write` error, an `Ack` error or mismatch, a run-context cancel, or close
  with the submission still queued. Every position replays.

Each submission ends in exactly one of these, defined by k, so the model is total. Persisted positions
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

R1 to R4 extend #3292's R1 to R4 to coalesced groups and barriers. Each test is written to fail without
its fix.

| # | Requirement | Test |
| --- | --- | --- |
| R1 | Group completion: a failure or cancel completes every submission in the taken group and in the queue exactly once, with k computed per the model | `TestWriter_WriteErrorCompletesGroup`, `TestWriter_CancelCompletesQueue`; outstanding counters reach zero |
| R2 | Cap look-ahead (#3292 R4): never take past either cap, never lose or reorder at the cut | `TestTakePolicy_RecordCap`, `TestTakePolicy_ByteCap`, and a rapid property: the `Write`s concatenated equal the submissions in order, each `Write` is under the caps unless it holds a single submission, and each submission completes once |
| R3 | Enqueue versus close (#3292 R2) | `TestDestinationTask_EnqueueRacesClose`, `-race -count=500` |
| R4 | A sent `Write` completes only through its real acks or a stream end (#3292 R3) | `TestDestinationTask_CloseWaitsForSentWriteAcks` |
| R5 | Nack fan-back in a coalesced `Write` | `TestCoalescedWrite_NackMapsToOwningSubmission`, with the nack first, middle and last, and across ack chunks |
| R6 | Graceful drain before source teardown (#3231) | `TestWorker_StopDrainsBeforeSourceTeardown`; chaos SIGTERM with zero duplicates; `pipelinedFlusherRestart` workaround removed |
| R7 | Per-source order on Nx1 | `worker_nsource_test.go`: 4 sources, 1 default destination |
| R8 | The estimator bounds wire size | `TestEstimateWireBytes_BoundsProtoSize` (rapid): at least `proto.Size`, within 5% |
| R9 | Accumulator byte cap (its own `fix:` PR first) | `TestDoBuffered_CutsOnByteCap`, and a standalone test over 4 MiB that fails on `main` |
| R10 | Existing suites pass in the new default | the `pkg/lifecycle-poc/funnel` and `tests/chaos` suites, unchanged |
| R11 | Every source ack and nack goes through the queue in order (H1) | `TestReadAhead_FilteredBatchWaitsForEarlierWrite`, `TestReadAhead_ProcessorNackWaitsForEarlierRecords`, and a chaos test that SIGKILLs between a processor nack and the write of records ahead of it; asserts no persisted position passes an undelivered record |
| R12 | Split-run votes are serialized (H2) | `TestReadAhead_SplitRunDivergentMembers_Race`, `-race -count=200` |
| R13 | Total completion model | `TestAckPending_NackFailsMidSubmission`: sub-batch 1 acked, sub-batch 2's DLQ write fails; the persisted position is the end of sub-batch 1, the run fails, and restart replays from there |

### Chaos

`TestSIGKILL_CoalescedWrite_CrashMidWrite`, modelled on `pipelined_flusher_sigkill_test.go`, uses a
default destination with a paced `Write`, so groups of two or more submissions are in flight. A test
hook asserts that at least one group of two or more was in flight at the kill; otherwise the test
proves nothing. It reuses `assertRecoveryInvariantsAtKill` (invariant 1) and
`assertRecoveryInvariantsAfterRestart` (invariant 3), and also asserts that the duplicates delivered
are no more than the replay-window bound in the table. There are variants with 4 sources into 1
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
several reads and sources. The SIGKILL replay window of a default pipeline grows to about 1024 records.
Source acks for filtered and processor-nacked records can lag behind later reads. Rollback is a revert.
The per-pipeline escape hatch is open question 1.

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
   54.5k), raise phase 1's in-flight limit to a records window, as in Alternative D, and re-run.
3. **Source batch 1, 10, 100** at 1x1: no regression above the floor at 100.
4. **Nx1** (`benchi/archv2-nxm` with M = 1): 4 sources into 1 destination.
5. **Latency:** a paced generator at 100 and 1000 records/s, p50 and p99 from read to ack. No p99
   regression above the floor.
6. **Standalone destination:** 2 KiB raw records and numeric-heavy structured records. This measures
   the estimator's cost at 1x1, batch 1, and proves the byte cap holds.
7. **`make bench-engine`:** add `BenchmarkDestinationTask_Coalesced` so the per-PR gate covers the
   path.

## Open questions for DeVaris

1. **Escape hatch.** Options: revert only; an explicit destination `sdk.batch.size: 1` meaning
   stop-and-wait, which needs `SDKBatchSettings` to tell unset apart from 1; or a temporary flag. I
   lean to the explicit `sdk.batch.size: 1`.
2. **Replay window.** Is about 1024 records (512 plus a 2x credit) acceptable for default pipelines?
   Or should the default credit be 1x, which gives about 512 at some cost to coalescing?
3. **Nx1 ownership.** Do you agree with "one destination means read-ahead mode", with #3292 narrowed to
   M of 2 or more?

## Related

- `docs/design-documents/20261027-funnel-shared-destination-concurrency.md` (#3292): the writer and
  AckReader, its R1 to R4, and its concurrent mode for NxM. A prerequisite.
- `docs/design-documents/20261027-default-source-batching.md`: the hard gate.
- `docs/design-documents/20261017-funnel-destination-batching.md` and
  `docs/design-documents/20261019-funnel-read-ahead-pipelining.md`: the accumulator, the fan-back, and
  the flusher this folds in.
- #3231: partial batch, and stop before drain.
- The H1, H2 and byte-cap fixes on `main` (prerequisites; see "Existing defects this depends on").
- #2978: the prototype and its review findings.
- Roadmap: v0.21 engine, "Adaptive batching by default" and "Destination ack reader". v0.22, "Default
  engine flip".
