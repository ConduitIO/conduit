# Pipeline engine guarantees

This page states, for an operator, what the pipeline engines promise about a record's journey
through Conduit, and for each promise it names what is tested and what is deliberately not
promised. It is the contract you can plan a pipeline against; [Life of a record](life-of-a-record.md)
is the walk-through of how a record actually moves.

Conduit runs one of two engines today:

- **v1** (`pkg/lifecycle`, `pkg/lifecycle/stream`) — the default. A graph of nodes, one message
  per record, goroutines connected by channels.
- **v2** (`pkg/lifecycle-poc`, `pkg/lifecycle-poc/funnel`) — "arch-v2", behind the preview flag
  `--preview.pipeline-arch-v2`. One worker per source, one batch at a time.

Both engines sit on the same connector layer (`pkg/connector`) and the same plugin boundary
(`pkg/plugin`), so most of what this page promises holds for either engine; where the two differ,
the guarantee says so. A single engine is planned to replace both
(ADR [`20261009-single-pipeline-engine`](../architecture-decision-records/20261009-single-pipeline-engine.md),
currently **proposed, held**); the default flip and the deletion of v1 are v0.22 work, not v0.21.

Each guarantee names the data-integrity invariants it upholds, from the list in
[`CLAUDE.md`](../../CLAUDE.md) (invariants 1 to 7). Where code upholds one, it carries a comment at
the enforcement site, e.g. `// Invariant 1: ack only after destination confirms durable write`.

How to read "how it is tested". These are the checks that back each claim, at different depths:

- **Engine tests** run both engines in-process with fake connectors
  ([`pkg/internal/enginetest/engines`](../../pkg/internal/enginetest/engines/invariants_test.go)).
- **Chaos tests** SIGKILL a real child process against a real position store
  ([`tests/chaos`](../../tests/chaos)).
- **Value-correctness** runs several pipelines in one process and checks the exact bytes and
  decoded values (`tests/valuecorrectness`).
- **Acceptance suite v2** (`DST-*`/`SRC-*` cases) is the connector certification contract, run in
  the SDK repo; its crash-and-replay cases are the strongest connector-side evidence
  ([`20261013-acceptance-suite-v2`](../design-documents/20261013-acceptance-suite-v2.md)).
- **The benchi gate** (`make bench-engine`, `.github/workflows/bench-engine.yml`) fails a PR that
  makes the engine more than 10% slower than base beyond the measured noise floor. It guards
  throughput, not correctness; the correctness claims below do not depend on it.

