# A columnar (Arrow) record representation is scoped to arch-v2 exploration, not adopted as a format change

## Summary

Conduit's in-memory record payload is `opencdc.StructuredData`, a `map[string]interface{}`, and
`StructuredData.Bytes()` serialises it with `json.Marshal`. That representation is a real and
probably significant throughput cost, and Apache Arrow is a credible replacement for it.

We are **not** adopting Arrow as a record format now. We are scoping the question to **arch-v2
exploration**, where batching already exists, gated on a benchi comparison against CDC-shaped
traffic. Avro stays where it is: it is Kafka Schema Registry interop, not a format we chose.

## Context

The suggestion arrived as "use Arrow instead of Avro." Those are two different questions and only
one of them is live.

**Avro is not a format decision.** `schema.Type` in `conduit-commons` has exactly one member,
`TypeAvro`, and the whole path exists to serve `pkg/schemaregistry` — Confluent Schema Registry
interop. Confluent supports Avro, Protobuf and JSON Schema; not Arrow. Debezium writes Avro to the
topic. Conduit reads what is already there. Changing our internal representation does not change
what is on the wire, and we would still have to decode Avro to get _into_ Arrow. The archived-codec
problem (see the Related ADR/issue below) survives any such migration untouched.

**The internal-representation critique is separate, and it is correct.** The specific claim — that
Conduit's weakness has "always been those JSON being sent/transformed everywhere" — checks out in
substance, with one correction:

- `opencdc.StructuredData` is `map[string]interface{}` (`conduit-commons` `opencdc/data.go:49`).
- `StructuredData.Bytes()` is `json.Marshal` (`data.go:53-61`), so key extraction, hashing and raw
  conversion all pay a JSON round-trip.
- Across the plugin boundary the encoding is **protobuf, not JSON** (`opencdc/proto.go`), so "JSON
  everywhere" overstates the wire. That does not rescue the argument: the in-memory shape is the
  expensive part. Every field access on a `map[string]any` is a hash lookup plus an interface
  unbox, every field is separately heap-allocated, and the memory layout makes SIMD impossible by
  construction. `map[string]any` is arguably worse than JSON for _processing_.

Columnar buffers fix exactly that: contiguous, type-homogeneous, alignable, vectorisable. This is
why DuckDB, DataFusion and comparable engines are Arrow-based, and the reasoning transfers.

**What does not transfer, and is why this is exploration rather than adoption:**

- **Ack granularity.** Conduit acks per record (`pkg/connector/source.go`, `deliverOneAck(positions
  []opencdc.Position)`), and data-integrity invariants 1–4 are per-record: ack only after durable
  downstream handling, monotonic crash-safe positions, at-least-once floor, per-source-partition
  ordering. Columnar wants batches. Batch-and-ack is possible but reshapes the most safety-critical
  code in the engine — the code where two real ack-ordering defects were found during v0.20.
- **Schema homogeneity.** Arrow wants one schema per batch. A single-table CDC stream is
  homogeneous; a multi-table Debezium stream is not. Per-table batching changes the funnel's shape.
- **Public contract.** `conduit-connector-protocol` is breaking-change territory with its own
  versioning process, and `opencdc.Record` is exported by `conduit-commons`, which the SDK and every
  connector import. A greenfield project pays none of this; we have a registry with published
  connectors.
