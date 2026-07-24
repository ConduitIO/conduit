# SDK & embedding developer experience

_A cohesive plan for the two developers who build **on** Conduit: the **plugin
author** (writes connectors and processors) and the **embedder** (runs Conduit as
a library inside their own app). These threads exist scattered across the roadmap
(Phase 1 scaffolding/WASM/embedded-v1, Phase 2 connectors, Phase 3 AI-assisted
connector dev); this doc unifies them into one DX plan with acceptance criteria,
edge cases, and a through-line, so both personas get an optimized, simplified
experience at every lifecycle stage._

## The problem

- **The plugin author's SDK is Go-only today.** `conduit-connector-sdk` (v0.14.1)
  and `conduit-processor-sdk` (v0.5.0) are Go; the acceptance-test harness
  (`acceptance_testing.go`) — the thing that actually defines "a connector" — exists
  only in Go. Python/Rust/TS are planned but unspecified as a cohesive experience.
- **The embedder has no real API.** Embedding today means driving `pkg/conduit`'s
  `Entrypoint.Serve(cfg)` / `NewRuntime(cfg)` — but `Serve` prints a splash and
  calls `os.Exit` on error (`entrypoint.go:56,84`), which is hostile to a host
  application. There is no stable, documented Go library surface, no
  pipelines-in-code builder, and **no embedding docs** in the repo.
- **AI generation is a bullet, not a design.** "generate from plain language" and
  "`--from-openapi`" appear in the roadmap with no shape.
- **The two personas share infrastructure but no coherent DX.** Both consume the
  connector protocol, the config format, structured errors (`ConduitError`), and the
  status vocabulary — yet nothing makes their experience feel like one product.

## Principles (the DX through-line)

1. **One contract, many languages.** "A connector" is defined by passing the
   versioned **acceptance-test suite**, not by a language. Every language SDK ships
   that suite; passing it is the compatibility guarantee.
2. **The same operations everywhere.** Scaffold, test, run, inspect, publish,
   embed, and the lifecycle verbs (start/stop/inspect) mean the same thing in the
   CLI, the MCP server, and every language binding. (Extends the four-renderings
   rule to the SDK surface.)
3. **Every "fast" is a measured test.** "< 30-min first connector," "< 15-min first
   embedded pipeline," "AI output passes acceptance ≥ N%" are CI-timed, not claimed.
4. **Errors teach across the boundary.** A plugin or embedder gets the same
   `ConduitError` shape (code / configPath / suggestion / structured fix) a CLI user
   or agent gets — the debugging experience is uniform.
5. **Two extension mechanisms, honestly documented.** gRPC (any language, separate
   process, mature) and WASM (component model, in-process, sandboxed, single
   artifact). The docs say plainly when to use which; we never pretend WASM is ready
   before it is (per the arch-v2 discipline).

---

## Persona A — the plugin author (connectors & processors)

### A1. Multi-language SDKs

**Strategy (tiers, in priority order):**

- **Go** — the reference SDK. Native in-process for built-ins; gRPC for standalone.
  Every other SDK is measured against its acceptance suite.
- **Python** — gRPC-standalone first (the fastest path to the huge Python data/AI
  audience), WASM fast-follow. First embed-client-library consumer on the author
  side (gRPC over the control-plane API, per
  `docs/design-documents/20260724-embed-grpc-client-libraries.md` — not the
  C-ABI `libconduit` this bullet originally named).
- **Rust** — the WASM component-model reference; proves the in-process WASM
  connector path before it's a supported product.
- **TypeScript** — WASM via `componentize-js`.
- **Java** — served by the Kafka Connect wrapper short-term; a native SDK is a
  Phase 3+ decision, demand-gated.
- **C# / Ruby** — demand-driven only; not speculatively built.

**Acceptance criteria:**

- The acceptance-test suite is **versioned** and published per language, so an
  author knows exactly which contract version they pass.
- A connector passing the suite in language X is functionally interchangeable with
  one in language Y from the engine's perspective (same protocol conformance).
- SDK/protocol version skew is caught: each SDK pins the protocol version and a
  nightly job regenerates + runs each SDK's template CI.

