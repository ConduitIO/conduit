# Funnel shared-destination concurrency (arch-v2)

## Summary

In an arch-v2 pipeline with more than one source, every source's `funnel.Worker` takes the shared
destination's `TaskNode.sharedMu` and holds it through the whole `Write` and the wait for that write's acks.
Each destination is therefore a critical section that one worker at a time can use, and adding sources makes
throughput per destination go down. On the AWS host-mode runs behind this doc, main went from 54.5k
records/s at 1x1 to 29.9k per sink at 4x4.

This doc replaces the lock, for a shared root that is a bare destination, with the destination half of the
staged batch engine (`20261009-staged-batch-engine.md`): a per-destination **writer** goroutine (the only
caller of `Destination.Write`), a per-destination **AckReader** goroutine (the only caller of
`Destination.Ack`), and an ordered in-flight FIFO between them. Each submitted write completes exactly once.
Workers stay stop-and-wait: a worker submits its batch, waits for its own write's completion, and acks its own
source on its own goroutine, as it does today. Writes from different sources overlap on the destination
stream. Writes from one source never do.

This is a slice of #2978, not all of it. Read-ahead credits, the per-source ledger and the coordinator are left
out. They change when a source gets acked. This slice only changes who touches the destination stream.

**Tier 1** (data path). No change to config, wire format or serialized state.

## Context and problem

### What the lock protects

`Worker.doTask` takes `sharedMu` when `taskNode.sharedBoundary` is set (`worker.go`, `doTask`). `funnel.NewSink`
marks every shared root. `buildSharedTail` returns one root per destination branch when there are no
pipeline-level processors, or a single processor root above all branches when there are (`service.go`). The lock
exists for two reasons, and only one of them is about concurrency safety:

1. **Ack attribution on a single stream.** `DestinationTask.Do` sends one `Write` and then reads `Ack` until it
   has an ack for every record it sent. It assumes the next acks on the stream belong to its own write. If two
   workers' `Do` calls interleaved on one stream, worker A could read B's acks. `validateAcks` compares position
   bytes, but positions are only unique within one source. When A's and B's position bytes collide, and
   `tests/chaos/nsource_sigkill_test.go` makes them collide on purpose, the check passes and A acks records that
   were never written. That breaks invariant 1. The H2 poison flag (`TaskNode.poisoned`,
   `pipeline.shared_destination_poisoned`) handles the error case of the same hazard: a `Do` that failed partway
   through leaves unread acks on the stream for the next lock holder.
2. **One goroutine per stream direction.** A gRPC client stream allows one goroutine calling `Send` and one
   calling `Recv` at the same time, but not two concurrent `Send`s or two concurrent `Recv`s. The builtin
   in-memory stream (`plugin/connector/builtin/stream.go`) uses channels and would tolerate concurrent senders,
   but a standalone plugin's gRPC stream would not.

The lock also covers shared pipeline-level processors and destination-scoped processors, because it is taken at the
shared root and held for the whole recursion beneath it. Those processors are not required to be safe for concurrent
calls.

The original design (`20260801-archv2-multiconnector-nsource.md`, "Consequence for throughput") accepted the
serialization because v1's `FaninNode` also funnels everything into one consumer goroutine. The difference is that
v1's consumer does not wait for acks before it sends the next write. The arch-v2 lock holder does.

### Evidence

AWS c7i.4xlarge (us-west-1), host mode with no Docker, generator to file, 3 interleaved rounds, records counted at
sink-1. This is **not** the A/A-controlled harness: spread across rounds was 3.5% or less. These are diagnostic numbers.
None of them is a performance claim.

| shape (default batching) | main a23efb64 | #2978 rebased onto a23efb64 ("protor") |
| --- | --- | --- |
| 1x1 | 54.5k rec/s | 61.3k (+12%) |
| 4x4, per sink | 29.9k rec/s | 47.2k (+58%) |
| 1x1, source batch 100 | 221-235k | 220k |

- **Mutex profile, main 4x4** (`bm-main-4x4-b0`): 156.6s of mutex wait, 99% of it in `Worker.doTask` on
  `sharedMu`. 226s of the four workers' roughly 200s of combined wall time is spent in the fan-out's
  `WaitGroup.Wait`. Every worker fans out to all four sinks, and each branch then queues on that sink's lock.
