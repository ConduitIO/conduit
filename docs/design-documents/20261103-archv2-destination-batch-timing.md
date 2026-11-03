# Arch-v2 destination batch timing and ownership

## Summary

Two arch-v2 (`--preview.pipeline-arch-v2`) destination-batching bugs, found in the v0.21 CDC-parity
smoke run and reproduced locally. Neither loses data: no record is acked before it is written, so
invariants 1 and 3 hold and a crash replays. Both break the contract the default-engine flip relies
on.

- **#3231, the partial batch has no timer.** The funnel's destination buffer flushes on size, or on
  `sdk.batch.delay` checked only when the next record arrives. When the source goes idle, the tail
  stays unwritten and unacked until more records arrive or the pipeline stops. The smoke run held the
  last 5,088 of 300,000 records for the whole settle window; they were written only on SIGTERM.
- **#3321, connector-default batching slips past the engine.** A destination whose connector enables
  SDK batching by default (`conduit-connector-s3` v0.12.0: 1000 records / 1 s) behind a 1-record
  source runs at about 1 record/s. The engine takes over batching only when the _pipeline config_
  sets `sdk.batch.*`, so the plugin batches, withholds each ack for 1 s, and the stop-and-wait loop
  waits for it.

Decisions:

1. **A one-shot flush timer per `DestinationTask`**, armed with the existing deadline when the first
   record enters an empty buffer. When it fires it takes the buffer under the same locks as
   `doBuffered` and hands it to the existing flush path. On the pipelined path it only enqueues; the
   flusher still does every `Write`, `Ack` and source ack. `sdk.batch.delay` then bounds how long a
   buffered record waits, as it does in the SDK.
2. **Arch-v2 owns destination batching.** The engine resolves the _effective_ `sdk.batch.size` and
   `sdk.batch.delay` per key (pipeline config first, then the connector spec default), batches with
   them, and configures the plugin with explicit `"0"` for both keys, so the plugin's own default
   cannot switch the SDK batcher on. The classic engine is unchanged.

