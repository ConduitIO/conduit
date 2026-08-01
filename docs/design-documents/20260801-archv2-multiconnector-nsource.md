# N-source fan-in for arch-v2 (slice 3b of the multi-connector epic)

## Summary

Lights up the multi-**source** axis of arch-v2's `funnel` runtime: N source connectors, one
`funnel.Worker` per source, all sharing exactly one destination (and its pipeline-level processors).
This is **slice 3b** of the arch-v2 multi-connector epic — the mirror image of slice 3a (1 source, M
destinations, `20260731-archv2-multiconnector.md`). N sources **and** M destinations together (N×M)
is explicitly out of scope — see "N×M is not blocked, but is untested" below. **Tier 1** — this is
data-path code; a wrong teardown ordering or a mis-attributed ack here drops or corrupts customer
data.

Removes the multi-source guard in `buildSourceTasks` (`pkg/lifecycle-poc/service.go`), which
previously rejected any pipeline with more than one source connector. In its place:

- **One `funnel.Worker` per source.** Each worker owns its own source, its own per-connector
  processors, and its own DLQ. Nothing new needed to be built for this part — each worker was
  already its own terminal acker; the only change is that `runPipeline` now spawns N of them instead
  of one.
- **A new `funnel.Sink` type** owns the shared, destination-side portion of the task graph
  (pipeline-level processors + destination branch(es)) that every worker's own chain converges on.
  This is the crux of the slice — see "Shared-sink teardown ordering" below.
- **`TaskNode.MarkSharedBoundary`**, a small addition to the existing `TaskNode`/`doTask` machinery
  that (a) excludes the shared subtree from an individual `Worker`'s `Open`/`Close` walk and (b)
  serializes concurrent workers' entry into it, since a destination's single gRPC stream requires a
  single writer.
- **Per-source DLQ naming** (`pl.ID+"-"+sourceID+"-dlq"`), since N sources can no longer share the
  single fixed `pl.ID+"-dlq"` name.
- **Status aggregation**: N workers' terminal outcomes feed one `tomb.Tomb`; the existing
  single-worker cleanup switch in `runPipeline` turns out to generalize to N workers without any
  logic changes, for a specific structural reason explained below.

## Context

`pkg/lifecycle-poc/funnel.Worker` processes a pipeline as a `TaskNode` tree: a source task, zero or
more per-connector processor tasks, then (since slice 3a) one or more destination branches. Before
this slice, `pkg/lifecycle-poc/service.go`'s `buildSourceTasks` had an explicit guard: any pipeline
with more than one source connector returned an uncoded error telling the operator to "disable the
experimental feature flag" — a remediation that stopped making sense once `Preview.PipelineArchV2`
started becoming the default path.

`Worker` was already structurally single-source: `Worker.Source` is set once, from the first task in
its own `TaskNode` tree, and `Worker.Ack`/`Worker.Nack` always call `w.Source.Ack`/DLQ methods on
_that_ field. Nothing about the worker's internals assumed a single _pipeline_ had only one worker —
the guard lived entirely in `service.go`'s pipeline-builder, not in `funnel`.

`pkg/lifecycle-poc/service.go`'s `runPipeline` ran exactly one worker per pipeline: one `w
*funnel.Worker` field on `runnablePipeline`, one `t.Go` call for it, one `t.Go` call for the cleanup
goroutine that waits on both, joined by the `registered`/`startupDone` channel pair documented at
length in `runPipeline` itself (a fast-failing worker completing between the two `t.Go` calls can
drop `tomb.alive` to 0 and panic the second `t.Go` — see that code's own comments).

`pkg/lifecycle` (v1) already supports N sources today, via `stream.FaninNode`: N `SourceNode`
goroutines each publish onto their own channel; `FaninNode` selects across all of them and republishes
onto a single channel that the shared processor/destination nodes consume from a single goroutine.
That single-consumer-goroutine property is what makes v1's shared destination safe for N sources
without any additional locking — the fan-in itself serializes everything downstream of it onto one
goroutine.

arch-v2's `funnel.Worker` has no equivalent of `FaninNode`: it does not merge streams onto a shared
channel. Each worker's `Do()` loop calls `doTask`/`doNextTask` directly, synchronously, on its own
goroutine, walking the _same_ `TaskNode` objects a sibling worker's goroutine would also walk if they
shared a destination. That is both what makes N-worker acking safe by construction (see "Cross-source
ack contamination" below) **and** the source of this slice's central hazard: without an equivalent of
v1's single-consumer serialization, two workers' goroutines really can call `Write`/`Ack` on the same
destination concurrently.

## Decision

### One worker per source, unchanged internals

`buildSourceTasks` (`service.go`) drops its guard and returns one `sourceTaskSet{sourceID, tasks}`
per source connector, instead of erroring past the first. `buildRunnablePipeline` builds one
`funnel.Worker` per `sourceTaskSet`, each with its own per-source `TaskNode` prefix (source task +
that connector's own processors) and its own DLQ (see "Per-source DLQ naming").
`runnablePipeline`'s single `w *funnel.Worker` field becomes `workers []*funnel.Worker` (with a
parallel `sourceIDs []string` for diagnostics), and a new `sink *funnel.Sink` field holds the one
shared destination-side subtree.

Nothing in `Worker` itself needed to change to make per-worker acking correct — see "Cross-source
ack contamination is impossible by construction" below.

### Shared-sink teardown ordering (the crux)

`Worker.Close` (pre-3b) tore down its own source **and its whole task chain, including the
destination** — because there was only ever one worker, that whole chain belonged to it alone. With N
workers sharing one destination, the first worker to finish (e.g. a snapshot-only source running out
of records, or an operator stopping just that connector — not yet an operator-facing feature, but the
underlying mechanism must not assume otherwise) must **not** close a destination its siblings are
still writing to. Naively generalizing "worker closes its own chain" would do exactly that.

**`funnel.Sink`** (new type, `pkg/lifecycle-poc/funnel/sink.go`) owns the shared subtree's lifecycle
independently of any one worker:

```go
type Sink struct {
    roots []*TaskNode
}