Where a guarantee does not hold, or is not yet enforced by a test, this page says so rather than
reword the promise. The [known gaps](#known-gaps) at the end collect the cases that are not
guarantees today.

## Guarantee 1 — at-least-once delivery

> A record is acknowledged to its source only after every destination has durably handled it, or the
> dead-letter queue (DLQ) has taken it. After a crash or a restart, processing resumes from the last
> durable position. Records may be delivered again; they are never skipped.

Invariants 1, 2 and 3.

**What it means.** The ack travels the whole way back: a destination ack/nack, through the
engine's vote-counting, back to `connector.Source.Ack`, which records the new position and hands it
to the persister (`pkg/connector/persister.go`). The source plugin is told about the ack only after
that position write has flushed durably (`Source.onPersistFlushed`), so a plugin that frees
upstream data on ack (a Postgres replication slot advancing its LSN) can never get ahead of
Conduit's own checkpoint. A record that fails is either written to the DLQ and then acked, or —
with the default DLQ (window 1, threshold 0) — halts the pipeline and is left unacked for
redelivery. Nothing is silently dropped.

**What it does not promise.** Exactly-once. A duplicate after a crash or redelivery is allowed and
not deduplicated. The replay window after a crash is bounded in practice — the persister flushes at
1 s or 10,000 positions (`DefaultPersisterDelayThreshold` / `DefaultPersisterBundleCountThreshold`)
— but no test asserts that bound. The engine tests drive fake connectors with no process boundary,
so the no-gap and persist-before-ack claims are carried by `pkg/connector` and `tests/chaos`, not by
the engine tests alone.

**How it is tested.**

- Engine: `TestInvariant1_NoSourceAckBeforeDestinationAck`, `TestInvariant1_FailedWriteIsNeverAcked`,
  `TestInvariant3_DestinationCrashFailsTheRunWithoutLoss`, `TestInvariant3_NackedRecordsReachTheDLQ`,
  `TestInvariant3_NackWithoutDLQHaltsAndIsNotAcked`.
- Chaos (SIGKILL, no gap): `TestSIGKILL_DurableUpstream_NoGap`, `TestSIGKILL_PruningUpstream_NoGap`,
  `TestSIGKILL_RecoveryLoop_CrashDuringBackoff` in [`tests/chaos`](../../tests/chaos).
- Persist-before-ack at the connector layer: `TestSource_Ack_DeferredUntilDurablyFlushed` in
  `pkg/connector`.
- Acceptance v2: `DST-04` (`crash_replay_at_least_once`) and `SRC-03` (`crash_resume_no_gap`, seven
  kills) — the connector-side crash-and-replay proof.

## Guarantee 2 — per-source ordering

> Each destination sees the records of one source in the order that source produced them, and acks
> reach the source in that same order. There is no ordering between records of different sources.

Invariant 4.

**What it means.** The unit of ordering is the source (the thing the position store tracks), not the
pipeline. Within one source, records are read, delivered and acked in source order; a processor with
`workers > 1` reassembles its output in dispatch order (v1's `ParallelNode`), and v2 releases
positions only as an in-order prefix (`multiAckNacker.releaseLocked`). Acks are delivered to the
plugin in `Source.Ack` call order, across persister flushes, by a per-source sequence counter rather
than by comparing opaque position bytes.

**What it does not promise.** Ordering across sources: records from different sources interleave at
the fan-in. Ordering by partition key inside one source is implied by source order but is not
separately tested with keyed records across a reordering step. There is no global order.

**How it is tested.**

- Engine: `TestInvariant2_AcksReachEachSourceInOrder` (five destination ack shapes, two sources, two
  destinations); `TestWorker_Ack_DeliversPositionsFIFOToV1Plugin`, `TestSourceAckerNode_AckOrder`,
  `TestMultiAckNacker_OutOfOrderCompletion_ReleasesInSourceOrder`.
- Chaos: `TestPerPartitionOrdering_AckFIFO` in [`tests/chaos/ordering_test.go`](../../tests/chaos/ordering_test.go)
  (ack delivery in `Source.Ack` order via a real per-key ledger).
- Acceptance v2: `SRC-04` and `DST-06` (`ordering_per_key`), which run only when a connector claims
  `OrdersByKey`.

## Guarantee 3 — atomic position and state writes

> A source's position and connector state are written as one transaction. A crash at any instant
> leaves either the previous complete state or the new complete state, never a torn write.

Invariant 5.

**What it means.** The persister writes a batch of connector state (which includes each source's
position) in a single transaction — `persister.writeAttempt` opens one `database.DB` transaction and
commits it (`pkg/connector/persister.go`). A failed write for one connector is retried in a fresh
transaction with the rest, so one connector's failure never half-commits a batch; a connector that
was not committed keeps its previous position. Whole-file documents (the connector registry's
install manifest) go through `pkg/foundation/atomicfile`, which writes durably and renames, so a
reader or a crash never sees a half-written file.

**What it does not promise.** This guarantee is about the position/checkpoint store and connector
state. The partition-scoped state layer (dedup, windows, keyed upserts) is a later phase and is not
shipped yet; when it lands, its writes will commit atomically with the pipeline checkpoint under the
same invariant, and every state feature must ship a kill-mid-write recovery test. A write that fails
after teardown has begun is surfaced, not swallowed (`Source.reportPersistError`).

**How it is tested.**

- Chaos (SIGKILL mid-write): the property-2 tests in [`tests/chaos`](../../tests/chaos)
  (`TestSIGKILL_Property2_PruningUpstream`, `TestSIGKILL_Property2_DurableUpstream`) and the no-gap
  recovery tests under Guarantee 1.
- Persister transaction/isolation: `persister_generation_test.go`, `persister_isolation_test.go`,
  `persister_store_error_test.go` in `pkg/connector`.
- `pkg/foundation/atomicfile` documents and tests the write-then-rename contract.

## Guarantee 4 — graceful shutdown

> SIGTERM drains in-flight records and persists the final position before exit. `kill -9` at any
> instant is recoverable without loss.

Invariant 7.

**What it means.** On SIGTERM the runtime stops the pipelines and waits for in-flight records to be
acked or nacked end to end, then tears connectors down
(`pkg/conduit/runtime.go`). Before a source plugin is torn down, `Source.Teardown` forces the
persister's debounce batch to flush and waits for the final deferred ack to actually be sent, so the
last position is durable and the plugin learns about it while the stream is still open. If a crash
(`kill -9`) happens instead, the last flushed position is the resume point, so nothing is skipped.

**What it does not promise.** A bounded stop when a destination withholds its acks. A graceful stop
with a destination that never acks does not complete — v1's `Stop` returns but the run never ends,
v2's `Stop` blocks ([#2990](https://github.com/ConduitIO/conduit/issues/2990)); a deterministically
failing record with the default DLQ loops in `Recovering`
([#2997](https://github.com/ConduitIO/conduit/issues/2997)). The process exits within a 30 s timeout,
but no test asserts the exit time on a wedged pipeline.

**How it is tested.**

- Engine: `TestInvariant7_StopDrainsInFlightRecords`, `TestInvariant7_StopWhileSourceReadIsStalled`,
  `TestInvariant7_ForceStopReleasesAPipelineWedgedOnAcks`,
  `TestServiceLifecycle_StopAndWait_DrainsAndPersists`. `WaitPipeline` (and so `StopAndWait`)
  returns only once every goroutine of the run it found has returned, also after that run has removed
  itself from the running set, and also once a recovery restart's predecessor, still unwinding when
  the restart was published, has returned. It does not wait for successors: called during a recovery
  backoff, it waits for the failed run and can return while the restart is live (`Service.Wait`
  covers every run): `TestServiceLifecycle_WaitPipeline_WaitsForNodeGoroutineTail` (v1),
  `TestServiceLifecycle_WaitPipeline_WaitsForCleanupTail` and
  `TestServiceLifecycle_WaitPipeline_WaitsForRecoveryPredecessor` (both engines;
  [#3087](https://github.com/ConduitIO/conduit/issues/3087)). An OnFailure handler must not call
  `WaitPipeline` or `StopAndWait` for its own pipeline: it runs on the run's cleanup goroutine.
- Chaos: `TestSIGTERM_GracefulTeardown_DrainsDeferredAck`, and the SIGKILL recovery tests under
  Guarantee 1 for the crash half of the guarantee.

## Guarantee 5 — schema handling never silently mangles data

> Unknown fields, type mismatches and schema drift follow the configured policy (halt, DLQ, or
> evolve). They are never silently coerced or truncated.

Invariant 6.

**What it means.** A record that does not match its schema does not pass through altered. An
over-limit Avro payload is an error, never a truncated decode
(`pkg/plugin/processor/builtin/impl/avro/decode.go`). A schema lookup is answered only by the
registry of the runtime that owns it, so two pipelines with same-named-but-different schemas do not
share codecs. What the operator configured is what happens: halt, route to the DLQ, or evolve.

**What it does not promise.** Automatic evolution unless the operator chose it. The multi-pipeline
value-correctness suite covers Avro only; Protobuf and JSON Schema are not exercised by it yet.

**How it is tested.**

- Value-correctness: [`tests/valuecorrectness`](../../tests/valuecorrectness) — several pipelines in
  one process with overlapping schema type names; every record's wire header, bytes and decoded value
  are compared against an isolated codec.
- Chaos: `TestSchemaDrift_TransportHalf_DLQRouting` and `TestSchemaDrift_TransportHalf_Halt_PositionNeverAdvances`
  in [`tests/chaos/property4_test.go`](../../tests/chaos/property4_test.go).
- Acceptance v2: `DST-07` and `SRC-05` (`schema_evolution`), run when a connector claims a schema
  policy.

## Guarantee 6 — the plugin boundary never silently mangles values

> Structured data crosses plugin boundaries exactly: 64-bit and unsigned integers, byte strings and
> decimals keep their type and value. Nothing is silently rounded or retyped, and a peer that cannot
> carry a value exactly is refused loudly, never silently coerced.

Invariant 6 at the protocol boundary.

**What it means.** v0.21 adds protocol v3 — connector protocol v3 carrying the typed `opencdc.v2`
record, and a WASM-processor capability — so `int64`, `uint64`, `[]byte` and decimal values cross a
standalone plugin boundary exactly, as the same Go types built-in plugins see in-process today. No
integer outside ±(2^53−1) is rounded, `[]byte` is no longer base64-encoded and decoded back to a
string, and NaN/±Inf are no longer stringified. The version is negotiated per peer (go-plugin's
`VersionedPlugins` for connectors, a capability flag in `Specify.Response` for WASM processors). A
peer that negotiates only protocol v2 keeps `opencdc.v1`, where an integer outside the safe range
±(2^53−1) is refused loudly with the code `plugin.integer_not_representable` and the DLQ policy
applies, instead of being rounded.

**What it does not promise.** The exact crossing requires both sides to negotiate v3; a peer still
on v2 refuses wide integers rather than preserving them. Built-in connectors and processors run
in-process and pass `opencdc.Record` values through, so they are unaffected either way.

**How it is tested.**

- commons codec round-trips for every `opencdc.v2` value kind in both directions, and the `ToProto`
  safe-range refusal for the v2 fallback, plus a fuzz target over the range (see the testing plan in
  [`20261015-structured-data-integer-precision`](../design-documents/20261015-structured-data-integer-precision.md)).
- The upgrade matrix — every combination of {old, new} Conduit and {old, new} plugin — asserts a
  wide value either crosses exactly (v3 on both sides) or is refused loudly, never rounded and never
  dropped (ADR [`20261015-lossless-structured-values-protocol-v3`](../architecture-decision-records/20261015-lossless-structured-values-protocol-v3.md)).
- Acceptance v2 checks integer fields on a rebuilt connector exactly.

## Known gaps

These are properties an operator might expect, that are not guarantees today. They are stated here so
the rest of the page is not read as promising them.

- **Memory bound.** v2 holds at most one batch in flight by construction (a slow destination stalls
  the source); v1 has no such bound and relies on the plugin boundary (channels for built-ins, gRPC
  flow control for standalone connectors) for backpressure ([#211](https://github.com/ConduitIO/conduit/issues/211)).
- **Bounded stop.** The gaps named under Guarantee 4 — a destination that withholds acks
  ([#2990](https://github.com/ConduitIO/conduit/issues/2990)) and a poison record under the default
  DLQ ([#2997](https://github.com/ConduitIO/conduit/issues/2997)).
- **Ordering by partition key**, tested separately from plain source order (see Guarantee 2).

## Keeping this page honest

When a PR changes engine behaviour, it updates the guarantee it touches in the same PR, and it moves
a claim to "tested" only with a test that fails without the change (CLAUDE.md: every bug fix ships
with the test that would have caught it). When a guarantee turns out not to hold, the page says so
and links the issue; it does not get reworded to fit. The coverage floor is live as a ratchet, and
the benchi regression gate is live and blocking — see the process-maturity table in `CLAUDE.md` for
what each actually enforces and what is still not required.
