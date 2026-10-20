# Life of a record

A record travels from a source connector's plugin to every destination connector's plugin. This page
follows one record through that journey, naming the types and functions that move it and the points
where the data-integrity invariants (the `// Invariant N:` comments in the code) are enforced. It
ends with what happens when each stage fails: a processor error, a destination failure, and a crash.

Two engines move the record, and they differ in how much they move at once and when they let go of
it. Everything the engines share sits below them and is described first.

| | v1 (default) | v2 (arch-v2, `--preview.pipeline-arch-v2`) |
| --- | --- | --- |
| Package | `pkg/lifecycle`, `pkg/lifecycle/stream` | `pkg/lifecycle-poc`, `pkg/lifecycle-poc/funnel` |
| Unit of work | one message per record, one goroutine per node, channels between nodes | one batch per pass, one worker per source, tasks called in sequence |
| Reads while a batch is in flight | yes, until the plugin boundary pushes back | no: the next read waits for every ack of the previous batch |
| Ack to the source | per record, in order, through a semaphore | per position, released as an in-order prefix |
| Destination write | one record per `Write` | one batch per `Write` |

A single engine is planned to replace both
(ADR [`20261009-single-pipeline-engine`](../architecture-decision-records/20261009-single-pipeline-engine.md),
**proposed, held**); its design is [`20261009-staged-batch-engine`](../design-documents/20261009-staged-batch-engine.md).
The default flip and the deletion of v1 are v0.22 work.

## The shared connector layer

Both engines talk to `connector.Source` and `connector.Destination` (`pkg/connector`), not to the
plugin. The wrapper owns the two rules that make acknowledgment safe, and it is where positions are
persisted.

```mermaid
flowchart LR
    SP[source plugin] -->|Read| CS[connector.Source]
    CS --> E{{engine}}
    E -->|Write| CD[connector.Destination]
    CD --> DP[destination plugin]
    DP -->|ack / nack| CD
    CD -->|Ack| E
    E -->|Ack positions| CS
    CS -->|persist position| P[(persister)]
    P -->|flushed| CS
    CS -->|plugin ack| SP
```

**Source read.** `connector.Source.Read` (`pkg/connector/source.go`) receives a batch over the plugin
run stream (`pconnector.SourceRunStreamClient.Recv`), sanitizes each record (`sanitizeRecord`: fills in
nil key/payload and sets `opencdc.MetadataReadAt` / `MetadataConduitSourceConnectorID`), and hands the
batch to the engine.

