# Arch-v2 destination batch timing and ownership

## Summary

Two arch-v2 (`--preview.pipeline-arch-v2`) destination-batching bugs, found in the v0.21 CDC-parity
smoke run and reproduced locally, and a third one next to them. None loses data: no record is acked
before it is written, so invariants 1 and 3 hold and a crash replays. All three break the contract the
default-engine flip relies on.

- **#3231, the partial batch has no timer.** The funnel's destination buffer flushes on size, or on
  `sdk.batch.delay` checked only when the next record arrives. When the source goes idle, the tail
  stays unwritten and unacked until more records arrive or the pipeline stops. The smoke run held the
  last 5,088 of 300,000 records for the whole settle window; they were written only on SIGTERM.
- **#3321, connector-default batching slips past the engine.** A destination whose connector enables
  SDK batching by default (`conduit-connector-s3` v0.12.0: 1000 records / 1 s) behind a 1-record
  source runs at about 1 record/s. The engine takes over batching only when the _pipeline config_
  sets `sdk.batch.*`, so the plugin batches, withholds each ack for 1 s, and the stop-and-wait loop
  waits for it.
- **Double batching when the pipeline does set `sdk.batch.*`.** To stop the plugin batching, the
  engine _deletes_ both keys before `Configure`. For S3 that brings back the connector's own
  1000 / 1 s default, so the plugin batches behind the engine. Every flush whose size is not a
  multiple of 1000 waits up to 1 s in the plugin for its last ack.

Decisions:

1. **A one-shot flush timer per `DestinationTask`**, armed with the existing deadline when the first
   record enters an empty buffer. The timer never adds a new path to the source ack:
   - On the pipelined path (one destination) it only takes the buffer and enqueues it; the flusher
     still does every `Write`, `Ack` and source ack.
   - On the synchronous path (fan-out) it runs the task's existing `Drain` while holding the
     processing lock of every worker that feeds the task, exactly as `Worker.Stop` already drains. A
     timer flush is then a pass with no new records: it never overlaps a pass, so every source ack
     is still issued from inside one pass at a time, the property fan-out is synchronous for.

   `sdk.batch.delay` then bounds how long a buffered record waits, as it does in the SDK.
2. **Arch-v2 owns destination batching.** The engine resolves the _effective_ `sdk.batch.size` and
   `sdk.batch.delay` per key (pipeline config first, then the connector spec default), batches with
   them, and configures the plugin with explicit `"0"` for each key that would otherwise turn the
   plugin's batcher on, instead of deleting it. That fixes #3321 and the double batching. The classic
   engine is unchanged.

All three fixes gate the v0.22 default-engine flip. Risk tier: Tier 1 (ack timing, destination batching,
shutdown). No serialized format, protocol or pipeline config schema changes.

## Context

### How arch-v2 batches a destination today

The funnel buffers destination writes itself (`20261017-funnel-destination-batching.md`) and, for a
single destination, pipelines them through a flusher goroutine
(`20261019-funnel-read-ahead-pipelining.md`). All code references are to `origin/main` at
`0aeba0e5`.

- `buildDestinationTasks` reads `sdk.batch.size` and `sdk.batch.delay` from the destination's
  pipeline config only (`config.SDKBatchSettings(instance.Config.Settings)`,
  `pkg/lifecycle-poc/service.go:1619-1625`). If either enables batching it calls
  `SetEngineBatches()` and passes the values to `funnel.NewBatchingDestinationTask`
  (`service.go:1627-1634`).
- `SetEngineBatches` makes `configure` _delete_ both keys before `Configure`
  (`pkg/connector/destination.go:393-399`).