Both fixes gate the v0.22 default-engine flip. Risk tier: Tier 1 (ack timing, destination batching,
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
- Graceful stop already drains before the source is torn down: `Worker.Stop` calls
  `drainDestinationsBeforeTeardown` (`funnel/worker.go:252-257`), and `DestinationTask.Drain` sends
  the buffer plus a barrier to the flusher and waits (`destination_batch.go:760-826`). That half of
  #3231 (invariant 7) is fixed on main. The timer half is not.

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

The 20261017 doc rejected a ticker (Alternative C) because the flush "would ack source records from a
non-worker goroutine". #3210's flusher has acked from a non-worker goroutine since 20261019, so that
reason no longer holds. #3291's Alternative B repeats it; the timer below does not ack on the
pipelined path at all.

A second gap sits next to it. When the flusher fails while the source is idle, nothing tells the
worker. `runFlusher` records the error (`finishFlush`, `destination_batch.go:724-729`) and exits; the
only readers of `flushErr` are `doBuffered`, `deferOrdered`, `Drain` and `Close`, all called on a
record arrival or a stop. The worker stays blocked in `Source.Read`, and the pipeline reports
running. The timer makes flushes on an idle source routine, so it has to close this gap too.

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
6. **The classic engine does not change.** It reads destination acks on their own goroutine, so SDK
   batching works there, and connectors that default to it (S3) depend on that.
7. **Plugins are not ours.** Standalone plugins can be built on any SDK since v0.6, or on none. No
   protocol change in this design (CLAUDE.md: protocol changes need a versioning discussion).
8. **No new dependencies.** Go 1.26 (`go.mod:3`) ships `testing/synctest`, which fakes time for
   `time.AfterFunc` and `time.Now` in tests without a clock interface.

## Alternatives considered

### #3231: where the timer lives

#### A. One-shot timer per `DestinationTask` that only takes the buffer (chosen)

`time.AfterFunc(batchDelay, ...)` armed under `mu` in `bufferBatch` when the first record enters an
empty buffer (the line that sets `flushDeadline` today). The callback takes the buffer exactly as
`doBuffered` does and hands it to the existing flush path. It covers both the pipelined and the
synchronous path, and a shared destination has one buffer, so one timer.

- **Pros:** exact deadline (same arming point as the SDK batcher); no goroutine while idle; reuses the
  existing take/enqueue units and their invariant comments; no new owner of the stream.
- **Cons:** a second goroutine can take `pending`. That is already true (`deferOrdered` from a sibling
  worker, `releaseSync` from a processor), and the timer uses the same locks.

#### B. Deadline case in `runFlusher`'s `select`

- **Lost:** the flusher exists only on the pipelined path (`funnel/destination.go:251-258`), so
  fan-out stays broken. To take `pending` the flusher would need `enqueueMu`, which it must never take
  (`funnel/destination.go:113-115`), and sending to its own full channel would deadlock.

#### C. Periodic ticker per destination

- **Lost:** wakes every idle pipeline every period, and a tick that is not aligned to the first record
  gives up to twice the configured delay. A one-shot timer is exact and costs nothing while idle.

#### D. Wake the worker from `Source.Read` to flush

- **Lost:** `connector.Source.Read` does not watch its ctx (`funnel/connector_errors.go:37-40`). On
  a shared destination the worker is not the buffer's owner anyway.

#### E. Keep arrival-only checks and document them

- **Lost:** latency for a quiet source stays unbounded, and `sdk.batch.delay` keeps meaning something
  different on the two engines. A CDC table that goes quiet after a burst would not deliver the tail.

### #3321: who owns destination batching

#### A. Engine resolves effective batching, takes it over, neutralises the plugin (chosen)

Per key, pipeline config first, then the connector spec default. The engine batches with the result
and configures the plugin with `sdk.batch.size: "0"` and `sdk.batch.delay: "0"`.

- **Pros:** works for every connector whose spec declares the SDK batch parameters, built-in or
  standalone, without connector releases. Keeps the connector author's intent (S3 wants 1000-record
  objects) under the engine's control. Classic is untouched.
- **Cons:** the engine must look up the plugin spec at build time; a connector whose code default and
  spec disagree gets the spec's value in the engine (the explicit `"0"` still turns its SDK batcher
  off, so the worst case is unbatched writes, not a stall).

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

- **Lost:** fixes the stall, but S3 then pays one `PutObject` per record, capped near 650 records/s by
  the measurement above. Adopting the default costs nothing extra once the timer exists.

#### F. Let the plugin batch and read acks ahead on the unbatched path

- **Lost:** the SDK batcher releases acks only after 1000 records or 1 s. The read-ahead credit is
  `maxInFlightFlushes = 2` flushes (`destination_batch.go:39-46`); with one record per flush that is
  three records in flight, about 3 records/s. Raising the credit to 1000 unacked flushes undoes the
  memory and replay bounds of #3210 and #3291.

## Decision

### 1. Time-bounded flush of the partial batch (#3231)

**Where.** A `flushTimer *time.Timer` and a `bufferGen uint64` on `DestinationTask`, both guarded by
`mu`.

- **Arm.** In `bufferBatch`, where `flushDeadline` is set today (`bufferedCount == 0 &&
  batchDelay > 0`): set the deadline, then `flushTimer = time.AfterFunc(batchDelay, func()
  { t.flushDue(gen) })`, capturing the current `bufferGen`. `drainNotSent`, which restores a buffer
  and marks it overdue (`destination_batch.go:858-860`), arms it with zero delay.
- **Disarm.** `resetBufferedLocked`, and the three inline resets in `flush`, `stopFlusher` and
  `drainNotSent` (which become calls to it), stop the timer and increment `bufferGen`. Every take
  goes through it, so a timer that fires after its buffer was taken by size, by an arrival past the
  deadline, by a queued-ack release, by `Drain` or by `Close` finds a different generation and
  returns. `Stop` returning false (the callback is already running) is fine for the same reason.
