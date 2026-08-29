# PCS as prior art for the arch-v2 columnar exploration

## Summary

PCS (<https://github.com/nassor/pcs>) is a columnar batch engine: transforms compile to WASI 0.2
components, and a Rust host owns IO, checkpointing, and distribution. It is explicitly a
playground, not production-ready. Its benchmark numbers are already analysed in the columnar ADR
(`20260823-columnar-record-representation-scoped-to-archv2.md`); this note records the
architectural ideas the ADR does not cover, for whoever picks up the arch-v2 columnar
exploration.

## Learn: derived scheduling from declared data overlap

PCS transforms declare which Arrow fields they read and write. That declaration is the only
scheduling input: the engine derives the dependency graph, groups non-conflicting work into
concurrent stages, and drives retry and distribution from it. The developer never writes a stage
list.

Why it matters to us: Conduit's parallelism is config-driven. A declaration-driven model —
concurrency falls out of what data a component touches, not out of operator knobs — is the
interesting idea here. It only transfers if the engine can see field-level shape, which needs
the columnar representation the ADR gates behind a benchi comparison. Record it; do not build
it before that gate clears.

## Learn: columnar checkpoints

PCS checkpoints as contiguous Arrow buffer writes and claims roughly 6x faster recovery than
row-oriented storage at 1M rows. Directionally consistent with the ADR's finding that columnar
wins scale with batch size. Conduit checkpoints are small per-partition positions in the state
store — latency-sensitive, not bulk data — so the pattern does not transfer as-is. Relevant only
at arch-v2 batch granularity, and only after the benchi gate.

## Validate: host-owned IO as the trust boundary

PCS components receive Arrow IPC bytes and nothing else — no sockets, no files. That is the same
deny-by-default, host-mediated model as Conduit's host-egress capability (SSRF gate, egress
ceiling, secrets resolved host-side). Two engines arriving at the same boundary independently is
a strong signal the model is right. Conduit already enforces it; nothing to adopt.

## Rejected for Conduit (do not reopen)

- **Raft + row-range leases**: engine-side clustering. Conduit bans membership and consensus
  primitives in the engine; distribution lives in the scheduling layer (per ADR).
- **WASI 0.2 component model for processors**: already decided — gRPC standalone primary, WASM
  component model deferred (`20260722-wasm-component-model-deferred.md`).

## License constraint

AGPL-3.0, except the `packages/` subtree (Apache-2.0). Prior-art use is pattern-level learning
only. Any concrete code must come from the Apache subtree or be re-derived. Conduit is
Apache-2.0 with a one-way ratchet on open-source licensing — flag loudly if anyone proposes
vendoring from this repo.

## Related

- `20260823-columnar-record-representation-scoped-to-archv2.md` — the scoping decision and the
  PCS benchmark analysis.
- `20260722-wasm-component-model-deferred.md` — processor runtime decision.
- `ROADMAP.md`, Iceberg-first lakehouse story — proposed first consumer of columnar records.
