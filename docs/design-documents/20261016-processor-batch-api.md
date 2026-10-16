# Processor batch API and the `stateless` declaration

## Summary

Add an opt-in batch surface to the processor contract and a `stateless`
declaration. A processor that implements the batch surface processes many
records in one call and reports one outcome per input record, in input order. A
processor that declares `stateless` tells the engine it keeps no state between
calls, which lets the single engine run its stage concurrently. Existing
single-record processors — built-in, standalone, and WASM — run unchanged.

## Context

The single engine (#3015) can run independent pipeline stages concurrently, but
only when it knows a stage does not carry state, and it can batch only when the
processor has a batch entry point. Today the processor contract is one record
at a time (`Process(ctx, record)`), so the engine cannot batch across records
and cannot safely parallelise a stage.

Ordering invariants constrain the design: results must stay in input order
(invariant 4), and an error on one record must not hide or reorder the outcomes
of the others (invariant 3).

## Alternatives

### A. Opt-in batch method (chosen)

Add a batch method the processor _may_ implement:

```go
ProcessBatch(ctx, []Record) ([]ProcessedRecord, error)
```

The result slice is the same length as the input and maps 1:1 by index. An
`ErrorRecord` at index `i` reports the failure of input record `i`; the other
indexes are unaffected.

The engine calls `ProcessBatch` when the processor implements it, otherwise it
falls back to calling `Process` per record — so every existing processor is
unchanged.

### B. Engine-side batch wrapper

The engine already has a batch of records; wrap them and call the single-record
`Process` in a loop.

Rejected: it gives no batching benefit to the processor (a processor that wants
to amortise setup, e.g. one schema fetch per batch, still cannot), and it gives
no concurrency signal. It is the status quo dressed up as an API.

## Decision

Ship alternative A. The batch surface is an optional interface in the Go SDK and
an optional exported function in the WASM ABI. The per-record `Process` path
remains the compatibility floor.

### `stateless` declaration

A processor declares `stateless` (SDK struct tag / WASM manifest field) to
assert it keeps no state between calls. The engine may then run that stage's
instances concurrently and reorder which instance handles which record.

A `stateless` processor that does keep state is a bug the conformance kit must
catch: a kit scenario feeds the processor a sequence whose correct output
depends on per-record state and asserts the stateless contract is honoured
across interleaved calls.

A processor that does _not_ declare `stateless` is treated as stateful: the
engine runs it serially, exactly as today.

## Failure modes

- **An outcome slice of the wrong length.** The engine rejects it: the batch is
  not committed, the error is surfaced, and the records are retried under the
  existing error-recovery path (invariant 3 — nothing is dropped, nothing is
  half-applied).
- **A `stateless` processor that keeps state.** The conformance kit fails it.
  In production the engine treats the declaration as an assertion it relies on;
  a violating processor can reorder within its own stage, which is the same
  trust boundary the declaration already draws.
- **An error at one index.** The other outcomes stand; the errored record
  follows the DLQ/halt policy. No reordering (invariant 4).

## Upgrade / rollback

The batch method and `stateless` are additive. A processor built against the
new SDK that uses `ProcessBatch` requires a runtime that knows the surface; the
SDK version gate handles this (an older engine sees a processor it cannot run
and refuses it cleanly rather than mis-running it). Processors that only
implement `Process` run on any engine version.

## Related

- #3016 (the lane), #3015 (engine behaviour), #2968 (schema middleware failed-record index).
- SDK parity (#3020, #2948): the batch API is the first protocol change the
  conformance matrix tracks; Go is the first consumer, Python second.