- **Block profile, main 1x1**: the worker goroutine is blocked 41.9s of about 50s, 21.5s in `Source.Read` and 20.3s
  in `Destination.Ack` `Recv`. It uses 1.36 of 16 cores. The engine is waiting, not CPU-bound.
- #2978 removes the lock for bare-destination shared roots (`lockFree` in its `doTask`) and adds read-ahead and the
  ledger. Its 4x4 gain is mostly the lock: at 1x1, where the lock is never contended, the same machinery adds only 12%.

What has **not** been measured: this doc's slice on its own (lock removed, workers still stop-and-wait). The +58% was
measured with read-ahead included. The benchi plan below decides whether the slice delivers enough of the gain by itself.

## Constraints

1. **Invariant 1.** A source record is acked only after the destination has acked that specific record. With
   several writes in flight on one stream, attribution must come from stream order (the FIFO), never from position
   bytes alone.
2. **Invariant 2.** Each source's positions reach `Source.Ack` in ascending order, from one goroutine.
3. **Invariant 3.** On any error, cancel or close path, a write in flight either completes with a real ack or
   completes with an error. Both happen at most once, and a write that errors casts no ack vote.
4. **Invariant 4.** Within one source, writes reach the destination stream in source order. Interleaving between
   sources is allowed and is unspecified, the same as today: lock-acquisition order is arbitrary too.
5. **Invariant 7.** A graceful stop drains every in-flight write before `Sink.Close` tears the destination down.
   SIGKILL at any point must recover without loss.
6. **Exactly one completion per write.** A write that never completes hangs its worker. `workersWg` then never
   drains and stop hangs. A write that completes twice can apply acks twice or report success after failure.
7. **One goroutine per stream direction** (see "What the lock protects", reason 2).
8. **No 1x1 regression.** A single-source pipeline must not pay extra goroutine hand-offs for a lock it never
   contends.

## Alternatives considered

### A. Per-destination writer and AckReader, workers stop-and-wait (chosen)

Covered in "Decision". In short: the stream gets two owners, one per direction. Workers submit and wait. Each write
carries its own completion, so routing an ack back to its source is a FIFO pop, not a byte comparison.

- **Pros:** removes both reasons for the lock at once. Ack order per source is trivial (one write in flight per
  source per destination). The worker's ack, nack, DLQ and split-run code does not change. It is the same component
  #2978 and the staged-engine doc already specify, so the work is not throwaway.
- **Cons:** two goroutines per shared destination. A new stream-protocol dependency: acks across consecutive
  `Write`s must arrive in write order (see "Failure modes"). Shared roots that carry processors keep the lock.

### B. Finer-grained locking: a ticketed send lock

Take a short lock to `Send` and take a ticket. Then wait for your turn to `Recv`, in ticket order. No new goroutines.

- **Lost because** an ack response can span two writes, and an SDK destination can return a partial response. The
  worker whose turn it is to read may receive acks that belong to the next ticket and has to hand them on. That is
  the AckReader's demux spread across N worker goroutines with no single owner. The error case is worse than today:
  a worker cancelled while it holds the read turn leaves the stream mid-response, and that is exactly the H2
  desync the poison flag exists for. A does the same job with one owner and one place to get the error cases right.

### C. One destination instance per worker

Open N plugin instances of each destination connector, one per source, so nothing is shared.

- **Lost because** it changes user-visible behavior. `connector.Destination.Open` refuses a second instance of the
  same connector ID. Connector state is keyed by connector ID. A destination with exclusive output (the file
  destination writing one file, an S3 writer naming objects by sequence) would corrupt or split its output. The
  target system sees N times the connections. It also violates the `20261009-single-pipeline-engine` ADR's model of
  one writer per destination.

### D. Do nothing and document it

The nsource doc's argument: v1 serializes too.

- **Lost because** the v0.22 engine flip (ROADMAP) is gated on arch-v2 being at parity or faster at shipped defaults,
  and N×M is the shape where arch-v2 gets worse as it grows. The prototype shows the limiter can be removed.

### E. Land all of #2978 at once