- **Fire, pipelined path.** `flushDue` takes `enqueueMu` with `LockCtx(runCtx)`, then `mu`. It returns
  without doing anything if the generation changed, the buffer is empty, `closed` is set or
  `flushErr` is set. Otherwise it takes `pending` the way `bufferBatch` does (`outstanding++`, reset
  in the same critical section), releases `mu`, and calls `enqueueFlush` while still holding
  `enqueueMu`. `enqueueFlush` either sends, or fails closed and records the error
  (`destination_batch.go:963-1011`). **The callback never calls `Write`, `Ack` or a source ack on
  this path**; the flusher does, in channel order.
- **Fire, synchronous path.** `flushDue` takes `syncMu` (blocking: the callback holds nothing else,
  so the lock order `syncMu` → `mu` cannot invert), then `mu`. It makes the same checks plus
  `outstanding == 0` (as `releaseSync` does, `destination_batch.go:408-415`), takes, flushes with
  `flushPending` + `finishFlush`, releases `syncMu`, then calls `releaseSync` for acks queued
  meanwhile. This is the `releaseSync` contract (flush and source ack under `syncMu`, without
  `sharedMu`) run from a timer instead of a processor.
- **Arrival check stays.** `bufferBatch` and `releasableLocked` keep their `now.After(flushDeadline)`
  checks. They are cheap and cover a timer whose callback is waiting on a lock. Both triggers take
  under the same lock and generation, so only one of them flushes a given buffer.

**Clock.** Monotonic. `time.Now()` carries a monotonic reading and runtime timers use the monotonic
clock, so wall-clock steps (NTP, VM resume) neither fire the timer early nor hold it back. The
deadline is "first record of this buffer + `batchDelay`", the SDK batcher's definition.

**Ack ordering.** The timer's take is the same unit as `doBuffered`'s: under `enqueueMu` (pipelined)
or `syncMu` (synchronous), so an ordered source ack that `deferOrdered` queues stays behind the
records ahead of it (`destination_batch.go:158-181`). On the pipelined path the flusher is still the
only writer (invariant 4) and acks in channel order (invariants 1 and 2). On the synchronous path
`syncMu` serialises the timer's flush with every other synchronous flush of the task.

**Graceful stop (SIGTERM).** `Worker.Stop` drains first (`worker.go:252-257`). `Drain` takes the
buffer through `resetBufferedLocked`, so a timer that fires afterwards sees a new generation. The tail
is written and acked while the source is alive, so it is checkpointed (invariant 7). A timer flush
already queued ahead of the drain's barrier completes before the barrier closes.

**Close.** `Close` sets `closed` under `mu` on both paths; today only `stopFlusher` does
(`destination_batch.go:922`), and the synchronous `flush` does not (`destination_batch.go:438-467`).
It stops the timer. A callback that started before `Close` either holds `enqueueMu` or `syncMu`, which
`stopFlusher` and `flush` wait for, or takes them afterwards and sees `closed`. Nothing touches the
destination after `Teardown`.

**kill -9.** Nothing changes about what is acked: the timer only moves the take earlier. Killed
before the timer fires, the tail is unacked and replays from the last persisted position. Killed
during a timer flush, the records in that flush were not acked (the ack follows the destination's
ack), so they replay; earlier flushes were acked in order, so the persisted position never passes an
unwritten record. The replay window shrinks: after this change a quiet tail is acked within the delay
instead of being held until the next record or stop.

**Fatal error and force-stop.** `runCtx` is cancelled. On the pipelined path the flusher exits and
fails closed (`destination_batch.go:702-708`); the callback's `LockCtx(runCtx)` fails, or it sees
`flushErr`, and returns. On the synchronous path a `Write` with a cancelled ctx fails and
`finishFlush` records the error in the same critical section that releases the slot. Either way the
records are unacked and replay.