func NewSink(roots ...*TaskNode) (*Sink, error) { /* marks each root shared, validates unique IDs */ }
func (s *Sink) Open(ctx context.Context) error   { /* opens every task reachable from every root, exactly once */ }
func (s *Sink) Close(ctx context.Context) error  { /* closes every task reachable from every root, exactly once */ }
```

`buildRunnablePipeline` builds the shared subtree **once** (`buildSharedTail`: pipeline-level
processors, if any, followed by the destination branch(es) — mirrors slice 3a's M-destination
fan-out node construction verbatim), wraps it in one `Sink`, and attaches it — by pointer, not by
copy — to _every_ source's own `TaskNode` prefix via `AppendToEnd`. N different parent nodes (one per
source) end up with their own `Next` field pointing at the _identical_ shared `TaskNode` objects; that
is safe because `TaskNode.Next` is just `[]*TaskNode` and attaching a node as a child never mutates
the child itself.

The remaining problem: `Worker.Open`/`Worker.Close` walk their tree via `TaskNode.Tasks()`
(`FirstTask.Tasks()`), which recurses through every reachable `Next`. Left alone, N workers would each
try to `Open`/`Close` the _same_ shared destination — a double-`Open` is a hard error
(`connector.Destination.Open`: "another instance of the connector is already running"), and a
double-`Close` from the first worker to finish is exactly the early-teardown bug this section exists
to prevent.

**`TaskNode.MarkSharedBoundary`** breaks this without touching the runtime execution path at all:

```go
func (t *TaskNode) MarkSharedBoundary() {
    t.sharedBoundary = true
    t.sharedMu = &sync.Mutex{}
}
```

`TaskNode.iterator()` (which backs `Tasks()`/`TaskNodes()` — the _only_ thing `Worker.Open`,
`Worker.Close`, and `NewWorker`'s `validateTasks` use to walk a tree) skips descending into any child
marked `sharedBoundary`:

```go
for _, next := range t.Next {
    if next.sharedBoundary {
        continue // owned by funnel.Sink, not this Worker
    }
    if !next.iterator()(yield) {
        return false
    }
}
```

Crucially, `Worker.doTask`/`doNextTask` (the runtime execution path) walk `taskNode.Next` **directly**
— they never call `Tasks()`/`TaskNodes()`. So this change is invisible to `Do()`: a worker's batch
still flows straight through into the shared destination at runtime; only the _lifecycle_ walk
(`Open`/`Close`) stops at the boundary.

Putting it together, in `pkg/lifecycle-poc/service.go`'s `runPipeline`:

```go
// Opened once, before any worker starts.
if err := rp.sink.Open(ctx); err != nil { ... }
for _, w := range rp.workers {
    if err := w.Open(ctx); err != nil { /* roll back already-opened workers + the sink */ }
}
...
// Each worker's own goroutine:
doErr := w.Do(ctx)
closeErr := w.Close(context.Background()) // now only tears down THIS worker's own source + DLQ
...
// Cleanup goroutine, only after ALL N worker goroutines have returned:
workersWg.Wait()
sinkCloseErr := rp.sink.Close(ctx) // closed exactly once, here
```

`workersWg.Wait()` is the same barrier the pre-3b single-worker code already had (it previously
waited for the one worker); generalizing it to N workers and gating `rp.sink.Close` behind it is what
makes "closed only after every worker has exited" true by construction, not by convention. **This is
why a destination can never be closed while records from a still-running sibling are in flight**: the
sink's `Close` call is lexically _after_ `workersWg.Wait()` returns, which by definition cannot happen
until every worker's `Do`+`Close` has already returned.

### Serializing concurrent writers on the shared destination

`DestinationTask.Do` (unchanged since before this slice) writes a batch and then reads back exactly
that many acks from the destination's single gRPC stream, matching each ack to the position it just
wrote **in strict send order** (`validateAcks`). This assumes a single writer: a destination's stream
does not carry per-request correlation IDs, and gRPC's own contract requires external synchronization
for concurrent `Send` calls on one stream (concurrent `Send`+`Recv` from different goroutines is
fine; concurrent `Send`+`Send` is not). Two workers' goroutines calling `Write` on the _same_
destination task concurrently — which, absent any lock, is exactly what N independent `Worker.Do()`
loops converging on one shared `TaskNode` would do — is therefore unsafe at two layers: gRPC's
`Send`/`Send` contract, and `DestinationTask.Do`'s own ack-position-matching logic (a second worker's
ack could be consumed as if it were the first worker's).

`MarkSharedBoundary` also allocates a `sync.Mutex` (`sharedMu`), acquired in `Worker.doTask`:

```go
if taskNode.sharedBoundary {
    taskNode.sharedMu.Lock()
    defer taskNode.sharedMu.Unlock()
}
```

Held for the **entire** call — including any synchronous recursion into `doNextTask`/`doTask` for
downstream shared nodes (retries, sub-batch splitting, and, for a future N×M pipeline, a fan-out to
multiple destination branches) — because that recursion happens on the same goroutine's stack before
the deferred `Unlock` fires. One worker's whole pass through the shared tail completes before any
other worker's batch can enter it. This is intentionally the same "boring, obviously correct, a mutex
around the risky bit" philosophy `multiAckNacker` (slice 3a) already used for the M-destination axis,
not a lock-free or channel-based redesign.

Marking is per-root, not per-node-in-the-subtree: if there are shared pipeline-level processors, only
the _first_ one is marked (its own `AppendToEnd` already attaches the destination branches beneath
it, so the lock's hold-for-the-whole-recursion property covers them too). If there are no shared
processors and M>1 destination branches, each branch root is marked (and locked) independently — see
`buildSharedTail`'s doc for why that is not just safe but _preferable_ for M>1 (different
destinations can then proceed without blocking on each other; this slice's own test suite only
exercises M=1, but the mechanism does not special-case M=1).

**Consequence for throughput**: writes to the shared destination are fully serialized across sources
— N sources buy read/decode/processor parallelism up to the shared boundary, never write-side
parallelism on one destination. This is not a regression versus v1: v1's `FaninNode` already
serializes onto one destination-consuming goroutine, so arch-v2's serialized-write behavior for
N-source pipelines matches v1's, it does not introduce a new bottleneck v1 didn't already have.

### Shared destination poisoning on error (H2, fixed)

The lock above is correct for the happy path, but adversarial review of #2734 found a gap on the
**error** path: `doTask`'s `defer sharedMu.Unlock()` fires unconditionally, including when
`doTaskAttempt` returns an error. `DestinationTask.Do` (`funnel/destination.go`) writes a batch, then
reads back exactly that many acks off the destination's single gRPC stream in a loop; if one of those
`Ack()` calls itself errors, `Do` returns immediately, leaving whatever acks were still queued **behind**
that failed call **unread on the stream**. The worker unwinds — `sharedMu` is released by the deferred
`Unlock` — and only afterwards does the worker's own goroutine (in `runPipeline`) run `w.Close`
(source teardown, a multi-second window) and finally `rp.t.Kill(err)`.

That gap is exploitable: a **sibling** worker blocked contending for `sharedMu` can acquire it the
instant it's released — before `rp.t.Kill`'s context cancellation has any chance to reach it — enter
`DestinationTask.Do`, and have its **own** first `Ack()` call return the **previous** worker's leftover,
unread ack. `validateAcks` compares the returned ack's position **byte-for-byte** against the sibling's
own expected positions. Positions are connector-defined and unique only **within** a source — two
sources emitting byte-identical position bytes is a routine N-source shape (two file sources, two
offset-based connectors, two of anything sharing a positioning scheme) — so if the bytes happen to
collide, the comparison **passes**, and the sibling silently acks a record upstream that its own
destination write never durably confirmed. Invariant 1 violated.

**Fix: poison the shared boundary, inside the critical section, before the lock is released.**
`TaskNode` gains a `poisoned atomic.Bool`, checked and set entirely within `Worker.doTask`:

```go
if taskNode.sharedBoundary {
    taskNode.sharedMu.Lock()
    defer taskNode.sharedMu.Unlock()
    if taskNode.poisoned.Load() {
        return /* CodeSharedDestinationPoisoned */
    }
}
err := w.doTaskAttempt(ctx, taskNode, b, acker, nil)
if err != nil && taskNode.sharedBoundary {
    taskNode.poisoned.Store(true) // BEFORE the deferred Unlock above runs
}
return err
```

Why this closes the race that relying on `rp.t.Kill`'s context cancellation alone would not: the
`Store` happens strictly before `sharedMu.Unlock()` (the store is synchronous code executing before the
function returns; the `Unlock` is a deferred call that only runs once the function actually returns),
and the next goroutine's `Load` happens strictly after **its own** `sharedMu.Lock()` succeeds. The
mutex's own release/acquire pair is what makes the poison flag visible-before-entry — a plain Go memory
model guarantee, with **no dependency on how fast `rp.t.Kill`'s context cancellation propagates**. That
timing dependency is exactly what the original bug exploited: a sibling already contending for
`sharedMu` has no way to "see" a `Kill` that hasn't happened yet.

The poison is intentionally **broad** (any error escaping the shared subtree, not narrowly scoped to
"an `Ack()` error specifically") — the safe default when it's not cheap to prove a given error class
never desynchronizes the stream. It is also intentionally **never cleared**: the only safe recovery is
a full pipeline restart, which builds a brand-new `TaskNode` (and therefore a fresh, unpoisoned flag)
via `buildRunnablePipeline` — poisoning does not need special-case handling in the recovery path,
because recovery already always builds a fresh task graph from scratch. A poisoned pipeline follows
the ordinary fatal/transient classification in `runPipeline`'s cleanup goroutine: `Degraded` if the
error was fatal-tagged, or an ordinary recovery attempt otherwise (recovery, again, produces a
completely fresh — unpoisoned — task graph, so it is a legitimate way to recover from this).

Regression test: `pkg/lifecycle-poc/funnel/worker_h2_poison_test.go`,
`TestNSource_H2_AckStreamErrorPoisonsSharedDestination` — two records from source A, the second
carrying the identical position bytes as source B's only record (the exact collision shape above); a
scripted destination double fails A's second `Ack()` call, leaving that ack queued; the test asserts B
is refused entry (never even calls `Write`) and never acks anything. Fails without the fix (B's write
succeeds and acks A's leftover ack), passes with it.

### Cross-source ack contamination is impossible by construction

Every `Worker`'s `Ack`/`Nack` methods call `w.Source.Ack` / `w.DLQ.*` — fields set once, at
construction, from _that worker's own_ source and DLQ. Nothing about sharing the destination TaskNode
changes this: `Worker.doTask`/`doNextTask` are methods on `*Worker`, and the `acker` parameter
threaded through every recursive call defaults to `w` itself (or a `multiAckNacker` wrapping `w`, for
the M-destination axis). Even though N workers' goroutines execute `doTask` over the _identical_
shared `TaskNode` objects (by pointer), each invocation runs as a method call bound to whichever
worker's own goroutine is currently making it — so when that call eventually reaches an `Ack`, it is
_always_ `w.Ack` for the calling worker's own `w`, never a sibling's.

Put differently: sharing the destination's _data structure_ (the `TaskNode`) does not share the
destination's _ack routing_, because routing is a property of the call stack (which worker's `Do()`
is currently executing), not of the `TaskNode` graph. This is why no new synchronization or tagging
was needed to prevent cross-source ack contamination — the property was already true before this
slice, and remains true because nothing here changes how `doTask` threads its `acker` argument. The
one thing that _would_ break it is a shared component that stored its own reference to a _single_
acker and called it directly, bypassing the calling worker's own `acker` parameter — the design here
introduces no such component (`funnel.Sink` owns only `Open`/`Close`, never `Ack`/`Nack`).

Enforcement sites (see the code comments, not just this doc):

- `funnel.Worker.doTask`'s `sharedMu` lock acquisition (worker.go) — invariant 1 comment on
  serialization.
- `runnablePipeline.workers`' field doc (service.go) — invariant statement on ack isolation.
- `TestNSource_NoCrossContamination_SourceStaysFixedWhileSiblingRuns`
  (`pkg/lifecycle-poc/funnel/worker_nsource_test.go`) — the direct behavioral proof: source A is
  stopped while source B streams through the identical shared destination; A's acked-position set is
  asserted to never move.

### A source finishing gracefully is not a failure

Before this slice, `Worker.Do()`'s loop (`for !w.stop.Load() { ... }`) only ever exited when
something _external_ set `w.stop` (an operator's `Stop` call) or when `doTask` returned a genuine
error. There was no way for a source to signal "I have permanently run out of records" (e.g. a
snapshot-only connector) without that looking like an error.

`Worker.doTask` gains one new branch, alongside the existing context-cancellation/`ErrPluginNotRunning`
graceful-stop handling:

```go
if taskNode.IsFirst() && cerrors.Is(err, io.EOF) && !w.stop.Load() {
    w.stop.Store(true)              // arm exactly like an external Stop would
    if tdErr := w.tearDownSource(ctx); tdErr != nil { return ... }
    return nil                      // Do()'s loop condition now exits cleanly
}
```

`io.EOF` is the same sentinel this package already treats specially elsewhere (`isClosedSourceStream`,
for acks against a closed stream) — reusing it rather than inventing a second sentinel keeps the
"stream has ended" signal consistent across the package. The branch is guarded on `!w.stop.Load()`
so it can never fire once an external `Stop` has already armed the flag (that path stays on the
existing `ErrPluginNotRunning` branch above it).

Because this worker's own goroutine (in `runPipeline`) only calls `rp.t.Kill` when `Do`+`Close`
return a **non-nil** error, a worker that exits this way returns `nil` and never kills the tomb — every
sibling worker's `ctx` stays alive, and the pipeline's status is untouched until `workersWg.Wait()`
unblocks (i.e. every worker has exited). A pipeline where source A finishes and source B keeps
streaming therefore simply **stays `Running`** for as long as B is alive; it reaches a terminal state
only once every worker has returned — never "some finished, some running" being mistaken for
terminal. Covered at the Service level (not just the funnel level — see "Coverage hole" below) by
`TestServiceLifecycle_NSource_OneSourceFinishesGracefully_StaysRunningUntilAllExit`.

**M3 (adversarial review of #2734 — honesty about this branch's real-world reach):** this branch
logs at Info (`source_id`, a message distinct from an externally-requested Stop) so an operator can
tell "this source decided on its own that it was done" apart from "something asked it to stop" —
before the fix it logged nothing, indistinguishable from a graceful operator stop. More importantly:
**as of this writing, no in-tree connector or the connector-sdk's `Run` loop actually returns `io.EOF`
this way** — the SDK only ends `Run` on ctx-cancellation or the plugin stream closing, neither of
which is "the connector decided it's done." This branch exists defensively, for a future connector
shape (e.g. a genuinely snapshot-only, non-streaming source) that might signal exhaustion this way; it
is not, today, an exercised production path. Do not read its presence as proof any real connector
uses it — see `Worker.doTaskAttempt`'s own comment for the same caveat at the enforcement site.

### Status aggregation

The cleanup goroutine's switch in `runPipeline` (fatal → `Degraded`; graceful → `UserStopped`/
`SystemStopped`; transient → recovery; etc.) is **unchanged** from the pre-3b, single-worker version.
That is deliberate, not an oversight — it generalizes to N workers for free, for a structural reason:

- Every worker's goroutine feeds the **same** `rp.t` (`tomb.Tomb`). tomb.v2 records only the _first_
  error ever passed to `Kill`; every later call is a no-op. A worker only calls `rp.t.Kill` when it
  has an actual (non-nil) error — a graceful exit (Stop, or the new io.EOF case) never does.
- So `rp.t.Err()`, read once by the cleanup goroutine after `workersWg.Wait()`, always reflects
  exactly one thing: **"the first reason any one source brought the whole pipeline down"** — or
  `tomb.ErrStillAlive` if nothing ever did.

This collapses the {all graceful, some graceful + some fatal, all fatal, some finished} space the
task described down to the same three-way split the pre-3b switch already handled:

- **All workers exit gracefully** (Stop, or self-exhausted/EOF) → `rp.t.Err()` is `tomb.ErrStillAlive`
  → `StatusUserStopped` / `StatusSystemStopped` (the existing branch).
- **Any one worker's error is fatal** → `rp.t.Err()` is that fatal error → `StatusDegraded`,
  degrading the **whole** pipeline, matching v1.
- **Any one worker's error is non-fatal (transient)** → `rp.t.Err()` is that transient error →
  bounded-backoff recovery (`recoverPipeline`), rebuilding **every** source's worker and the shared
  sink from scratch.
- **The shared sink itself fails to close** (`sinkCloseErr`) → folded into `err` exactly like a
  worker's own error (see below) → the same fatal/transient classification as above, not a fifth
  case.

A fatal error in any one source degrading the **whole** pipeline (not just that source) matches v1:
the tomb-wide `Kill` cancels `ctx` for every sibling worker, so they all wind down together (as
`context.Canceled`, handled by the existing graceful-stop branch in `doTask` — this is "collateral",
not a second failure being reported). A **non-fatal** error does the same tomb-wide `Kill`, but is
classified into the recovery path instead — recovery here is pipeline-wide (it rebuilds every
source's worker and the shared sink via a fresh `buildRunnablePipeline` call), which is exactly why a
transient error in one source legitimately winds down every sibling first: a partially-running
pipeline against a destination that recovery is about to tear down and rebuild would be a silent-
partial-delivery trap, not a resilience feature.

`sinkCloseErr` (the shared sink's own `Close` failing) is computed once, right after
`workersWg.Wait()`, and folded into `err` via the same fatal/transient rules — if every worker
otherwise exited cleanly (`err == tomb.ErrStillAlive`) but the sink failed to close, that failure is
promoted into `err` so it goes through the same classification instead of being silently swallowed as
"graceful stop"; if the pipeline is already terminal for some other reason, the sink-close failure is
logged but does not override that classification.

This mapping — and _why_ the switch itself needed no branch-count changes — is documented at the
`runPipeline` function doc comment (which also carries the `//nolint:gocyclo` this function already
needed pre-3b, per `doTask`'s identical existing precedent in `funnel/worker.go`).

