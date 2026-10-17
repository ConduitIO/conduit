# Pipeline engine guarantees

> Draft. Written to be corrected: where a guarantee is not enforced by a test, or does not hold, it says so.

This page states what the pipeline engines promise about a record's journey, and for each promise names
the test that fails if it breaks. It covers the engines (`pkg/lifecycle` v1, `pkg/lifecycle-poc` v2); how a
record moves is in [Life of a record](life-of-a-record.md). The numbered invariants are the ones in
[`CLAUDE.md`](../../CLAUDE.md) (data integrity invariants 1 to 7).

How to read the status column:

- **Enforced**: a test in CI fails if this breaks. The test is linked.
- **Partly enforced**: a test covers some of it; what is missing is stated.
- **Not yet enforced**: nothing in CI would catch a regression. This is not a statement that it holds.
- **Does not hold**: known to be false for the engine named. Linked to an issue.

Two kinds of test back these claims. The **engine tests** run on both engines with fake connectors, in
process, in seconds ([`pkg/internal/enginetest/engines`](../../pkg/internal/enginetest/engines/invariants_test.go)).
The **chaos tests** SIGKILL a real child process against a real position store
([`tests/chaos`](../../tests/chaos)). They exercise the **connector layer** (`connector.Source`, the
persister, the position store), not the engines: the child's read/ack loop (`runReadAckLoop` in
`tests/chaos/child.go`) calls `Source.Read` and `Source.Ack` directly and bypasses both the v1 node graph and
the v2 funnel. No SIGKILL test drives v1's node graph, and none drives the v2 worker. Four tests in the package do
drive an in-process `funnel.Worker` against the real connector and position store, with no process kill:
`TestSchemaDrift_TransportHalf_*` (`property4_test.go`), `TestArchV2_LiveApplyDrain_UnderLoad_NoGap` and
`TestArchV2_LiveApplyDrain_StuckDestination_O2Bound` (`archv2_drain_test.go`), and
`TestArchV2_StoreFault_FailsRunWithoutAck` (`archv2_storefault_test.go`). They are v2-only; v1 has no
equivalent. Rows below that cite chaos tests
are marked "Connector layer (engine-independent)" for that reason. Neither kind of test runs against a real
connector plugin or a real network.

## At-least-once delivery

> A record is acknowledged to its source only after every destination durably handled it, or the DLQ took it.
> After a crash or a restart, processing resumes from the last durable position. Records may be delivered
> again; they are never skipped.

Invariants 1, 2 and 3. Exactly-once is not promised.

| Claim | Engine | Status | Enforced by |
| --- | --- | --- | --- |
| No source ack before the destination ack | v1, v2 | Enforced | `TestInvariant1_NoSourceAckBeforeDestinationAck` |
| A failed destination write is never acked | v1, v2 | Enforced | `TestInvariant1_FailedWriteIsNeverAcked` |
| A destination crash fails the run without acking undelivered records | v1, v2 | Enforced | `TestInvariant3_DestinationCrashFailsTheRunWithoutLoss` |
| A nacked record reaches the DLQ and is then acked; nothing is dropped | v1, v2 | Enforced | `TestInvariant3_NackedRecordsReachTheDLQ` |
| With the DLQ disabled a nack halts the pipeline and the record is not acked | v1, v2 | Enforced | `TestInvariant3_NackWithoutDLQHaltsAndIsNotAcked` |
| v1 and v2 make the same DLQ decisions for the same ack/nack sequence | v1, v2 | Enforced | [`TestDLQParity_Scripted`](../../pkg/lifecycle/dlqparity/dlq_parity_test.go), `TestDLQParity_RandomizedBatching` |
| Unanimity across destinations: acked only when all ack, first nack wins, per position | v2 | Enforced | [`multi_ack_nacker_test.go`](../../pkg/lifecycle-poc/funnel/multi_ack_nacker_test.go) |
| A record split by a processor is acked only when every piece is terminal | v2 | Enforced | [`run_ledger_test.go`](../../pkg/lifecycle-poc/funnel/run_ledger_test.go), `TestFullPipeline_Property_MixedProcessorOutputAndDestinationFailure` |
| The plugin is told of an ack only after the position is durably persisted | Connector layer (engine-independent) | Enforced | [`TestSource_Ack_DeferredUntilDurablyFlushed`](../../pkg/connector/source_test.go), `TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder`, `TestSIGKILL_Property2_PruningUpstream`, `TestSIGKILL_Property2_DurableUpstream` |
| After SIGKILL at any point, restart does not skip a record | Connector layer (engine-independent) | Enforced | `TestSIGKILL_DurableUpstream_NoGap`, `TestSIGKILL_PruningUpstream_NoGap`, `TestSIGKILL_RecoveryLoop_CrashDuringBackoff`, `TestSIGKILL_RecoveryLoop_CrashDuringRecoveredRun` in [`tests/chaos`](../../tests/chaos) |
| The replay window after a crash is bounded | v1, v2 | Not yet enforced | The persister flushes at 1 s or 10,000 positions (`pkg/connector/persister.go`); no test asserts the bound on duplicates |