**Errors while idle.** Every flush error recorded by the timer path or the flusher is also sent, once,
on a new buffered (capacity 1) task error channel. `watchConnectorErrors`
(`connector_errors.go:64-123`) reads it next to `Destination.Errors()`, and the run fails through the
tomb as it does for a persister error. A run-context cancellation is not sent; it is the run ending.
Without this, a timer flush that fails on an idle source would leave the pipeline running with
nothing written.

**DLQ path.** A nack in a timer-triggered flush is routed by `ackPending` → `acker.Nack`, on the same
goroutine that runs the flush (the flusher, or the timer on the synchronous path), as today. The DLQ
connector itself is not engine-batched and gets no timer; its in-plugin batching is handled in part 2.
A DLQ write that fails returns a `deferredAckError`, fails the flush and is now surfaced while idle by
the error channel above.

**What `sdk.batch.delay` means under arch-v2.** The longest a buffered record waits before its buffer
is _taken_ for writing, counted from the first record of that buffer. Delivery adds the flush queue
(bounded by the read-ahead credit) and the write itself. That matches the SDK. Specifically:

| Destination settings | Flush triggers |
| --- | --- |
| size > 1, delay > 0 | size, byte cap, delay timer, stop |
| delay > 0 only | size 3000 (`defaultMaxBatchSize`, `destination_batch.go:37`), byte cap, delay timer, stop |
| size > 1 only | size, byte cap, stop. No time bound, as in the SDK. |
| neither | unbuffered `Do`, unchanged |

Size-only batching keeps SDK parity: no timer. The engine logs a WARN at build time with a new code,
`pipeline.archv2_destination_batch_unbounded_wait`, naming the connector and suggesting
`sdk.batch.delay`. Whether arch-v2 should impose a default ceiling instead is open question 1.

### 2. Ownership of destination batching (#3321)

**Resolve.** `buildDestinationTasks` resolves, per key:

1. the pipeline config value, if the key is present and parses (`strconv.Atoi`,
   `time.ParseDuration`, as `SDKBatchSettings` does);
2. otherwise the default in the plugin's destination spec, if the spec declares the key;
3. otherwise 0.

The spec comes from a new `PluginService` method that returns the already-loaded specification from
the builtin or standalone registry, resolving the name with the same rules as `newDispenser`
(standalone first for an unprefixed name, builtin on `ErrPluginNotFound`,
`pkg/plugin/connector/service.go:212-233`). It does not start a plugin process. A spec lookup that
fails fails the pipeline start with the lookup error; the dispenser for the same name is created a
few lines earlier, so in practice it cannot fail alone.

**Take over.** The task is built with the resolved size and delay, so S3 v0.12.0 with no `sdk.batch.*`
in the pipeline runs as an engine-batched 1000 / 1 s destination: the worker reads on, records
accumulate, and the timer from part 1 bounds the tail.

**Neutralise.** `SetEngineBatches` is replaced by a setter that tells `configure` which keys the spec
declares. For each declared key whose pipeline value is absent or parses, `configure` sets it to
`"0"` instead of deleting it (`pkg/connector/destination.go:393-399` today). That holds even when the
resolved batching is off, so a connector whose code default disagrees with its spec cannot switch
its batcher on. A key whose pipeline value does not parse is passed through unchanged, so the plugin
still rejects it in `Configure`, as today; the engine never hides a config error.

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
`sdk.batch.size`/`sdk.batch.delay` in the pipeline so the engine batches and reads ahead. Runtime
detection is open question 4.