### Partial graceful stop escalation (H1, fixed)

`stopRunnablePipeline`'s graceful-stop branch (`Service.Stop`/`StopAndWait`) calls every worker's
`Stop(ctx)` with a **single, deadline-bound** `ctx` (the O2 bound from `StopAndWait`'s doc). `Worker.Stop`
fails only when `acquireProcessingLock(ctx)` loses the race to that deadline — and it fails **before**
arming `w.stop` (see `Worker.Stopping`'s doc). Pre-3b, with exactly one worker, this could only ever
produce one of two outcomes: the single worker armed, or it didn't — never a mixture.

With N workers, the initial implementation still called `Stop` **sequentially**, in a loop. Because
`ctx` carries an **absolute** deadline (not a per-call budget), a sequential loop does not give every
worker an equal window to arm: an earlier worker's `Stop` call could block for most or all of the
deadline (e.g. contending for its own `processingLock`, itself held by its own `Do` goroutine waiting
on a **sibling's** slow shared-destination write via `sharedMu`), leaving a later worker's `Stop` call
almost no time at all — so which workers armed depended on loop position, not on how long each
genuinely needed. Adversarial review of #2734 (**H1**) found the resulting failure mode: some
source(s) armed and tore down their connector, other(s) did not — and NOTHING in the pre-fix code
detected or handled that mixture. `workersWg` never drains (the unarmed worker's `Do` loop keeps
running with no signal that anything is wrong), so `runPipeline`'s cleanup goroutine — the only thing
that ever writes a terminal status — never runs. The pipeline is left reporting `StatusRunning`
indefinitely, with the only visible signal an error return to a caller that may already have
disconnected (e.g. an HTTP client that gave up on a slow `StopAndWait` call).

**Fix, two parts:**

1. **Dispatch every worker's `Stop` call concurrently** (a goroutine + `sync.WaitGroup` per worker,
   collecting each one's error and `Stopping()` result), instead of sequentially. This gives every
   worker the same wall-clock window against the shared deadline, closing the _spurious_ half of
   partial arming — the part that was purely an artifact of loop position, not a genuine timing
   conflict.
2. **Detect and escalate a genuine partial result.** Concurrent dispatch does not eliminate partial
   arming outright — two sources can legitimately need different amounts of time to reach a safe stop
   point within one bounded deadline. After `wg.Wait()`, `stopRunnablePipeline` partitions workers into
   armed/unarmed sets:
   - **None armed** → unchanged from pre-3b: roll back `intentionalStop`, pipeline is genuinely
     untouched, safe to retry.
   - **Some armed, some didn't** → **escalate**: `rp.t.Kill(cerrors.FatalError(...))` with a coded
     `CodePartialGracefulStopEscalated` error naming which source(s) armed and which didn't. Killing
     the tomb cancels the `ctx` threaded into every worker's `Do` call (`runPipeline`'s `ctx :=
     rp.t.Context(nil)`), so every still-unarmed worker's next context check (a blocked `Read`, or a
     wait on `sharedMu`/`processingLock`) observes cancellation and unwinds via the existing
     `context.Canceled` path in `Worker.doTaskAttempt` — guaranteeing `workersWg` DOES drain and the
     cleanup goroutine DOES run, landing the pipeline in `StatusDegraded` instead of a silent,
     invisible half-stopped state.
   - **All armed** → unchanged: full success, per-worker `Stop` errors (if any — a source can still
     fail its own teardown after arming) are returned but need no rollback or escalation.

This trades "graceful" for "terminates deterministically" — the only safe choice once some sources are
already torn down. At-least-once is preserved: the escalation never acks anything, so an interrupted
worker's in-flight, unacked batch simply replays on the pipeline's next start (invariant 3). A
genuinely wedged I/O call that ignores `ctx` cancellation entirely (not merely slow) is a separate,
**pre-existing** limitation this does not introduce or claim to fix — it applied identically to the
single-worker, pre-3b case (`DefaultStopAndWaitTimeout`'s doc already documents it): even there, a
truly-wedged `Write` that never returns and never respects `ctx` leaves that worker's `Do` goroutine
running forever regardless of how many times `Stop` is retried.

**Compounding fix:** `stopAndWaitTimeoutErr`'s `"stop"`-phase message ("the pipeline never began
stopping … still running, exactly as before this call — safe to retry") was written for the
single-worker invariant and is **false** for the escalated N>1 case — the pipeline is NOT untouched,
it is actively winding down toward `Degraded`. `StopAndWait` now checks for
`CodePartialGracefulStopEscalated` before the generic `context.DeadlineExceeded` check and routes it
to a distinct `"stop-escalated"` phase with accurate wording.

Regression test: `pkg/lifecycle-poc/service_test.go`,
`TestServiceLifecycle_NSource_PartialGracefulStop_Escalates` — two sources sharing one destination,
source A's write held in flight (so its `Stop` cannot acquire its own `processingLock` within a short
deadline) while source B is idle (so its `Stop` arms immediately); asserts `Stop` returns the coded
escalation error naming both sources, and that the pipeline reaches `StatusDegraded` (not stuck)
once A's write is released. Fails without the fix — reverting the escalation logic back to the
original sequential loop reproduces the exact "stuck" shape: the assertion for the coded error fails,
and (since nothing ever tears the pipeline down) the test process does not exit cleanly within its
timeout, matching the review's own description of the hazard.

### Coverage hole that hid H1

Adversarial review also found the gap that let H1 ship undetected: **there was no Service-level test
that ever drove `runPipeline`'s N-worker choreography at all.** `service_test.go` only asserted
`buildRunnablePipeline`'s wiring (task graph shape), the funnel-level tests build `Worker`/`Sink` by
hand (bypassing `lifecycle-poc.Service` entirely), and `tests/chaos/nsource_child.go` imports only
`pkg/lifecycle-poc/funnel`, never `lifecycle-poc.Service`. So `runPipeline`'s N-worker startup,
its open-rollback path, and **all** of `stopRunnablePipeline` had zero execution coverage through the
actual production entry point. Closed by four new Service-level tests in `service_test.go`:
`TestServiceLifecycle_NSource_PartialGracefulStop_Escalates` (H1, above),
`TestServiceLifecycle_NSource_FatalErrorOneSource_DegradesWholePipeline`,
`TestServiceLifecycle_NSource_TransientErrorOneSource_Recovers` (proves recovery rebuilds every
source's worker, not just the failing one, across an actual restart), and
`TestServiceLifecycle_NSource_OneSourceFinishesGracefully_StaysRunningUntilAllExit`.

### Per-source DLQ naming

`buildDLQ` named the DLQ connector `pl.ID+"-dlq"` — with N sources, N `buildDLQ` calls would try to
create N connectors with the _identical_ ID. It initially became `pl.ID+"-"+sourceID+"-dlq"`; **L1
(adversarial review of #2734, fixed)** found that format double-embedded the pipeline ID: a
provisioned connector ID is already `pipelineID+":"+name` (`pkg/provisioning/config/enrich.go`'s
`enrichConnectors`), so `sourceID` itself already contains `pl.ID` once — the old naming pushed a
long-but-previously-valid pipeline ID over `connector.IDLengthLimit` (256,
`pkg/connector/service.go`), refusing to start a pipeline whose own ID was never too long on its own.
It now uses `buildDLQName(pl.ID, sourceID)`, which keys on a short (16 hex char), deterministic SHA-256
hash of `sourceID` instead of its full text — bounded length regardless of source-name length, stable
across restarts (same `sourceID` always hashes the same, so a recovery restart addresses the SAME DLQ
connector), and collision-free in practice (64 bits of hash across the typically-small N sources in
one pipeline).

No stored-state migration is needed: the DLQ connector is created with `connector.ProvisionTypeDLQ`,
which `connector.Destination.Open`/`Teardown` check to _skip_ `persister.ConnectorStarted`/
`ConnectorStopped` — the DLQ connector (and therefore its ID) is never written to the connector
store. Nothing durable ever referenced any previous naming scheme across a restart; it existed only for
the lifetime of a single running pipeline instance. Grepped the rest of the codebase
(provisioning, the HTTP/gRPC API, the UI) for any reference to the DLQ's _ID_ specifically (as opposed
to its `pl.DLQ.Plugin`/`Settings` config, which is unaffected) and found none — the DLQ's ID has never
been a public/documented contract.

### Per-source DLQ window semantics (M1, documented not redesigned)

Every source's `funnel.DLQ` owns its own `windowSize`/`windowNackThreshold` window (see `funnel/dlq.go`'s
`dlqWindow`) — pre-3b, with exactly one source, "halt the pipeline after 5 nacks" and "halt after 5
nacks from THIS source" were the identical statement. With N sources, each source's window is
**independent**: a pipeline configured with `windowNackThreshold: 5` now tolerates up to 5 nacks **PER
SOURCE** (5×N pipeline-wide in the worst case) before any one of them halts the pipeline — not 5 total.

This is a deliberate choice, not an oversight: keeping the DLQ genuinely per-source (matching every
other per-source DLQ property — naming, the destination connector instance, the ack window) was
preferred over introducing a NEW piece of shared mutable state across N worker goroutines (a shared
`*dlqWindow` behind its own mutex) purely to preserve the old pipeline-wide count, absent a concrete
operator need for that specific semantic. That is a real, buildable option if the need materializes —
it is not attempted here per the "no speculative generality" engineering guideline. **Operators running
N-source pipelines who need a pipeline-wide bound today should divide their desired total by the
number of sources when setting `windowNackThreshold`.**

### Shared DLQ destination settings across sources (M2, documented not redesigned)

N sources' DLQs all share `pl.DLQ.Settings` — the same target, same credentials, same everything
except the connector ID and window (see "Per-source DLQ naming" above). This is harmless for a
naturally-concurrent-safe target (`builtin:log`, most message-queue/object-store DLQs), but a target
that is **not** safe for concurrent writers from independent connector instances — a local file DLQ
being the sharpest example — will see either interleaved/corrupted writes, or an `Open` failure the
moment a second source's DLQ instance tries to start against an already-held exclusive resource. There
is no per-source `Settings` override today. **Operators running N-source pipelines should pick a DLQ
plugin known to tolerate concurrent, independent instances writing to the same target, or avoid
file-based (or otherwise exclusive-resource) DLQs until a per-source override exists.**

### N×M is not blocked, but is untested

The serialization mechanism above (`MarkSharedBoundary`'s per-root lock) happens to make an N-source,
M-destination pipeline structurally safe already: `buildSharedTail` returns one lock per shared root
regardless of how many destination branches hang off it, and nothing in `Worker`/`Sink` assumes M=1.
This was not deliberately engineered as an N×M feature — it falls out of the same mechanism this slice
needed for N×1. Per the epic's scope split, N×M is explicitly **slice 3c**: it has not been
benchmarked (benchi parity is 3c's job), the multi-source guard's error-message/docs sweep for the
combined case hasn't been done, and this slice's test suite only exercises N×1. Operators should not
be told N×M works until 3c says so.

## Consequences

- N-source pipelines are now supported by `Preview.PipelineArchV2`; the guard's uncoded error and
  stale "disable the flag" remediation are gone.
- Every source has its own DLQ; DLQ misattribution across sources is impossible (they are entirely
  separate connectors, never sharing state).
- Write throughput to a single shared destination is serialized across sources — matching v1, not a
  new bottleneck, but also not a new _win_: N sources do not multiply destination write throughput.
  That would require the N×M axis (multiple destinations) or a future destination-side batching
  redesign, neither of which this slice attempts.
- `funnel.Worker.Open`/`Close`'s behavior is now conditional on tree shape (they stop at
  `sharedBoundary` nodes) — existing single-source, single-destination pipelines are unaffected
  in behavior (the shared tail there is exactly the M=1 destination branch, still opened/closed
  correctly, just now via `Sink` instead of inline in `Worker`), but a future reader of `Worker.Open`
  needs to know this boundary concept exists. Documented at the `sharedBoundary` field and
  `MarkSharedBoundary`.
- `runnablePipeline.w` (single field) is gone; any future code reaching into `runnablePipeline`
  directly (none exists outside `service.go`/`service_test.go` today) must use `.workers`/`.sourceIDs`.

## Failure modes

- **Fatal error in one source.** Tomb-wide `Kill` cancels every sibling's `ctx`; all workers wind
  down (collaterally, as `context.Canceled`, not a second reported failure); cleanup goroutine sees
  the fatal error via `rp.t.Err()`; pipeline → `Degraded`. Covered by
  `TestServiceLifecycle_PipelineError` (pre-existing, single-source) AND — closing what was
  previously a disclosed gap (adversarial review of #2734) — by
  `TestServiceLifecycle_NSource_FatalErrorOneSource_DegradesWholePipeline`, which drives this through
  the real `lifecycle-poc.Service` entry point with two sources sharing one destination.
- **Non-fatal (transient) error in one source.** Same tomb-wide `Kill`, but classified into
  `recoverPipeline`/`StartWithBackoff`, which rebuilds every source's worker and the shared sink from
  scratch. A sibling source's in-flight batch at the moment of the kill is thrown away _unacked_ (see
  `doTask`'s existing "stop signal received just before starting to process next batch" handling,
  unchanged) — a benign duplicate on the rebuilt pipeline's next read, never a gap. Covered at the
  Service level by `TestServiceLifecycle_NSource_TransientErrorOneSource_Recovers`, which also proves
  the idle sibling source is correctly re-dispensed on the recovery restart (not just the failing one).
- **A source exhausts its records (io.EOF) while a sibling streams.** That worker's `Do()` returns
  `nil`; its own `Close` tears down only its own source + DLQ; the shared sink is untouched; the
  pipeline stays `Running`. Terminal only once every worker has exited. Covered at the funnel level by
  `TestNSource_SourceFinishesGracefully_DoesNotTouchSharedSink` and at the Service level by
  `TestServiceLifecycle_NSource_OneSourceFinishesGracefully_StaysRunningUntilAllExit`. See M3 above:
  this exhaustion signal is not currently exercised by any real in-tree connector.
- **Two workers' batches would otherwise race on the shared destination's Write/Ack pairing.**
  Prevented by `sharedMu`; covered deterministically (not just probabilistically via `-race`) by
  `TestNSource_ConcurrentWorkers_SerializeSharedDestinationWrites`'s `exclusiveDestination`, which
  fails the test immediately if two goroutines ever hold the shared destination concurrently.
- **An error inside the shared subtree could desynchronize the shared destination's ack stream for a
  SIBLING worker (H2, fixed).** See "Shared destination poisoning on error" above — the shared
  boundary is poisoned before its lock is released, so a sibling is refused entry rather than risking
  a leftover ack read. Covered by `TestNSource_H2_AckStreamErrorPoisonsSharedDestination`.
- **Process crash (SIGKILL) mid-run, with sources at different progress.** Each source's own
  connector position is independently, durably persisted (unrelated sources, unrelated persisters in
  production — separate connector instances). A resumed run continues each source from its own
  position; the shared destination's ledger accumulates the union of both runs' contributions, with
  duplicates allowed and gaps forbidden. Covered end-to-end by
  `TestSIGKILL_NSource_FastAndSlow_GaplessIndependentResume` (`tests/chaos/nsource_sigkill_test.go`),
  `-race -count=10` clean.
- **Operator stops the pipeline while some workers have armed and others haven't (H1, fixed).**
  `stopRunnablePipeline`'s generalization keeps `intentionalStop` true as long as _any_ worker armed
  (`Stopping()==true`); only rolls it back if literally none did. A worker that armed and later
  surfaces a transient drain error is still classified `UserStopped`, not misread as a spontaneous
  failure needing recovery — this generalizes the single-worker rollback condition ("nothing began
  stopping") to "no worker began stopping". If SOME but not all workers armed, the pipeline is no
  longer left stuck: see "Partial graceful stop escalation" above — it force-kills the tomb and lands
  on `Degraded` instead. Covered by `TestServiceLifecycle_NSource_PartialGracefulStop_Escalates`.
- **The `registered`/`startupDone` double-goroutine-registration hazard, times N.** A fast-failing
  worker completing between two `t.Go` calls could previously (single-worker) drop `tomb.alive` to 0
  before the cleanup goroutine registered, panicking the second `t.Go`. With N workers this is _more_
  likely (any of N, not just one), not less — `registered` now gates all N worker goroutines plus the
  cleanup goroutine (N+1 total `t.Go` calls) before any of them can proceed, closed only after every
  one of the N+1 calls has been made.
- **L2 (documented, not changed): all sources exhaust gracefully (io.EOF) with no operator Stop
  involved.** The cleanup goroutine's switch cannot distinguish "every worker armed via an external
  Stop" from "every worker armed itself via io.EOF" — both look identical at `rp.t.Err() ==
  tomb.ErrStillAlive`, and the resulting status is whichever the `isGracefulShutdown` flag selects
  between (`StatusSystemStopped` on a Conduit-wide shutdown, `StatusUserStopped` otherwise) — the
  latter even though no user actually asked this pipeline to stop. This is a pre-existing decision
  (unchanged by this review round) rather than a bug: the alternative would require a new terminal
  status this slice does not introduce. Documented here so a future reader does not mistake
  `StatusUserStopped` for proof an operator issued a `Stop` call.

## Upgrade / rollback

No serialized-state or wire-format changes. `runnablePipeline` is an in-memory-only struct (not
persisted); the only persisted artifact this slice touches is the DLQ connector's _ID_ naming scheme,
which — per "Per-source DLQ naming" above — is never persisted in the first place (`ProvisionTypeDLQ`
connectors are skipped by the persister). A pipeline running N=1 source behaves identically to before
this slice (one worker, one DLQ named `pl.ID+"-"+sourceID+"-dlq"` instead of `pl.ID+"-dlq"` — a
cosmetic rename of a non-persisted, non-public ID). Rollback is simply reverting this change; no data
migration is implicated either direction.

## Observability

- Every worker's stop/error log line now carries the `source_id` (`log.ConnectorIDField`) it belongs
  to, so an operator reading logs for a multi-source pipeline can attribute a given failure/graceful-
  stop line to the right connector.
- `buildRunnablePipeline`'s task-graph debug log line is now emitted per source (tagged with
  `source_id`), rather than once for the whole pipeline.
- A shared-sink close failure is logged even when it doesn't change the pipeline's terminal
  classification (see the `sinkCloseErr` handling in "Status aggregation"), so a destination teardown
  problem is never silently invisible just because some other reason already decided the pipeline's
  fate.

## Related

- `20260731-archv2-multiconnector.md` — slice 3a (M-destination fan-out), the sibling design this one
  mirrors on the source axis, and whose `multiAckNacker`/pool-of-branch-goroutines pattern this slice
  deliberately does _not_ need (fan-in has no per-record divergence to reconcile — the ack routing is
  already unambiguous per worker; see "Cross-source ack contamination" above).
- `../architecture-decision-records/20260731-archv2-fanout-ack-model.md` — the ADR behind slice 3a's
  ack model; this slice introduces no new ADR-worthy decision of its own (the "one mutex, boring and
  obviously correct" choice is documented here and at the `sharedMu` acquisition site, not elevated to
  an ADR, since it is a direct application of the same philosophy that ADR already established).
- `pkg/lifecycle/stream/fanin.go` — v1's N-source reference (`FaninNode`), whose single-consumer-
  goroutine property this slice's `sharedMu` lock reproduces via mutual exclusion instead of a merged
  channel.
- `pkg/lifecycle-poc/funnel/sink.go` — `Sink`'s doc comment, which restates this design doc's
  teardown-ordering rationale at the enforcement site.
- `pkg/lifecycle-poc/funnel/worker_nsource_test.go`,
  `pkg/lifecycle-poc/service_test.go` (`TestServiceLifecycle_buildRunnablePipeline_MultipleSources`),
  `tests/chaos/nsource_child.go` / `nsource_sigkill_test.go` — the tests this design doc's claims are
  backed by.
- `pkg/lifecycle-poc/funnel/worker_h2_poison_test.go`
  (`TestNSource_H2_AckStreamErrorPoisonsSharedDestination`) and
  `pkg/lifecycle-poc/service_test.go`'s four `TestServiceLifecycle_NSource_*` tests — added by
  adversarial review of #2734 (H1, H2, and the Service-level coverage hole that hid H1); see
  "Partial graceful stop escalation", "Coverage hole that hid H1", and "Shared destination poisoning
  on error" above.
