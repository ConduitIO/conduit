# No bespoke transformation DSL; transformations are real-language code

## Summary

Conduit will never introduce a bespoke transformation or configuration DSL (a Bloblang-style
config language). Transformations are written in real programming languages — Go, Python, Rust,
TypeScript — and, for standalone processors, compiled to WASM. Prebuilt processors cover the
common 90% with zero code; anything beyond that is code you can test, version, and reuse.

## Context

Competing tools (Benthos / Redpanda Connect, and its Bento fork) center on Bloblang, a custom
mapping language. A DSL is attractive early — it makes simple transforms terse and keeps them in
config — but it carries permanent costs: users must learn a new language, it is hard to test and
debug, tooling (editors, linters, type checkers) has to be built from scratch, and the surface
grows without bound as users push it past its design point.

Conduit already has a WASM processor runtime (wazero) and an SDK model that decouples plugins
from the engine. Real languages are therefore available to us as the transformation substrate
without inventing anything.

This is also a stated public principle, not merely an implementation preference — users and
contributors should be able to rely on it.

## Decision

Reject bespoke DSLs as a category. Specifically:

- Transformations are real-language code. Standalone processors compile to WASM and run in the
  wazero runtime, sandboxed and portable; built-in processors are Go compiled into the binary.
- Prebuilt, configuration-only processors cover the common cases (field extraction, renaming,
  filtering, format conversion, common SMT equivalents) so that most users write no code at all.
- When configuration is not enough, the escalation path is a real language with real tests and
  real tooling — never a new config-language dialect.
- Kafka Connect SMTs are matched with 1:1 processor equivalents, not by building an SMT
  expression language.

This decision is settled and is not relitigated in design docs.

## Consequences

- Transformations are testable, versionable, and reusable with ordinary language tooling; no
  bespoke editor/linter/debugger investment is required from us.
- The learning curve for advanced transforms is "a language you already know," not "our DSL."
- Terse one-line mappings are slightly more verbose than a DSL would make them; the prebuilt
  processor library must be good enough that users rarely feel this.
- We commit to maintaining the WASM component toolchain and per-language SDKs as the
  transformation path — that is a real, ongoing cost we accept in exchange for never owning a
  language runtime.
- Any PR proposing an expression/config language is a review blocker on principle.

## Related

- `ROADMAP.md` — Principle 2 (real languages, no bespoke DSL); Phase 1 WASM connectors and SDK
  rollout; Phase 2 SMT compatibility pack
- [20231117-better-processors.md](../design-documents/20231117-better-processors.md) — processor
  design
- [20260704-wasm-component-model.md](20260704-wasm-component-model.md) — the WASM substrate that
  makes real-language plugins portable