- **Not chosen for this step.** #2978 also changes when sources are acked (ledger, prefix release, read-ahead), and
  its review found four concurrency bugs (below), two of them in the destination path. Landing the destination half
  first, behind the existing stop-and-wait ack path, puts those fixes and their tests on main with a smaller blast
  radius. The staged engine then builds on a `Submit` that has already been reviewed. If benchi shows the slice alone
  does not clear the gate, this is the fallback (open question 1).

## Decision

### Scope: where the lock is removed

A shared root runs **concurrently** (no `sharedMu`, no poison check) only when all of these hold:

- the root's task is a `*DestinationTask` with no `Next` (a bare destination: no destination-scoped processors,
  no pipeline-level processors above it),
- the destination does not batch (`batchingEnabled() == false`; see "Interaction with batching and read-ahead"),
- at least two workers attach to the root (`len(srcTaskSets) >= 2`, passed to `NewSink`). At 1x1 and 1xM the root
  keeps today's direct `Do`. No hand-off is added where no lock was contended (constraint 8).

Every other shared root keeps `sharedMu` and the H2 poison flag exactly as they are. A shared root with stateful
processors needs the staged engine's merge stage. That is out of scope here.

### Mechanism

`DestinationTask` gets a concurrent mode, set once before `Open` (like `SetPipelined`). In this mode:

- **`Open`** starts the writer and the AckReader. The state is created eagerly. #2978 created it lazily on the
  first `Submit`, which is half of bug 3 below.
- **`Do(ctx, b)`** becomes submit-and-wait. It builds an `inflightWrite` (records, positions, a completion channel,
  a slot for the acks), enqueues it on the inbox, then waits for completion or `ctx.Done()`. When it completes
  without error, `Do` applies the acks to `b` on the worker goroutine (`markBatchRecords`, `metrics.Observe`) and
  returns. From there `doTaskAttempt`'s existing ack and nack handling runs unchanged. The AckReader never touches
  `b`. It only fills the write's ack slot, and the completion channel close publishes the slot to the worker.
- **Writer:** the only caller of `Destination.Write`. It takes the next write from the inbox and, under `qMu`,
  checks for a sticky error or closing. It appends the write to the pending FIFO **before** `Write`, so no ack can
  arrive for a write the AckReader does not know about, and FIFO order equals stream order. It never holds `qMu`
  across `Write`.
- **AckReader:** the only caller of `Destination.Ack`. It checks the whole response against the FIFO before
  applying any of it: position bytes per record, no surplus, no ack with nothing outstanding. A response may span
  writes or cover part of one. A write completes once every one of its records has an ack.
- **Sticky failure:** a `Write` error, `Ack` error or ack mismatch records the first error, wakes blocked
  submitters, and completes every pending and queued write with that error, exactly once. Later `Submit`s return
  `pipeline.shared_destination_poisoned` with the original error as cause. The code stays registered (ADR
  `20261009-single-pipeline-engine` retires it together with the funnel loop), so operators see the same code for
  the same condition.
- **Bound:** in-flight writes per destination are at most the number of attached workers, because each worker has
  at most one write in flight per branch. The inbox has that capacity, so `Submit` blocks only if the bound is
  broken (asserted in tests).
- **Fan-out:** with M concurrent branches, `doNextTask`'s pool runs each branch's submit-and-wait concurrently, as
  it does today. A follow-up may submit inline and skip the per-branch goroutines (the 226s `WaitGroup.Wait`), but
  that is an optimization, not part of this change.

`doTask` skips `sharedMu` and the poison store for a concurrent root. That is the whole worker-side change.

### Required fixes from #2978's review (design requirements)

