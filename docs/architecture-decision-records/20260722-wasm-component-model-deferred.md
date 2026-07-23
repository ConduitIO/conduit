# Plugin runtime: gRPC standalone primary, WASM component model deferred

## Summary

**Status: pending ratification (PR #2646 open).** This ADR is not yet binding — an ADR takes effect
only once merged. Until then the direction below is proposed, not in force; treat every "primary"/
"NO-GO"/"effective" statement in this document as _pending ratification (PR #2646 open)_, not as a
settled decision. The 2026-07-04 ADR it supersedes remains the ADR of record until this one merges.

This supersedes [20260704-wasm-component-model](20260704-wasm-component-model.md), which adopted the
WASI Preview 2 **component model** as Conduit's _primary_ any-language plugin architecture. Verified
research in 2026 shows the component model is not achievable on a pure-Go / no-CGO runtime for the
foreseeable future. Therefore, on ratification (pending — PR #2646 open): **gRPC standalone
(HashiCorp go-plugin) becomes the primary any-language plugin architecture for both connectors and
processors.** WASM processors continue on core WASM + WASI Preview 1 (Go/TinyGo). WASM connectors are
deferred (NO-GO) behind explicit flip conditions; v0.19 ships only the WIT-interface RFC to lock the
future design.

## Context

The 2026-07-04 ADR committed to WASI Preview 2 / the component model as the primary plugin
architecture — its typed WIT interfaces are genuinely the right long-term shape for any-language
plugins. But that commitment was aspirational, and two things now contradict it:

- **It is not pure-Go feasible.** wazero (Conduit's pure-Go WASM runtime) does not implement the
  component model, and its maintainer has stated explicitly and repeatedly that it will not until the
  component model reaches W3C recommendation — estimated 5+ years out (wazero issues #2200,
  2024-05-02, and #2289, 2024-07-17). The only mature component-model hosts (wasmtime) require **CGO**,
  which breaks Conduit's single-static-binary / no-CGO principle. The two known pure-Go component-model
  shims (`arcjet/gravity`, `partite-ai/wacogo`) are pre-alpha with zero tagged releases and barebones
  type coverage. This was confirmed against primary sources; see
  `docs/design-documents/` / the WASM connector go/no-go analysis.
- **The shipped code already doesn't use it.** Conduit's existing standalone WASM _processors_ run on
  **core WASM + WASI Preview 1** via wazero (`pkg/plugin/processor/standalone/registry.go` imports
  `wasi_snapshot_preview1`), with a hand-rolled protobuf command-pump over shared memory — not the
  component model. They support Go (native, `GOOS=wasip1 GOARCH=wasm`) and TinyGo; the Python
  processor SDK is experimental and year-stale.

Crucially, **any-language plugins already work today via gRPC standalone** (the go-plugin protocol in
`conduit-connector-protocol`): the Go SDK is production; the Python connector SDK is design-ready; a
Rust connector SDK is designed. So deferring the component model loses no _shippable_ capability — it
only defers a nicer packaging (a single sandboxed in-process artifact).

Why processors run on WASM today but connectors do not: a processor is a synchronous batch call
(`Process(records) -> records`), cleanly hand-rollable over WASI P1. A connector (Source/Destination)
needs bidirectional streaming with out-of-order async acks — materially harder to hand-roll over the
same primitive, and exactly what the component model would make clean. Without a pure-Go component
host, that streaming model stays on gRPC.

## Decision

1. **gRPC standalone (HashiCorp go-plugin) is the primary any-language plugin architecture** for both
   connectors and processors. Language reach is delivered by per-language SDKs on this protocol
   (Go shipped; Python next per the SDK priority; Rust designed).
2. **WASM processors continue unchanged** on core WASM + WASI Preview 1 via wazero — a supported but
   Go/TinyGo-only path. This is not deprecated; it remains the in-process option for processors.
3. **WASM connectors: NO-GO for now.** No WASM connector runtime ships in the release binary. v0.19
   ships the **WIT-interface RFC only** — locking the future typed interface — plus, if pursued, a
   reference component built solely on an experimental CGO/wasmtime branch that is never merged into
   the release binary.
4. **Flip conditions** (revisit this decision when either holds): wazero reverses its stated
   component-model position, **or** a pure-Go component-model host reaches a tagged, full-type-coverage
   release with a real production adopter. Track `partite-ai/wacogo` and `arcjet/gravity`.

## Consequences

- **Positive.** The single-static-binary / no-CGO / no-JVM principle is preserved. Any-language plugins
  work today (gRPC standalone). The decision record now matches the shipped code and the verified
  ecosystem reality, so future contributors do not build on an unachievable premise.
- **Negative.** No single-artifact, in-process, sandboxed WASM connectors yet — the operational and
  distribution benefits of that packaging wait. WASM processors remain Go/TinyGo-only, so the
  "any-language via WASM" promise is not yet realized even for processors; the near-term any-language
  answer is gRPC standalone SDKs, not WASM.
- **Doc updates this ADR triggers** (to be applied in the same or a fast-follow change): `ROADMAP.md`'s
  "WASM everywhere" / "WASI Preview 2 adoption" framing is reframed to "gRPC standalone primary; WASM
  component model behind the flip conditions above"; `docs/design-documents/20260705-sdk-and-embedding-dx.md`
  and the Rust connector SDK design doc drop the "Rust is the WASM component-model reference language"
  framing (Rust ships as a gRPC-standalone SDK); the v0.19 plan reflects WIT-RFC-only for WASM.

## Related

- [20260704-wasm-component-model](20260704-wasm-component-model.md) — **to be superseded by this ADR
  on ratification** (pending — PR #2646 open; remains the ADR of record until this one merges)
- `docs/design-documents/20260722-rust-connector-sdk.md` — the gRPC-standalone Rust SDK (the practical
  any-language path this ADR endorses)
- `ROADMAP.md` — Phase 1 "WASM everywhere" (to be reframed per Consequences)
- `conduit-connector-protocol` — the gRPC standalone plugin protocol that is now the primary architecture