- **The measurements that motivated the suggestion quantify why it does not transfer.** They come
  from PCS (<https://nassor.github.io/pcs/benchmarks/>, recorded 2026-08-22), which is careful work
  — published configs, exact compiler flags, a reproduction script, and its own negative results
  reported rather than hidden. It is worth reading. Three of its findings bear directly on Conduit:

  - **"Processing 100 000 rows one at a time costs 325x the wall time of processing them in a
    single batch."** Conduit's model _is_ one at a time: per-record acks, per-source-partition
    ordering. That single number is the strongest available evidence that the columnar win is a
    function of batch size, not of representation.
  - **One-row checkpoints show 118x encoding overhead** versus a row-oriented format, attributed to
    Arrow IPC framing; and the framework's per-item floor is **247 ns**. Both are small-batch
    penalties, and small batches are where CDC lives.
  - It is **not uniformly faster even in its own domain**: TPC-H Q1 aggregation comes out **2.18x
    slower** than the scalar baseline, alongside a 13.54x win on Q6.

  Two confounds make the headline numbers a poor predictor for us. PCS is **Rust**, built with
  `-C target-cpu=native`, `opt-level=3`, thin LTO and mimalloc, so much of the win is SIMD that Go
  cannot portably reach (see the SIMD note below). And the workload is **TPC-H Q1/Q6 over 100K-1M
  row sets** — bulk analytics — with no comparison against Conduit, Kafka Connect, or any CDC
  system.

  None of this says columnar is wrong for Conduit. It says the case has to be made at Conduit's
  batch sizes, on CDC-shaped data, in Go — which is what the benchi gate below is for. CLAUDE.md
  forbids performance claims without reproducible benchi runs, and borrowing someone else's numbers
  from a different language and a different workload would be exactly that.

**Two related observations, recorded so they are not rediscovered.** WASI Preview 3 streams would
reduce host↔guest copying for standalone processors; Conduit already instantiates WASM modules
per-processor rather than per-call (`pkg/plugin/processor/standalone/processor.go`,
`newWASMProcessor`), so it does not pay the per-call Store allocation cost that motivates that
argument elsewhere. And Go has no portable SIMD intrinsics, so vectorisation would come from
arrow-go's own kernels rather than from hand-written code — a real constraint on the expected win.

## Decision

1. **Avro stays.** It is interop, not a preference. The archived-decoder problem is solved on its
   own terms (codec swap plus explicit allocation limits), independently of this ADR.
2. **A columnar record representation is an arch-v2 exploration item**, not a v0.20 or v0.21 format
   change. arch-v2 (`pkg/lifecycle-poc`) already owns batching and fan-out and is already behind a
   preview flag, which makes it the only place this can be tried without destabilising arch-v1 or
   breaking the connector protocol.
3. **The first consumer, if any, is the Iceberg destination** (Phase 2). It is batch-shaped, writes
   Parquet — for which Arrow is the natural in-memory form — and needs no change to ack
   granularity. That is the cheapest place to learn whether the win is real.
4. **A benchi comparison against CDC-shaped traffic gates any further commitment.** Committed
   configs, medians with variance, compared against the current `map[string]any` path. If it does
   not win there, it does not proceed, however well it performs on analytics-shaped data.
5. **Adopting it beyond that requires its own ADR**, because it would touch `opencdc.Record`, the
   connector protocol, and ack granularity — each of which has its own process.

## Consequences

- The known cost of `map[string]any` + `json.Marshal` remains in v0.20 and v0.21. We are accepting
  a measured-by-nobody inefficiency rather than trading it for an unmeasured architectural risk.
- arch-v2 acquires a second reason to exist beyond fan-out, which strengthens the case for its
  graduation but also widens its scope. That widening is deliberate and should be tracked, not
  allowed to grow silently.
- Choosing Arrow later means depending on `apache/arrow-go`. Worth noting given the Avro
  experience: it is first-party Apache, actively maintained (v18.7.0, July 2026), and materially
  healthier than the Avro ecosystem — where Apache ships no Go implementation at all.
- Deferring costs us little in optionality. Arrow is an in-memory representation, not a serialized
  format, so adopting it later does not require a data migration — unlike a wire-format change,
  which would.
- A separate gap surfaced while evaluating this and is **not** addressed here: `schema.Type` has
  exactly one member, so Conduit cannot read Protobuf-encoded topics at all. That is a
  Kafka-Connect-parity hole and likely higher value than re-platforming the record model. It needs
  its own issue.

## Related

- `docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md` — arch-v2's per-record
  ack tally, the invariant this proposal would have to preserve.
- `docs/architecture-decision-records/20260801-archv2-run-join.md` — arch-v2 fan-out dispatch.
- [#2817](https://github.com/ConduitIO/conduit/issues/2817) - the archived Avro codec, and why it
  is independent of this decision.
- `docs/design-documents/20260823-avro-codec-archived-decoder-advisories.md` — the codec decision.
- `ROADMAP.md`, "Iceberg-first lakehouse story" — the proposed first consumer.