`proto-1x1/FINDINGS.md` section 6 found four bugs in #2978's asynchronous destination path. All four were fixed in
the rebased scratch port, each with a test that fails or hangs without its fix. Any port of this mechanism to main
must include all of them. In this slice a missed completion hangs a worker and therefore the stop, and a double
completion can apply acks twice. Neither loses data on its own here. Both become loss bugs once the ledger votes
from completions (#2978).

| # | Bug | Requirement | Test (must fail or hang without the fix) |
| --- | --- | --- | --- |
| R1 | Drained-write completion: #2978's writer returns on `err`/`closing` without completing the write it already took from the inbox | Every write the writer dequeued, and every write still in the inbox when failure or close happens, completes exactly once with an error | `TestDestinationTask_Submit_CloseCompletesEveryWrite` |
| R2 | `Submit` vs close TOCTOU: `closeAsync` never drains the inbox, and lazy state creation can miss a close | `Submit` checks `closing` again after it enqueues and drains if closing; state creation and close are mutually exclusive (`asyncMu`); state is created in `Open` | `TestDestinationTask_Submit_RacesClose` (hangs without fix) |
| R3 | Dispatched-write orphaning: the AckReader exits on `closing`, so writes already sent never complete | Close does not fail pending writes itself. The AckReader keeps reading until teardown ends the stream, the join fails whatever is left, `complete()` is once-only, and a real ack that arrives first wins | `TestDestinationTask_Close_RealAckWins` |
| R4 | Coalescing cap look-ahead: the coalescing writer takes a write from the inbox before it checks the cap, so a Write overshoots the cap by one batch | Only when coalescing lands (phase 2): a carry slot holds a write that would overshoot, and a byte cap below the standalone gRPC 4 MiB message limit applies too | `TestDestinationTask_Writer_CoalesceCap` |

R4 is not reachable in this slice, which has no coalescing. It is listed so phase 2 cannot drop it.

### Interaction with batching and read-ahead

- **Destination batching (#3201, #3203) and read-ahead (#3210, `20261019-funnel-read-ahead-pipelining.md`)** are
  unchanged in this slice. A destination with `sdk.batch.*` set keeps `doBuffered` under `sharedMu`. For N:1 with
  batching, the pipelined flusher already does the overlap: the lock is held only to append or enqueue, and the
  flusher owns the stream. **N×M with batching is still serialized.** Fan-out disables the flusher, so
  `flushPending` runs synchronously under `sharedMu` (`benchi/archv2-nxm-batched` is that shape). This slice does
  not fix it.
- **Phase 2** (separate PR, needs a design doc update): fold the flusher into the writer. Batching becomes
  writer-side coalescing bounded by `sdk.batch.size`/`sdk.batch.delay` and the R4 caps. One per-destination
  goroutine pair then serves batching and non-batching destinations alike, and N×M batched loses the lock too.
  The cross-batch ack-ordering problem that made #3210 refuse fan-out does not come up here, because each worker
  still acks only after it has seen its own completion.

## Failure modes

- **SIGKILL with writes from several sources in flight.** None of them has been acked to its source. Positions
  were not persisted, so the writes replay on restart: duplicates, never loss (invariant 3). Covered by the new
  chaos test below.
- **Destination `Write`/`Ack` error or ack mismatch.** The failure is sticky. Every in-flight write fails exactly
  once, each worker returns the error, and the pipeline fails as it does today. No vote is cast for a failed write,
  and later submits get `pipeline.shared_destination_poisoned`.
- **Acks reordered across writes by a misbehaving destination.** The byte check catches it when positions differ.
  It cannot catch it when a reordered pair of acks has identical bytes from two sources. That case is worse than
  today: today only one write is ever in flight, so a reorder across writes cannot happen. The Go SDK is safe by
  construction: `destinationPluginAdapter.Run` (SDK v0.17.2) handles one `Recv` and one `Write` at a time and acks
  in record order. SDKs in other languages, current and planned, have to give the same guarantee. That makes "acks are in
  write order across consecutive Write requests" part of the connector protocol, and the acceptance suite has to
  test it (open question 3).
- **Worker cancelled while it waits.** `Do` returns `ctx.Err()` and acks nothing. The write may still complete
  later. Its completion only closes a channel that nobody reads and applies nothing to the batch, so a late ack
  cannot reach a source. Today a sibling waiting on `sharedMu` ignores cancellation (`service.go`, the H1
  escalation comment). With this change it observes `ctx.Done()`, so force-stop gets faster.
- **Graceful stop.** Each worker finishes its pass, which includes waiting for its own write, before it exits.
  `Sink.Close` runs after `workersWg.Wait()` and normally finds nothing in flight. If a worker exited on
  cancellation and left a write in flight, close follows R3: tear down, the AckReader drains to end of stream, and
  whatever is left fails once. Records are at-least-once.
- **Slow destination.** Each worker blocks in its own wait. Backpressure works the same as today, but N writes can
  be queued at the plugin instead of one. Memory is bounded at N batches per destination.
- **Shared records (#2910).** Bare-destination branches share records read-only. The writer only reads them, and
  the builtin stream clones on `Send`. The sharing rule does not change.
- **Deadlock.** The worker waits on completion. Completion waits on the AckReader, the AckReader waits only on the
  plugin, the plugin waits on the writer's `Send`, and the writer waits only on the inbox and `qMu`, which is never
  held across I/O. No cycle runs back through a worker.

## Tests

Unit and race (`go test -race -count=20 ./pkg/lifecycle-poc/funnel/...`):

- R1 to R3 tests from the table, ported from the scratch port. Each is verified to fail or hang with its fix reverted.
- `TestDestinationTask_Concurrent_PerSourceOrder`: N submitters, a destination that returns acks in random chunk
  sizes that span writes. Asserts each submitter's records reach the stream in order and each submitter gets
  exactly its own acks, with **colliding position bytes** across submitters.
- `TestDestinationTask_Concurrent_ExactlyOnceCompletion`: a property test (`rapid`, already used in this package)
  over interleavings of submit, Write error, Ack error, mismatch, close and cancel. Every write completes exactly
  once, no write completes successfully after the sticky error, and the writer and AckReader have both exited
  after close (joined and checked in the test; no new dependency).
- `TestDestinationTask_Concurrent_AckMismatchFailsAll`: a surplus ack, a wrong position, or an ack with nothing
  outstanding fails every in-flight write, and no worker acks.
- `TestDoTask_ConcurrentRoot_SkipsLock` and `TestDoTask_ProcessorRoot_KeepsLock`: the scope rule. Existing
  `worker_nsource_test.go`'s concurrent-`Write` detector stays and now targets the mock destination, so it proves
  `Write` is still single-caller.
- `TestBuildRunnablePipeline_ConcurrentMode`: 1x1 and 1xM are not concurrent, Nx1 and NxM with bare destinations
  are, and batching or processor branches are not.
- `worker_h2_poison_test.go` keeps passing for processor roots. A twin covers the sticky-error path for concurrent
  roots.

Chaos (`tests/chaos`, required context `tests/chaos (race, x3)`):

- `TestSIGKILL_NxM_ConcurrentSharedDestination`: 4 sources × 2 bare destinations. Positions overlap across sources,
  as in `nsource_sigkill_test.go`. The child's destination holds acks until writes from at least two sources are in
  flight on the same destination, then the parent sends SIGKILL. On restart, for each source and each destination,
  every position from 1 to total is delivered (duplicates allowed, gaps forbidden), and no source's persisted
  position is past what **both** destinations received.
- `TestSIGKILL_Nx1_ConcurrentSharedDestination_SpanningAcks`: 4 sources × 1 destination whose acks span writes,
  killed at random offsets across several iterations. Same assertions.
- `TestSIGTERM_NxM_ConcurrentDrain`: a graceful stop with writes in flight returns within the stop deadline, writes
  no new duplicates on restart, and leaks no goroutines.
- `TestSIGKILL_NxM_DestinationErrorMidFanIn`: one destination fails a `Write` while other sources have writes in
  flight. The pipeline fails and, on restart, every source resumes with no gaps.

`tests/chaos/doc.go` lists the new tests, and the chaos workflow's data-path detector already covers
`pkg/lifecycle-poc/**`.

## Benchmarks (what must confirm the gain)

No number from this doc goes into release notes or docs. The implementation PR has to show:

1. **In-repo engine gate** (`make bench-engine`, `.github/workflows/bench-engine.yml`, not a required check):
   shapes `1x1`, `2x2`, `4x4` and the rest of `benchShapes`. No shape may regress more than 10% beyond twice the
   A/A floor. `2x2` and `4x4` are expected to improve. These are in-process fakes, so they are a regression signal,
   not a claim.
2. **benchi on AWS** with the archv2-gate cells, with the configs committed under `benchi/archv2-gate/`. The bench
   code refers to that directory, but it is not in the tree yet. Generator to file, host mode, c7i.4xlarge, 60s
   steady state, 5 rounds interleaving base (main), head (this change) and base again as an A/A control, with the
   order rotated each round. Medians with spread, per `benchi/METHODOLOGY.md`. Cells: 1x1, 4x1 (pure fan-in), 2x2
   and 4x4 at default batching, plus 1x1 and 4x4 with source batch 100. Per-sink records/s, CPU cores, and a mutex
   profile for 4x4.
3. **Acceptance.** The A/A floor is under 5% for the run. 1x1 and 1x1 source batch 100 are within the floor (the
   1x1 path does not change, so any movement there is noise or a bug). 4x4 per-sink improves by more than twice the
   floor, and `sharedMu` disappears from the 4x4 mutex profile. The target is at least +30% at 4x4, about half of
   protor's +58%. If the slice falls short of that, the read-ahead half of #2978 lands with it, and that is decided
   before merge (open question 1).

## Upgrade and rollback

There is no change to config, pipeline config schema, connector protocol messages, positions or state. Upgrading
and downgrading between versions with and without this change needs no migration, and a restart replays
un-acked records under either version. Rollback is reverting the commit. Arch-v2 is `preview` in v0.21, so a fix or
revert can ship in a patch release. No feature flag is proposed (open question 2).

## Observability

- New gauge `conduit_funnel_destination_inflight_writes{pipeline_name, component_id}`: writes submitted but not yet
  completed, on concurrent destinations only. It should sit between 0 and the number of sources. A value stuck at
  N while throughput is 0 means the destination is wedged.
- New histogram `conduit_funnel_destination_submit_wait_seconds{pipeline_name, component_id}`: how long `Do`
  waited from submit to completion. Read next to the existing
  `conduit_connector_execution_duration_seconds` (Write to ack) to separate queueing from destination time.
- Logs: debug when concurrent mode starts (destination, number of attached workers). Error on sticky failure with
  the cause and the number of in-flight writes failed, per source.
- A `docs/operations/` runbook entry for `pipeline.shared_destination_poisoned` (none exists today). Symptom: the
  pipeline is degraded with that code. Diagnosis: the earlier error in the logs that names the destination.
  Remediation: fix the destination and restart the pipeline. Records are replayed, not lost.
- `docs/architecture` text on arch-v2 fan-in and `llms.txt` are updated in the implementation PR. The nsource
  design doc stays as it is (design docs are records). This doc supersedes its "Consequence for throughput" for
  bare-destination roots.

## Open questions for DeVaris

1. **Land the slice alone, or with read-ahead?** The slice has not been measured on its own. If benchi shows less
   than +30% at 4x4, should the ledger and read-ahead half of #2978 come in the same PR (bigger Tier 1 review),
   or should the slice land anyway for whatever it gains?
2. **Feature flag?** The recommendation is no flag: arch-v2 is preview and rollback is a revert. Do you want a
   `preview.archv2.concurrent-destinations` switch for the v0.22 flip window anyway?
3. **Ack order across Writes as a protocol guarantee.** This change makes it load-bearing for invariant 1 when
   position bytes collide across sources. Does it go in the `conduit-connector-protocol` spec and the SDK
   acceptance suite before this merges, or after with a release note? It changes no protocol message, but it is a
   contract.
4. **Shared roots with processors.** These keep the lock until the staged engine's merge stage exists. Is that
   acceptable for v0.22, or do pipelines with pipeline-level processors need this before the flip?
5. **Phase 2 (fold the flusher into the writer).** Should it be the next PR, ahead of the rest of #2978, given that
   N×M with batching is still serialized?

## Related

- `docs/design-documents/20260801-archv2-multiconnector-nsource.md` (where `sharedMu` and H2 come from).
- `docs/design-documents/20261009-staged-batch-engine.md` and ADR
  `docs/architecture-decision-records/20261009-single-pipeline-engine.md` (writer, AckReader and inbox, of which
  this is the first slice).
- `docs/design-documents/20261017-funnel-destination-batching.md` (#3201, #3203) and
  `docs/design-documents/20261019-funnel-read-ahead-pipelining.md` (#3210).
- PR #2978 (`proto/staged-batch-engine`), the prototype this slice comes from.
- Roadmap: v0.22, "Default engine flip and v1 engine deletion" (the #2978 staged batch engine lands first as the
  performance substrate).