**DLQ.** The DLQ is not wired through engine batching (`batching_guard.go:26-31`). `buildDLQ` applies
the same neutralisation, so a DLQ connector with a batching spec default (S3 as a DLQ) writes each
nack synchronously instead of waiting 1 s. An explicit DLQ `sdk.batch.size > 1` is still refused. The
guard also refuses an explicit DLQ `sdk.batch.delay > 0`: the SDK turns delay-only into size 1000
(`destination.go:189-197` in v0.17.2), which withholds acks the same way, and the guard misses it
today. Same code, `pipeline.archv2_destination_batching_unsupported`
(`pkg/lifecycle-poc/codes.go:104`). Open question 3.

**Classic engine.** Nothing changes. `pkg/lifecycle` never calls the setter, settings reach the plugin
verbatim, the SDK batches, and classic reads acks asynchronously. The resolution and neutralisation
live in `pkg/lifecycle-poc` and in the `pkg/connector` setter that only arch-v2 calls.

**Relationship to #3291 and #3292.** Their writer and AckReader are designs, not code on main. When
the writer replaces the flusher, the timer's take hands its buffer to the writer queue instead of
`flushCh`; the contract stays "the timer takes, it never writes or acks on the read-ahead path". The
spec-default resolution feeds their take policy unchanged: an S3 destination arrives with an explicit
size of 1000.

## Failure modes

| Failure | What happens | Invariants |
| --- | --- | --- |
| Source idle with a partial batch | Timer takes it after `delay`; written, then acked in order | 1, 2 |
| Timer fires while `doBuffered` holds `enqueueMu`/`syncMu` | Callback waits; on entry finds a new generation (arrival took it) or takes it itself | 1, 4 |
| Timer fires after a size flush took the buffer | Generation mismatch, no-op | 4 |
| Timer and `deferOrdered` race | Both take under the same lock; queued acks stay behind earlier records | 1, 2 |
| Timer fires during `Drain` | `Drain` took the buffer (new generation), or the timer's flush is queued ahead of the barrier | 4, 7 |
| Timer fires during or after `Close` | Waits on the lock `Close` holds, then sees `closed`; no destination call after `Teardown` | 3 |
| Timer flush `Write` or `Ack` fails | `flushErr` set with the slot release; nothing behind it runs; run fails via the error channel | 3 |
| Flush queue full when the timer fires | Callback blocks in `enqueueFlush` (backpressure), unblocked by the flusher, `flusherDone` or `runCtx` | 3 |
| Fatal error or force-stop | `runCtx` ends; callback cannot lock or sees `flushErr`; unacked records replay | 3 |
| SIGKILL before the timer fires | Tail never acked, replays | 1, 3 |
| SIGKILL mid timer flush | That flush unacked, replays; earlier flushes acked in order | 1, 2, 3 |
| SIGTERM with a partial batch | `Worker.Stop` drains before teardown; tail acked and checkpointed | 7 |
| Wall-clock step | No effect; monotonic timer and deadline | none |
| Spec default batches, pipeline sets nothing (S3) | Engine batches at the default; plugin gets `"0"`/`"0"` | 1 |
| Pipeline sets one key, spec the other | Merged per key, as the SDK would | none |
| Pipeline value does not parse | Passed through; plugin rejects it in `Configure` | none |
| Spec does not declare `sdk.batch.*` | No injection; engine batches only if the pipeline asks | none |
| Plugin batches internally without declaring keys | Not neutralised; still slow; documented workaround | 1 holds |
| Code default and spec disagree | Plugin batcher off anyway (`"0"`); engine uses the spec value | none |
| DLQ with a batching spec default | Neutralised; nacks written synchronously | 1, 3 |

No path in either change acks a record that is not durable, drops a record without delivering it or
routing it to the DLQ, or reorders records within a source.

## Test plan

Every test below fails on `origin/main` and passes with the fix. Each fix PR shows the failure in its
description.

### #3231 regression tests (funnel, fake clock)

Time is faked with `testing/synctest` (`synctest.Test`, `synctest.Wait`); the mocks are in-memory, so
every goroutine blocks durably. If a test needs a non-durable block, the fallback is an unexported
`afterFunc` field on the task that tests replace; no clock dependency is added.

