# Funnel shared-destination concurrency (arch-v2)

## Summary

In an arch-v2 pipeline with more than one source, every source's `funnel.Worker` takes the shared destination's
`TaskNode.sharedMu` and holds it through the whole `Write` and the wait for that write's acks. Each destination
becomes a critical section that only one worker can use at a time, so throughput per destination drops as sources are
added. In exploratory AWS runs, main went from 54.5k records/s at 1x1 to 29.9k per sink at 4x4.

This doc defines two things:

1. **A per-destination stream owner.** It has two goroutines. The **writer** is the only caller of
   `Destination.Write`. The **AckReader** is the only caller of `Destination.Ack`. Between them sits an ordered
   in-flight FIFO, and every submission completes exactly once. This is the destination half of the staged batch
   engine (`20261009-staged-batch-engine.md`, #2978). The write-coalescing design (#3291) builds its take policy and
   its read-ahead mode on this component.
2. **Concurrent mode for NxM.** This applies when N ≥ 2 sources fan out to M ≥ 2 bare destinations that are not
   batching. In that shape the shared root runs without `sharedMu`. Each worker submits its batch on every branch and
   waits until every branch's submission completes before it reads again. Workers overlap one another on the
   destination. A single worker's batches never overlap each other.

**Tier 1** (data path). Pipeline config and serialized state do not change. A separate protocol PR documents an
ack-order guarantee and makes one additive proto change: an optional `orderedAcks` field on the destination open
response, shipped as a minor bump after the versioning discussion (see "The ack-order contract" and open question 1).

### Ownership across this doc and #3291

This table matches the one in `20261027-funnel-write-coalescing.md` (#3291). It describes the state after both docs
have landed.

| Shape | Stream owner | Worker waits for its write? | Coalescing | Owning doc |
| --- | --- | --- | --- | --- |
| 1x1, any batching | writer and AckReader, read-ahead mode | no | yes | #3291 |
| Nx1, any batching | writer and AckReader, read-ahead mode | no | yes, across sources | #3291 |
| 1xM, M ≥ 2 | direct `Do` (unchanged) | yes | no | neither |
| NxM, N ≥ 2 and M ≥ 2, bare, no batching | writer and AckReader, **concurrent mode** | yes | per #3291's take policy | **this doc** |
| NxM with batching or processor roots | synchronous under `sharedMu` (unchanged) | yes | no | neither (needs the #2978 ledger) |

- **One stream owner.** Each destination has one writer and one AckReader in every mode. #3291 folds today's #3210
  flusher into the writer. The earlier draft's "phase 2" of this doc now belongs to #3291.
- **One coalescing site.** Coalescing happens only in #3291's take policy, the rule the writer uses to pick queued
  submissions. This doc's former R4 (the cap look-ahead) is #3291's R2. Until the take policy lands, the writer takes
  one submission per `Write`.
- **Dependencies, both ways.** #3291 depends on this doc for the writer, the AckReader, the completion contract, and
  R1 to R3. This doc's concurrent mode uses #3291's take policy for coalescing once it exists, but does not need it to
  be correct. The two can land in either order. If this doc lands first, NxM runs without coalescing until #3291
  lands.
- **What this doc does not provide.** Concurrent mode gives no ordered cross-batch source-ack release and needs none.
  Its workers wait for their own completions, so batch N is acked on every branch before batch N+1 is read. Read-ahead
  under fan-out, where the worker does not wait, needs the #2978 per-source ledger. That is out of scope for both
  docs.

### Prerequisites on main

These fixes are in progress separately. Both docs name them as prerequisites:

- **Out-of-order source acks** (#3291's "H1, out-of-order source acks"). An all-filtered batch, or a sub-batch that a
  processor nacked, is acked at once on the worker goroutine. That happens even while earlier batches from the same
  source are still buffered or in flight, so a later position can be persisted first. Concurrent mode does not reach
  this path, because nothing of the worker's own is in flight while it waits. The shared component does reach it, so
  the fix lands first.
- **`splitRun` voting from two goroutines** (#3291's "H2, unsynchronized `splitRun` voting").

## Context and problem

### What the lock protects

`Worker.doTask` takes `sharedMu` when `taskNode.sharedBoundary` is set. `funnel.NewSink` marks every shared root.
`buildSharedTail` returns one root per destination branch when there are no pipeline-level processors. When there are,
it returns a single processor root above all the branches. Destination-scoped processors are prepended inside their
branch, so such a branch's root is a processor. The lock exists for two reasons.

1. **Ack attribution on one stream.** `DestinationTask.Do` sends one `Write`, then reads `Ack` responses and assumes
   they belong to that write. If two workers interleaved on one stream, worker A could read B's acks. `validateAcks`
   compares position bytes, but positions are unique only within a single source. `tests/chaos/nsource_sigkill_test.go`
   makes them collide on purpose. When they collide, the check passes and A acks records that were never written,
   which breaks invariant 1. The H2 poison flag (`TaskNode.poisoned`, `pipeline.shared_destination_poisoned`) covers
   the error case: a `Do` that fails partway leaves unread acks behind for the next lock holder.
2. **One goroutine per stream direction.** A gRPC client stream allows one goroutine in `Send` and one in `Recv` at
   the same time, never two in the same direction. The builtin in-memory stream would tolerate concurrent senders,
   because it is built on channels. A standalone plugin's stream would not.

The lock also covers shared pipeline-level processors and destination processors, which do not have to be safe for
concurrent calls. The poison flag trips on any error that escapes a shared pass, including acker and DLQ errors.

The nsource design (`20260801-archv2-multiconnector-nsource.md`, "Consequence for throughput") accepted this
serialization because v1's `FaninNode` also funnels into one destination consumer. But that consumer does not wait
for acks before sending the next write. `pkg/lifecycle/stream/destination_acker.go` queues every message it sends and
pairs acks to that queue in FIFO order with only a position byte check. The arch-v2 lock holder waits instead.

### Evidence

Setup: AWS c7i.4xlarge (us-west-1), host mode without Docker, generator to file, 3 interleaved rounds, records
counted at sink-1. The spread across rounds was 3.5% or less. This is **not** the A/A-controlled harness, so these are
diagnostic numbers, not a performance claim. Profiles come from run `20261027T054415Z` (results bucket
`conduit-proto1x1-profile-132836343092`).

| shape (default batching) | main a23efb64 | #2978 rebased | #2978 rebased + coalescing |
| --- | --- | --- | --- |
| 1x1 | 54.5k rec/s | 61.3k | 90.8k |
| 4x4, per sink | 29.9k rec/s | 47.2k | 152.9k |
| 1x1, source batch 100 | 221-235k | 220k | 221k |

- **Mutex profile, main 4x4.** 156.6s of contention delay, 99% of it charged to `Worker.doTask`. The mutex profile
  charges delay to the unlock site, and here that is `sharedMu`.
- **Block profile, main 4x4.** This profile is cumulative from process start, not a window. The four `Worker.Do`
  goroutines were blocked 228.4s in total. 226.2s of that was in the fan-out's `WaitGroup.Wait`, waiting for branch
  goroutines that were queued on `sharedMu` (123.3s of `Mutex.Lock` delay). The earlier draft compared 226s against
  a "200s wall" denominator, which was wrong: the profile has no wall-time window.
- #2978 removes the lock for bare-destination roots and adds read-ahead, a ledger, and several `Write`s in flight.
  At 1x1, where the lock is never contended, that machinery adds 12%. At 4x4 it adds 58%.

**Not measured:** concurrent mode on its own, meaning no lock, workers stop-and-wait, and no read-ahead. The benchmark
plan decides whether that is enough.

## Constraints

1. **Invariant 1.** A source record is acked only after the destination acked that exact record. Attribution comes
   from stream order. The byte check must be able to catch a missing or reordered ack.
2. **Invariant 2.** Each source's positions reach `Source.Ack` in ascending order.
3. **Invariant 3.** Every submission reaches exactly one terminal state on every error, cancel and close path. An
   abandoned submission is never acked.
4. **Invariant 4.** Within one source, records reach the destination in source order. The interleaving between
   sources is unspecified, as it is today.
5. **Invariant 7.** A graceful stop drains in-flight submissions before teardown. SIGKILL at any point recovers
   without loss.
6. **One goroutine per stream direction.**
7. **No regression for 1x1 or 1xM.** In concurrent mode these shapes do not change. #3291 covers 1x1.
8. **No silent entry for unverified destinations.** A standalone destination built with an SDK nobody has verified
   must not run a mode whose correctness depends on behavior that has never been tested.

## Alternatives considered

### A. Writer plus AckReader per destination; NxM workers stop-and-wait (chosen)

- **Pros.** It removes both reasons for the lock at once. Each submission carries its own completion, so attribution
  is a FIFO pop, not a byte comparison. The worker's ack, nack, DLQ and split-run code stays the same. It is the same
  component that the staged engine and #3291 build on, so none of the work is thrown away. How many `Write`s may be
  in flight becomes a parameter, so a destination can run the safe setting of 1.
- **Cons.** Two goroutines per destination. With more than one `Write` in flight, correctness depends on acks arriving
  in write order across `Write`s, which this doc pins down below.

### B. Keep #3210's flusher (Write, then drain acks) as the only owner

- **Lost because** it is Alternative A with the in-flight limit fixed at 1 and no separate AckReader. It cannot
  overlap a `Write` with the previous write's acks, and the limit could never be raised. A covers B as the in-flight=1
  setting, which is the setting unverified destinations get.

### C. Ticketed send lock

Take a short lock to `Send` and get a ticket, then wait for your turn to `Recv`.

- **Lost because** ack responses can span writes, so whoever holds the read turn has to pass other workers' acks on.
  That is a demultiplexer spread across N goroutines with no single owner. A worker cancelled while it holds the read
  turn leaves the stream in the middle of a response, which is exactly the H2 desync.

### D. One destination instance per worker

- **Lost because** `connector.Destination.Open` refuses a second instance with the same connector ID, and connector
  state is keyed by connector ID. Destinations with exclusive output would corrupt or split their output: the file
  destination writes a single file, and S3 names objects by sequence. It also breaks the single-pipeline-engine ADR's
  rule of one writer per destination.

### E. Do nothing

- **Lost because** the v0.22 engine flip is gated on parity or better at shipped defaults, and NxM is the shape where
  arch-v2 gets worse as it grows.

### F. Land all of #2978 at once

- **Not chosen.** It also changes when sources get acked (the ledger and read-ahead). Its review found four bugs in
  the destination path. Landing the component first, behind stop-and-wait acks, gets those fixes and their tests onto
  main with a smaller blast radius.

## Decision

### The component

`DestinationTask` gets a writer, an AckReader, and a submission queue, all created in `Open`. A submission holds its
records, their positions, an ack slot, and a completion.

- **Writer.** It takes the next submissions from the queue: one per `Write`, or what #3291's take policy picks once
  that exists. Under `qMu` it checks for a sticky error, for `closed`, and for the in-flight limit. It appends the
  submissions to the in-flight FIFO **before** calling `Write`, so the AckReader never sees an ack it has no entry for
  and FIFO order always equals stream order. It never holds `qMu` across `Write`.
- **AckReader.** It validates each response in full against the FIFO before it applies any of it. Each position must
  match byte for byte, a surplus ack is an error, and so is an ack when nothing is outstanding. A response may span
  submissions or cover only part of one. A submission completes once every one of its records has an ack. The
  AckReader only fills the submission's ack slot. It never touches the `*Batch`.
- **In-flight limit.** For a verified destination (defined below) this is the number of attached workers in
  concurrent mode. For an unverified destination it is 1, so attribution never spans two `Write`s.
- **Completion.** Exactly once, enforced by a once-guarded `complete()`. When a real ack and a close-or-teardown
  failure race, the real ack wins. Completing closes a channel. In concurrent mode the waiting worker then applies the
  acks to its batch (`markBatchRecords`, `metrics.Observe`) on its own goroutine, and the existing
  `doTaskAttempt` ack/nack path takes over unchanged. #3291's read-ahead mode attaches its own completion to the same
  hook.
- **Sticky failure.** A `Write` error, an `Ack` error, or an ack mismatch records the first error. The writer stops
  calling `Write`. Every in-flight and queued submission completes with that error, exactly once. Any later submit
  returns `pipeline.shared_destination_poisoned` with the cause. That code stays registered until the
  single-pipeline-engine ADR retires it.

### Concurrent mode (NxM)

**Scope rule.** A shared root runs without `sharedMu` and without the poison check when all of the following hold:
its task is a `*DestinationTask` with no `Next` (a bare destination); the pipeline has N ≥ 2 sources and M ≥ 2
destinations; and the destination has no `sdk.batch.*` set. Nx1 belongs to #3291. Every other root keeps `sharedMu`
and poisoning exactly as today. In concurrent mode an acker or DLQ error fails only the worker it happened in, because
the stream is not desynchronized. The pipeline is killed either way, so the visible result is the same.

- **Submit and wait.** On each branch, `DestinationTask.Do` checks `ctx.Err()` and the sticky error, submits, then
  waits for completion or `ctx.Done()`. `doNextTask`'s pool runs the M branches concurrently, as it does today.
- **Ordering across batches.** The worker reads batch N+1 only after all M branches of batch N have returned, so after
  N's `multiAckNacker` has called `Source.Ack` (invariant 2).
- **At most one submission per source per destination.** If a wait ends on `ctx.Done()`, the submission is still
  outstanding. The task records this, and refuses any further submit from that worker until the submission
  completes. This keeps the bound true even if a caller carries on after a cancel.
- **Record lifetime (#2910).** Bare-destination branches share records read-only. Today the worker's `doNextTask`
  returns after `p.Wait()`. That can now come before the writer's last read of a submission whose wait was
  cancelled. The rule therefore becomes: records are immutable until every submission that references them has
  completed. The submission keeps its own slice (`ActiveRecords()` already returns a new one), and the builtin stream
  clones on `Send`.
- **Empty submissions.** A branch batch with no active records is never submitted. It completes on the worker with no
  `Write`. The SDK adapter indexes `batch.Records[len-1]` and would panic on an empty request, and a submission with no
  records could never complete by ack count. Under stop-and-wait none of the worker's own batches is in flight, so
  completing early cannot ack anything out of order.
- `doTask` skips `sharedMu` for such a root. The stale comment in `worker.go` about `nsource_child.go`'s "deliberately
  disjoint posOffset" gets fixed in the same PR, because the chaos child now collides positions on purpose.

### The ack-order contract (H1)

The connector protocol does not promise that acks arrive in write order across consecutive `Write` requests, or that
every record gets exactly one ack. The Go SDK v0.17.2 does both (`destinationPluginAdapter.Run` handles one `Recv`,
one `Write` and the acks in sequence), and so does the Python SDK. That is a side effect of how they are written, not
a specified guarantee. v1 already relies on it for every destination: `destination_acker.go` pairs acks in FIFO order
with several writes in flight. Decision:

1. **It becomes a documented protocol guarantee.** A separate `conduit-connector-protocol` PR states it in the
   `DestinationRunResponse` comments and the README: acks are sent in the order records were received, across all
   `Write`s on the stream, with exactly one ack per record. The same PR carries the additive `orderedAcks` field from
   item 3; no existing field changes. **This is a protocol change in the sense of CLAUDE.md** and needs the explicit
   versioning discussion before it merges (open question 1). The proposal is a minor version bump.
2. **The acceptance suite asserts it.** `conduit-connector-sdk`'s DST-01 and DST-02 (`acceptance_v2_destination.go`)
   send writes without waiting for acks. They get an order assertion: with unique positions, the sequence of acked
   positions must equal the sequence sent. Today `requireAllAcked` checks only set membership, so a reorder passes.
3. **The capability gate.** A destination counts as _verified_ if it is builtin, because it runs the in-tree SDK
   pinned in `go.mod`, or if it is standalone and declares the capability the protocol PR adds. The proposal is an
   optional `orderedAcks` field in the destination open response. Old plugins read as false. Unverified destinations
   run with an in-flight limit of 1 and only single-source `Write`s (H2 below). They still lose `sharedMu`, but
   nothing about them depends on the contract. There is no user-facing setting. The gate is automatic and errs on the
   safe side.

### Colliding positions and the guard (H2)

Suppose one `Write` carries records from two sources, or two `Write`s are in flight. A **dropped** ack is then a loss
case that no check catches. Take A's record at position `p` and B's record at a byte-identical `p`. If the destination
drops the ack for A's record, B's ack matches A's slot, and A is acked upstream for a record the destination never
confirmed. B then stalls or fails. After a restart, A's persisted position is past a record that was never written.
That is loss, not a duplicate. A reordered pair of identical positions has the same blind spot. Main has this
exposure today for Nx1 with `sdk.batch.*`, because `flushPending` mixes sources in one `Write`.

**The guard.** For unverified destinations, records from different sources never share a `Write`, and only one
`Write` is in flight. Positions are unique within a source, so a dropped or reordered ack changes the byte sequence
and fails loudly. The guard is a requirement on the one take site, #3291's take policy. Until that policy exists,
this doc's writer takes one submission per `Write`, which already satisfies the guard. Because the rule lives in the
take policy, it also covers #3291's Nx1 shape and the opt-in accumulator, and so closes main's existing
Nx1-with-batching exposure for standalone destinations.

**Rejected: a guard only for colliding positions** (mixing sources unless their positions collide). **The headline
benchmark collides by construction**, because the generator numbers each source from 1. That guard would remove the
gain in exactly the shape we measure. Correctness for verified destinations rests on the contract above, and the
acceptance suite checks it. The guard covers every other destination.

### Requirements carried from the #2978 review

A review of #2978's async destination path, done on a local rebase onto a23efb64 (not in the repo), found the four
bugs below. They are written out here so they can be reviewed from the tree.

| # | Bug as found in #2978 | Requirement | Test (fails or hangs without the fix) |
| --- | --- | --- | --- |
| R1 | The writer took a submission from the inbox, then returned on a sticky error or close without completing it. Its worker waited forever. | Every submission the writer took, and every submission still queued, completes exactly once on error, cancel or close. | `TestDestinationTask_Submit_CloseCompletesEveryWrite`; fan-out twin: all M waiting workers return |
| R2 | `closeAsync` never drained the inbox, and state created lazily on the first `Submit` could miss a close. A racing `Submit` hung. | Submit and `closed` share one mutex. A submit after close is refused and never acked. State is created in `Open`. | `TestDestinationTask_Submit_RacesClose`, `-race -count=500` |
| R3 | The AckReader exited on `closing`, so writes already sent never completed. | A sent `Write` completes only through its real acks, a stream error, or teardown. Completion is once-only, and a real ack wins a race. | `TestDestinationTask_Close_RealAckWins` |
| (R4) | The coalescing drain took one item past the cap. | Moved to #3291's take policy as its R2, together with the byte cap. | `TestFlusher_CoalesceRespectsRecordCap` / `ByteCap` (#3291) |

### Close order (M3)

`Sink.Close` runs after `workersWg.Wait()`. The steps are:

1. Set `closed` under the queue mutex. New submits are refused (R2).
2. Wait, bounded by the close context, until nothing is queued or in flight. Normally this is immediate, because every
   worker waited for its own submissions before it exited.
3. Call `destination.Teardown`. Its `stopStream` is the only thing that releases an AckReader parked in `Recv`.
   `connector.Destination.Ack` ignores its context, so cancelling the context does not.
4. Join the writer and the AckReader.
5. Complete every leftover submission with an error, exactly once. None of them is acked, so they replay.

Joining before teardown deadlocks on a destination wedged in `Recv`. Teardown followed by join is the only order that
works. #3291's `Close` follows R2 and R3 and should use this same order.

### AckReader edge cases (M1)

- **A response with zero acks** makes no progress and counts toward nothing. The AckReader loops on "an entry is
  outstanding", never on the number of `Recv` calls, and `Recv` blocks, so it cannot spin. The same review found a
  related bug on main. `DestinationTask.Do` loops `for range len(positions)`. If a response is empty, the loop can end
  with records still unacked and return `nil`, and the worker then acks those records upstream. The Go SDK never sends
  an empty response (`ack` returns early when `len(r) == 0`), but the protocol does not forbid one. This needs its own
  `fix:` with a regression test (open question 3).
- **A partial response** advances the head entry's `acked` count. **A response spanning entries** completes the head
  and continues into the next. **Acks when nothing is outstanding, or more acks than outstanding records,** are a
  mismatch and become a sticky failure.
- **After a sticky failure** the writer sends no further `Write`. The AckReader stops matching. Teardown (close step
  3) releases a plugin blocked while sending an ack.
- **A destination that stops answering.** For example, a Python destination whose SIGTERM handling breaks out of its
  `Run` loop leaves queued writes unanswered, with up to N workers waiting. They are released by the stop deadline
  (`ctx.Done()`) and by teardown. Their submissions complete with an error and replay.

## Failure modes

- **SIGKILL with several sources' submissions in flight.** None of them was acked upstream, so they replay. The
  result is duplicates, never loss.
- **A `Write` or `Ack` error, or an ack mismatch.** The failure is sticky. Every submission completes once with the
  error, every waiting worker returns, and the pipeline fails as it does today.
- **A dropped or reordered ack where position bytes collide across sources.** This is loss if a verified destination
  breaks the contract (H2). For unverified destinations the guard prevents it. A test pins this case so the
  dependency on the contract stays visible.
- **A worker cancelled while it waits.** It returns `ctx.Err()`, acks nothing, and the task refuses its further
  submits. Today a sibling waiting on `sharedMu` ignores cancellation (`service.go`, the H1 escalation comment). The
  new wait observes it.
- **A slow destination.** Workers block in their waits. At most N submissions are queued per destination.
- **Deadlock.** A worker waits on completion. Completion waits on the AckReader, the AckReader waits only on the
  plugin, the plugin waits on the writer's `Send`, and the writer waits only on the queue, the in-flight limit and
  `qMu`, which it never holds across I/O. Teardown releases a parked `Recv`.

## Tests

Unit and race tests (`go test -race -count=20 ./pkg/lifecycle-poc/funnel/...`):

- R1 to R3, each verified to fail or hang with its fix reverted.
- `TestDestinationTask_Concurrent_PerSourceOrder`: N submitters on a verified destination. Ack chunks are random,
  span submissions, and position bytes **collide** across submitters. Asserts that each submitter's records reach the
  stream in order and that each submitter gets exactly its own acks.
- `TestDestinationTask_Concurrent_ExactlyOnceCompletion`: a property test with `rapid` (already used in this package)
  over interleavings of submit, `Write` error, `Ack` error, mismatch, close and cancel. Asserts that every submission
  completes exactly once, nothing succeeds after the sticky error, and the writer and AckReader have both exited after
  close (joined, checked in the test).
- `TestDestinationTask_CollidingPositions_DroppedAck_IsUndetectable`: pins the known limit. On a verified
  destination, with two submissions in flight and an ack dropped, the engine misattributes the acks. The test asserts
  this and names the contract it relies on.
- `TestDestinationTask_Unverified_InFlightOne_SingleSource` and `..._DroppedAck_FailsLoud`: the guard. The same
  scenario on an unverified destination fails with a mismatch and acks nothing.
- `TestDestinationTask_AckMismatchFailsAll`: distinct position bytes. A surplus ack, a wrong position, or an ack with
  nothing outstanding fails every submission, and no worker acks.
- `TestDestinationTask_EmptyAckResponse_NoProgress`, plus a separate regression test for the `Do` loop on main.
- `TestFanOut_CancelBeforeSend_RecordsUntouched` (`-race`): a cancelled wait, then a late `Send`. Asserts no race on
  the shared records and that the next submit is refused.
- `TestFanOut_StopAndWait`: no worker reads batch N+1 before all M branches of batch N have completed and N's
  `Source.Ack` has returned.
- `TestDoTask_ConcurrentRoot_SkipsLock`, `TestDoTask_ProcessorRoot_KeepsLock`, and a `buildRunnablePipeline` table
  for the scope rule: 1x1, Nx1, 1xM and batching are not in concurrent mode; NxM with bare destinations is.
  `worker_nsource_test.go`'s concurrent-`Write` detector stays, and proves `Write` has one caller.

Chaos tests (`tests/chaos`, required context `tests/chaos (race, x3)`):

- `TestSIGKILL_NxM_ConcurrentSharedDestination`: 4 sources × 2 bare builtin destinations, positions colliding across
  sources. The child holds acks until submissions from at least two sources are in flight on one destination, then
  the parent sends SIGKILL. Asserts that for every source and destination, every position from 1 to total is
  delivered (duplicates allowed, gaps not), and that no persisted position is past what both destinations received.
- `TestSIGKILL_NxM_Unverified`: the same with an unverified destination. Also asserts that no `Write` mixed sources
  and that no more than one was ever in flight.
- `TestSIGTERM_NxM_ConcurrentDrain`: a graceful stop with submissions in flight returns within the deadline, adds no
  duplicates on restart, and leaves no goroutines.
- `TestSIGKILL_NxM_DestinationErrorMidFanIn`: one destination fails a `Write` while others have work in flight. The
  pipeline fails, and the restart leaves no gaps.

Mutation gate (#3262): the implementation PR adds `// guard: <name> tests=<regex>` at each new enforcement site:
`funnel-dest-fifo-append-before-write`, `funnel-dest-ack-byte-check`, `funnel-dest-exactly-once-completion`,
`funnel-dest-sticky-failure`, `funnel-dest-unverified-inflight-one`, `funnel-concurrent-scope-bare-nxm`, and
`funnel-fanout-stop-and-wait`.

## Benchmarks

No number from this doc goes into release notes. The implementation PR must show the following.

1. **`make bench-engine`** (not a required check): no shape regresses more than 10% beyond twice the A/A floor, and
   `2x2` and `4x4` improve.
2. **benchi on AWS** with the archv2-gate cells. Configs are committed under `benchi/archv2-gate/`, which
   `bench_test.go` refers to but which is not in the tree. Generator to file, host mode, c7i.4xlarge, 60s steady
   state, 5 rounds of base, head and base again (A/A), with the order rotated. Report medians with spread, per
   `benchi/METHODOLOGY.md`. Cells: 1x1 as a control, 2x2, 4x4, 4x4 with source batch 100, and **4x4 with a standalone
   file destination**, which is unverified and measures the guarded path. Record per-sink records/s, cores, and a 4x4
   mutex profile. The 4x1 cell belongs to #3291.
3. **Acceptance:** the A/A floor is under 5%. 1x1 is within the floor. 4x4 per sink improves by at least 30%, and
   `sharedMu` disappears from the profile. The headline 4x4 cell uses a verified destination with positions that
   collide by construction, so it measures the unguarded path. The standalone cell is reported next to it.
4. **If 4x4 misses +30%:** by default this does not merge alone. The next step is designing read-ahead under fan-out
   on the #2978 ledger. The PR is not re-scoped after the fact.

## Upgrade and rollback

Config, positions and state are unchanged. The protocol PR is additive: documentation plus one optional
`orderedAcks` field (minor bump), which old plugins and old engines ignore. Upgrade and downgrade need no migration, and records that were not acked replay under either version.
Rollback is a revert. Arch-v2 is `preview` in v0.21.

## Observability

- `conduit_funnel_destination_inflight_writes{pipeline_name, plugin, type, component_id}`: gauge of submissions
  written but not yet fully acked.
- `conduit_funnel_destination_submit_wait_seconds{...}`: histogram from submit to completion, measured on the
  worker. `conduit_connector_execution_duration_seconds` keeps measuring from the writer's `Write` call to the last
  ack of that submission (`Observe`'s `start` is the `Write` time). The difference between the two is queueing time.
- `conduit_funnel_destination_mixed_source_writes_total{...}`: counter of `Write`s that carried more than one source.
  It must stay at 0 for unverified destinations.
- #3291's take-policy metrics cover coalescing.
- Logs: at debug, the mode a destination enters, with N, M and whether it is verified. At error, a sticky failure
  with its cause and the number of submissions failed per source.
- A runbook entry in `docs/operations/` for `pipeline.shared_destination_poisoned` (there is none today): symptom,
  the earlier logged error that names the destination, and the fix (repair the destination and restart; records
  replay).
- `llms.txt` and the arch-v2 architecture docs are updated in the implementation PR.

## Open questions for DeVaris

1. **Protocol versioning for H1.** Is a minor bump with a documented clarification and the optional `orderedAcks`
   field acceptable, or should it be tied to protocol v3?
2. **Landing order.** Proposed: the main prerequisites, then this doc's component and concurrent mode, then #3291's
   take policy and read-ahead mode, then the protocol PR. The protocol PR is needed only to let standalone
   destinations leave the guarded setting.
3. **The empty-ack-response bug in `DestinationTask.Do`** (see M1). Ship it now as its own `fix:`?
4. **Shared roots with processors** keep the lock until the merge stage exists. Is that acceptable for v0.22?

## Related

- `docs/design-documents/20261027-funnel-write-coalescing.md` (#3291): the take policy and read-ahead mode built on
  this component.
- `docs/design-documents/20260801-archv2-multiconnector-nsource.md`: where `sharedMu` and H2 come from.
- `docs/design-documents/20261009-staged-batch-engine.md` and ADR
  `docs/architecture-decision-records/20261009-single-pipeline-engine.md`.
- `docs/design-documents/20261017-funnel-destination-batching.md` (#3201, #3203) and
  `docs/design-documents/20261019-funnel-read-ahead-pipelining.md` (#3210).
- `pkg/lifecycle/stream/destination_acker.go`: v1's FIFO ack pairing with several writes in flight.
- PR #2978 (`proto/staged-batch-engine`).
- Roadmap: v0.22, "Default engine flip and v1 engine deletion".
