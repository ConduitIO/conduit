# Local-state-only stateful processing; no distributed snapshots

## Summary

Conduit's stateful processing uses local state — an embedded key-value store, checkpointed
together with the pipeline. It does not implement distributed snapshots, pluggable state
backends, or event-time watermark machinery. The scope is deliberately bounded to deduplication
(with TTL), lookup/enrichment tables, and simple window aggregations. Heavy stateful workloads
(large joins, complex event-time) are served by integrating with streaming SQL engines, not by
growing Conduit into one.

## Context

Most real-world stream-processing jobs are read-transform-write with light state: dedup a stream,
enrich records from a reference table, roll up counts over a window. A small minority need true
distributed stateful processing — large shuffles/joins, complex event-time with watermarks and
late-data handling. Flink serves that minority well, at a heavy operational cost.

The temptation is to keep adding "just one more" stateful feature until Conduit is a worse Flink.
That path destroys the project's core promise (boring to operate, embeddable, single static
binary) and pulls the engine toward distributed-snapshot machinery that contradicts the
single-node-engine decision.

We need a bright, defensible scope line for state, and a partnering story for everything past it.

## Decision

Constrain stateful processing to local, checkpointed state.

- **State is a local embedded KV store**, checkpointed atomically with the pipeline. Recovery is
  resume-from-checkpoint, governed by the data-integrity invariants (atomic checkpoint writes,
  crash-safe positions). Every state feature ships with a kill-mid-write recovery test.
- **In scope:** deduplication with TTL, lookup/enrichment tables (cached reference data from a DB
  or topic), tumbling and sliding window aggregations (counts, sums, simple rollups).
- **Explicitly out of scope:** distributed snapshots, pluggable/tunable state backends,
  event-time watermarks, large distributed joins, late-data reprocessing frameworks. If a design
  doc starts growing these, stop and flag it — scope creep here is an existential risk to the
  revival.
- **The other 20% is a partnership, not a build.** Conduit provides best-in-class ingest/egress
  for RisingWave, Materialize, and ClickHouse. The approved framing is "Conduit + streaming SQL
  engine replaces Kafka Connect + Flink," never "Conduit replaces Flink."
- Documentation must state plainly what this state layer is (most real jobs) and is not (large
  joins, complex event-time), so users self-select correctly.

## Consequences

- The engine stays single-node, embeddable, and operationally boring; state adds no consensus and
  no snapshot coordinator.
- Recovery correctness reduces to the existing data-integrity invariants rather than a new
  distributed-snapshot protocol.
- Users with genuinely heavy stateful needs must run a streaming SQL engine alongside Conduit;
  we make that integration excellent instead of pretending to replace it.
- We give up the "single tool for all stream processing" pitch by choice; the payoff is a tool
  that is trustworthy and simple for the workloads it does claim.
- Every proposed state feature is measured against the scope line above; "add a pluggable state
  backend" or "add watermarks" requires explicit maintainer sign-off and, realistically, a
  superseding ADR — not a design-doc footnote.

## Related

- `ROADMAP.md` — Principle 7 (right-sized state); Phase 3 lightweight state layer and streaming
  SQL partnerships
- [20260704-single-node-engine.md](20260704-single-node-engine.md) — local-only state is what
  keeps checkpoint/recovery correct without consensus
