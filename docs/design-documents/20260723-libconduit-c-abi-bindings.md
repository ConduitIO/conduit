# `libconduit` B3 — embedding from any language via a C-ABI

## Summary

**This is a design-ahead document. No implementation ships from it.** The build itself is Phase 2
("embedded v1.1" per `ROADMAP.md`'s "Embedded v1" section and
`20260722-embed-libconduit-v1.md`'s AC-8, which explicitly scoped the C-ABI/bindings out of v0.19).
The reason to write the design now, before the build, is that B1 (the Go embedding API,
merged as `github.com/conduitio/conduit` in #2667) and B2 (the pipelines-in-code builder,
in flight on `feat/embed-b2-pipeline-builder`) are being finalized this cycle — and a Go API
shape that is easy to embed from Go can still be accidentally hostile to a future C-ABI if nobody
checks now. This doc is that check: it designs the C-ABI surface, the bindings built on it, and
the alternatives considered, then derives concrete constraints on the _current_ frozen Go API from
that design (§Decision, D8).

- **The C-ABI surface** mirrors the B1 lifecycle (`New`/`Run`/`Stop`/`Close`/`Import`) plus the B2
  builder, as a small set of `extern "C"` functions operating on opaque handles, with all
  structured data (options, pipeline config, errors) crossing as JSON strings — cgo cannot safely
  pass Go pointers, slices, or interfaces across the boundary, so nothing here tries to.
- **Memory ownership and threading** follow a single-writer-per-handle discipline, extended from
  `20260722-embed-libconduit-v1.md`'s AC-8 sketch: every opaque handle is a `runtime/cgo.Handle`;
  every string/error crossing the boundary is a plain `C.CString`-allocated buffer with an explicit
  free function; a Go panic reaching the boundary is recovered and turned into an error, never left
  to abort the host process.
- **Errors propagate** as the existing `conduiterr.ConduitError` shape (code, message, config path,
  suggestion, structured fix, docs URL), JSON-encoded across one C struct field, translated by each
  binding into an idiomatic exception — never a bare code or a null pointer reaching application
  code.
- **Bindings ship Python first, then Node.js** (per the vision doc's stated priority), each
  exposing the same lifecycle verbs and the same pipelines-in-code builder shape, idiomatic to the
  language (Python context managers + dual sync/async; Node N-API + Promises), packaged as
  self-contained, per-platform prebuilt artifacts — the same packaging discipline
  `20260707-python-connector-sdk.md` already had to solve for a different reason (no inherited
  `PATH`, an absolute/self-resolving artifact).
- **Alternatives considered and rejected:** a local gRPC/UDS bridge to an out-of-process engine
  (not in-process — a different product), and WASM (rejected on the same grounds as
  `20260722-wasm-component-model-deferred.md`, extended from "plugins in Conduit" to "Conduit in a
  host," the opposite direction).
- **Constraints on the frozen Go API, to apply now:** `PipelineConfig` and its `Connector`/
  `Processor`/`DLQ` siblings have no `json` struct tags today (confirmed by reading
  `pkg/provisioning/config/parser.go`) — add them now, matching the casing already used by
  `pkg/provisioning/config/yaml/v2/model.go`, before a JSON-crossing ABI has to invent a casing
  convention under time pressure. Three more constraints are listed in D8.

Risk tier: **Tier 3** (documentation only — no code, no build target, no public contract changes in
this PR). The eventual implementation PR is Tier 2 (embedding surface, CLI-adjacent), same as B1.

## Context

### What exists today (grounded in code, not the vision doc's sketch)

- **B1 is merged** (`conduit.go`, `doc.go`, root package `github.com/conduitio/conduit`, PR #2667):
  `Options{Logger, MetricsRegisterer, DB, PipelinesDir, API}`, `New(ctx, Options) (*Engine, error)`,
  `(*Engine) Run(ctx) (*Handle, error)`, `(*Handle) Stop(ctx) error`, `(*Engine) Close(ctx) error`,
  `(*Engine) Import(ctx, PipelineConfig) error`, `(*Engine) StartPipeline`/`StopPipeline`.
  `PipelineConfig` is a type alias (not a copy) of `provisioning/config.Pipeline`.
- **B2 is in flight** (`builder.go`, branch `feat/embed-b2-pipeline-builder`): `NewPipeline(id)
  *PipelineBuilder`, chained `.WithName`/`.WithConnector`/`.WithProcessor`/`.WithDLQ`, terminal
  `.Build() (PipelineConfig, error)` — which runs the same `provisioningconfig.Enrich`-then-
  `Validate` sequence `conduit pipelines validate` runs, but **returns the raw, unenriched value**,
  not the enriched one (`Build`'s own doc comment: "which is what keeps a hand-built PipelineConfig
  indistinguishable from a parsed one"). `ConnectorBuilder`/`ProcessorBuilder`/`DLQBuilder` are
  single-owner, write-once, non-concurrent-safe values — the same constraint this doc's C-ABI
  design has to respect if it exposes anything resembling them across the boundary (it doesn't;
  see D1.3).
- **`20260722-embed-libconduit-v1.md`'s AC-8** already sketched the shape this doc completes:
  `conduit_new`/`conduit_import`/`conduit_run`/`conduit_stop`/`conduit_free`/`conduit_free_error`,
  JSON envelopes for data, a `runtime/cgo.Handle`-wrapped opaque pointer, single-writer-per-handle,
  errors crossing as the `ConduitError` shape. That doc explicitly deferred B3 "on purpose (no
  cgo, no shared-library build target, no bindings, no ASAN gate)" as v0.19-out-of-scope, "per
  CLAUDE.md's 'no speculative generality'" — this doc is the design that discharges that deferral
  when Phase 2 picks it up, not a reversal of the deferral itself.
- **`conduiterr.ConduitError`** (`pkg/foundation/cerrors/conduiterr/conduiterr.go`) has the exact
  shape this doc's error propagation reuses: `Code` (a registered `{reason string, grpcCode
  codes.Code}` pair — `Reason()` is "the source of truth for docs, llms.txt, and UI rendering"),
  `Message`, `ConfigPath`, `Suggestion`, `Fix` (a structured, machine-appliable change: `ConfigPath`/
  `Op`/`Value`), `DocsURL`. This is already the shape crossing the CLI/API/MCP boundary; the C-ABI
  reuses it rather than inventing a fourth error encoding.
- **`20260707-python-connector-sdk.md`** (the Python _connector_ SDK — a different persona, gRPC-
  standalone subprocess plugins, not embedding) already had to solve adjacent packaging problems:
  the launched process gets **no inherited `PATH`** (Conduit's dispenser sets `cmd.Env =
  make([]string, 0)`), so the packaged artifact must resolve its own interpreter/library by an
  absolute or package-relative path, never PATH lookup. B3's bindings face the same class of
  problem (locating a bundled shared library, not a bundled interpreter) and reuse the same lesson:
  self-contained, per-platform artifacts, never ambient-environment lookup.
- **`20260722-wasm-component-model-deferred.md`** (pending ratification) establishes that a pure-Go
  WASM host (wazero) cannot run the component model for the foreseeable future, and that CGO-
  dependent WASM hosts (wasmtime) break the single-static-binary / no-CGO principle for Conduit's
  _own released binary_. This matters for D_alternatives.c below, and for a nuance worth stating
  precisely: `libconduit`'s own build (Go's `-buildmode=c-shared`) requires `CGO_ENABLED=1` and a
  C toolchain, exactly like the WASM/CGO tradeoff that ADR describes — but `libconduit` is a
  **separate build artifact**, never linked into the released `conduit` CLI binary. Building it
  does not reintroduce CGO into the thing that ADR protects (Conduit's own single static binary);
  it is a new, additional artifact with its own build matrix, discussed as a real cost in
  §Failure modes, not a violation of that ADR.

### Why this is a separate doc, not an extension of the merged B1 doc

`20260722-embed-libconduit-v1.md` is the shipped, Tier-2-reviewed design for B1/B2; re-opening it
to add unshipped B3 detail would blur what's actually built (B1, merged; B2, in review) against
what's still a sketch. This doc is scoped to B3 alone, cross-references the merged doc's AC-8 as
its starting point, and is explicit throughout about what is design-ahead versus what already
exists in code.

## Goals / Non-goals

**Goals (design-ahead; nothing here ships from this PR):**

- A complete `extern "C"` function surface covering B1's lifecycle plus B2's builder, with named
  opaque handle types and a documented ownership/threading model.
- A single, reused error-propagation shape (`ConduitError` → one JSON-carrying C struct → a
  per-language exception), so "errors teach across the boundary" (the vision doc's principle 4)
  holds for the C-ABI exactly as it already holds for the CLI/API/MCP surfaces.
- A binding design for Python (first) and Node.js (second), each idiomatic to its language, not a
  mechanical transliteration of the Go fluent builder.
- A packaging story per binding that reuses the Python connector SDK's hard-won lessons
  (self-contained artifacts, no ambient-environment lookup) rather than rediscovering them.
- An ABI versioning and frozen-contract policy consistent with CLAUDE.md's existing
  announce → warn → remove discipline, extended to a new surface (C symbol names, shared-library
  name, and the JSON wire shape of the types crossing it).
- Concrete, actionable constraints on the _current_ B1/B2 Go API (D8) that keep it C-ABI-friendly
  without requiring a breaking change later.

**Non-goals (explicitly out of scope, same discipline `20260722-embed-libconduit-v1.md`'s AC-8
already applied to this exact topic):**

- No `cgo` code, no shared-library build target, no bindings, no ASAN/race CI gate ships from this
  doc. Building any of it before B1/B2 have a single production embedder would be exactly the kind
  of interface "built on a hunch rather than two real implementations" CLAUDE.md's no-speculative-
  generality rule blocks.
- No Java or Ruby binding design — both remain demand-gated per the vision doc, not committed here
  even at the design level.
- No change to B1/B2's actual Go signatures. D8 lists constraints and additive annotations (struct
  tags, documentation), never a signature change to `Options`, `Engine`, `Handle`, or
  `PipelineBuilder`.
- No metrics story across the ABI (see §Decision, D_observability) — Prometheus's own per-language
  client libraries, or the existing `Options.API`-exposed `/metrics` route, already answer this;
  inventing a bespoke ABI metrics callback would be speculative generality with no committed
  demand.

## Decision

### D1 — The C-ABI surface: functions and opaque handles

Extending `20260722-embed-libconduit-v1.md`'s AC-8 sketch with the B2 builder, `Import`, the
per-pipeline lifecycle verbs, and the free functions a complete surface needs:

```c
// Version negotiation (see D7).
uint32_t conduit_abi_version(void);

// Engine lifecycle — mirrors B1's New/Close.
conduit_engine_t* conduit_engine_new(const char* options_json, conduit_error_t** out_err);
conduit_error_t*  conduit_engine_close(conduit_engine_t* engine);
void              conduit_engine_free(conduit_engine_t* engine);

// Run lifecycle — mirrors B1's Engine.Run/Handle.Stop. Named conduit_run_t, not
// conduit_handle_t, to avoid colliding with "handle" as the generic term for every
// opaque pointer in this table — the same kind of acknowledged, documented wart
// AC-1 accepted for the root/pkg.conduit name collision, not an oversight.
conduit_run_t*    conduit_engine_run(conduit_engine_t* engine, conduit_error_t** out_err);
conduit_error_t*  conduit_run_stop(conduit_run_t* run, int64_t timeout_ms);
void              conduit_run_free(conduit_run_t* run);

// Pipeline provisioning — mirrors B1's Import/StartPipeline/StopPipeline.
conduit_error_t*  conduit_engine_import(conduit_engine_t* engine, const char* pipeline_json);
conduit_error_t*  conduit_engine_start_pipeline(conduit_engine_t* engine, const char* pipeline_id);
conduit_error_t*  conduit_engine_stop_pipeline(conduit_engine_t* engine, const char* pipeline_id,
                                                bool force);

// Pipelines-in-code (B2) — a stateless, engine-independent validate+build call; see D1.3
// for why the builder itself never crosses the ABI as a chain of handles.
bool              conduit_pipeline_build(const char* builder_spec_json, char** out_pipeline_json,
                                          conduit_error_t** out_err);

// Cleanup — every out-param above (conduit_error_t*, char*) must be freed exactly once
// through these, never through the host language's own allocator.
void              conduit_error_free(conduit_error_t* err);
void              conduit_string_free(char* s);
```

All functions are `extern "C"`, prefixed `conduit_`, and exported from a single shared library
named `libconduit.{so,dylib,dll}` — the name AC-1 (in the merged B1 doc) explicitly reserved for
this artifact rather than the Go package, precisely to avoid the "two things called `libconduit`"
confusion that alternative was rejected over.

### D1.1 — Why JSON envelopes, not raw structs

Unchanged from AC-8's reasoning, restated because it now governs more surface area: cgo cannot
safely pass Go pointers, slices, or interfaces across the ABI boundary. `options_json` and
`pipeline_json` are UTF-8 JSON strings the Go side parses with `encoding/json` into `Options`
(the JSON-representable subset — see D8.2) and `PipelineConfig` respectively; `out_pipeline_json`
is the reverse. This is not a new decision — it is AC-8's decision, applied consistently to every
function this doc adds.

### D1.2 — Error struct as one JSON string, not discrete C fields

```c
typedef struct conduit_error {
    char* json; // {"code":"...", "message":"...", "configPath":"...", "suggestion":"...",
                //  "fix":{"configPath":"...","op":"...","value":"..."}, "docsUrl":"..."}
} conduit_error_t;
```

A single JSON string, not one C field per `ConduitError` field, for three reasons: (1) it matches
the existing CLI/API/MCP wire convention (`status.go`'s `google.rpc.Status`/`ErrorInfo` mapping)
instead of inventing a fourth encoding of the same error; (2) `Fix` is itself a nested structured
object — flattening it into top-level C fields either loses the nesting or needs a second C struct
with its own ownership rules, for no benefit; (3) it is forward-compatible by construction: a
future `ConduitError` field needs a JSON key addition, not a C-ABI/struct change, consistent with
the announce → warn → remove discipline already governing `ConduitError`'s Go shape.

### D1.3 — Why the B2 builder crosses as one data payload, not a chain of handles

Two shapes were considered for exposing the pipelines-in-code builder across the ABI:

1. **A handle per builder object**, with one C function per `With...` method
   (`conduit_pipeline_builder_with_connector(...)`, etc.), mirroring Go's fluent chain call-for-
   call. Rejected: it multiplies the opaque-handle surface by the number of builder types (four:
   `PipelineBuilder`, `ConnectorBuilder`, `ProcessorBuilder`, `DLQBuilder`) and the number of
   methods on each, each needing its own ownership/lifetime story (when is a `ConnectorBuilder`
   handle freed — when it's attached to a pipeline, or does the caller still own it after?). It
   also means one cgo call **per chained method** on a pipeline definition — a pipeline with three
   connectors and five processors is a dozen-plus round trips across a boundary that is not free
   (Go's own cgo call overhead is on the order of ~100ns–1µs per call, dwarfing the actual work of
   setting one string field).
2. **(Chosen) One data-in, data-out call.** Each binding's own builder is entirely idiomatic to its
   host language and lives entirely in host-language memory — a Python `PipelineBuilder` with
   fluent methods or keyword arguments, a Node object literal or fluent chain — assembling a plain
   JSON document isomorphic to what Go's `PipelineBuilder.Build()` would produce _before_
   enrichment. Only the terminal step crosses the ABI once: `conduit_pipeline_build` runs the same
   `Enrich`-then-`Validate` sequence Go's `Build()` runs, and returns the **raw, unenriched**
   pipeline JSON back out (matching `Build()`'s own documented behavior exactly — see D8.4 for why
   this specific detail is a frozen contract, not an implementation accident) or a `ConduitError` if
   validation fails. This is simpler, needs zero opaque handles for the builder itself, costs one
   cgo call per pipeline (not per field), and keeps each binding's builder ergonomics fully
   idiomatic — consistent with the Python connector SDK doc's own stance that a good language
   binding is "idiomatically [X] rather than a transliteration."

`conduit_pipeline_build` is deliberately **not** a method on `conduit_engine_t*` — Go's own
`NewPipeline(...).Build()` needs no `*Engine` either (it is a package-level builder, not an
`Engine` method), so a binding can validate a pipeline definition before constructing, or without
ever constructing, an engine — the same benefit Go embedders already get.

### D2 — Memory ownership and threading model across the cgo boundary

**Ownership:**

- `conduit_engine_t*` / `conduit_run_t*` are opaque pointers wrapping a `runtime/cgo.Handle`
  referencing the real Go `*conduit.Engine` / `*conduit.Handle` value — `cgo.Handle` keeps the
  referenced Go value reachable and non-moving for as long as a C caller holds the corresponding
  opaque pointer. They are released only via `conduit_engine_free` / `conduit_run_free`, which
  delete the `cgo.Handle` entry _and_ release the underlying Go resource (mirroring `Engine.Close`/
  the drain path). **Calling any other function on a handle after it has been freed is undefined
  behavior** — the C ABI has no compiler-enforced move-after-free protection, unlike Go's own
  `-race`-detectable data races. This is documented, not detected, matching the stance
  `20260722-embed-libconduit-v1.md`'s failure mode 8 already takes toward Go-side lifecycle misuse
  ("programmer errors, not runtime conditions"), extended here because C offers strictly weaker
  guardrails than Go does.
- `conduit_error_t*` is **not** a `cgo.Handle` — its JSON payload is copied into a
  `C.CString`-allocated buffer at the moment the error is constructed, so the caller holds a plain,
  GC-independent block of memory, freed only via `conduit_error_free` (which calls `C.free`
  internally — the same convention Go's own `-buildmode=c-shared` output already uses for `char*`
  return values). This is deliberately different from the engine/run handles: an error must remain
  safely readable and freeable even after the engine that produced it has already been freed (a
  caller logging an error during cleanup, for instance).
- `char*` outputs (`out_pipeline_json`, etc.) follow the identical `C.CString`/`conduit_string_free`
  convention. Freeing them with the host language's own allocator (e.g., calling libc `free()`
  directly from a Python `ctypes` binding instead of `conduit_string_free`) is a concrete new
  footgun bindings must never expose to application code — application code touches no raw
  pointers at all; only the binding's own adapter layer does.
- Input strings (`options_json`, `pipeline_json`, `builder_spec_json`) are caller-owned: Go copies
  out of them synchronously (`C.GoString`) before any function returns, so the caller may free them
  immediately after the call returns.

**Threading — single-writer-per-handle, and where that discipline must live:**

`20260722-embed-libconduit-v1.md`'s AC-8 already states "language bindings are responsible for
serializing access" to a handle. This doc makes explicit **where**, because both target host
runtimes are capable of running native code across more than one OS thread even when the
scripting-language caller writes ordinary-looking sequential code:

- **Python**: `ctypes`/`cffi` calls release the GIL for the duration of the native call by design
  — two Python threads (or two `asyncio` tasks each dispatched to a thread-pool executor, the
  pattern D4 recommends) can genuinely execute two C-ABI calls on the same handle concurrently even
  though the Python-level code looks like it "awaits in order." The binding's native adapter layer
  — not application code, and not a documentation note — must hold a per-handle lock (e.g., one
  `threading.Lock` per `Engine`/`Handle` wrapper) so a caller cannot violate single-writer-per-
  handle by writing normal `async`/`await` code.
- **Node.js**: N-API async work runs on libuv's threadpool; the same risk applies — a naive addon
  dispatching two overlapping libuv workers against the same handle races exactly like two
  concurrent Go-level `Stop` calls would, except now with an added C-boundary reentrancy risk. The
  native addon must serialize per-handle access itself (a mutex or single-flight queue keyed by
  handle), not rely on JS-level `await` ordering, which provides no such guarantee across
  threadpool workers.

Both bindings therefore own a strictly harder threading problem than a pure Go-to-Go embedder does:
Go's own `sync.Once`/`atomic.Bool` guards inside `Engine`/`Handle` (per B1) protect Go-side
invariants from concurrent Go callers, but do not — and cannot — protect against two native-thread
callers racing at the C-ABI call-sequencing level (e.g., a `Stop` racing a `Free` from two threads
that never coordinate above the ABI).

**Panics crossing the FFI boundary:** a Go panic that unwinds across a cgo call boundary uncaught
is **fatal to the entire host process** — unlike a Go-to-Go panic, which any caller up the stack
can `recover()`, a panic reaching a cgo boundary cannot be caught by the C caller at all; the Go
runtime aborts the process. Every exported C function must therefore wrap its Go body in a
top-level `defer`/`recover()` that converts any recovered panic into a `conduit_error_t` (a generic
internal-error code, the panic value as the message, and a note that it indicates a Conduit bug to
report) rather than letting it propagate. This is a genuinely new requirement B1's Go API never
needed — a Go-to-Go caller can always recover a panic itself — stated here explicitly because it
must hold for every function in D1's table, not just the obviously risky ones, and because D8.5
depends on it remaining true as B1/B2 evolve.

### D3 — Error propagation: `ConduitError` → C struct → per-language exception

Per-language idiomatic translation of the one JSON payload in D1.2:

- **Python**: `conduit.ConduitError(Exception)` with `.code` (the dotted `Reason` string, e.g.
  `"connector.plugin_not_found"` — not the numeric gRPC code, since `Reason` is documented in
  `conduiterr.go` as "the source of truth for docs, llms.txt, and UI rendering," and the binding
  preserves that, not the transport-level status), `.message`, `.config_path`, `.suggestion`,
  `.fix`, `.docs_url` — parsed from the JSON once, at the binding's adapter boundary. Application
  code never sees a raw JSON string or a bare code.
- **Node.js**: `class ConduitError extends Error` with the same fields as camelCase properties
  (`code`, `configPath`, `suggestion`, `fix`, `docsUrl`) — matching the JSON's own key casing (see
  D8.1) so translation is a straight `JSON.parse`, with no per-binding re-casing logic needed.
- Every function in D1 that can fail returns its error via an out-param or return value — **never**
  by throwing/panicking across the boundary (D2) — so each binding's adapter layer is the _only_
  place a raw error becomes a language exception. This is the same principle the vision doc states
  as "errors teach across the boundary," now verified true for the C-ABI specifically, not assumed.

### D4 — Python binding

- **Package name**: a new PyPI package, distinct from both `conduit-connector-sdk` (the Python
  _connector-authoring_ SDK — persona A, a different persona entirely) and the `libconduit` shared
  library it wraps. Proposed: `conduit-embed` (mirrors "the embedder" persona naming the vision doc
  already uses).
- **FFI mechanism**: `ctypes` (Python stdlib) is the recommended default over `cffi` or a native
  extension (`pybind11`/`nanobind`) — the ABI's surface is deliberately small and JSON-string-based
  (D1.1), so there is no complex struct marshaling that would justify a heavier tool, and `ctypes`
  needs no C compiler at install time, which matters directly for the packaging goal below.
  `ctypes` still needs the compiled `libconduit.{so,dylib}` bundled inside the wheel — it does not
  compile anything itself, only `dlopen()`s a prebuilt binary.
- **Packaging**: a platform-specific wheel per OS/arch (via `cibuildwheel` or an equivalent
  multi-platform build), with `libconduit` resolved via a package-relative path
  (`importlib.resources`), never `LD_LIBRARY_PATH`/`PATH` lookup — directly reusing the
  `20260707-python-connector-sdk.md` lesson that a Conduit-launched (or, here, Conduit-embedding)
  process cannot rely on an inherited environment; the wheel must be the whole self-contained
  artifact.
- **Lifecycle ergonomics**: `with conduit.Engine(options) as engine:` — `__enter__` performs `New`,
  `__exit__` performs `Close` — mirroring the `New`/`Close` pairing precisely (not `Stop`, which is
  a different Go-level operation scoped to a running engine, per B1's "Lifecycle contract" doc). A
  second, separate context manager wraps `Run`/`Stop`: `async with engine.run() as run:` — kept
  distinct from the `Engine` context manager rather than collapsed into one `with` block, because
  collapsing them would imply a lifecycle guarantee (`Close` on every `Stop`, or vice versa) that
  the Go API deliberately does not make.
- **Async**: `Run`/`Stop` are Go-side blocking calls bounded by a timeout, not natively
  interruptible from a synchronous `ctypes` call. The binding runs each blocking ABI call in a
  thread-pool executor (`loop.run_in_executor`) so `await engine.run()`/`await run.stop(timeout)`
  never block the event loop — reusing the exact "dual sync/async ergonomics" precedent
  `20260707-python-connector-sdk.md` §2.1 already established for the connector SDK, for
  consistency across the org's Python surface rather than inventing a second async convention.

### D5 — Node.js binding

- **Mechanism**: N-API, not NAN or raw V8 bindings — N-API is ABI-stable across Node major
  versions, so a prebuilt addon does not need recompiling per Node release, which is the same
  self-contained-artifact goal as the Python wheel.
- **Async**: every blocking ABI call runs inside an N-API `AsyncWorker` (libuv threadpool),
  resolving a JS `Promise` — mirroring Python's thread-pool-executor choice for the identical
  underlying reason (the Go-side calls block).
- **Lifecycle ergonomics**: `const engine = await Conduit.Engine.create(options)`, `const run =
  await engine.run()`, `await run.stop(timeoutMs)`, `await engine.close()`. Node has no
  context-manager equivalent at the time of this doc; document the required `try/finally` pattern
  explicitly. TC39 explicit resource management (`using`/`await using`, Node 22+) is a closer match
  once the org's minimum-supported Node version reaches it — flagged as a future ergonomic upgrade,
  not required for a v1 binding.
- **Packaging**: prebuilt, per-platform native addons (`prebuildify`-style bundling of the addon
  plus `libconduit` itself) published to npm, so `npm install` never triggers a local compile —
  the direct Node analog of the Python wheel goal. This is flagged in §Failure modes as the single
  biggest Node-specific packaging risk: native-addon prebuild matrices across OS/arch/Node-ABI
  combinations are a well-known source of "works on my machine" breakage.

### D6 — Packaging discipline (shared across bindings)

Both bindings converge on the same discipline, restated once here rather than per-binding:
self-contained, per-platform prebuilt artifacts; no reliance on an inherited `PATH` or
`LD_LIBRARY_PATH`/`DYLD_LIBRARY_PATH` to locate `libconduit`; resolution via a package-relative
path baked in at build time. This is a direct reuse of
`20260707-python-connector-sdk.md`'s hard-won handshake/packaging lesson (§1.1.6 there: "the
connector's launch shebang/entry point must be an absolute interpreter path ... anything that
relies on `PATH`-based resolution at exec time will fail"), applied here to locating a shared
library instead of an interpreter. The new cost this doc's approach adds beyond that precedent:
`libconduit`'s own build needs a C toolchain per target platform (Go's `-buildmode=c-shared`
requires `CGO_ENABLED=1`, unlike B1/B2's pure-Go, cross-compile-for-free story) — a real CI-matrix
cost, named explicitly in §Failure modes rather than assumed away.

### D7 — ABI versioning and frozen-contract discipline

- The C symbol names (`conduit_*`) and the shared-library name (`libconduit.{so,dylib,dll}`) become
  a frozen public contract the moment the first binding ships, under the **same** announce → warn →
  remove policy AC-7 (in the merged B1 doc) already established for the Go API — extended here to a
  third surface (the first two being the Go API itself, and the JSON wire shape of `PipelineConfig`/
  `ConduitError` per D8.1). Adding a new C function is additive and safe; changing an existing
  function's signature, removing it, or renaming it is breaking and follows the same
  minimum-two-minor-release warning window.
- `conduit_abi_version()` returns a monotonically increasing `uint32`. A binding checks it against
  the version it was compiled against at load time and fails fast with an actionable error — never
  a segfault — on mismatch. This is the C-ABI's own version-negotiation mechanism, loosely modeled
  on the `PLUGIN_PROTOCOL_VERSIONS` pattern `20260707-python-connector-sdk.md` already documents for
  a different boundary, reused here for consistency across the org's cross-language surfaces.
- The JSON wire shape of `PipelineConfig` and `ConduitError` crossing the ABI is **also** frozen by
  this doc — extending AC-3/AC-7's "by name" contract-extension (which already covers their Go
  struct shape) to their JSON encoding specifically. A JSON key rename is a breaking change to every
  binding even if the underlying Go field name is untouched.

### D8 — Constraints this places on the current, frozen Go API (do now, ship later)

This section is the actual payoff of writing the design ahead of the build: concrete, small,
additive changes the B1/B2 Go surface should make **now**, while it is still young, so the C-ABI
implementation PR (Phase 2) does not have to make a breaking change to accommodate a decision that
could have been made for free today.

1. **Give `PipelineConfig` (and `Connector`/`Processor`/`DLQ`) explicit `json` struct tags today.**
   Confirmed by reading `pkg/provisioning/config/parser.go`: none of these four types carry any
   `json` tag right now. `encoding/json` would serialize them using bare, capitalized Go field names
   (`ID`, `Status`, `Connectors`, `DLQ`, ...) if marshaled as-is — neither the casing convention
   `pkg/provisioning/config/yaml/v2/model.go` already uses for the wire YAML shape (`id`, `status`,
   `dead-letter-queue` — lower/kebab-case) nor anything a Python/Node consumer would expect from a
   JSON API. Since AC-3/AC-7 of the merged B1 doc already made these types' Go shape a frozen
   public contract by name (via the `PipelineConfig` alias), adding `json` tags now is small,
   additive, and non-breaking — waiting until the C-ABI implementation PR would mean either shipping
   a casing nobody deliberately chose, or a breaking rename after bindings already depend on it.
   **Recommendation**: match the `yaml/v2` model's own key names exactly, so the future JSON
   envelope and the existing YAML wire format read as the same vocabulary in two syntaxes, not two
   divergent ones.
2. **Keep `Options`'s JSON-crossing subset explicit and separately named.** `Options` (B1) embeds
   `prometheus.Registerer`, `*slog.Logger`, and `database.DB` — all Go interfaces/objects,
   fundamentally not JSON-representable. This doc does not ask B1 to change `Options`'s Go shape (a
   Go embedder still wants to hand in a real `*slog.Logger`); it asks that the **JSON-crossing
   subset** be a documented, explicitly-named projection (e.g., a future `EmbedOptionsJSON` covering
   only `DB`/`PipelinesDir`/`API` — the fields that _are_ representable — with logging handled
   ABI-side by a level string plus an optional per-line callback, and metrics left out of v1
   entirely, see §Non-goals). Flagging this now means the eventual implementation PR does not
   discover, unplanned, that it needs an ad hoc "which `Options` fields make sense over JSON" pass.
3. **`Engine.Run`/`Handle.Stop`'s existing context-deadline contract (AC-4) needs no change — it is
   already exactly what the ABI needs.** `timeout_ms` on `conduit_run_stop` maps directly to a
   Go-side `context.WithTimeout(ctx, timeout)`; `Stop`'s "block until drain-complete-or-deadline,
   never fire-and-forget" semantic is already the correct one. This is named explicitly so a future
   change to `Stop`'s signature is evaluated against this direct mapping before it ships, not
   discovered as broken after the ABI is built.
4. **`PipelineBuilder.Build()` returning the raw, unenriched `PipelineConfig` must stay that way.**
   `conduit_pipeline_build` (D1.3) mirrors this exactly. If a future change to `Build()` started
   returning the enriched value instead, the ABI's round-trip identity with a hand-built pipeline
   (and the existing round-trip test's guarantee that a hand-built value is indistinguishable from
   a parsed YAML one) would silently break for every binding at once. This is a frozen _behavioral_
   contract of `Build()`, not just its signature — worth a comment at the call site when B2 merges.
5. **Panics anywhere in the call graph reachable from `New`/`Run`/`Stop`/`Close`/`Import`/`Build`
   must remain ordinary, recoverable Go panics — never `os.Exit`, never a fatal abort bypassing
   `defer`/`recover`.** B1 already commits to "no `os.Exit`" for the Go API; this names the
   adjacent commitment the C-ABI's panic-safety net (D2) depends on: any future code path that
   spawns a goroutine without its own `recover()` and lets it panic crashes the whole process in Go
   regardless of cgo, silently defeating the ABI's top-level recovery wrapper. This is not a new
   rule for B1/B2 today (Go-to-Go callers already tolerate an unrecovered panic by crashing, same
   as any Go program) — it is flagged here because the C-ABI's mitigation only works if it stays
   true.

## Alternatives considered

**(a) cgo C-ABI shared library — chosen.** In-process, sharing the host application's address
space and process lifetime — no extra deployable, no network hop on the hot `Import`/`Run`/`Stop`
path, matching "embeddable" as CLAUDE.md and the roadmap frame it. Cost: a C-toolchain-per-target
cross-compilation burden Go's own pure-Go builds never had (D6), a documented but
compiler-unenforced ownership/threading contract (D2), and new panic-safety plumbing B1's Go API
never needed (D2, D8.5).

**(b) A local gRPC/UDS bridge to an out-of-process engine.** Spawn or attach to a separate `conduit`
engine process over a loopback/Unix-domain-socket gRPC connection, reusing the _existing_ HTTP/gRPC
API (`Options.API`) instead of inventing a C ABI at all.

- Pro: zero new protocol — the gRPC API already exists; any language with a gRPC client gets an
  "embedding" story with less new design than a C ABI needs.
- Con, and why it loses for B3 specifically: it is **not in-process embedding** — it is a second
  process the host must spawn, monitor, and restart on crash, over IPC with serialization and
  syscall overhead on the hot path. It reintroduces precisely the subprocess-lifecycle-management
  problem B1 exists to remove, rather than solving it. It is the right answer for a different,
  legitimate use case this doc does not claim to solve: a host that wants Conduit as a managed
  sidecar rather than embedded in-process — that use case is already fully served today by `conduit
  run` with `Options.API.Enabled`, no new design required. The eventual embedding docs should
  present this as an explicit, honest alternative for embedders who don't need in-process, not omit
  it in favor of only the C-ABI path.

**(c) WASM (compile Conduit itself to a WASM module; the host embeds a WASM runtime).** Rejected on
the same grounds `20260722-wasm-component-model-deferred.md` (pending ratification) already
establishes for connector/processor _plugins_, extended here in the opposite direction — Conduit's
own engine (goroutine scheduling, real file/network I/O, a database driver, an HTTP/gRPC server) is
a poor fit for any WASM sandbox today: WASI Preview 1 has no engine-scale concurrency model, and the
component model needed for anything richer is explicitly NO-GO on a pure-Go host per that ADR.
Where WASM does fit this picture is the _opposite_ direction from B3 entirely: WASM is how a plugin
runs inside Conduit, not how Conduit runs inside a host. This doc's C-ABI is strictly "run the whole
engine inside someone else's process" — an orthogonal problem WASM was never aimed at solving here,
and the two should never be conflated in embedding docs.

## Failure modes

1. **Binding lifetime/threading bugs** (handle-after-free, concurrent same-handle calls from two
   native threads). Mitigation: the documented ownership/threading model (D2) plus an ASAN gate
   (C/cgo side) and the Go race detector (Go side) on the binding's own CI, exercised by a stress
   test hammering the ABI from many threads. Explicitly a **Phase-2-build-time** gate — not claimed
   live by this design-ahead doc, per CLAUDE.md's Process-maturity honesty rule.
2. **A language with poor FFI ergonomics, evaluated before committing.** Per CLAUDE.md's
   no-speculative-generality rule and the vision doc's own stated edge case: before the real
   implementation PR, spike Node's N-API prebuild story and Python's `ctypes`-vs-`cffi` tradeoff
   with a throwaway proof of concept. If a language's FFI story would force an awful, un-idiomatic,
   thin passthrough to hit the "same lifecycle verbs" bar, the correct call is to defer that
   language's binding — matching this doc's own treatment of Java/Ruby as demand-gated, not
   committed — rather than ship a checkbox binding to hit a roadmap line.
3. **Eager-vs-lazy resource lifecycle across FFI, mirroring B1's Go-side decision.** B1's `New`
   eagerly opens the configured database; the C ABI inherits this as-is (`conduit_engine_new` does
   the same eager open). A binding's `Engine.create()`/`Engine(options)` constructor must **not**
   defer that work to first use "for a nicer async constructor API" — doing so would silently
   change _when_ a `conduiterr.CodeUnavailable` (DB-already-locked) error surfaces relative to what
   the Go docs promise, breaking the "same errors, same timing" cross-boundary parity this doc's D3
   depends on. A binding wanting lazy-feeling construction ergonomics must still eagerly call
   `conduit_engine_new` under the hood and defer only the visible, language-level object
   construction, never the actual Go-side open.
4. **Panic/abort crossing the FFI boundary (D2).** A Go panic reaching a cgo boundary uncaught is
   fatal to the _entire host process_ — strictly worse than a Go-to-Go panic, which at least stays
   within one process and is recoverable by any caller up the stack. Mitigated only by the top-level
   `defer`/`recover`-into-`conduit_error_t` convention (D2) on every function in D1's table; any code
   path that bypasses that convention (per D8.5, an unrecovered goroutine panic) reopens this as a
   live risk that must be caught in code review of the eventual implementation PR, not assumed away
   here.
5. **Struct/JSON schema drift** between the Go API's actual `PipelineConfig`/`ConduitError` shape
   and what a binding's static type stubs (Python type hints, TypeScript `.d.ts`) claim. Same class
   of risk `20260707-python-connector-sdk.md` already flags for its own gRPC stub generation
   (`compat-nightly.yml`). Recommendation for the eventual implementation PR: an equivalent nightly
   job that marshals an example `PipelineConfig`/`ConduitError` to JSON on the Go side and diffs it
   against the committed binding schema, so a Go-side field addition or rename is caught before a
   binding silently drops or misreads a field.
6. **Cross-compilation / CI-matrix cost is itself a failure mode** in the "we ship an untested
   platform" sense: a `libconduit` build only ever built/tested on the CI runner's native platform
   but _claimed_ to support macOS/Windows/arm64 without an actual per-platform CI leg is exactly the
   kind of unverified claim CLAUDE.md's benchmark discipline ("never assert numbers without a
   benchmark in the repo") already forbids for performance — extended here to platform-support
   claims for a compiled artifact.

## Upgrade / rollback

No new serialized/persisted format is introduced by the ABI itself: `PipelineConfig`/`ConduitError`
already have (once D8.1 lands) a stable JSON shape governed by the same announce → warn → remove
policy as their Go shape — nothing on disk changes, so invariants 2 and 5's "versioned migration
path" requirement does not apply here, consistent with how the merged B1 doc treats its own
Upgrade/rollback section.

The ABI/symbol versioning story (D7) **is** the rollback story for the compiled artifact itself:
`conduit_abi_version()` lets a binding detect a mismatched `libconduit` at load time and fail with
an actionable "rebuild/reinstall your binding, ABI vX required, vY found" error rather than a
segfault or silent misbehavior. An embedder rolls back by reinstalling the prior binding version
(which bundles the matching prior `libconduit` build, per D6's packaging discipline) — the same
"ordinary module rollback, no coordinated migration" story AC-7 already establishes for B1's Go
API, applied here to a compiled artifact instead of a `go.mod` dependency.

A breaking ABI change (a symbol removed or resignatured) follows the two-minor-release warning
window from D7; a breaking JSON-shape change to `PipelineConfig`/`ConduitError` follows the same
window it already follows on the Go side (AC-7) — enforced across one more surface, not a separate
policy.

## Testing (what a real implementation PR must add — not proposed as done now)

Per CLAUDE.md's Process-maturity honesty rule, every item below is a requirement on the _eventual_
Phase-2 implementation PR, not a claim about this design-ahead doc:

- An ASAN gate (C side) and `-race` (Go side) on any implementation PR.
- A same-handle-concurrent-call stress test — extending the merged B1 doc's own "Stop idempotency"
  and "two-engine-in-one-process" test pattern across the ABI — asserting no crash/UB under
  `-race`+ASAN for concurrent `Stop` calls, at minimum. A call issued after `Free` is documented as
  undefined behavior (an honest "should fail cleanly, not crash" best-effort target, not a
  guarantee C itself can make).
- The nightly binding-schema-drift job (Failure mode 5).
- A per-language "first embedded pipeline in under 15 minutes" CI-timed example — reusing, not
  relaxing, the exact acceptance criterion `20260705-sdk-and-embedding-dx.md`'s B3 section already
  committed to; this doc specifies the mechanism (D1–D7) that has to exist before that bar can be
  measured at all.

**Observability**: the `Options.Logger`/`MetricsRegisterer` injection story from B1 (AC-2) does
**not** cross the ABI as arbitrary objects (D8.2) — instead, each binding gets a log-level string
plus an optional native callback the Go side invokes per structured log line (itself a small JSON
envelope: level, message, fields), so a host can pipe Conduit's logs into its own logging without
implementing an `slog.Handler` in Go. Metrics are out of scope for a v1 binding entirely (see
§Non-goals): the existing `Options.API`-exposed `/metrics` route, or the host's own per-language
Prometheus client reading it, already answers this without a bespoke ABI metrics callback.

## Related

- `docs/design-documents/20260705-sdk-and-embedding-dx.md` — the persona-B / B3 vision this doc
  makes concrete; its Phase-1/Phase-2 mapping is corrected in this same change (see the
  accompanying drift fix) to point back here.
- `docs/design-documents/20260722-embed-libconduit-v1.md` — the merged B1 (+ B2 in review) doc this
  design constrains via D8; its AC-8 is this doc's starting sketch, completed and extended here.
- `docs/design-documents/20260707-python-connector-sdk.md` — the packaging/handshake lessons
  (no inherited `PATH`, self-contained artifacts) this doc's Python binding packaging (D4, D6)
  reuses directly, for a different boundary (a bundled shared library, not a bundled interpreter).
- `docs/architecture-decision-records/20260722-wasm-component-model-deferred.md` (pending
  ratification) — grounds why WASM is rejected as the in-process embedding mechanism (Alternatives,
  (c)), and clarifies why `libconduit`'s own CGO-based build does not violate its no-CGO principle
  (Context).
- `docs/architecture-decision-records/20260704-single-node-engine.md` — the engine-scope ADR this
  doc's C-ABI surface stays consistent with (no clustering primitives exposed across the boundary).
- `pkg/foundation/cerrors/conduiterr/conduiterr.go` — the `ConduitError` shape this doc's error
  propagation (D3) is grounded in.
- `conduit.go`, `doc.go` (B1, merged) and `builder.go` (B2, in review) — the exact Go surface D8's
  constraints apply to.
- `ROADMAP.md`, "Embedded v1" (Phase 1) — note: `ROADMAP.md` itself still lists the C-ABI/bindings
  bullet under the same "Embedded v1" heading without a Phase-1/Phase-2 split, and
  `docs/design-documents/20260704-phase-1-execution-plan.md` (lines ~343–346) has the identical
  drift. Both are out of scope for this PR (scoped to the one file named in the task), flagged here
  as a fast-follow so the same correction is not lost.