**The plugin boundary.** Built-in connectors run in-process (`pkg/plugin/connector/builtin`) and pass
`opencdc.Record` values through; standalone connectors run over gRPC
(`pkg/plugin/connector/standalone`). Since v0.21 the structured values in a record cross a standalone
boundary exactly, via protocol v3 (`conduit-connector-protocol` v3 carrying the typed `opencdc.v2`
record, negotiated per peer): `int64`, `uint64`, `[]byte` and decimals keep their type and value, with
no 2^53 rounding, no `[]byte`→base64, no NaN/±Inf→string. A peer that negotiates only protocol v2
keeps `opencdc.v1`, where an integer outside ±(2^53−1) is refused loudly rather than rounded
(see [Guarantees](guarantees.md#guarantee-6--the-plugin-boundary-never-silently-mangles-values)).

**Destination write and ack.** `connector.Destination.Write` (`pkg/connector/destination.go`) sends the
records over the plugin stream; `connector.Destination.Ack` receives the plugin's acknowledgments in
write order. A destination ack is the moment a record counts as durably handled downstream.

**The checkpoint (ack back to the source).** `connector.Source.Ack` records the new position and hands
it to the persister (`pkg/connector/persister.go`), which batches writes and debounces them at 1 s or
10,000 positions. The plugin is told about the ack **only after** that write has flushed durably
(`Source.onPersistFlushed`, then the per-source delivery goroutine `deliverDeferredAcks`), so a plugin
that frees upstream data on ack (a Postgres slot advancing its LSN) can never get ahead of Conduit's
own checkpoint. This is invariant 1 at the source end, and the persist-then-rename/transaction write is
invariant 5. See
[`20260723-source-ack-persist-ordering-fix`](../design-documents/20260723-source-ack-persist-ordering-fix.md).

## v1: a graph of nodes, one message per record

`Service.buildNodes` (`pkg/lifecycle/service.go`) builds one graph per pipeline out of
`pkg/lifecycle/stream` nodes. Nodes are goroutines connected by channels; each record is a
`stream.Message` that carries its own ack and nack handlers, so the ack travels backwards through the
same objects that carried the record forwards.

```mermaid
flowchart LR
    subgraph per source
        SN[SourceNode] --> SA[SourceAckerNode] --> SM[metrics] --> SPR[source processors]
    end
    SPR --> FI[FaninNode]
    FI --> PPR[pipeline processors]
    PPR --> FO[FanoutNode]
    subgraph per destination
        DPR[destination processors] --> DM[metrics] --> DN[DestinationNode] --> DA[DestinationAckerNode]
    end
    FO --> DPR
    SA -. nack .-> DLQ[DLQHandlerNode]
```

Forward path:

1. `SourceNode` calls `Source.Read`, wraps each returned record in a `Message` and sends it downstream.
   It keeps reading while the channels accept messages; nothing in the engine limits how many are open
   at once (see [Guarantees](guarantees.md#known-gaps)).
2. `SourceAckerNode` registers the ack and nack handlers on the message and an entry in an ordering
   semaphore, then forwards it.
3. Processor nodes transform, filter or split. `workers > 1` wraps a processor in a `ParallelNode` that
   reassembles results in dispatch order.
4. `FaninNode` merges the sources; `FanoutNode` sends a copy per destination and counts the votes.
5. `DestinationNode` writes **one record per `Write`** and passes the message to
   `DestinationAckerNode`, which runs `Destination.Ack` on its own goroutine and matches each ack to the
   next message in its queue.

Backward path:

- A destination ack acks the message. `FanoutNode` waits for every destination; the first nack wins.
- When the message is acked, the `SourceAckerNode` handler takes its turn in the semaphore (so acks reach
  the source in read order — invariant 4), calls `Source.Ack`, and reports the ack to the DLQ window.
- When it is nacked, the handler asks the `DLQHandlerNode`. If the nack window still has room, the record is
  written to the DLQ destination and then acked to the source. If not, the handler returns an error and
  the pipeline stops; a non-fatal error sends the run to recovery.

Stop: `SourceNode.Stop` asks the plugin to stop (`Source.Stop` returns the last position it produced) and
injects a control message. The node returns after emitting that position, the channels close
downstream-wards, and `SourceNode` waits for its open-message tracker, i.e. for every in-flight message to
be acked or nacked end to end, before it calls `Source.Teardown`.

## v2: one worker per source, one batch at a time

`Service.buildRunnablePipeline` (`pkg/lifecycle-poc/service.go`) builds one `funnel.Worker` per source and
one shared `funnel.Sink` for everything after the sources. The worker is a loop over tasks; there are no
goroutines or channels between them.

```mermaid
flowchart LR
    subgraph worker per source
        ST[SourceTask] --> SPT[source processor tasks]
    end
    subgraph "shared sink (serialized by sharedMu)"
        PPT[pipeline processor tasks] --> B1[destination branch 1]
        PPT --> B2[destination branch M]
    end
    SPT --> PPT
    W[Worker.Do loop] -. drives .-> ST
    B1 -->|votes| MA[multiAckNacker]
    B2 -->|votes| MA
    MA --> RA[runAckNacker] --> WA[Worker.Ack / Nack]
    WA -->|Ack positions| SRC[connector.Source]
    WA -. nack .-> DLQ[per-source DLQ]
```

One pass of `Worker.Do`:

1. `SourceTask.Do` calls `Source.Read` and gets a batch: the plugin's `sdk.batch.size`, which defaults to 0
   and so reads batches of one (effectively 1).
2. Source processor tasks run on the batch. A processor may filter records or split one into several.
3. The pass enters the shared sink under `sharedMu`, which is what serializes N sources onto one set of
   destinations. Pipeline processors run, then the batch fans out to each destination branch.
4. `DestinationTask.Do` calls `Destination.Write` with the whole batch and then `Destination.Ack` until
   every position has an ack. **The pass does not return until then.** This is the stop-and-wait that the
   single-engine proposal aims to replace, and why a destination that holds acks until it has a full
   batch stalls v2 ([#2990](https://github.com/ConduitIO/conduit/issues/2990)).
5. Votes go to `multiAckNacker`, which tracks ack/nack per original source position, not per batch
   (ADR [`20260731-archv2-fanout-ack-model`](../architecture-decision-records/20260731-archv2-fanout-ack-model.md)).
   A position is acked when every branch acked it; the first nack wins (invariant 3). It releases the
   leading run of terminal positions to the parent in source order (invariant 4).
6. `runAckNacker` holds back the position of a record that a processor split until every piece is terminal
   (invariant 1 for split records).
7. `Worker.Ack` calls `Source.Ack` with the released positions. `Worker.Nack` writes the nacked records that
   the DLQ window accepts to this source's DLQ and acks those to the source. If the window cannot take all of
   them it returns an error, and the rest stay unacked and are redelivered.

Only after step 7 does the loop read again. So at any moment a v2 worker has at most one batch in flight,
and the stream of acks to the source is naturally ordered.

Stop: `Worker.Stop` waits for the in-flight batch (it takes the processing lock the pass holds), sets the
stop flag, then tears down the source, which is what unblocks a `Read` that is parked. `Do` returns nil on
the next check. The shared sink is closed once, after every worker has exited
(`Sink.Close`, invariant 1 and 3), because closing it earlier would tear down a destination a sibling is
still writing to.

## What happens on a processor error

A processor reports a failed record by returning an `sdk.ErrorRecord` (an error, not a record) from
`processor.RunnableProcessor.Process` (`pkg/processor/runnable_processor.go`); a record it drops
intentionally is an `sdk.FilterRecord`, which is an ack, not an error. On an `ErrorRecord` the engine
nacks the record it holds, which routes it to the DLQ: if the DLQ window has room the record is written
to the DLQ destination and then acked to the source; if the window cannot take it the pipeline halts, or
— under the default DLQ (window 1, threshold 0) — loops in `Recovering`. Nothing is silently dropped
(invariant 3).

The record that reaches the DLQ is the record the engine holds, so a processor that edits its input and
then errors must not be able to corrupt that copy. Input isolation closes exactly that hole
([#3153](https://github.com/ConduitIO/conduit/issues/3153), design
[`20261015-processor-input-isolation-for-dlq`](../design-documents/20261015-processor-input-isolation-for-dlq.md)):
`RunnableProcessor.Process` deep-copies each input record (`opencdc.Record.Clone`) before invoking the
plugin, so a mutating-then-failing processor (for example `custom.javascript`, or a built-in that edits
shared maps before failing) only ever sees a copy. The engine's record — the one it nacks and writes to
the DLQ, and the one sibling branches still see — is untouched by construction (invariant 6). Standalone
(WASM) processors already cross their boundary serialized, which is a copy by construction; built-ins pay
one deep copy per record per invocation.

The DLQ writer differs by engine: v1 writes `msg.Record.Map()` through `pkg/lifecycle/stream/dlq.go`, v2
writes the batch record through `pkg/lifecycle-poc/funnel/dlq.go`.

## What happens on a destination failure

`connector.Destination.Write` returns an error, or `Destination.Ack` returns an ack with an error set.
Either way the engine nacks the record: with a tolerant DLQ it goes to the DLQ and the pipeline keeps
running; without one the pipeline halts, and the record is not acked, so a restart redelivers it. The
failure is never turned into a silent drop (invariant 3), and it is never acked (invariant 1).

Backpressure is where the two engines differ, and it is a behaviour to plan around, not a bug:

- **v2** is stop-and-wait: one batch in flight, and the pass does not return until every position is
  acked. A destination that holds its acks (for example one that buffers until it has a full batch) stalls
  the source behind it. v2 also bounds retries (`maxRetryAttempts` / `maxRetryStall` in the funnel worker)
  so a non-converging batch fails rather than spinning forever.
- **v1** keeps reading while the channels accept messages and relies on the plugin boundary for
  backpressure — channels for built-ins, gRPC flow control for standalone connectors. There is no engine
  bound on records in flight.

Bounded stop with a destination that never acks is a known gap on both engines
([#2990](https://github.com/ConduitIO/conduit/issues/2990)), as is a poison record looping under the
default DLQ ([#2997](https://github.com/ConduitIO/conduit/issues/2997)) — see [Guarantees](guarantees.md#known-gaps).

## What happens on a crash

The persisted position is the checkpoint. `Source.Ack` writes the new position to the persister, and the
persister commits it in a transaction (`persister.writeAttempt`), so a crash leaves the old position or
the new one, never a torn write (invariant 5). Because the plugin ack is deferred until after that write
flushes (`Source.onPersistFlushed`), a crash in the window before the flush means the plugin was never
told to commit past the position either — the upstream still holds the data, and on restart the source is
opened from the last durable position (`Source.open` passes `state().Position`) and redelivers. The result
is at-least-once: duplicates are possible, gaps are not (invariants 1, 2 and 3).

The SIGKILL tests in [`tests/chaos`](../../tests/chaos) kill a real child process mid-snapshot,
mid-batch and mid-position-write and assert no gap on recovery; the acceptance suite v2 cases `DST-04`
and `SRC-03` (seven kills) prove the same at the connector boundary
([`20261013-acceptance-suite-v2`](../design-documents/20261013-acceptance-suite-v2.md)).

## Where the engines differ in behaviour

Differences found while building the test kit ([#3027](https://github.com/ConduitIO/conduit/pull/3027)) and in
the existing parity work. None is a bug on its own; each matters to anyone moving a pipeline between engines.

| Behaviour | v1 | v2 |
| --- | --- | --- |
| Source returns `io.EOF` | pipeline error: `source stream was stopped unexpectedly` | that source ends gracefully; siblings keep running |
| Position on a DLQ record | a message ID | the source position; the original record is also in the payload |
| Destination that acks only when it has a full batch (SDK batching, delay 0, reads smaller than the batch) | works: the next record is written without waiting | no progress, #2979 refuses it at start, #2990 tracks the rest |
| Processor `workers > 1` | honoured, order preserved | ignored |
| Records read ahead of a destination that holds its acks | unbounded in the engine | one batch |
| Poison record with the default DLQ (window 1, threshold 0) | destination-side ack errors loop in `Recovering` | every nack type loops in `Recovering` ([#2997](https://github.com/ConduitIO/conduit/issues/2997)) |

## Where to look next

- [Guarantees](guarantees.md): what each engine promises and which test enforces it.
- `pkg/lifecycle/stream/doc.go` for the v1 message states; `pkg/connector/doc.go`,
  `pkg/processor/doc.go`, `pkg/plugin/doc.go` and `pkg/schemaregistry/doc.go` for the layer around them.
- [`docs/package_structure.md`](../package_structure.md) for the wider package layout.
- [`CONTRIBUTING.md`](../../CONTRIBUTING.md#changing-a-pipeline-engine) before changing either engine.