- `TestDestinationTask_TimerFlushesIdleTail_Pipelined`: size 10000, delay 1 s; one batch of 5,088
  records; no more arrivals. At 999 ms no `Write`. At 1 s one `Write` of 5,088 and one source ack.
  On main: no `Write` ever (the test fails on its timeout).
- `TestDestinationTask_TimerFlushesIdleTail_Sync`: the same on the synchronous path (no flusher).
- `TestDestinationTask_TimerRacesTake`: hooks (`testAfterTake`, `testBeforeEnqueueSend`) hold the
  arrival take open while the timer fires; assert one `Write`, no empty flush, acks in order.
- `TestDestinationTask_TimerBehindQueuedAck`: a filtered batch queued by `deferOrdered` behind
  buffered records; the timer flush acks records, then the filtered batch.
- `TestDestinationTask_TimerAfterDrainAndClose`: the timer fires after `Drain`, and again after
  `Close`; assert no destination call after `Teardown`. Run with `-race -count=200`.
- `TestDestinationTask_TimerFlushError_FailsRunWhileIdle`: `Write` fails in the timer flush; the run's
  fail func is called within the test bound and nothing is acked. On main: hangs.
- `TestService_ArchV2_TrailingPartialBatch` (`pkg/lifecycle-poc`, repro A in-process): generator
  300,000 at 8192/100 ms → file at 10000/1000 ms; all 300,000 lines and the final source ack within
  3 s of the last read, before any stop.

### #3321 regression tests

- `TestBuildDestinationTasks_AdoptsSpecDefaultBatching`: a destination plugin whose spec declares
  `sdk.batch.size` default 1000 and `sdk.batch.delay` default 1s; pipeline sets neither. Assert the
  task is built 1000 / 1 s and the plugin's `Configure` receives `"0"`/`"0"`.
- `TestArchV2_SpecDefaultBatching_TimeToAck`: a fake destination built on the SDK with
  `DefaultDestinationMiddleware{DestinationWithBatch{BatchSize: 1000, BatchDelay: time.Second}}`
  behind a source that returns 1-record batches, 3,000 records. Assert every record is acked within
  5 s (fix: about 3 s from the timer and size flushes; main: about 3,000 s, so the test fails on its
  timeout).
- Table test of the resolution: config only, spec only, per-key merge, unparsable value passed
  through, spec without the keys gets nothing injected, explicit `0`/`0` disables both.
- `TestBuildDLQ_NeutralisesSpecDefaultBatching` and a guard test for an explicit DLQ
  `sdk.batch.delay`.
- Classic: `pkg/lifecycle` destination `Configure` receives the settings verbatim (no `"0"` injected).

### Chaos (`tests/chaos`)

- `TestSIGKILL_PartialBatchHeldByTimerWindow`: destination batch 10000 / 2 s, a non-aligned total, the
  source idles after the last record; SIGKILL at a random instant inside the 2 s window, restart,
  verify no loss, per-source order and replay ≤ one batch.
- `TestSIGKILL_DuringTimerFlush`: a slow destination holds the timer flush's `Write`; SIGKILL while it
  is in flight; restart and verify as above.
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
  when its buffer is taken. A p99 above twice `sdk.batch.delay` means the timer or the flush queue is
  not keeping up.
- DEBUG log per timer flush: connector ID, records, linger.
- At build, one INFO log per destination whose batching came at least partly from the connector spec:
  `destination batching taken over from connector defaults`, with connector ID, plugin, resolved
  size and delay, and the origin of each (`pipeline_config` or `connector_default`). Counter
  `conduit_funnel_destination_plugin_batching_neutralised_total` (same labels), incremented once per
  destination open where a declared key was overridden to `"0"`.