- `doBuffered` appends to `pending` and returns `errBuffered`, so the worker reads on without acking
  (`pkg/lifecycle-poc/funnel/destination_batch.go:133-217`). `bufferBatch` arms `flushDeadline` on
  the first record of an empty buffer and checks it only on arrival
  (`destination_batch.go:235-252`; comment at 237-239: "checked synchronously here on every arrival
  (not on a ticker); a trailing partial batch is flushed on Close").
- Pipelined path (single destination): `Open` starts the flusher when batching is on
  (`funnel/destination.go:251-258`). `runFlusher` waits on `ctx.Done()` and `flushCh` only
  (`destination_batch.go:700-736`); it is the only caller of `Destination.Write`/`Ack` and issues
  the source acks (`flushPending` → `ackPending`, `destination_batch.go:496-536, 615-653`).
- Synchronous path (fan-out, or no flusher): flushes run on the calling goroutine under `syncMu`,
  and `releaseSync` already flushes from a goroutine that does not hold the shared lock
  (`destination_batch.go:380-433`). Lock order: `sharedMu`, then `syncMu`, then `mu`
  (`destination_batch.go:171`); `enqueueMu`, then `mu` on the pipelined path
  (`funnel/destination.go:113-118`).
- Graceful stop already drains before the source is torn down: `Worker.Stop` takes the worker's
  processing lock and calls `drainDestinationsBeforeTeardown` (`funnel/worker.go:234, 252-257`), and
  the EOF path does the same under the lock (`worker.go:743-746`). `drainNode` takes the shared
  boundary mutex on the way down (`worker.go:281-295`). `DestinationTask.Drain` is `flush` on the
  synchronous path (`destination_batch.go:764-765`) and sends the buffer plus a barrier to the
  flusher on the pipelined path (`destination_batch.go:760-826`). That half of #3231 (invariant 7) is
  fixed on main. The timer half is not.

### Why fan-out is synchronous

A pass holds the worker's processing lock from the moment its source read returns until the batch is
processed end to end (`worker.go:762-768`). Fan-out runs the M branches concurrently inside the pass
and waits for all of them (`worker.go:1047, 1088`), so every source ack of a fan-out worker is issued
inside one pass at a time. `service.go:1665-1676`, `SetPipelined`'s godoc
(`funnel/destination.go:234-239`) and the 20261019 doc keep fan-out off the pipelined path for that
reason: M flushers would ack the one source with no cross-batch happens-before. Any timer design has
to keep that property.

### #3231: the trailing partial batch

Smoke cell H-kafka-s3 `conduit-t`: source batch 8192, destination `sdk.batch.size=10000`. 294,912 =
36 × 8192 records were written; the 37th source batch (5,088 records) stayed below 10,000 and nothing
fired its deadline. Local repro A (generator 300,000 at 8192/100 ms → file at 10000/1000 ms,
arch-v2): 294,912 lines at t = 5 s, still 294,912 at 30, 60 and 120 s; the last 5,088 appeared on
SIGTERM.

This is a contract regression, not only a latency one. The SDK batcher arms a real timer on the first
enqueue (`time.AfterFunc`, `conduit-connector-sdk` v0.17.2 `internal/batcher.go:79-81`), so under the
classic engine `sdk.batch.delay: 1s` flushes an idle tail within about 1 s. Under arch-v2 the engine
strips the key from the plugin and then does not honour it. With size-only batching both engines wait
without bound; that case is a documentation question (see Decision).

The 20261017 doc rejected a ticker (Alternative C) because the flush "would ack source records from
a non-worker goroutine". #3210's flusher has acked from a non-worker goroutine since 20261019, but only
on the single-destination path. The timer below keeps both halves of that: it never acks on the
pipelined path, and on the fan-out path it acks only while it holds the worker's processing lock.

A second gap sits next to it. When the flusher fails while the source is idle, nothing tells the
worker. `runFlusher` records the error (`finishFlush`, `destination_batch.go:724-729`) and exits; the
only readers of `flushErr` are `doBuffered`, `deferOrdered`, `Drain` and `Close`, all called on a
record arrival or a stop. The worker stays blocked in `Source.Read`, and the pipeline reports
running. This is pre-existing and gets its own fix (Rollout, step 1); the timer makes flushes on an
idle source routine, so it depends on that fix.

### #3321: connector-default batching

Smoke cell H-kafka-s3 `conduit-s`: 2,690 records in 2,700 s, SUT CPU 11 s over the run. Local repro
B (generator → S3 on MinIO, no `sdk.batch.*` anywhere, arch-v2): 30 objects in 30 s, one record per
object.

1. The kafka v0.16.0 source has no `ReadN`, so with no `sdk.batch.*` each read is one record.
2. The pipeline config sets no destination `sdk.batch.*`, so `SDKBatchSettings` reports batching off
   (`pkg/provisioning/config/batching.go:75-79`), `SetEngineBatches` is not called, and the funnel
   writes each record with the unbuffered `Do` and waits for its ack.
3. S3 v0.12.0 sets `BatchSize: 1000, BatchDelay: 1s` on its config before the SDK parses it
   (`destination/destination.go:45-48, 66-82`), and its spec says so (`connector.yaml:709-719`,
   defaults `1s` and `"1000"`). The SDK sees batching on (`destination.go:198-199` in v0.17.2) and
   withholds each ack until the 1 s timer flushes.
4. Each record waits 1 s for its ack; the next read waits for that ack.

`batching_guard.go` reads only the DLQ's pipeline config (`pkg/lifecycle-poc/batching_guard.go:49-59`),
so it does not see this either. S3 is the only built-in destination whose spec default batches
(file, kafka, log, postgres declare `"0"`/`"0"`). Third-party connectors can do the same.

Default source batching (#3309, open) does not fix this. It moves sources to SDK v0.18.1 defaults
(100 records, adaptive drain), so the kafka source would return up to 100 records per read, and the
cell would run at roughly 100 records/s instead of 1. S3 v0.13.0, which #3309 bumps to, still declares
the 1000 / 1 s destination default (`connector.yaml:711-719` at v0.13.0).

### Double batching when the pipeline sets `sdk.batch.*`

The opposite case is broken too. When the pipeline sets `sdk.batch.size` or `sdk.batch.delay`, the
engine batches and `configure` deletes both keys (`pkg/connector/destination.go:393-399`). Deleting
a key does not disable the plugin's batcher: the SDK applies the connector's default to a missing key,
and S3's default is 1000 / 1 s. Smoke cell `conduit-t` (S3, `sdk.batch.size: 10000`) runs this way
today. A flush of 10,000 records is acked once the plugin's ten 1000-record batches go out, but a
flush of 1,500 (any partial batch, including the timer's) waits up to 1 s inside the plugin for the
last 500. With the timer, the tail latency would become the engine delay plus the plugin delay.

## Constraints

1. **Invariant 1.** A source ack only after the destination has written the record and returned its
   ack. A timer may change _when_ a buffer is taken, never the write-then-ack order.
2. **Invariant 2.** Positions stay monotonic: source acks are issued in source order, so
   `Source.Ack`, which persists the last position it is given, never moves backwards or past a
   record that is not durable (`deferOrdered` comment, `destination_batch.go:297-303`).
3. **Invariant 3.** On any failure path (timer flush fails, ctx ends, close races) the taken records
   are either written and acked or left unacked to replay. After a flush error, nothing queued behind
   it may run.
4. **Invariant 4.** One writer per destination stream; flushes complete in take order.
5. **Invariant 7.** SIGTERM drains and checkpoints the tail; `kill -9` at any instant recovers
   without loss.
6. **Fan-out stays synchronous.** No change may issue a fan-out worker's source ack outside a pass
   (see "Why fan-out is synchronous").
7. **The classic engine does not change.** It reads destination acks on their own goroutine, so SDK
   batching works there, and connectors that default to it (S3) depend on that.
8. **Plugins are not ours.** Standalone plugins can be built on any SDK since v0.6, or on none. No
   protocol change in this design (CLAUDE.md: protocol changes need a versioning discussion).
9. **No new dependencies.** Go 1.26 (`go.mod:3`) ships `testing/synctest`, which fakes time for
   `time.AfterFunc` and `time.Now` in tests without a clock interface.

## Alternatives considered

### #3231: where the timer lives

#### A. One-shot timer per task; enqueue when pipelined, drain under the processing lock (chosen)

`time.AfterFunc(batchDelay, ...)` armed under `mu` in `bufferBatch` when the first record enters an
empty buffer (the line that sets `flushDeadline` today). On the pipelined path the callback takes the
buffer as `doBuffered` does and enqueues it. On the synchronous path it acquires the processing lock
of each worker that feeds the task, then runs the task's `drainNode` step, the code `Worker.Stop`
already runs under the same lock.

- **Pros:** exact deadline (same arming point as the SDK batcher); no goroutine while idle; reuses the
  take/enqueue unit on the pipelined path and the stop drain on the synchronous path; no new owner of
  the stream and no new source-ack path on either.
- **Cons:** the synchronous path needs each task to know the processing locks of the workers that
  feed it (fixed when the task tree is built). A timer flush delays the next pass by the length of
  the flush, as a size flush inside the pass would.

#### A2. One timer goroutine per task that flushes under `syncMu` only (the first draft of this doc)

- **Lost:** under fan-out it gives each of M destinations its own goroutine that reaches
  `Worker.Ack`/`Nack` → `Source.Ack` outside the worker's pass, which is what `service.go:1665-1676`
  forbids. An ordering argument exists (the last vote on batch N+1 is program-ordered after the same
  destination's vote on N; after #3324 it covers nacks too, see Decision), but a release that runs
  concurrently with a pass and with another destination's flush would still race the pass's own
  releases and the DLQ writes. Holding the processing lock costs one lock and removes the question.

#### A3. The timer only signals the worker loop, which flushes

- **Lost:** an idle worker is blocked in `Source.Read`, which does not watch its ctx
  (`funnel/connector_errors.go:37-40`). The signal would be acted on when the next record arrives,
  which is the bug. Making `Read` return on a signal needs either a protocol change or a reader
  goroutine that reads ahead of the pass, which changes backpressure and the stop path.

#### A4. Flush from the source read loop's idle tick

- **Lost:** there is no idle tick: the read loop is one blocking `Read` per pass. Adding one has the
  same cost as A3.

#### A5. Timer on the pipelined path only; fan-out keeps arrival checks plus a WARN

- **Lost:** simplest, but a fan-out pipeline whose source goes quiet never delivers its tail, and
  `sdk.batch.delay` would mean different things depending on how many destinations a pipeline has.
  After #3324 it costs more than latency. A nacked position is released only once every branch has
  voted, and a batching destination votes when it flushes, so with arrival-only checks on fan-out a
  record a sibling nacks on a quiet source never reaches the DLQ, and with the default DLQ (window 1,
  threshold 0) the pipeline never fails: it reports running with a failed record. Both last until the
  next record or a stop. That is a reason to prefer A, not a neutral fallback. It remains the fallback
  only if the fan-out fire is held back (Rollout, step 2), and then only with the WARN naming this
  cost.

#### B. Deadline case in `runFlusher`'s `select`

- **Lost:** the flusher exists only on the pipelined path (`funnel/destination.go:251-258`), so
  fan-out stays broken. To take `pending` the flusher would need `enqueueMu`, which it must never take
  (`funnel/destination.go:113-115`), and sending to its own full channel would deadlock.

#### C. Periodic ticker per destination

- **Lost:** wakes every idle pipeline every period, and a tick that is not aligned to the first record
  gives up to twice the configured delay. A one-shot timer is exact and costs nothing while idle.

#### D. Keep arrival-only checks and document them

- **Lost:** latency for a quiet source stays unbounded, and `sdk.batch.delay` keeps meaning something
  different on the two engines. A CDC table that goes quiet after a burst would not deliver the tail.

### #3321: who owns destination batching

#### A. Engine resolves effective batching, takes it over, neutralises the plugin (chosen)

Per key, pipeline config first, then the connector spec default. The engine batches with the result
and configures the plugin with `"0"` for each key that would otherwise enable the plugin's batcher.

- **Pros:** works for every connector whose spec declares the SDK batch parameters, built-in or
  standalone, without connector releases. Keeps the connector author's intent (S3 wants 1000-record
  objects) under the engine's control. Fixes the double batching. Classic is untouched.
- **Cons:** the engine must read the plugin spec at build time. A connector whose code default
  batches while its spec says it does not is not neutralised; it is the undeclared-batching case
  below.

#### B. Connector changes only (S3 drops its default, or keeps it for classic only)

- **Lost:** a connector cannot tell which engine runs it; `Configure` carries no engine signal.
  Dropping the default regresses classic S3 to one `PutObject` per record (the connector measured
  this at about 1.5 ms/record, 360× slower than a 1000-record batch, `destination/destination.go:35-37`).
  It also does nothing for third-party connectors.

#### C. The guard refuses connector-default batching

- **Lost:** every S3 pipeline would fail to start on the v0.22 default engine until the user sets
  `0`/`0`, and then it writes one object per record. A refusal for a configuration the engine can run
  well is the wrong default.

#### D. A pipeline-start warning only

- **Lost:** the pipeline still runs at 1 record/s.

#### E. Neutralise but do not adopt (plugin gets `0`/`0`, engine does not batch)

- **Lost:** fixes the stall, but S3 then pays one `PutObject` per record, capped near 650 records/s
  by the measurement above. Adopting the default costs nothing extra once the timer exists.

#### F. Let the plugin batch and read acks ahead on the unbatched path

- **Lost:** the SDK batcher releases acks only after 1000 records or 1 s. The read-ahead credit is
  `maxInFlightFlushes = 2` flushes (`destination_batch.go:39-46`); with one record per flush that is
  three records in flight, about 3 records/s. Raising the credit to 1000 unacked flushes undoes the
  memory and replay bounds of #3210 and #3291.

## Decision

### 1. Time-bounded flush of the partial batch (#3231)

**Where.** A `flushTimer *time.Timer` and a `bufferGen uint64` on `DestinationTask`, both guarded by
`mu`, and, for the synchronous path, `feeders []func(context.Context) (release func(), err error)`:
the `acquireProcessingLock` of each worker whose tree reaches the task, in worker index order. Each
worker registers itself with the batching destinations below its `FirstTask` when it is built
(`NewWorker`, which the service calls in source order after the shared sink is marked), walking
`Next` as `drainNode` does and recording the shared boundary node above the task. The task tree is
fixed before any worker starts (`hasBatchingBelow`'s invariant), so the list is fixed too.

- **Arm.** In `bufferBatch`, where `flushDeadline` is set today (`bufferedCount == 0 &&
  batchDelay > 0`): set the deadline, then `flushTimer = time.AfterFunc(batchDelay, func()
  { t.flushDue(gen) })`, capturing the current `bufferGen`. `drainNotSent` does not arm it: it always
  sets `flushErr` (`destination_batch.go:866-868`), so the task has failed closed and a timer would
  only no-op.
- **Disarm.** `resetBufferedLocked`, and the three inline resets in `flush`, `stopFlusher` and
  `drainNotSent` (which become calls to it), stop the timer and increment `bufferGen`. Every take
  goes through it, so a timer that fires after its buffer was taken by size, by an arrival past the
  deadline, by a queued-ack release, by `Drain` or by `Close` finds a different generation and
  returns. `Stop` returning false (the callback is already running) is fine for the same reason.
  The generation is the only no-op check that is not terminal (`closed`, `flushErr` and an ended
  `runCtx` are terminal), so a no-op callback never leaves a non-empty buffer of its own generation
  without a timer.
- **Fire, pipelined path.** `flushDue` takes `enqueueMu` with `LockCtx(runCtx)`, then `mu`. It returns
  without doing anything if the generation changed, the buffer is empty, `closed` is set or
  `flushErr` is set. Otherwise it takes `pending` the way `bufferBatch` does (`outstanding++`, reset
  in the same critical section), releases `mu`, and calls `enqueueFlush` while still holding
  `enqueueMu`. `enqueueFlush` either sends, or fails closed and records the error
  (`destination_batch.go:963-1011`). **The callback never calls `Write`, `Ack` or a source ack on
  this path**; the flusher does, in channel order. There is one destination, so one flusher, as
  20261019 requires.
- **Fire, synchronous path.** `flushDue` acquires every feeder's processing lock in order with
  `runCtx`, then checks the generation under `mu` (released before the next step), then does what
  `drainNode` does for one task: the shared boundary mutex if the task is under one, then
  `flush` (`syncMu`, `mu`, the `flushErr` check, take, `flushPending`, `finishFlush`). It releases
  the locks in reverse order. While it holds the processing locks no pass of those workers is in
  flight, so the timer's source acks are serialised with every pass exactly as the stop drain's are.
  Lock order: processing locks (worker index order), shared boundary mutex, `syncMu`, `mu`. A pass
  holds only its own worker's processing lock before the shared mutex, `Stop` and the EOF path hold
  only their own, and the timer is the only holder of several, always in the same order, so no cycle
  is possible. The callback holds nothing when it starts.
- **Arrival check stays.** `bufferBatch` and `releasableLocked` keep their `now.After(flushDeadline)`
  checks. They are cheap and cover a timer whose callback is waiting on a lock. Both triggers take
  under the same lock and generation, so only one of them flushes a given buffer.

**Clock.** Monotonic. `time.Now()` carries a monotonic reading and runtime timers use the monotonic
clock, so wall-clock steps (NTP, VM resume) neither fire the timer early nor hold it back. The
deadline is "first record of this buffer + `batchDelay`", the SDK batcher's definition.

**Ack ordering.**

- Pipelined: the timer's take is the same unit as `doBuffered`'s, under `enqueueMu`, so an ordered
  source ack that `deferOrdered` queues stays behind the records ahead of it
  (`destination_batch.go:158-181`). The flusher is still the only writer (invariant 4) and acks in
  channel order (invariants 1 and 2).
- Synchronous, one destination per worker: the timer flush is serialised with every pass by the
  processing lock and with every other flush of the task by `syncMu`.
- Synchronous, fan-out: the timer adds no interleaving that main does not already have. On main,
  destination B can flush batch N during pass N+1 while destination A still holds N; a timer flush
  of B is the same flush, run between passes instead of during one. For the all-ack case ordering
  holds: each destination votes in source order on its one stream, and a batch is released to the
  source by the call that casts its last vote, synchronously. Batch N+1 needs A's vote on N+1, which
  A casts after its vote on N, so N's `Source.Ack` returns before N+1 can be released. Since #3324
  (#3323, ADR `20261103-archv2-fanout-nack-waits-for-every-vote`) the same argument covers nacks: a nacked position is
  released (DLQ write, then `Source.Ack`) only once every branch has voted for it, nack still winning.
  So on fan-out the timer flush is the normal place where a sibling's nack is released: when B nacks
  a record of N+1 while A buffers N, the DLQ write happens when A's timer flush casts A's vote, not
  when B nacks. That release runs under the processing locks the timer holds, like every other
  source ack. A release that fails is final for that batch: later votes return the same error and
  never retry it (#3324).

**Graceful stop (SIGTERM).** `Worker.Stop` drains first, under its processing lock
(`worker.go:234-257`). On the synchronous path the timer needs the same lock, so it runs either
before the drain (and the drain finds an empty buffer) or after it (and finds a new generation). On
the pipelined path `Drain` takes the buffer through `resetBufferedLocked`; a timer flush already
queued ahead of the drain's barrier completes before the barrier closes. Either way the tail is
written and acked while the source is alive, so it is checkpointed (invariant 7).

**Close.** `Close` sets `closed` under `mu` first, on both paths, then calls `stopFlusher` or `flush`
as today, and stops the timer. Today only `stopFlusher` sets it (`destination_batch.go:922`). It is
set in `Close`, not in `flush`: on the synchronous path `Drain` _is_ `flush`
(`destination_batch.go:764-765`), and so is the timer's synchronous fire, so setting it there would
make `Worker.Stop` or a timer close a shared fan-out destination under its sibling workers, whose
`doBuffered`/`deferOrdered` would start returning "destination is closed". A callback that started
before `Close` holds `enqueueMu` or `syncMu`, which `stopFlusher` and `flush` wait for, or takes them
afterwards and sees `closed`. Nothing touches the destination after `Teardown`.

**kill -9.** Nothing changes about what is acked: the timer only moves the take earlier. Killed
before the timer fires, the tail is unacked and replays from the last persisted position. Killed
during a timer flush, the records in that flush were not acked (the ack follows the destination's
ack), so they replay; earlier flushes were acked in order, so the persisted position never passes an
unwritten record. The replay window shrinks: after this change a quiet tail is acked within the delay
instead of being held until the next record or stop.

**Fatal error and force-stop.** `runCtx` is cancelled. On the pipelined path the flusher exits and
fails closed (`destination_batch.go:702-708`); the callback's `LockCtx(runCtx)` fails, or it sees
`flushErr`, and returns. On the synchronous path the processing-lock acquire returns on `runCtx`, or
a `Write` with a cancelled ctx fails and `finishFlush` records the error in the same critical section
that releases the slot. Either way the records are unacked and replay.

**Errors while idle (separate fix, lands first).** A new buffered (capacity 1) task error channel,
read by `watchConnectorErrors` (`connector_errors.go:64-123`) next to `Destination.Errors()`; the run
fails through the tomb as it does for a persister error. It is sent to only from the two places that
record a flush's own failure (`finishFlush` after a failed flush, and `enqueueFlush` failing closed),
and only for an error that `Close` would report as `CodeFlushOnCloseFailed`
(`funnel/destination.go:300`). Not sent:

- the run's own cancellation (`runCancelled`);
- `plugin.ErrPluginNotRunning`: a flush after `drainNotStarted` left the buffer behind and the source
  was torn down, so the source ack fails and the records replay, the expected case `Close` already
  logs;
- a drain error recorded by `drainNotSent` when the stop context ran out during a graceful
  `Worker.Stop` (`destination_batch.go:843-871`): the worker is stopping and the records replay;
- a synchronous drain's own flush that failed because its context ended: on the fan-out path
  `Worker.Stop`'s `Drain` is `flush`, run with the stop context, so a stop deadline that ends while
  the destination's `Write` or `Ack` waits makes `finishFlush` record the context error. The worker is
  stopping and the records replay, as in the bullet above;
- a `poisonBatchingDestinations` echo (`setFlushErr` from a sibling's failed pass): that failure
  already failed the run where it happened.

**DLQ path.** A nack in a timer-triggered flush is routed by `ackPending` → `acker.Nack`, on the same
goroutine that runs the flush (the flusher, or the timer under the processing locks), as today. The
DLQ connector itself is not engine-batched and gets no timer; its in-plugin batching is handled in
part 2. Two error shapes reach the flush, and only one is a `deferredAckError`:

- A queued ordered action (a processor nack or filter above the destination, `ackPending`'s
  `p.action` branch) that fails returns a `deferredAckError`.
- A nack the destination itself returns, or a sibling's nack whose last vote this flush casts, goes
  `ackPending` → `p.acker.Nack` (`destination_batch.go:643`) → `runAckNacker` → `multiAckNacker` →
  `Worker.Nack`, which returns the raw error: `cerrors.FatalError` for a failed DLQ write or an
  exceeded nack threshold, and the plain record error (wrapped as "failed to nack N records") for
  the default DLQ (window 1, threshold 0). A failed `Source.Ack` after a successful DLQ write is a
  plain error too.

After #3324 the second shape is the one the fan-out timer meets routinely: a default-DLQ fan-out
pipeline whose source is idle fails only through the timer flush that casts the batching
destination's vote, while the worker is blocked in `Source.Read`. So the idle error channel must:

- send it. These errors fail the flush and leave the records unacked, so `Close` would report them as
  `CodeFlushOnCloseFailed`; none is on the not-sent list above. The filter must not special-case
  them.
- keep the fatal/non-fatal classification. The channel carries the error itself or wraps it with
  `%w`, never a fresh error built from its text: `cerrors.IsFatalError` uses `As`, so the chain must
  survive to the tomb. A threshold-exceeded or DLQ-write error that lost its fatal mark would land in
  the bounded-backoff recovery arm (infinite retries by default), and the source would re-read,
  re-nack and write the DLQ again on every restart: the loop `Worker.Nack`'s own comment warns about.
  A default-DLQ record error stays non-fatal, as on the single-destination path.

**What `sdk.batch.delay` means under arch-v2.** The longest a buffered record waits before its buffer
is _taken_ for writing, counted from the first record of that buffer. Delivery adds the flush queue
(bounded by the read-ahead credit), on fan-out any pass in flight, and the write itself. That matches
the SDK. Specifically:

| Destination settings | Flush triggers |
| --- | --- |
| size > 1, delay > 0 | size, byte cap, delay timer, stop |
| delay > 0 only | size 3000 (`defaultMaxBatchSize`, `destination_batch.go:37`), byte cap, delay timer, stop |
| size > 1 only | size, byte cap, stop. No time bound, as in the SDK. |
| neither | unbuffered `Do`, unchanged |

Size-only batching keeps SDK parity: no timer. The engine logs a WARN at build time with a new code,
`pipeline.archv2_destination_batch_unbounded_wait`, naming the connector and suggesting
`sdk.batch.delay`. On a fan-out the WARN also says that, after #3324, a sibling destination's nack is
routed to the DLQ, and with the default DLQ fails the pipeline, only when this destination flushes,
so on a quiet source those wait without bound too. A bounded default is proposed for later under
open question 1.

### 2. Ownership of destination batching (#3321)

**Resolve.** `buildDestinationTasks` resolves, per key:

1. the pipeline config value, if the key is present and parses (`strconv.Atoi`,
   `time.ParseDuration`, as `SDKBatchSettings` does; keys matched after trimming, as `settingValue`
   does, `pkg/provisioning/config/batching.go:84-92`);
2. otherwise the default in the plugin's destination spec, if the spec declares the key;
3. otherwise 0.

The spec comes from the same resolution that builds the destination's dispenser
(`instance.Connector`, `service.go:1608`, via `newDispenser`, `pkg/plugin/connector/service.go:212-233`):
the registry that `newDispenser` picked returns the already-loaded specification with the dispenser,
and the connector exposes it. The name is resolved once, so the spec and the running plugin cannot
disagree, and no plugin process is started for it. (As implemented: the built-in and standalone
dispensers carry the spec of the blueprint they were built from, the plugin service's cleanup wrapper
forwards it, and `connector.Destination.Specification` reads it. A dispenser built outside the
registries, which only tests do, carries none; then a key counts as declared only when the pipeline
config sets it, so an enabling pipeline value is still replaced by `"0"` and no key is added.
`pkg/plugin/connector/external.Dispenser` carries none either and is not reachable from `newDispenser`;
its package doc makes attaching the spec a prerequisite for wiring it in, and a test pins the gap.)

**Take over.** The task is built with the resolved size and delay, so S3 v0.12.0 with no `sdk.batch.*`
in the pipeline runs as an engine-batched 1000 / 1 s destination: the worker reads on, records
accumulate, and the timer from part 1 bounds the tail.

**Neutralise.** `SetEngineBatches` is replaced by a setter that tells `configure` which keys to
override. A key is overridden to `"0"` (replacing today's `delete`, `pkg/connector/destination.go:393-399`)
when the spec declares it and its value, pipeline or spec default, would turn the plugin's batcher on:
size > 1, or delay > 0. That covers both the S3 default (#3321) and a pipeline that sets
`sdk.batch.*` (the double batching). Otherwise the key is left as the pipeline wrote it, or absent:

- a value that does not parse, or parses below zero (`-5`, `-1s`), is passed through, so the plugin
  rejects it in `Configure` (`validate:"gt=-1"`), as today; the engine never hides a config error;
- an explicit `0` is already off;
- a key absent from the pipeline whose spec default is off gets nothing, so a connector whose spec
  forbids `0` (for example a size validation `greater-than: 0`) keeps working as it does today.

The trade-off: a connector whose code default batches while its spec default does not is not
neutralised. That connector is lying about its defaults; it falls under the undeclared-batching case
below, with the same workaround. Injecting `"0"` into every declared key would catch it, but would
break the connector whose spec forbids `0`, which is a pipeline that starts today and would stop.

Why `"0"` works across SDK versions:

- SDK v0.10 through v0.18.1 turn the batcher on only for size > 1 or delay > 0
  (`destination.go:135` in v0.10.1, `198` in v0.17.2, `205` in v0.18.1), and apply a default only to
  a missing key. S3 v0.13.0 tests exactly this: explicit `"0"`/`"0"` over its 1000 / 1 s default
  parses to 0/0 (`destination/default_batch_test.go`, "disable with zero").
- SDK v0.6.0 applies its default only when the value is empty (`destination_middleware.go:141-142`)
  and treats size > 0 as batching (`destination.go:162`), so `"0"` is right and `"1"` would not be.

Why only declared keys: SDK-based plugins validate settings against their spec and reject unknown
keys (`conduit-commons` v0.13.0 `config/config.go:112`, `ErrUnrecognizedParameter`). A plugin whose
spec does not declare `sdk.batch.*` gets no injected key.

**Old or non-SDK standalone plugins.** Every Go SDK release that has destination batching declares
both keys through its middleware, so the explicit `"0"` covers plugins built on old SDKs. What this
cannot cover is a plugin that batches internally without declaring `sdk.batch.*` (a custom or
non-Go implementation that blocks in `Write` or holds acks on its own). The engine cannot see that,
and the stall remains for that plugin. It is documented in the runbook with the workaround: set
`sdk.batch.size`/`sdk.batch.delay` in the pipeline so the engine batches and reads ahead. See open
question 4.

**DLQ.** The DLQ is not wired through engine batching (`batching_guard.go:26-31`). `buildDLQ` applies
the same neutralisation, so a DLQ connector with a batching spec default (S3 as a DLQ) writes each
nack synchronously instead of waiting 1 s. An explicit DLQ `sdk.batch.size > 1` is still refused. The
guard also refuses an explicit DLQ `sdk.batch.delay > 0`: the SDK turns delay-only into size 1000
(`destination.go:189-197` in v0.17.2), which withholds acks the same way, and the guard misses it
today. Same code, `pipeline.archv2_destination_batching_unsupported`
(`pkg/lifecycle-poc/codes.go:104`), through the same `batchingOffenders` path, so
`PreflightPipelineConfig` refuses it on a live apply too. The offline check behind
`conduit pipelines validate` and `conduit doctor` shares the predicate (`config.SDKBatchDelayAboveZero`), so
they report the same refusal. See open question 3.

**Values must be literals (as implemented, from the #3321 review).** The resolution reads the stored
settings, and `${VAR}` references are expanded afterwards, only for the plugin
(`connector.Instance.resolveSettings`). A reference such as `sdk.batch.size: ${S3_BATCH}` would read as
unset to the engine (which adopts the spec default and neutralises only the other key) while the
plugin gets the expanded value: with `S3_BATCH=10000` a size-only plugin batcher behind 1000-record
engine flushes, which stalls; with a delay reference, double batching. On the DLQ a reference would
also get past the refusal above. Under arch-v2, a destination or DLQ `sdk.batch.*` value containing
`$` (what the settings resolver acts on) is refused with
`pipeline.archv2_destination_batching_not_literal`, at Start before anything is built, in
`PreflightPipelineConfig`, and offline in `validate`/`doctor`, all through `config.SDKBatchNonLiteral`.
The alternative, resolving the references before the batching resolution and the Start guard, keeps
templated batch settings working but cannot be done in preflight or offline (they do not expand
variables), so those surfaces would still need the refusal. Refusing everywhere is smaller and fails
closed. Classic is unchanged.

**Classic engine.** Nothing changes. `pkg/lifecycle` never calls the setter, settings reach the plugin
verbatim, the SDK batches, and classic reads acks asynchronously. The resolution and neutralisation
live in `pkg/lifecycle-poc` and in the `pkg/connector` setter that only arch-v2 calls.

**Relationship to #3291 and #3292.** Their writer and AckReader are designs, not code on main. When
the writer replaces the flusher, the pipelined timer's take hands its buffer to the writer queue
instead of `flushCh`; the contract stays "the timer takes, it never writes or acks on the read-ahead
path". If #3292 gives fan-out its own concurrency model, the synchronous fire moves with it; until
then it is a drain under the processing locks. The spec-default resolution feeds their take policy
unchanged: an S3 destination arrives with an explicit size of 1000.

## Failure modes

| Failure | What happens | Invariants |
| --- | --- | --- |
| Source idle with a partial batch | Timer takes it after `delay`; written, then acked in order | 1, 2 |
| Timer fires while `doBuffered` holds `enqueueMu`/`syncMu` | Callback waits; on entry finds a new generation (arrival took it) or takes it itself | 1, 4 |
| Timer fires during a fan-out pass | Waits for the processing lock; runs after the pass, never during it | 1, 2 |
| Two destinations' timers fire together (fan-out) | Serialised by the processing lock, acquired in worker order. The second timer's flush may cast the last vote for a position another branch already nacked, and so be the flush that releases that deferred nack (#3324) | 2, 3 |
| Timer flush releases a sibling's deferred nack (fan-out, #3324): DLQ write fails, nack threshold exceeded, or default DLQ | `Worker.Nack` returns the raw error (fatal for the first two, the plain record error for the default DLQ); the flush records `flushErr`; nothing behind it is acked; the idle error channel fails the run with the classification intact, so a fatal error does not enter the backoff restart loop | 1, 3 |
| Timer fires after a size flush took the buffer | Generation mismatch, no-op | 4 |
| Timer and `deferOrdered` race | Both take under the same lock; queued acks stay behind earlier records | 1, 2 |
| Timer fires during `Drain` | Pipelined: `Drain` took the buffer, or the timer's flush is queued ahead of the barrier. Sync: serialised by the processing lock | 4, 7 |
| Timer fires during or after `Close` | Waits on the lock `Close` holds, then sees `closed`; no destination call after `Teardown` | 3 |
| Timer flush `Write` or `Ack` fails | `flushErr` set with the slot release; nothing behind it runs; run fails via the error channel | 3 |
| Drain times out during a graceful stop | Logged; records replay; run not failed by the error channel | 3, 7 |
| Flush after source teardown (`drainNotStarted`) | Source ack fails with `ErrPluginNotRunning`; logged, records replay; run not failed | 3 |
| Flush queue full when the timer fires | Callback blocks in `enqueueFlush` (backpressure), unblocked by the flusher, `flusherDone` or `runCtx` | 3 |
| Fatal error or force-stop | `runCtx` ends; callback cannot lock or sees `flushErr`; unacked records replay | 3 |
| SIGKILL before the timer fires | Tail never acked, replays | 1, 3 |
| SIGKILL mid timer flush | That flush unacked, replays; earlier flushes acked in order | 1, 2, 3 |
| SIGTERM with a partial batch | `Worker.Stop` drains before teardown; tail acked and checkpointed | 7 |
| Wall-clock step | No effect; monotonic timer and deadline | none |
| Spec default batches, pipeline sets nothing (S3) | Engine batches at the default; plugin gets `"0"`/`"0"` | 1 |
| Pipeline sets `sdk.batch.*` on S3 | Engine batches; plugin gets `"0"`/`"0"`, so its 1000 / 1 s default no longer comes back | 1 |
| Pipeline sets one key, spec the other | Merged per key, as the SDK would | none |
| Pipeline value does not parse, or is negative | Passed through; plugin rejects it in `Configure` | none |
| Spec does not declare `sdk.batch.*` | No injection; engine batches only if the pipeline asks | none |
| Spec forbids `0` and its default is off | No injection; unchanged from today | none |
| Plugin batches internally without declaring keys, or its code default disagrees with its spec | Not neutralised; still slow; documented workaround | 1 holds |
| DLQ with a batching spec default | Neutralised; nacks written synchronously | 1, 3 |
| Destination or DLQ `sdk.batch.*` is an environment reference | Refused at start, preflight, validate and doctor (`pipeline.archv2_destination_batching_not_literal`); nothing opened | 1, 3 |

No path in either change acks a record that is not durable, drops a record without delivering it or
routing it to the DLQ, or reorders records within a source.

## Test plan

Each test says whether it fails on `origin/main`. "Fails on main" tests are regression tests: each fix
PR shows the failure in its description. "New behaviour" tests cover code that does not exist on main
(the timer, the channel filter), so they cannot fail there in a meaningful way; they pin the new
code's contract.

### Idle error channel (its own PR)

- `TestWorker_FlusherErrorWhileIdle_FailsRun`: the flusher's `Write` fails after the last read; the
  source then blocks. Assert the run's fail func is called. **Fails on main** (hangs until the test
  bound).
- `TestWorker_StopDrainTimeout_DoesNotFailRun`: a graceful `Stop` whose ctx expires while the drain
  waits on the flusher. Assert the run is not failed and the records are unacked. New behaviour.
- `TestDestinationTask_FlushAfterSourceTeardown_DoesNotFailRun`: `drainNotStarted`, source torn down,
  then a flush whose source ack returns `ErrPluginNotRunning`. Assert it is logged, not sent. New
  behaviour.
- `TestDestinationTask_PoisonEcho_NotSent`: a sibling's failed pass poisons the task; assert the
  channel stays empty. New behaviour.

### #3231 (funnel, fake clock)

Time is faked with `testing/synctest` (`synctest.Test`, `synctest.Wait`); the mocks are in-memory, so
every goroutine blocks durably. If a test needs a non-durable block, the fallback is an unexported
`afterFunc` field on the task that tests replace; no clock dependency is added.

- `TestDestinationTask_TimerFlushesIdleTail_Pipelined`: size 10000, delay 1 s; one batch of 5,088
  records; no more arrivals. At 999 ms no `Write`. At 1 s one `Write` of 5,088 and one source ack.
  **Fails on main** (no `Write` ever).
- `TestDestinationTask_TimerFlushesIdleTail_Sync`: the same on the synchronous path. **Fails on main.**
- `TestWorker_FanOut_TimerFlushesIdleTail`: two destinations, delays 1 s and 2 s; assert each tail is
  written at its own deadline and the source ack for the batch is issued once both are. **Fails on
  main.**
- `TestWorker_FanOut_TimerWaitsForPass`: a pass is held open (hook in the second destination's
  `Write`) when the first destination's timer fires; assert the timer's flush starts only after the
  pass releases the processing lock, and every `Source.Ack` position is strictly increasing. New
  behaviour.
- `TestWorker_FanOut_TimerReleasesSiblingNack`: the timer variant of #3324's
  `TestWorker_FanOutBatching_SiblingNackDoesNotAckPastBufferedBatch` and its siblings (the size-only
  versions live in #3324). A has `sdk.batch.delay`, B is unbatched; batch N buffered on A; B nacks a
  record of N+1; the source goes idle. Assert: the sibling-nacked record reaches the DLQ only when A's
  timer flush casts A's vote, not when B nacks; the DLQ write and its `Source.Ack` both run while the
  timer holds the processing locks; `Source.Ack` positions stay monotonic. New behaviour.
- `TestWorker_FanOut_TimerReleasesSiblingNack_DefaultDLQFailsIdleRun`: the same with the default DLQ
  and an idle source. Assert the run fails through the idle error channel within the delay, the error
  is the record error and not fatal, and only positions before the nacked one are acked. A threshold
  variant (window 2, threshold 1, two nacks) asserts the error reaching the run is still
  `cerrors.IsFatalError`. New behaviour; both need #3324 and the idle error channel.
- `TestDestinationTask_TimerRacesTake`: hooks (`testAfterTake`, `testBeforeEnqueueSend`) hold the
  arrival take open while the timer fires; assert one `Write`, no empty flush, acks in order. New
  behaviour.
- `TestDestinationTask_TimerBehindQueuedAck`: a filtered batch queued by `deferOrdered` behind
  buffered records; the timer flush acks records, then the filtered batch. New behaviour.
- `TestDestinationTask_TimerAfterDrainAndClose`: the timer fires after `Drain`, and again after
  `Close`; assert no destination call after `Teardown`. Run with `-race -count=200`. New behaviour.
- `TestDestinationTask_SyncDrainLeavesTaskOpen`: `Drain` on the synchronous path, then `doBuffered`
  and `deferOrdered` from a sibling succeed; only `Close` makes them return "destination is closed".
  Passes on main; guards the `closed` move into `Close`.
- `TestService_ArchV2_TrailingPartialBatch` (`pkg/lifecycle-poc`, repro A in-process, real plugins,
  not in a synctest bubble): generator 300,000 at 8192/100 ms → file at 10000/1000 ms. Assert all 300,000
  lines are written and the last source position is acked before any stop is requested, polling with
  a 15 s bound. No assertion on how soon; the latency bound is the funnel tests' job and the benchi
  cell's. **Fails on main** (stays at 294,912).

### #3321 and the double batching

- `TestBuildDestinationTasks_AdoptsSpecDefaultBatching`: a destination plugin whose spec declares
  `sdk.batch.size` default 1000 and `sdk.batch.delay` default 1s; pipeline sets neither. Assert the
  task is built 1000 / 1 s and the plugin's `Configure` receives `"0"`/`"0"`. **Fails on main.**
- `TestArchV2_SpecDefaultBatching_TimeToAck`: an in-process fake destination built on the SDK with
  `DefaultDestinationMiddleware{DestinationWithBatch{BatchSize: 1000, BatchDelay: time.Second}}`
  behind a source that returns 1-record batches, 3,000 records, inside a synctest bubble. Assert every
  record is acked by fake time 5 s (fix: about 3 s from the size flushes and the timer; main: about
  3,000 s of fake time). **Fails on main.**
- `TestArchV2_PipelineBatching_PluginDefaultDoesNotComeBack`: the pipeline sets
  `sdk.batch.size: 10000` on the same S3-shaped fake (code default 1000 / 1 s). Assert `Configure`
  receives `"0"`/`"0"`, and a 1,500-record flush is fully acked with no 1 s wait in the plugin (fake
  time does not advance during the flush). **Fails on main** (keys deleted, plugin batches, the last
  500 wait 1 s).
- Table test of the resolution and the override: config only, spec only, per-key merge, unparsable
  value passed through, negative value passed through, padded key matched once (no second injected
  key), spec without the keys gets nothing, spec default off gets nothing, explicit `0`/`0` disables
  both. New behaviour.
- `TestBuildDLQ_NeutralisesSpecDefaultBatching` (**fails on main**: no `"0"` reaches `Configure`) and
  `TestBatchingGuard_RefusesDLQDelay`, including `PreflightPipelineConfig` on a live apply (**fails
  on main**: not refused).
- Classic: `pkg/lifecycle` destination `Configure` receives the settings verbatim (no `"0"` injected).
  Passes on main; guards constraint 7.

### Chaos (`tests/chaos`)

Every "random instant" is drawn from a seed that the test logs and accepts through an env var, so a
failure reproduces.

- `TestSIGKILL_PartialBatchHeldByTimerWindow`: destination batch 10000 / 2 s, a non-aligned total, the
  source idles after the last record; SIGKILL at a seeded random instant inside the 2 s window,
  restart, verify no loss, per-source order and replay ≤ one batch.
- `TestSIGKILL_DuringTimerFlush`: a slow destination holds the timer flush's `Write`; SIGKILL while
  it is in flight; restart and verify as above.
- `TestSIGKILL_FanOutTimerFlush` (on `fanout_child.go`): two destinations with different delays, the
  source idles; SIGKILL at a seeded instant between the two deadlines; restart and verify no loss on
  either destination and a monotonic persisted position.
- `TestSIGTERM_TimerFlushedTailCheckpointed`: idle tail flushed by the timer, then SIGTERM; restart
  replays zero records.
- `pipelinedFlusherRestart` (`tests/chaos/pipelined_flusher_sigkill_test.go:137-146`) no longer
  relies on `Worker.Stop`'s drain for the tail; its comment is updated, and the restart asserts the
  tail is acked before the stop.

### Benchmark (benchi)

New `benchi/archv2-destination-batch-timing/`, configs committed, run per `benchi/METHODOLOGY.md`
(ground-truth counts from the sink, medians of 5 with the A/A noise floor):

- **Throughput cell, kafka → S3 (MinIO).** Kafka source pinned to 1-record reads (`sdk.batch.size: 0`,
  `sdk.batch.delay: 0`, so the cell keeps its shape after #3309). Arms: classic, arch-v2 at main,
  arch-v2 with the fix. Pass: arch-v2 with the fix within 10% of classic records/s. Main is expected
  near 1 record/s.
- **Tail-latency cell, generator → file.** Repro A's shape: a 300,000-record burst, then idle. Metric:
  time from the last record produced to the last line visible. Pass: at most the delay plus 1 s.

No throughput or latency number is claimed until these runs are committed.

## Observability

- `conduit_funnel_destination_flushes_total{trigger}` (counter, labelled like `conduit_connector_*`):
  `trigger` is `size`, `bytes`, `deadline_arrival`, `deadline_timer`, `ack_queue`, `drain` or `close`.
  `deadline_timer` is "partial batch flushed by timer".
- `conduit_funnel_destination_flush_linger_seconds` (histogram): age of the oldest buffered record
  when its buffer is taken. A p99 above twice `sdk.batch.delay` means the timer, the flush queue or,
  on fan-out, the passes it waits behind are not keeping up.
- DEBUG log per timer flush: connector ID, records, linger.
- At build, one INFO log per destination whose batching came at least partly from the connector spec:
  `destination batching taken over from connector defaults`, with connector ID, plugin, resolved
  size and delay, and the origin of each (`pipeline_config` or `connector_default`). Counter
  `conduit_funnel_destination_plugin_batching_neutralised_total` (same labels), incremented once per
  destination open where at least one key was overridden to `"0"` (counted when the run is built, which
  precedes each open), which by the rule above means a
  value that would have turned the plugin's batcher on. Destinations with nothing to neutralise
  (file, kafka, log, postgres at their defaults) do not count.
- WARN `pipeline.archv2_destination_batch_unbounded_wait` for size-only batching (see Decision).
- Runbook: `docs/operations/archv2-destination-batching.md` is rewritten. It still describes the
  pre-20261017 refusal of destination batching, which only applies to the DLQ now. New entries: tail
  held longer than the delay (check `flush_linger_seconds` and `flushes_total{trigger="deadline_timer"}`),
  a destination that is slow behind a 1-record source (check the neutralised counter; a plugin that
  batches without declaring `sdk.batch.*`), and the two codes. #3324 adds the entry "Fan-out: a nack
  waits for the batching destination"; this design updates it: with `sdk.batch.delay` the wait is
  bounded by the timer, and with size-only batching the WARN above names it.

## Upgrade and rollback

- **Formats.** No change to positions, checkpoints, pipeline config schema or the connector protocol.
  N+1 needs no migration.
- **Behaviour change (arch-v2 only).** `sdk.batch.delay` bounds tail latency. A destination whose
  connector defaults to batching (S3) is batched by the engine at that default instead of stalling,
  so it writes fewer, larger objects than v0.21 arch-v2 did. An S3 destination with `sdk.batch.*` set
  is no longer batched a second time inside the plugin. An explicit DLQ `sdk.batch.delay > 0` is
  refused. Classic is unchanged.
- **Rollback.** Revert the PRs. Records already written stay written; at-least-once holds across
  the rollback because acks were only ever issued after durable writes. A user who wants the old
  plugin behaviour on arch-v2 has no reason to: it was the 1 record/s stall.

## Rollout

Arch-v2 is preview in v0.21, so none of this blocks v0.21. The fixes land on `main` for the v0.22
nightlies as three Tier 1 PRs, in this order:

1. The idle error channel with its filter, and its tests. Pre-existing bug, own regression test.
2. The timer (#3231), with its funnel, service and chaos tests. It depends on step 1. The fan-out
   (synchronous) fire lands after #3324, which makes it the place a sibling's nack is released. If
   #3324 has not merged, this PR ships the pipelined timer, keeps fan-out on arrival checks with the
   WARN (Alternative A5, with the cost stated there), and the fan-out fire follows #3324.
3. Spec-default resolution, the neutralisation (which also ends the double batching) and the DLQ
   guard extension (#3321), with its tests and the benchi cells. Rebases after #3309, which touches
   `pkg/connector/destination.go` and `pkg/plugin/connector/service.go`.

The v0.22 default-engine flip PR lists all three as prerequisites and does not merge until they are
on `main` and the benchi cells are committed. #3309 can land before or after; the benchi kafka arm
pins its source batch so the result does not depend on the order.

Docs in the same PRs: the runbook above, `llms.txt`/`llms-full.txt` and the `conduit.io` text on
`sdk.batch.delay` under arch-v2, the stale `--preview.pipeline-arch-v2` help text (it still says a
destination with `sdk.batch.size` above 1 is refused, `pkg/conduit/config.go:423`), the
20261017-era comments in `destination_batch.go:237-239` and `funnel/destination.go:162-168`, the
`SetPipelined` godoc and the `service.go:1665-1676` comment (fan-out stays synchronous; its timer runs
under the processing lock), and the v0.22 release notes.

## Open questions for DeVaris

Each has a proposed resolution; DeVaris decides.

1. **Size-only batching under arch-v2.** _Proposed:_ keep classic and SDK parity (no time bound) plus
   the startup WARN in v0.22. Then add a bounded default through the deprecation policy (announce,
   warn, change two minors later), as SDK v0.18.1 did for sources with `sdk.batch.unbounded_wait`.
   The unbounded wait loses no data, but it pins the source position (WAL retention on Postgres),
   and after #3324 it also delays, without bound, a fan-out sibling's DLQ routing and the default-DLQ
   pipeline failure. That strengthens the case for the bounded default.
2. **Adopting connector defaults.** _Proposed:_ adopt S3's 1000 / 1 s in the engine. Alternative E caps
   S3 near 650 records/s, a regression against classic, and adopting gives arch-v2 users the object
   layout classic users already have. The layout change goes in the v0.22 release notes.
3. **DLQ `sdk.batch.delay > 0`.** _Proposed:_ refuse it without a deprecation window (arch-v2 is
   preview, and the setting withholds acks today), through `batchingOffenders`, so a live apply is
   refused too. A release-note line tells existing arch-v2 pipelines with this setting that they will
   no longer start.
4. **Plugins that batch without declaring `sdk.batch.*`.** _Proposed:_ document only. A latency
   heuristic cannot tell a batching plugin from a slow destination, and an alert that fires falsely
   gets ignored. The runbook entry and the linger histogram are enough.
5. **Idle error channel.** _Proposed:_ its own fix PR, landing first (Rollout, step 1), with the
   filter above and a regression test that hangs on main. The timer PR then gets smaller and depends
   on it.

## Related

- Issues #3231, #3321. Smoke run root cause, sections 1d and 1e (cells H-kafka-s3 `conduit-s` and
  `conduit-t`).
- #3323 (fan-out nack acked the source past a batch a sibling still buffered), fixed by #3324 and
  ADR `20261103-archv2-fanout-nack-waits-for-every-vote`: a nacked position is released only after all M branches
  have voted, nack still winning; a failed release is final. The timer reaches the source only
  through the acker chain, under the processing locks, so the deferred nack release rides the timer
  flush unchanged. The fan-out fire is gated on #3324 (Rollout, step 2). Postmortem:
  `docs/postmortems/20261103-archv2-fanout-sibling-nack-acks-past-buffered-batch.md`.
- `docs/design-documents/20261017-funnel-destination-batching.md` (Alternative C, superseded here for
  the timer).
- `docs/design-documents/20261019-funnel-read-ahead-pipelining.md` (#3210, the flusher, and why fan-out
  is synchronous).
- `docs/design-documents/20261027-funnel-write-coalescing.md` (#3291) and
  `docs/design-documents/20261027-funnel-shared-destination-concurrency.md` (#3292).
- `docs/design-documents/20261027-default-source-batching.md` and PR #3309.
- `docs/operations/archv2-destination-batching.md`.
- Roadmap: v0.22 default-engine flip.