**Edge cases & mitigation:**

- _Protocol evolves, SDKs lag_ → the acceptance suite is the gate; a protocol bump
  is additive-and-versioned (per CLAUDE.md), and the nightly template CI surfaces
  drift before authors hit it.
- _A language can't express a protocol feature cleanly_ → document the gap per
  language rather than silently degrade; the acceptance suite marks which optional
  capabilities a connector passed.

### A2. Scaffolding

- `conduit connector new --lang go|python|rust|ts` and `conduit processor new`
  generate a **complete** repo: SDK wiring, passing tests, CI, release workflow, and
  the acceptance-test harness pre-wired.

**Acceptance criteria:**

- **Measured:** a scripted, CI-timed run proves **first working custom connector
  < 30 minutes** per language on a clean machine.
- Generated repos build and pass their own CI on first commit.
- The scaffold includes a runnable example pipeline and a README with a config
  reference and delivery-semantics notes.

**Edge cases:** missing language toolchain → a `doctor`-style preflight in `new`
with actionable install guidance; Windows path/line-ending issues → template CI
matrix includes Windows.

### A3. Local testing experience (the inner loop)

The single biggest lever on author DX. Today the acceptance harness runs via
`go test`; the goal is a first-class, language-agnostic local loop.

- `conduit connector test [--acceptance] [--integration]` — runs the acceptance
  suite **and** a real pipeline against docker-compose fixtures, one command, in any
  language. No hand-wiring `go test` incantations.
- **Golden record fixtures** for every record shape the connector must handle: raw,
  structured, tombstone, schema-carrying, error — so an author tests the hard shapes
  by default, not just the happy path.
- **Record inspector + dry-run** in the dev loop: pipe sample records through the
  connector, see input/output per stage, without standing up real infra.
- Delivery-semantics assertions in the harness (at-least-once, ordering
  per-partition, ack-on-durable) so an author can't accidentally ship a connector
  that violates an invariant.

**Acceptance criteria:**

- `conduit connector test` is the one documented way to test a connector in any
  language; the harness is byte-identical in contract across languages.
- The golden fixtures are shared across languages (one corpus), so "handles a
  tombstone" means the same thing everywhere.

**Edge cases:** docker unavailable → the acceptance suite (no external infra) still
runs; integration tests skip with a clear message. Slow/flaky target system →
fixtures use deterministic containers pinned by digest.

### A4. AI-assisted generation

- `conduit connector generate "<plain-language description>"` — scaffolds a working
  connector from a description.
- `conduit connector generate --from-openapi <spec>` and `--from-docs <url>` —
  from an API spec or documentation.
- The MCP server exposes the same tools, so an **agent** builds a connector the same
  way a human does (shared code path).

**Acceptance criteria:**

- **Grounded, never fabricated:** generated output is scaffold + config + tests, and
  is run through `conduit connector test` (acceptance) before being presented;
  output that doesn't pass is iterated or reported, never shipped silently.
- Benchmarked: on a fixed set of N canonical requests / M real OpenAPI specs, the
  generated connector passes acceptance ≥ a stated bar; the bar and set are
  committed (per the benchmark discipline).
- Generation emits the same `ConduitError`-coded diagnostics when it can't proceed
  (missing auth in the spec, ambiguous pagination) with a suggestion — never a
  hallucinated stub.

**Edge cases:** spec omits auth/pagination/rate-limits → generator surfaces a coded
gap and asks, rather than guessing; unknown/exotic API shape → falls back to a
best-effort scaffold clearly marked as needing author completion, with TODOs.

### A5. Publish

- Registry publishing via a GitHub Action + signing; `conduit connectors install
  <name>` pulls a **signed** artifact (verified before execution).
- The same acceptance suite that gates local testing gates registry acceptance — a
  connector isn't "supported" without it passing in CI.

---

### A6. WASM processors (the standalone-processor experience)