The engine tests above do not exercise the persister or a real process boundary. The persist-before-ack
and no-gap rows are carried by `pkg/connector` and `tests/chaos`, which test the connector layer both engines
sit on. What is not tested is a crash with an engine in the loop: nothing kills a process mid-batch while a
v1 node graph or a v2 worker holds records, so "the engine hands the connector layer a safe position at every
instant" rests on the engine tests plus review.

## Ordering

> Each destination sees the records of one source in the order that source produced them. Acks reach the
> source in that same order. There is no ordering between records of different sources.

Invariant 4. Ordering is per source (the unit the position store tracks), not global.

| Claim | Engine | Status | Enforced by |
| --- | --- | --- | --- |
| A destination receives each source's records in source order | v1, v2 | Enforced | `TestInvariant2_AcksReachEachSourceInOrder` (five destination ack shapes, two sources, two destinations) |
| Acks reach the source, in order, with no gaps or duplicates | v1, v2 | Enforced | same test; [`TestSourceAckerNode_AckOrder`](../../pkg/lifecycle/stream/source_acker_test.go), [`TestWorker_Ack_DeliversPositionsFIFOToV1Plugin`](../../pkg/lifecycle-poc/funnel/worker_ack_order_test.go) |
| Acks reach the plugin in `Source.Ack` call order, across persister flushes | Connector layer (engine-independent) | Enforced | `TestPerPartitionOrdering_AckFIFO` in [`tests/chaos`](../../tests/chaos/ordering_test.go) |
| Out-of-order destination completion still releases source acks in order | v2 | Enforced | `TestMultiAckNacker_OutOfOrderCompletion_ReleasesInSourceOrder` |
| A processor with `workers > 1` preserves record order | v1 | Enforced | [`TestParallelNode_Coordinator`](../../pkg/lifecycle/stream/parallel_test.go) (the coordinator collects results in dispatch order) |
| A processor with `workers > 1` preserves record order | v2 | Not applicable | v2 ignores `workers`; the setting has no effect |
| Order across sources | v1, v2 | Not promised | Sources interleave at fan-in |
| Order by partition key inside one source | v1, v2 | Not yet enforced | The engines preserve source order, which implies it; no test uses keyed records across a reordering step |

## Memory bound

> The engine holds a bounded number of records in memory per pipeline, so a slow destination slows the
> source down instead of growing memory.

This is a promise v1 does not make and v2 makes by construction.

