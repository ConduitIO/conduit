# WASM component model for any-language plugins

## Summary

Conduit's path to any-language connectors and processors is WebAssembly using the WASI Preview 2
component model, run in-process in the wazero runtime. WASM is the target for standalone plugins
across languages; the existing gRPC/go-plugin standalone path remains supported, and is the
first-ship path for languages whose WASM component tooling is still immature (notably Python).

## Context

A core differentiator is that connectors and processors can be written in real languages, and
that the community — not a central team — builds the long tail of the catalog. Two plugin
transports are available to us:

- **gRPC via go-plugin** (today's standalone model): a separate process, language-agnostic,
  mature, but requires a sidecar process and its lifecycle, and pays serialization and IPC costs.
- **WASM**: a single portable, sandboxed artifact that runs in-process with no sidecar. The WASI
  Preview 2 component model gives us typed interfaces (WIT) across languages, which fits a
  multi-language SDK strategy far better than raw core WASM.

Standalone processors already run as WASM in wazero. Connectors do not yet — they use the
gRPC/go-plugin path. The component model is maturing at different rates per language: Rust and
Go/TinyGo are ready, TypeScript via componentize-js is workable, Python's component tooling lags.

## Decision

Adopt the WASI Preview 2 component model as the primary any-language plugin architecture.

- **Interfaces are defined in WIT** and shared across language SDKs, giving typed, portable
  contracts rather than per-language ad hoc bindings.
- **wazero is the runtime** — pure-Go, no CGo, embeddable, consistent with the single static
  binary and library-embedding goals.
- **WASM connectors** extend the model that WASM processors already use: in-process, sandboxed,
  a single portable artifact, no gRPC sidecar.
- **gRPC/go-plugin stays supported** and is the pragmatic first-ship path where WASM component
  tooling is immature. Python ships on the gRPC standalone path first, with WASM as a
  fast-follow; Rust proves the full component-model connector path.
- The connector protocol changes this implies are breaking-change territory and follow the
  protocol versioning discipline.

## Consequences

- One portable, sandboxed artifact per plugin, distributable through a registry with signing —
  no per-platform binaries or sidecar process management for WASM plugins.
- In-process execution removes IPC/serialization overhead relative to the gRPC path and
  simplifies the plugin lifecycle.
- The sandbox gives us a security boundary for community-published plugins by default.
- We depend on the maturity of per-language component tooling; the SDK rollout order is
  constrained by it (Rust and Go lead, Python starts on gRPC). This is an accepted, temporary
  asymmetry, not a permanent two-transport strategy.
- Maintaining WIT interfaces and multiple SDK toolchains is an ongoing cost; it is the cost of
  the any-language differentiator and is accepted.
- Plugins needing host capabilities beyond what WASI Preview 2 exposes may be blocked until the
  relevant WASI proposals land; such gaps are documented rather than worked around with bespoke
  host escapes.

## Related

- `ROADMAP.md` — Phase 1 "WASM everywhere," WASI Preview 2 / component model adoption, SDK
  rollout order
- [20220121-conduit-plugin-architecture.md](20220121-conduit-plugin-architecture.md) — the
  built-in vs. standalone dispenser model this builds on
- [20260704-no-bespoke-dsl.md](20260704-no-bespoke-dsl.md) — WASM is what makes real-language
  transformations portable, so no DSL is needed