Standalone processors compile to WASM (`GOOS=wasip1 GOARCH=wasm`, run by wazero —
the SDK's `//go:build wasm` entrypoint + its Makefile pattern today). This is the
"real languages, no bespoke DSL" bet and Conduit's most differentiated author
experience — and the least polished. The build/test/debug/publish loop needs
first-class treatment; today it is hand-managed WASM flags.

- **`conduit processor build`** wraps the WASM build so an author never
  hand-manages `GOOS`/`GOARCH`/build tags. Supports Go (native), **TinyGo** (much
  smaller artifacts), and — via the component model — Rust/TS.
- **`conduit processor test`** runs the processor against the shared golden record
  shapes (raw/structured/tombstone/error) with no full pipeline; input/output
  inspectable per invocation.
- **Errors and logs cross the WASM boundary** as the same `ConduitError` model and
  structured logs a built-in processor emits — debugging a WASM processor is not a
  black box.
- **Scaffold & AI generation:** `conduit processor new --lang` and
  `conduit processor generate "<plain language>"` produce a WASM-ready processor
  with a passing test. The existing `conduit-processor-template`,
  `conduit-processor-example`, and `conduit-processor-sdk-python` are the seed.

**Acceptance criteria:**

- **Measured:** first working standalone WASM processor **< 30 min**, any supported
  language, CI-timed.
- **A committed benchmark for per-record WASM invocation overhead (built-in vs
  WASM)** — the two things that make or break WASM-processor DX are artifact size
  and per-record invocation cost, so both are measured, reported at build time, and
  gated against regression. We never claim WASM matches built-in latency.
- The processor acceptance/golden-fixture corpus is **shared** with the connector
  record-shape fixtures — one corpus, so "handles a tombstone" means the same thing
  for processors and connectors.

**Edge cases & mitigation:**

- _Standalone (WASM) vs built-in (Go, in-process) decision_ → documented plainly:
  WASM for portability/sandboxing/any-language; built-in when per-record latency
  dominates. The build reports size + the benchmark reports overhead so the choice
  is informed.
- _TinyGo/toolchain missing_ → `doctor`-style preflight with install guidance.
- _Artifact too large / slow_ → size and per-record cost reported at build; TinyGo
  and dependency-diet guidance documented.
- _Opacity across the boundary_ → structured logs + coded errors cross it; a
  documented debug mode.

## Persona B — the embedder (Conduit as a library)

### B1. A real, stable Go embedding API

Today's `Entrypoint.Serve` is CLI-shaped (splash, `os.Exit`). Embedders need a
library surface that **returns errors, never exits the host process, and is
semver-committed.**

- A clean entry: construct an engine, control its lifecycle, get errors back.
  Sketch:

  ```go
  eng, err := conduit.New(conduit.Options{...})   // no splash, no os.Exit
  if err != nil { /_ host handles it _/ }
  handle, err := eng.Start(ctx)                    // returns a lifecycle handle
  // ... host does its thing ...
  err = handle.Stop(ctx)                           // graceful, honors invariant 7
  ```

- The provisioning `Import` path (hardened in #1274) is part of the committed
  contract — it already takes a parsed `config.Pipeline`, the natural programmatic
  entry point.

**Acceptance criteria:**

- A documented, semver-stable `embed` package boundary; internal packages stay
  internal. Breaking it follows the announce → warn → remove policy.
- No embedding API call ever calls `os.Exit` or writes a splash; all failures are
  returned errors (`ConduitError` where user-facing).
- Graceful shutdown from the host honors the data-integrity invariants (drain +
  checkpoint), reusing the SIGTERM/graceful-drain work.

**Edge cases:** host wants multiple engines in one process → the API supports it (no
package-global state); host cancels context mid-pipeline → clean drain-or-deadline,
same semantics as SIGTERM.

### B2. Pipelines in code (easy and intuitive)

Assembling raw `config.Pipeline` structs is not a good authoring experience. Provide
a **fluent builder** that produces the _same_ artifact the YAML produces, so code and
config round-trip.

- Sketch:

  ```go
  p := pipeline.New("pg-to-s3").
      From(connectors.Postgres(pgCfg)).
      Through(processors.Filter(`.payload.after.type == "order"`)).
      To(connectors.S3(s3Cfg))
  handle, err := eng.Run(ctx, p)
  ```

**Acceptance criteria:**

- The builder emits exactly the `config.Pipeline` the equivalent YAML parses to —
  verified by a round-trip test (build → serialize → parse → equal). One artifact,
  two authoring surfaces (YAML and code), never divergent.
- Type-safe connector/processor config where the language allows it; validation is
  the same `conduit pipeline validate` logic, returning `ConduitError`.
- Discoverable: an IDE autocompletes available connectors/processors and their
  config.

**Edge cases:** a code-built pipeline that's invalid → the same coded validation
error a YAML user gets, at build time; mixing code and YAML pipelines in one engine →
supported, both go through the same provisioning path.

### B3. Embedding from any language

**Superseded 2026-07-24:** this section originally specified `libconduit`, a C-ABI shared
library, as the binding mechanism. Per
[`docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md`](../architecture-decision-records/20260724-embed-bindings-via-grpc.md),
embed bindings are **gRPC client libraries** over the existing control-plane API
(`proto/api/v1/api.proto`) plus a new external-connector engine feature — not a C-ABI. See
[`docs/design-documents/20260724-embed-grpc-client-libraries.md`](20260724-embed-grpc-client-libraries.md)
for the full design. The bullets below are kept for history; read the linked ADR/design doc for
the current plan.

- ~~**`libconduit`** — a C ABI shared library exposing the B1 lifecycle surface, the
  base for bindings.~~ Replaced by a gRPC client library driving the control-plane API.
- **Bindings**, in priority order: Python and Node.js first (the embedder audience
  overlap with the AI/data and app-dev worlds), Java/Ruby on demand. (Unchanged.)
- Each binding exposes the **same lifecycle verbs and the pipelines-in-code
  builder** idiomatic to the language — not a thin, mechanical transliteration. (Unchanged.)

**Acceptance criteria:**

- **Measured:** first embedded, running pipeline from a copy-paste doc example
  **< 15 minutes** per supported language, CI-timed.
- ~~The C ABI documents ownership and threading; ASAN/race tests on the bindings.~~ No ABI —
  the gRPC client library documents connection/retry semantics; race detector on the Go engine
  side only, standard client-library testing on the binding side.
- The same `ConduitError` codes cross the boundary (as a structured payload via
  `google.rpc.Status`/`ErrorInfo`, not a bare string) so a Python/Node embedder gets actionable
  errors.

**Edge cases:** engine subprocess/service unreachable, version mismatch between client library and
engine, external-connector host-reachability (see the gRPC design doc's Failure modes); a language
without good gRPC ergonomics → evaluated before committing (don't ship an awful binding to hit a
checkbox).

---

## Cross-cutting: optimized at every lifecycle stage

The same stages, optimized for **both** personas, sharing one mental model:

| Stage | Plugin author | Embedder |
| --- | --- | --- |
| **Scaffold** | `connector/processor new --lang` (or AI generate) | `conduit init --project` + embed template per language |
| **Develop** | hot-reload + record inspector | pipelines-in-code with IDE autocomplete |
| **Test** | `conduit connector test` (acceptance + integration, golden fixtures) | in-process test harness; assert records land |
| **Debug** | `ConduitError` codes + inspector | same `ConduitError` codes across the gRPC boundary |
| **Ship / embed** | signed registry publish | semver-stable gRPC client library per language (not `libconduit`) |
| **Operate** | delivery-semantics documented per connector | lifecycle verbs + health/metrics from the host |

The shared spine: **one acceptance contract, one config artifact, one error model,
one status vocabulary, one set of verbs** — across languages and across both
personas.

## Docs (clear, per-persona, per-language, examples that compile)

- **Per-language connector/processor tutorials** (Go, Python, Rust, TS) — the same
  worked example (e.g. an HTTP source) in each, so a reader compares apples to apples.
- **Embedding guide per language** — from zero to a running embedded pipeline, the
  < 15-min path, copy-paste-runnable.
- **Examples that compile** — Go `Example*` funcs run by `go test`; equivalent
  runnable, CI-tested examples per language and per binding. A doc example that
  doesn't run is a bug.
- **"gRPC vs WASM, and when to embed vs run standalone"** — an honest decision guide,
  not marketing.
- **Godoc/docstrings on every exported SDK and embedding symbol** — say something the
  signature doesn't (behavior, invariants, error semantics, concurrency safety).
- The connector/processor cookbook (30+ recipes) and the embedding guide are
  maintained with the code; `llms.txt` includes the SDK + embedding surface so agents
  can build and embed too.

## SDK, protocol & template repos — the maintenance bar

The author experience is only as good as the repos behind it. All of these are
held to one standard — **current, performant, documented, released on cadence:**

- `conduit-connector-sdk`, `conduit-processor-sdk` (+ `conduit-processor-sdk-python`)
- `conduit-connector-protocol` (breaking-change-sensitive — additive/versioned only)
- `conduit-connector-template`, `conduit-processor-template`, `conduit-processor-example`

The bar (every repo, every release):

- **Current** — latest stable Go (1.25 today), dependencies current, released on the
  monthly cadence. No dormant-but-unreleased dep drift.
- **Performant** — benchmarks on the hot paths (record (de)serialization, protocol
  marshal/unmarshal, processor invocation), committed and run as regression gates.
- **Documented** — `doc.go` on every package; godoc on every exported symbol (say
  something the signature doesn't — behavior, invariants, error/concurrency
  semantics); a README with a runnable example; examples that compile in CI.
- **Templates track the SDK** — the connector/processor templates are regenerated
  and CI-run against the current SDK + protocol nightly, so `conduit connector new`
  and `conduit processor new` never scaffold against a stale contract.

**Current-state audit (the gap to close):** all of these are on **Go 1.24.2**
(Conduit is on 1.25) with unreleased dependency bumps sitting on `main`; the
**protocol and processor SDK have zero benchmarks**; and package/godoc coverage is
partial (2 `doc.go` each). This is tracked as the **"SDK ecosystem currency"**
workstream — the immediate, low-risk step is the Go 1.25 upgrade + dep currency +
a release for each, with the benchmark and deeper doc pass following alongside the
v0.16 error-code work (the protocol change for plugin-originated `ConduitError`
codes lands there).

## How this maps into the roadmap (no new phase; makes existing threads cohesive)

- **Phase 1:** Go+Python scaffolding, the `conduit connector test` local loop, the
  real Go embedding API + pipelines-in-code builder (B1/B2), the first AI
  `generate`. (These already live in Phase 1 as separate bullets — this doc makes
  them one coherent workstream with shared acceptance.)
- **Phase 2:** the connector fleet rides the same multi-language SDK + acceptance
  contract; AI generation extends to `--from-openapi` at scale. **Python/Node embed
  client libraries (B3)** are no longer necessarily Phase-2-gated: per
  `docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md` and
  `docs/design-documents/20260724-embed-grpc-client-libraries.md` (superseding the
  `libconduit` C-ABI framing this bullet previously pointed at), Slice 1 (a Python
  client library over the already-merged control-plane API) needs **no core
  changes** and is release-target TBD (v0.19 fast-follow or v0.20 — open question
  in that doc); only Slice 2 (the external-connector engine feature) is new
  engine-side work that needs its own phase placement at implementation time.
- **Phase 3:** Rust/TS WASM connectors graduate; `conduit connector generate
  --from-openapi` hardens; more bindings on demand.

## Acceptance criteria (measurable, roll-up)

- First working custom connector **< 30 min**, any supported language (CI-timed).
- First embedded, running pipeline **< 15 min**, any supported language (CI-timed).
- A connector is "supported" **iff** it passes the versioned acceptance suite in CI.
- AI-generated connectors pass acceptance ≥ a committed bar on a committed benchmark.
- Every SDK/embedding doc example is CI-run; a non-running example fails the build.
- One `ConduitError` model, one config artifact, one status vocabulary, one verb set
  across all languages and both personas — verified by round-trip/contract tests.

## Related

- Phase 1 execution plan (`docs/design-documents/20260704-phase-1-execution-plan.md`)
- ConduitError design (`docs/design-documents/20260705-conduit-error-and-structured-output.md`)
- ADRs: WASM component model, single-node engine, no-bespoke-DSL
- `conduit-connector-sdk`, `conduit-processor-sdk`, `conduit-connector-protocol`,
  `conduit-connector-template`