- WARN `pipeline.archv2_destination_batch_unbounded_wait` for size-only batching (see Decision).
- Runbook: `docs/operations/archv2-destination-batching.md` is rewritten. It still describes the
  pre-20261017 refusal of destination batching, which only applies to the DLQ now. New entries: tail
  held longer than the delay (check `flush_linger_seconds` and `flushes_total{trigger="deadline_timer"}`),
  a destination that is slow behind a 1-record source (check the neutralised counter; a plugin that
  batches without declaring `sdk.batch.*`), and the two codes.

## Upgrade and rollback

- **Formats.** No change to positions, checkpoints, pipeline config schema or the connector protocol.
  N+1 needs no migration.
- **Behaviour change (arch-v2 only).** `sdk.batch.delay` bounds tail latency. A destination whose
  connector defaults to batching (S3) is batched by the engine at that default instead of stalling, so
  it writes fewer, larger objects than v0.21 arch-v2 did. Classic is unchanged.
- **Rollback.** Revert the two PRs. Records already written stay written; at-least-once holds across
  the rollback because acks were only ever issued after durable writes. A user who wants the old
  plugin behaviour on arch-v2 has no reason to: it was the 1 record/s stall.

## Rollout

Arch-v2 is preview in v0.21, so neither bug blocks v0.21. Both fixes land on `main` for the v0.22
nightlies as two Tier 1 PRs, in this order:

1. The timer and the idle error channel (#3231), with its funnel, service and chaos tests.
2. Spec-default resolution, neutralisation and the DLQ guard extension (#3321), with its tests and the
   benchi cells.

The v0.22 default-engine flip PR lists both as prerequisites and does not merge until both are on
`main` and the benchi cells are committed. #3309 can land before or after either; the benchi kafka
arm pins its source batch so the result does not depend on the order.

Docs in the same PRs: the runbook above, `llms.txt`/`llms-full.txt` and the `conduit.io` text on
`sdk.batch.delay` under arch-v2, the stale `--preview.pipeline-arch-v2` help text (it still says a
destination with `sdk.batch.size` above 1 is refused, `pkg/conduit/config.go:423`), the
20261017-era comments in `destination_batch.go:237-239` and `funnel/destination.go:162-168`, and the
v0.22 release notes.

## Open questions for DeVaris

1. **Size-only batching under arch-v2.** This design keeps SDK parity (no time bound) and warns. The
   alternative is a default ceiling (for example 1 s) when only `sdk.batch.size` is set, which bounds
   every quiet tail but diverges from classic. Which one?
2. **Adopting connector defaults.** The design adopts S3's 1000 / 1 s in the engine rather than only
   neutralising it (Alternative E). That changes the object layout for arch-v2 S3 users compared with
   v0.21. Confirm.
3. **DLQ `sdk.batch.delay > 0` refusal.** New refusal on the preview engine for a configuration that
   withholds acks today. Acceptable without a deprecation window, given arch-v2 is preview?
4. **Plugins that batch without declaring `sdk.batch.*`.** Document only (this design), or add a
   runtime WARN when a 1-record `Write` repeatedly takes longer than a threshold to ack? The heuristic
   would also fire on a slow destination.
5. **Idle error channel.** It also fixes a pre-existing gap (a flusher failure on an idle source is
   not surfaced). Ship it inside the #3231 PR, or as its own fix PR first?

## Related

- Issues #3231, #3321. Smoke run root cause, sections 1d and 1e (cells H-kafka-s3 `conduit-s` and
  `conduit-t`).
- `docs/design-documents/20261017-funnel-destination-batching.md` (Alternative C, superseded here for
  the timer).
- `docs/design-documents/20261019-funnel-read-ahead-pipelining.md` (#3210, the flusher).
- `docs/design-documents/20261027-funnel-write-coalescing.md` (#3291) and
  `docs/design-documents/20261027-funnel-shared-destination-concurrency.md` (#3292).
- `docs/design-documents/20261027-default-source-batching.md` and PR #3309.
- `docs/operations/archv2-destination-batching.md`.
- Roadmap: v0.22 default-engine flip.
