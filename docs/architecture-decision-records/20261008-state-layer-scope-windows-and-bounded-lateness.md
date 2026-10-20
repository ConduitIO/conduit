# State-layer scope: keyed state, windows and bounded-lateness event time

## Summary

Conduit's state layer grows from "dedup, lookup tables and simple windows" to the stateful
processing integration and AI pipelines actually need: dedup with TTL, lookup/enrichment tables and
stream-table joins, keyed upsert/merge, tumbling/sliding/session windows with standard aggregates,
and event time with **bounded lateness only**. State stays local, partition-scoped and atomic with
the pipeline checkpoint. Read-only key lookups of state tables over the API and MCP are in scope;
queries, scans and SQL are not. Global watermarks, a triggers framework, stream-stream joins,
distributed snapshots, pluggable state backends and any expression/config DSL stay out.

This ADR supersedes the scope line of
[20260704-local-state-only](20260704-local-state-only.md). Everything else in that ADR — local
embedded KV state, no distributed snapshots, no pluggable backends, the streaming-SQL partnership
for heavy workloads — still stands.

Decided by DeVaris, 2026-10-08.

## Context

[20260704-local-state-only](20260704-local-state-only.md) limited state to dedup with TTL, lookup
tables, and tumbling/sliding windows, and put event-time watermarks out of scope entirely. That
line kept the engine out of Flink territory, and it did its job while there was no state layer to
scope.

Two things have changed:

- **The audience for state is clearer.** Teams moving off Kafka Connect need keyed upsert and
  stream-table joins to reproduce what they run today. AI and data-application builders need
  curated, deduplicated, joined streams; windowed summaries over model I/O; and session windows
  over user activity. Under the old line, every one of those either does not fit or needs a
  separate engine for a small amount of state.
- **Event time can't be avoided, but it can be bounded.** CDC and event streams carry their own
  timestamps, and processing-time windows over replayed or backfilled data give wrong answers.
  What makes event time expensive in general-purpose engines is the coordination: watermarks
  propagated across a distributed job, and trigger frameworks to decide when to emit. A per-
  partition rule with a fixed allowance needs neither.

The risks the original ADR guarded against are still real: growing into a worse Flink, adding
coordination that contradicts [20260704-single-node-engine](20260704-single-node-engine.md), and
making correctness depend on a new distributed protocol. This decision widens what state may do
without loosening any of those constraints.

## Decision

### In scope

- Deduplication with TTL.
- Lookup/enrichment tables and stream-table joins (reference data kept current from a database,
  CDC stream or topic).
- Keyed upsert and merge.
- Tumbling, sliding and session windows.
- Standard aggregates (count, sum, min, max, avg, distinct, top-K, last and similar).
- Event time with **bounded lateness only**, as defined below.
- Read-only lookups of state tables by key over the API and the MCP server.

### Bounded lateness

Per partition, a window closes when the maximum event time observed on that partition, minus a
fixed allowance configured on the pipeline, passes the window's end. Records that arrive later than
that go to the DLQ or a late-data output, according to the configured policy. They are never
silently dropped (invariant 6: schema and data handling never silently mangles or loses data).

There is no watermark shared across partitions, instances or pipelines, and no mechanism to emit,
retract or re-fire a window after it closes beyond what the configured policy does with late
records.

### How state is held

- **Partition-scoped.** State is keyed by source partition and moves with partition claims. No
  state is shared across partitions, so moving a partition between instances moves its state with
  it and needs no coordination.
- **Local embedded KV.** State lives in the engine's embedded key-value store. No pluggable or
  remote state backends.
- **Atomic with the checkpoint.** Every state write commits atomically with the pipeline checkpoint
  (invariant 5: state and checkpoint writes are atomic). After a crash, state and positions resume
  from the same checkpoint; a torn write between them is impossible by construction.
- **Tested by killing it.** Every state feature ships with a kill-mid-write recovery test that
  SIGKILLs the engine mid-write and verifies state and positions on recovery.

### API and MCP access

State tables can be read by key over the HTTP/gRPC API and the MCP server, read-only. Queries,
range scans, secondary indexes and SQL are not in scope. Agents and applications get live context
by key; anything that needs a query belongs in a downstream store or a streaming SQL engine.

### Still excluded

- Global or distributed watermarks.
- A triggers framework.
- Stream-stream joins.
- Distributed snapshots.
- Pluggable state backends.
- Any expression or configuration DSL. Logic beyond prebuilt processors is real code written
  against the processor state API ([20260704-no-bespoke-dsl](20260704-no-bespoke-dsl.md)); heavy
  SQL workloads go to partner engines (RisingWave, Materialize, ClickHouse).

Conduit is not a Flink replacement and is not described as one. The positioning is operational
stream processing for integration and AI, with streaming SQL engines for the workloads past this
line.

### Prerequisites

State work beyond design does not start until all three hold:

1. arch-v2 is the default engine
   ([20261006-archv2-graduation-gate](20261006-archv2-graduation-gate.md)).
2. The partition-claims protocol has shipped
   ([design](../design-documents/20260723-partition-claims-protocol.md)), because state is scoped
   to the partition and moves with its claim.
3. Replay and backfill are first-class verbs, because rebuilding state is a replay.

Growing the maintainer team is not a prerequisite.

## Consequences

- Teams can dedup, join against reference data, upsert by key and window with event time without
  running a second engine for small state.
- Correctness still reduces to the existing data-integrity invariants: state recovery is
  checkpoint recovery. No new distributed protocol is introduced.
- Bounded lateness gives up completeness for very late data in exchange for no cross-partition
  coordination. Users with out-of-order data beyond a fixed allowance, or that need results
  re-fired after close, must route late records to the DLQ/late-data output or use a streaming SQL
  engine. Documentation must state this plainly.
- Partition scoping means aggregates across partitions are not computed inside one pipeline
  instance's state. Keyed state across instances follows partition claims and is a later step;
  cross-partition joins of two streams stay excluded.
- The processor state API becomes a public contract in every officially supported language and on
  both processor runtimes; changes to it follow the protocol versioning and deprecation policy.
- Every proposed state feature is checked against the in-scope and excluded lists above. Anything
  on the excluded list needs a superseding ADR, not a design-doc footnote.

## Related

- [20260704-local-state-only](20260704-local-state-only.md) — superseded in part: its in-scope and
  out-of-scope lists (in particular "event-time watermarks" as wholly out of scope) are replaced by
  this ADR. The rest of it stands.
- [20260704-single-node-engine](20260704-single-node-engine.md) — no consensus; partition claims
  are assigned by the scheduling layer
- [20260704-no-bespoke-dsl](20260704-no-bespoke-dsl.md) — complex logic is real code via the
  processor state API
- [20261006-archv2-graduation-gate](20261006-archv2-graduation-gate.md) — prerequisite 1
- [Partition-claims protocol RFC](../design-documents/20260723-partition-claims-protocol.md) —
  prerequisite 2
- `ROADMAP.md` — Principle 7 (right-sized state); v0.25 state foundations, v0.26 keyed upsert,
  v0.27 windows and aggregations, v0.28 read-only state lookups over the API and MCP