| Claim | Engine | Status | Enforced by |
| --- | --- | --- | --- |
| With a destination that withholds acks, at most one batch is in flight | v2 | Enforced | `TestMemoryBound_RecordsInFlightWithWithheldAcks` |
| The engine bounds records in flight | v1 | **Does not hold** | With every ack withheld the source node kept reading until the probe stopped it, at both batch size 1 and 100; nothing in the engine pushed back. (The probe was a throwaway; the committed test pins only the v2 bound.) Back-pressure is left to the plugin boundary (channels for built-ins, gRPC flow control for standalone connectors), [#211](https://github.com/ConduitIO/conduit/issues/211). The test is skipped with that link. |
| Memory per record does not grow over a long run | v1, v2 | Not yet enforced | No soak test is live; the 24 h soak is targeted for Phase 2 in the process-maturity table |
| Allocations per record on the data path | v1, v2 | Partly enforced | `BenchmarkStreamOld` and `BenchmarkStreamNew` report them; nothing fails on a regression. `make bench-engine` and the per-PR comment show the numbers; the `bench-engine (gate)` check fails a >10% throughput regression beyond the A/A floor |

v2 buys its bound with throughput: one batch in flight is also why it stalls behind a destination that
buffers (see [Life of a record](life-of-a-record.md#where-the-engines-differ-in-behaviour)). The
single-engine design ([#2971](https://github.com/ConduitIO/conduit/pull/2971)) replaces it with credits.

## Stop time

> SIGTERM drains in-flight records, persists the final position and exits. If a pipeline cannot drain,
> the process gives up after the exit timeout and the records are replayed from the last persisted position.

What happens at the timeout differs. v2 escalates to a force stop on the last of its six 5 s waits, then
gives up at 30 s. v1 has no force step in the runtime shutdown path (`registerCleanupV1` in
`pkg/conduit/runtime.go` calls `StopAll` and waits up to 30 s; v2's is `registerCleanupV2`); it simply stops waiting.

Invariant 7. "Drains" and "exits within a bound" are two promises; the second is the weaker one today.

| Claim | Engine | Status | Enforced by |
| --- | --- | --- | --- |
| A graceful stop acks every record the destination durably handled, then tears all connectors down | v1, v2 | Enforced | `TestInvariant7_StopDrainsInFlightRecords` |
| A graceful stop completes when a source `Read` is parked | v1, v2 | Enforced | `TestInvariant7_StopWhileSourceReadIsStalled` |
| The final position is flushed before teardown completes | v1, v2 | Enforced | [`TestServiceLifecycle_StopAndWait_DrainsAndPersists`](../../pkg/lifecycle/stop_and_wait_test.go) (service level, both engines) |
| The final deferred ack is delivered on SIGTERM teardown | Connector layer (engine-independent) | Enforced | `TestSIGTERM_GracefulTeardown_DrainsDeferredAck` |
| A graceful drain under load leaves no gap and the position durable | v2 (in-process worker) | Enforced | `TestArchV2_LiveApplyDrain_UnderLoad_NoGap`; no v1 equivalent |
| A force stop releases a pipeline wedged on withheld acks and acks nothing it should not | v1, v2 | Enforced | `TestInvariant7_ForceStopReleasesAPipelineWedgedOnAcks` |
| A graceful stop with a destination that withholds acks completes | v1, v2 | **Does not hold** | The engines wait for the acks, with no bound: v1's `Stop` returns but the run never ends, v2's `Stop` blocks. [#2990](https://github.com/ConduitIO/conduit/issues/2990) tracks the v2 SIGTERM hang. A bounded `Stop` on v2 is tested at the worker level (`TestArchV2_LiveApplyDrain_StuckDestination_O2Bound`), which returns a deadline error rather than completing the stop |
| The process exits within the exit timeout (30 s) of SIGTERM | v1, v2 | Not yet enforced | `pkg/conduit/runtime.go` waits 30 s (v2 force-stops at 25 s, v1 does not). No test starts a wedged pipeline and asserts the exit time |
| `kill -9` at any instant is recoverable without loss | Connector layer (engine-independent) | Enforced | The SIGKILL tests under [At-least-once](#at-least-once-delivery), which test the connector layer only; no SIGKILL test drives either engine |

## Bad-record behaviour

> A record that fails is never silently dropped. It goes to the DLQ if the DLQ window has room, and
> otherwise stops the pipeline with the cause.

Invariant 3 and 6 (the schema part of 6 is not covered here).

| Claim | Engine | Status | Enforced by |
| --- | --- | --- | --- |
| A nacked record is written to the DLQ and then acked | v1, v2 | Enforced | `TestInvariant3_NackedRecordsReachTheDLQ` |
| The DLQ window and threshold behave identically | v1, v2 | Enforced | `TestDLQParity_Scripted`, `TestDLQParity_WindowDisabled`, `TestDLQParity_RandomizedBatching` |
| A failed DLQ write is fatal | v2 | Enforced | `TestMultiAckNacker_FatalDLQError_Propagates` |
| A record with an empty position is refused, not acked | v2 | Enforced | `TestWorker_Nack_SourceEmittedEmptyPosition_RefusesAndIsFatal`, `TestWorker_Nack_TailOnlySubBatch_RefusesToWipeSourcePosition` |
| With the default DLQ (window 1, threshold 0) a record that deterministically fails ends the pipeline in `Degraded` | v1, v2 | **Does not hold** | It loops in `Recovering` with the default infinite retries. Nothing is lost or acked, but the pipeline never makes progress. [#2997](https://github.com/ConduitIO/conduit/issues/2997) |
| Window size 0 is the "never reject" sentinel: every nack goes to the DLQ, none halts the pipeline | v1, v2 | Enforced | `TestDLQParity_WindowDisabled`. It is easy to hit by accident in a test or config: it is the opposite of "DLQ off", which is window 1 with threshold 0 (the default) |
| Schema drift follows the configured policy | v1, v2 | Not covered here | Invariant 6; the transport half is `TestSchemaDrift_TransportHalf_DLQRouting` and `TestSchemaDrift_TransportHalf_Halt_PositionNeverAdvances` in [`tests/chaos`](../../tests/chaos/property4_test.go) |

## Keeping this page honest

When a PR changes engine behaviour, it updates the row it touches in the same PR and moves a row to
**Enforced** only with a test that fails without the change (CLAUDE.md: every bug fix ships with the test
that would have caught it). When a guarantee turns out not to hold, the row says **Does not hold** and
links the issue; it does not get reworded to fit.

The coverage floor is live as a ratchet and the benchi regression gate is live and blocking (see the
process-maturity table in `CLAUDE.md` for what each enforces and what is still not required).
