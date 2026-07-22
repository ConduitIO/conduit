# Rust connector SDK (gRPC-standalone)

## Summary

Design and phased plan for `conduit-connector-sdk-rust`, a new repo delivering a production
Rust SDK for building Conduit **source and destination connectors** that run as standalone
(subprocess, gRPC, go-plugin-handshake) plugins — the same mechanism Go and the planned Python
SDK use, no Conduit-side code changes required. This document is planning/design only; it does
not implement the SDK.

This is a **deliberate course correction**, not a natural next tier. ROADMAP.md and
[SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md) currently frame Rust as
"the WASM component-model reference [language]; proves the WASM connector architecture" — a
proof-of-concept role, not a production SDK. That framing assumed WASM would become a real
connector-extension mechanism. It didn't:
`conduit-v019-plans/wasm-connector-go-no-go.md` is a **NO-GO** on shipping a WASM connector
runtime in the v0.19 binary, and ships instead "a WIT-interface RFC + a Rust reference component
... built and tested on an experimental CGO/wasmtime branch that is not part of the release
binary." Under that plan, Rust connector scaffolding was headed toward "RFC + reference
implementation, not a supported `--lang rust` scaffold path a user should build production
connectors against" (`conduit-v019-plans/v019-execution-plan.md`, Workstream 4, NO-GO branch).

DeVaris's call, which this document executes: give Rust the same production tier Python already
has planned, using the mechanism that already works — gRPC-standalone — so
`conduit connector new --lang rust` scaffolds something that actually runs, rather than an RFC a
user can't ship. The Rust WASM reference component (proving the WIT interface, on the
experimental wasmtime branch) is unaffected and continues as separate, non-production spec work;
this SDK does not touch it and is not a substitute for it. **Consequences** covers the doc
updates (ROADMAP.md, the DX doc, the v0.19 plan) this decision requires once accepted.

The technical crux, identical in kind to Python's, is replicating HashiCorp go-plugin's
subprocess handshake — but from a language with no async-trait-object story as settled as
Python's and no equivalent of `grpcio`/`grpc.aio` as an off-the-shelf, fully-featured go-plugin
counterpart. The handshake itself is protocol, not Go, and is fully characterized below,
carried forward from the same verified go-plugin v1.8.0 source the Python doc cites. The harder,
Rust-specific problem is the **two internal go-plugin gRPC services** (`GRPCController`,
`GRPCBroker`) that have no existing Rust crate — Section 1.5 treats this honestly, not as a
solved problem copied from Python, though independent review found its actual surface area
small; the shutdown-hang semantics and trait-shape decision in Section 2 are the bigger risks
(see Consequences for the re-ranked order).

## Context

### The problem

- No Rust connector SDK exists. `conduit-connector-sdk` (Go) is the only mature SDK; the Python
  SDK is itself still design-only (`20260707-python-connector-sdk.md`). Rust has **no prior art
  in this repo family at all** for the gRPC-standalone path — unlike Python, which had a
  WASM-processor sibling repo to borrow codegen precedent from, there is no
  `conduit-connector-sdk-rust` prototype and no known published Rust crate implementing the
  go-plugin server side. This is flagged explicitly in Risks, not assumed away.
- "A connector" is defined by conformance to `conduit-connector-protocol` and by passing the
  acceptance-test suite (`AcceptanceTest`, `conduit-connector-sdk/acceptance_testing.go:54`) —
  not by language. That suite exists only in Go today, and — a finding worth stating plainly
  because the task framing assumed otherwise — **it is not a subprocess/gRPC black-box
  harness**. Reading `acceptance_testing.go`, `AcceptanceTestDriver.Connector()` returns an
  in-process Go `Connector` (the struct with `NewSource`/`NewDestination` factories that
  `NewSourcePlugin`/`NewDestinationPlugin` wrap directly, `source.go:139`, `destination.go:104`).
  The Go suite tests the **trait/interface contract in-process**, never the actual go-plugin
  handshake or wire encoding. That has a real consequence for this doc's acceptance-harness
  design (§4): "drive a Rust connector from the Go harness" is not something that exists to
  reuse as-is — building it would mean adding subprocess-driving capability to the Go harness
  first, which is out of scope here and named as a separate, cross-SDK opportunity in Related.
- Standalone connectors are launched as **subprocesses** using HashiCorp go-plugin over gRPC
  (`conduit/pkg/plugin/connector/standalone/dispenser.go:55` →
  `conduit-connector-protocol/pconnector/client/client.go`). A Rust binary must replicate that
  launch protocol exactly or Conduit cannot start it — a hard gate before any connector logic
  matters, identical in kind to Python's Phase 1 gate.

### Constraints (from CLAUDE.md, binding on this design)

- No speculative generality: this SDK targets the gRPC-standalone connector path only. It is not
  a WASM SDK and must not grow WASM ambitions without a documented demand signal — that track is
  owned separately by the go/no-go doc's reference-component work.
- Design doc before code: this **is** that doc for the SDK's own public surface (traits, config
  model, wire behavior, error taxonomy). Downstream connector authors build against what's
  decided here from v0.1 — treat it as a public contract.
- Docs and template move with the code: the phased plan ships a runnable example connector, not
  a library with no worked path.
- Errors are API: every user-facing error needs a stable shape and actionable message.

### Relationship to the Rust WASM reference component (not reusable, different mechanism)

The go/no-go doc's Rust deliverable — a `cargo-component` + `wit-bindgen` component compiled
against a WIT world, run on an experimental `wasmtime-go`/CGO branch **not in the release
binary** — targets an entirely different plugin kind (in-process WASM component, not a
subprocess) and an entirely different toolchain (`wit-bindgen`, not `tonic`/`prost`). It proves
the WIT interface shape is expressible in Rust; it says nothing about gRPC, go-plugin, or
`tokio`. **Do not conflate the two.** This SDK does not depend on that work, and that work does
not need to track this SDK. The two share only a language, the way `conduit-processor-sdk-python`
(WASM) and this design's Python sibling share only a language.

## Goals / Non-goals

### Goals

- A `Source`/`Destination` trait API idiomatic to Rust (async, `Result`-based errors, no
  transliteration of Go's `(n, err)` shape) that a connector author can implement and compile to
  a standalone binary Conduit runs unmodified.
- Full protocol v2 coverage: `Configure`/`Open`/`Run` (bidi)/`Stop`/`Teardown`/lifecycle hooks,
  for both `SourcePlugin` and `DestinationPlugin`, plus `SpecifierPlugin.Specify`.
- Config parameter generation with parity to the Go SDK's `config.Parameter`/`Validation` model
  (`ParameterType`: String/Int/Float/Bool/File/Duration; `ValidationType`:
  Required/GreaterThan/LessThan/Inclusion/Exclusion/Regex — confirmed against
  `conduit-commons@v0.6.0/config/parameter.go:35-41` and `config/validation.go:37-44`).
- A working, CI-verified worked example connector, launched by a real unmodified Conduit binary.
- A path to an acceptance-equivalent test suite so "a Rust connector" is a checkable claim.

### Non-goals (this doc)

- WASM/component-model connectors — separate track, separate doc, separate go/no-go.
- `conduit connector new --lang rust` CLI integration — Phase 3 here, and gated on the
  `conduit-v019-plans` Workstream 4 sequencing decision (which this doc's existence answers:
  Rust connector scaffolding can now target this SDK instead of the RFC-reference branch).
- TypeScript — untouched by this decision; TS remains on its own (WASM/`componentize-js`) track.
- Schema registry / Avro encoding parity — deferred to Phase 2, matching the Python plan.

## Decision

### 1. Protocol & handshake

#### 1.1 The handshake is protocol, not Go

Conduit (client) and any SDK (server) share the same `HandshakeConfig` values — not matching by
convention, but importing (Go) or hard-coding the identical constant (non-Go):

```go
// conduit-connector-protocol/pconnector/pconnector.go:19-22
var HandshakeConfig = plugin.HandshakeConfig{
    MagicCookieKey:   "CONDUIT_PLUGIN_MAGIC_COOKIE",
    MagicCookieValue: "204e8e812c3a1bb73b838928c575b42a105dd2e9aa449be481bc4590486df53f",
}
```

- Server (SDK) side: `pconnector/server/serve.go:42` passes it into `plugin.ServeConfig` and
  registers **both** protocol versions in `VersionedPlugins` (`serve.go:43-46`) — v1
  (`pconnector/v1/version.go:19`, `Version = 1`) and v2 (`pconnector/v2/version.go:18`,
  `Version = 2`).
- Client (Conduit) side: `pconnector/client/client.go:47-48` passes the identical value into
  `plugin.ClientConfig`, also registering both versions (`client.go:49-60`). Conduit's dispenser
  delegates to this `client.New(...)` (`conduit/pkg/plugin/connector/standalone/dispenser.go:55`)
  rather than constructing its own.
- These citations are the same ones independently byte-verified for the Python doc's review
  (2026-07-07, "Handshake independently byte-verified against go-plugin v1.8.0 — the crux is
  sound"). The handshake is language-agnostic by construction; nothing below is Python-specific
  and none of it needs re-deriving, only re-implementing in Rust.

A Rust plugin binary must, in order:

1. Read `CONDUIT_PLUGIN_MAGIC_COOKIE` from the environment and compare it to the value above
   before printing anything to stdout; exit non-zero with a stderr diagnostic on mismatch or
   absence. Nothing in go-plugin's protocol enforces this from the client side beyond requiring
   _a_ handshake line — a wrong or missing check is silently the SDK's own bug, not caught
   upstream.
2. Negotiate the app-protocol version via `PLUGIN_PROTOCOL_VERSIONS`
   (`go-plugin@v1.8.0/client.go:642`), picking the highest version the client also lists
   (`server.go:148-221`). **Target v2** — same rationale as Python: v1
   (`proto/connector/v1/connector.proto`, 368 lines, single-service) is the legacy shape; v2
   (`proto/connector/v2/{source,destination,specifier}.proto`) is current.
3. Print exactly one line to **stdout** once the gRPC server is listening:

   ```text
   CORE-PROTOCOL-VERSION|APP-PROTOCOL-VERSION|NETWORK|ADDRESS|PROTOCOL|SERVER-CERT
   ```

   `CORE-PROTOCOL-VERSION` is always literal `1` (go-plugin's own core protocol, unrelated to
   the connector-protocol v1/v2 negotiated above); `APP-PROTOCOL-VERSION` is `2`; `NETWORK` is
   `tcp`; `ADDRESS` is the bound listen address; `PROTOCOL` is literal `grpc` (NetRPC is disabled
   both sides via `plugin.NetRPCUnsupportedPlugin`); `SERVER-CERT` is empty — Conduit's client
   never sets `AutoMTLS: true` (absent from `client.go`), so no TLS is in play. Format and field
   semantics: `go-plugin@v1.8.0/server.go:426-445`; parse side: `client.go:838-926`.
4. Bind **TCP** on `127.0.0.1:0` (OS-assigned port), not a Unix socket — same reasoning as
   Python: the client-side parser is transport-agnostic (`client.go:888-901`), and TCP is
   trivial with `tokio::net::TcpListener` / `tonic::transport::Server::serve` with no
   Unix-socket temp-dir/permission bookkeeping, and it is free Windows support (no platform
   branch) matching the Windows CI bar this doc's CI matrix also carries.
5. Serve, on that listener, a plain `tonic` gRPC server registering:
   - **`grpc.health.v1.Health`**, set to `SERVING` — `tonic-health` provides this directly
     (`tonic_health::server::health_reporter()`), low effort.
   - **`GRPCController.Shutdown`** — Conduit's `Close()` RPCs this on teardown
     (`grpc_client.go:106-108`); unimplemented, it falls back to a ~2s force-kill
     (`client.go:530-567`) — functional but not graceful, and a direct hit against invariant 7
     (graceful shutdown by default). **Must implement, and must return within a bounded time no
     matter what** — see §2.5 for why the ~2s fallback does not rescue a handler that hangs
     rather than erroring.
   - `GRPCBroker` — exists for nested/multiplexed plugin scenarios; the connector protocol's
     three services run on the single primary connection, so **a no-op broker stub suffices**;
     no need to reimplement go-plugin's internal yamux multiplexer. Low-risk surface: it's one
     bidi RPC, and Conduit's own client discards the `StartStream` error, so a stub costs little.
   - **`GRPCStdio`** — **not served**. Conduit's client dials `GRPCStdio.StreamStdio` on connect
     but degrades gracefully on `Unavailable`/`Unimplemented` (logged and ignored), never
     treating it as fatal to the handshake. Stderr/stdout forwarding already works via
     go-plugin's own process-level stdout/stderr capture, independent of this RPC. This SDK
     vendors the `.proto` (§1.5, §3) for a uniform codegen pipeline but implements no server for
     it — one less RPC surface to get right in Phase 0.
   - gRPC reflection — optional polish (`tonic-reflection`), aids `grpcurl` debugging.
6. Exec with a clean environment. Conduit's dispenser sets `cmd.Env = make([]string, 0)`
   (`client.go:45`) before appending its own vars — the subprocess gets **no inherited `PATH`**.
   Practical consequence: the release artifact must be a single, statically-resolvable binary
   (a static or near-static Rust binary is the natural fit here — better than Python's
   interpreter-resolution problem, see Alternatives) invoked by absolute path; no shebang/`PATH`
   resolution at exec time.

#### 1.2 Env vars a Rust subprocess reads

| Var | Source | Purpose |
| --- | --- | --- |
| `CONDUIT_PLUGIN_MAGIC_COOKIE` | go-plugin | handshake cookie, must match §1.1 |
| `PLUGIN_PROTOCOL_VERSIONS` | go-plugin | negotiate app-protocol version |
| `CONDUIT_CONNECTOR_UTILITIES_GRPC_TARGET` | `pconnutils/env_vars.go:18` | connector-utilities gRPC address (schema registry, etc.) |
| `CONDUIT_CONNECTOR_TOKEN` | `pconnutils/env_vars.go:19` | auth token for calling back into Conduit |
| `CONDUIT_CONNECTOR_ID` | `pconnector/env_vars.go:18` | this connector instance's ID |
| `CONDUIT_CONNECTOR_LOG_LEVEL` | `pconnector/env_vars.go:19` | log level to honor |
| `CONDUIT_CONNECTOR_MAX_RECEIVE_RECORD_SIZE` | `pconnector/env_vars.go:20` | gRPC max message size |

Confirmed directly against `pconnector/env_vars.go` and `pconnector/config.go`
(`PluginConfig{Token, ConnectorID, LogLevel, Grpc GRPCConfig{ConnectorUtilitiesTarget,
MaxReceiveRecordSize}}`) — same fields the Go SDK's `serve.go:118-150` reads.
`PLUGIN_UNIX_SOCKET_DIR`/`PLUGIN_CLIENT_CERT` are irrelevant given the TCP/no-AutoMTLS choice.

#### 1.3 RPC surface (protocol v2)

Confirmed directly from `proto/connector/v2/{source,destination,specifier}.proto`:

- **`SourcePlugin`**: `Configure` (unary) → `Open` (unary, takes last-acked `position`) → `Run`
  (**bidi stream**: plugin → Conduit is `repeated Record`, Conduit → plugin is
  `repeated ack_positions` — independently, concurrently, per-message) → `Stop` (unary, plugin
  returns `last_position`) → `Teardown` (unary). Plus `LifecycleOnCreated`/`OnUpdated`/
  `OnDeleted` (unary).
- **`DestinationPlugin`**: mirror shape — `Run` is bidi with Conduit → plugin `repeated Record`
  and plugin → Conduit `repeated Ack{position, error}` (one `error` string per record, empty on
  success). `Configure`/`Open`/`Stop` (Conduit → plugin `last_position`)/`Teardown` plus the same
  three lifecycle hooks.
- **`SpecifierPlugin`**: `Specify` (unary) → `Specification{name, summary, description, version,
  author, source_params, destination_params}`.

`Run` is the only structurally interesting RPC; everything else is unary request/response and
maps onto `tonic`'s generated unary trait methods with no special handling.

#### 1.4 Wire record shape

Confirmed from `conduit-commons@v0.6.0/proto/opencdc/v1/opencdc.proto:91-131` (identical to what
the Python doc verified — this is not re-derived, it's the same wire contract):

```protobuf
message Record {
  bytes position = 1;
  Operation operation = 2;             // OPERATION_{CREATE,UPDATE,DELETE,SNAPSHOT} = 1..4
  map<string, string> metadata = 3;
  Data key = 4;
  Change payload = 5;
}
message Change { Data before = 1; Data after = 2; }
message Data { oneof data { bytes raw_data = 1; google.protobuf.Struct structured_data = 2; } }
```

Go's `opencdc.Data` interface (`Bytes()`, `Clone()`, `ToProto()`,
`conduit-commons@v0.6.0/opencdc/data.go:29-34`) exists to unify `RawData []byte` and
`StructuredData map[string]interface{}` behind one contract. At the wire level it's a two-way
`oneof`. Rust's enum is a structurally exact fit with no interface indirection needed:

```rust
pub enum Data {
    Raw(Vec<u8>),
    Structured(StructuredData), // BTreeMap<String, Value>-shaped, bridges prost_types::Struct
}
```

**The same `Struct` fidelity gap the Python review flagged (B3) applies identically here** —
this is a protocol-level fact, not a Python bug: `google.protobuf.Struct` only carries
JSON-like values (`google.protobuf.Value`'s `number_value` is `double`), so a Rust `i64` field
inside `StructuredData` silently becomes a `f64` on the wire (`{"count": 5}` → `5.0`; precision
loss above 2^53). This SDK inherits that boundary and must document it identically, with
round-trip property tests asserting identity where representable and an explicit documented
limit where not (see Testing).

#### 1.5 Codegen: `buf generate` with Rust remote plugins

**Recommendation: `buf generate` against `buf.build/conduitio/conduit-connector-protocol`, using
the community `buf.build/community/neoeinstein-prost` (message types) and
`buf.build/community/neoeinstein-tonic` (gRPC service stubs) remote plugins**, mirroring the
org's existing `buf generate`-against-BSR pattern (`conduit-processor-sdk-python`'s
`proto/buf.gen.yaml`, and this design's own §1.5 sibling in the Python doc) rather than adopting
a third, divergent Rust-specific toolchain. `prost`/`tonic` are the de facto standard Rust
protobuf/gRPC stack; there is no betterproto-equivalent debate here — `prost-build`/`tonic-build`
are the only serious option, unlike Python where `betterproto` was a real (rejected)
alternative.

**The unresolved, Rust-specific part (flagged, not glossed over): go-plugin's own internal
services (`GRPCController`, `GRPCBroker`, `GRPCStdio`) have no published `.proto` on BSR and no
existing Rust implementation to build against.** Go generates their stubs from `.proto` files
vendored inside `hashicorp/go-plugin`'s own module (`internal/plugin/*.proto` in that repo, per
the citations already used in §1.1 for `grpc_server.go`/`grpc_client.go`). Phase 0 of this SDK
must: (a) locate and vendor those exact `.proto` files from the pinned `go-plugin v1.8.0` tag,
(b) compile them with the same `tonic-build` pipeline, (c) hand-verify the generated
`GRPCController::Shutdown` server trait matches what `client.go:530-567`'s `Close()` call
actually sends. This is real, unavoidable, first-of-its-kind work for this SDK, called out here
rather than assumed away. **It is not, however, the single largest risk in this document** —
independent review against the actual go-plugin surface found both internal services small:
`GRPCController` is one `Shutdown(Empty) -> Empty` RPC, and `GRPCBroker` is one bidi RPC whose
`StartStream` error Conduit's own client discards, so a no-op stub is fine. The bigger risks are
the shutdown-hang semantics (§2.5) and the trait-shape decision (§2.1) — see Consequences for
the re-ranked order. No existing Rust crate (`go-plugin-rs`, or similar) was found to shortcut
the vendoring; if one surfaces during implementation it should be evaluated, but this plan does
not assume it exists.

### 2. Idiomatic Rust API design

Design goal, same brief as Python: feel like a native Rust library — `async`, `Result`, traits
with default methods for forward compatibility — not a mechanical port of Go's interfaces.

#### 2.1 Async runtime: tokio, `async_trait` for object safety

The `Run` RPC is bidirectional: a source must emit records **and** receive ack callbacks
concurrently on one logical stream; a destination must receive record batches **and** emit acks
concurrently. `tonic`'s generated streaming methods return `Streaming<T>` (an async stream) and
accept a `Stream` for the response side — the natural implementation is two concurrently
running `tokio` tasks per `Run` call, joined under a `tokio::task::JoinSet` so a panic or error
in either side terminates the whole call cleanly (see §2.6 for the full design).

`Source`/`Destination` are traits invoked through a `dyn` adapter layer (the SDK's own
`SourcePluginAdapter`/`DestinationPluginAdapter`, mirroring Go's `sourcePluginAdapter`/
`destinationPluginAdapter` in `source.go:152`/`destination.go:116`), so they must be
**object-safe**. Native `async fn` in traits (stable since Rust 1.75, return-position `impl
Trait` in traits) is **not** object-safe without boxing the returned future — as of this design's
writing there is no ergonomic, stable way to call an async trait method through `dyn Trait`
without either boxing or a manual vtable. **Recommendation: the `async-trait` crate** for the
public `Source`/`Destination` trait definitions. It costs one heap allocation per call
(`Read`/`Write`/`Ack`/lifecycle hooks are not the multi-million-calls/sec hot path — the
underlying I/O to the third-party system dominates), in exchange for object safety and a
familiar `async fn` surface for authors. This is called out explicitly in Alternatives as the
one place this SDK's ergonomics lag a hypothetical future Rust (dyn-safe async traits are an
active area of the language and may make `async-trait` avoidable in a later SDK major version
— tracked as a non-breaking internal implementation swap, not a public API commitment).

**Independent review flagged a more fundamental question this section had not previously
examined: does this SDK need `dyn` dispatch at all?** A Rust connector binary compiles exactly
**one** concrete `Source`/`Destination` implementation — there is no in-process
runtime-polymorphism requirement analogous to Go's, where the dispatch layer itself must be
generic. Go's own `dyn`-style adapter is a Go idiom driven by Go's interface model, not something
the protocol requires of every language. A monomorphized generic entry point
(`serve<S: Source>(source: S)`, called from each author's own `main()`) would use native stable
`async fn` in traits with **zero boxing**, and separately sidesteps an object-safety problem this
design had not previously named: `Source`/`Destination` each carry an associated `type Config`
referenced in method signatures, so `dyn Source` is not directly usable as written — it would
require `dyn Source<Config = Concrete>`, which is exactly as concrete as the generic alternative
and gives up the polymorphism reaching for `dyn` was supposed to buy. This is captured as its own
entry in Alternatives, **not resolved here** — `async-trait` remains the working recommendation,
but the trait-shape decision must be settled deliberately before Phase 1 locks `Source`/
`Destination` as a public, semver-governed contract: changing the dispatch shape later (unlike
swapping `async-trait`'s internals for a future dyn-safe-async-trait feature under the same
signatures) is an SDK-breaking change to the trait definitions themselves.

```rust
#[async_trait]
pub trait Source: Send + Sync {
    type Config: DeserializeOwned + ToParameters;

    async fn open(&mut self, position: Option<Position>) -> Result<(), ConnectorError> { Ok(()) }
    async fn read(&mut self) -> Result<Record, ConnectorError>;
    async fn ack(&mut self, position: Position) -> Result<(), ConnectorError> { Ok(()) }
    async fn teardown(&mut self) -> Result<(), ConnectorError> { Ok(()) }

    async fn lifecycle_on_created(&mut self, cfg: &Config) -> Result<(), ConnectorError> { Ok(()) }
    async fn lifecycle_on_updated(&mut self, before: &Config, after: &Config) -> Result<(), ConnectorError> { Ok(()) }
    async fn lifecycle_on_deleted(&mut self, cfg: &Config) -> Result<(), ConnectorError> { Ok(()) }
}
```

Default bodies on every method except `read` (and the analogous `write` on `Destination`) give
the same forward-compatibility property Go achieves with `mustEmbedUnimplementedSource()` and
Python with ABC default methods — **natively**, via ordinary trait defaults. No seal method is
needed: a struct implementing `Source` today keeps compiling if the SDK adds a new
default-bodied method later; only a new _required_ method would break existing implementors,
which is exactly the same compatibility contract Go and Python each reach by different means.

#### 2.2 Config: `serde` + a derive macro, not a Go-style codegen step

Go needs `paramgen` (a separate `//go:generate` pass) because Go's runtime reflection can't turn
struct tags into a `config.Parameter` map without codegen. Python solved this with runtime
introspection (`pydantic`'s `model_fields`) — no codegen at all. **Rust has neither Go's
limitation nor Python's runtime-reflection luxury**: Rust has no runtime type reflection, so a
Python-style zero-codegen introspection function is not available. The Rust-idiomatic middle
ground is a **derive macro** (`#[derive(ConnectorConfig)]`) that expands at compile time —
still no separate `go:generate`-style manual step (it runs automatically as part of normal
`cargo build` macro expansion, unlike Go's paramgen), but it is codegen, just compiler-integrated
codegen rather than either Go's external tool or Python's runtime trick. This is named
explicitly as the honest three-way tradeoff (Go: external tool; Python: free at runtime; Rust:
free at build time via proc-macro) rather than implying Rust matches Python's "no codegen at
all" claim.

```rust
#[derive(ConnectorConfig, Deserialize)]
struct Config {
    #[conduit(description = "HTTP endpoint to poll.")]
    url: String,                                          // -> required (no default)
    #[conduit(default = "1000", ge = 100, description = "Delay between empty polls, ms.")]
    poll_interval_ms: u64,                                 // -> greater-than
    #[conduit(inclusion = ["json", "csv"], default = "json")]
    format: String,                                        // -> inclusion
}
```

Mapping to `config::Parameter`/`Validation` (confirmed field-for-field against
`conduit-commons@v0.6.0/config/parameter.go:35-41`, `config/validation.go:37-44`): a field with
no `default` attribute → `ValidationTypeRequired`; `ge=`/`le=` → `GreaterThan`/`LessThan`;
`inclusion=[...]`/`exclusion=[...]` → `Inclusion`/`Exclusion` (both present in the confirmed enum
— the Python doc flagged `Exclusion` as an unmapped gap at review time; this design maps it from
day one); `regex=` → `Regex`; the Rust field type maps to `ParameterType`
(`u8..u64`/`i8..i64` → `Int`, `f32`/`f64` → `Float`, `bool` → `Bool`, `String`/`PathBuf` → `String`
/`File` by attribute, `Duration` (a `#[conduit(duration)]`-tagged `String` or a `humantime`-parsed
field, since Go's duration parameter type uses Go's `"5s"` duration syntax, not
`std::time::Duration`'s Rust `Debug` format — this needs its own small parsing shim, named here
so it isn't discovered late the way the Python review's A-gap on `ParameterTypeDuration` was).
The derive macro emits a `Config::to_parameters() -> HashMap<String, config::Parameter>`
associated function, so the `Specify` handler is one line, same as Python's design.

#### 2.3 Errors: `Result` + `thiserror`, and the partial-batch-ack fix applied from day one

Go's `Write(ctx, batch) (n int, err error)` needs the SDK to defend an invariant
(`n == len(batch)` implies `err == nil`) that's easy to violate — and the Python review's **B1
blocker** found that a naive `{index: error}` sparse-map translation of that invariant into
Python can silently ACK records that were never attempted, because "absent from the map" was
read as "succeeded." **This design does not repeat that shape.** Rust's `write` returns an
**exhaustive, positional** result, not a sparse map — making "success by omission" a type it's
awkward to even construct incorrectly:

```rust
#[async_trait]
pub trait Destination: Send + Sync {
    type Config: DeserializeOwned + ToParameters;

    async fn open(&mut self) -> Result<(), ConnectorError> { Ok(()) }
    /// Returns one `Result` per input record, same length and order as `records`.
    /// A record's outcome is never inferred from absence — the SDK rejects (as an
    /// internal adapter bug, fail-closed) any response whose length doesn't match input.
    async fn write(&mut self, records: &[Record]) -> Vec<Result<(), String>>;
    async fn teardown(&mut self) -> Result<(), ConnectorError> { Ok(()) }
    // lifecycle hooks as in Source, default no-op bodies
}
```

The SDK's adapter maps `Vec<Result<(), String>>` 1:1 onto `Destination.Run.Response.Ack{position,
error}` and treats a length mismatch between `records` and the returned `Vec` as an adapter-level
error (fail closed — the whole batch nacked, connection reset with a diagnostic), never as
"assume the rest succeeded." This is a strictly stronger shape than the Python design's
`BatchWriteError({index: exc})` (which required the B1 fix as a rule enforced by convention) —
Rust gets the same safety property structurally, because the exhaustive `Vec` has no
"missing = success" state to misinterpret in the first place. Worth calling out in Alternatives
as a case where Rust's type system earns a real correctness win over the Python equivalent, not
just a stylistic one.

For `Source::read`: an `ErrBackoffRetry`-equivalent variant (`ConnectorError::BackoffRetry`)
signals "no record right now, retry with backoff" — same `Factor: 2, Min: 100ms, Max: 5s`
constants the Go SDK uses (`error.go:19-27`, `source.go:280-289`), reused here for behavioral
parity across SDKs. Any other error surfaces as a gRPC `INTERNAL` status with the error's
`Display` string as detail.

```rust
#[derive(thiserror::Error, Debug)]
pub enum ConnectorError {
    #[error("backoff retry")]
    BackoffRetry,
    #[error("method not implemented")]
    Unimplemented,
    #[error("{0}")]
    Internal(String),
}
```

`ConnectorError` (like Python's) reserves room for a `code: Option<String>` — unpopulated today,
since **the connector protocol has no stable plugin-originated error-code scheme yet**
(confirmed: no sentinel/coded-error taxonomy beyond `ErrBackoffRetry`/`ErrUnimplemented`,
`pconnector/errors.go:19`, `conduit-connector-sdk/error.go:19-27` — same fact the Python doc
already established, carried forward rather than re-derived) — a protocol change adding
`ConduitError` codes is flagged as landing around v0.16 per the SDK DX doc; reserving the field
now avoids an SDK-breaking change later.

#### 2.4 The bidirectional `Run` stream — the hardest part

**Source side.** On `Run`, the adapter spawns two cooperating `tokio` tasks under one
`JoinSet`, sharing a bounded `tokio::sync::mpsc` channel as the internal record buffer (its
capacity is the SDK's backpressure knob — a config default, author-overridable, analogous to
`sdk.batch.size` in the Go SDK):

- **Producer task**: loops calling the author's `Source::read()`, pushing each record onto the
  bounded channel. `BackoffRetry` triggers a `tokio::time::sleep` with the shared backoff state,
  then retries — never blocks the consumer task. Channel-full backpressure is the mechanism that
  naturally slows the producer when Conduit is acking slower than the source can produce,
  without any additional flow-control logic needed.
- **Stream-out task**: drains the channel into the `tonic` response `Stream` Conduit reads
  records from.
- **Ack-in task**: concurrently reads the inbound `Streaming<Source.Run.Request>` (each message
  carrying `repeated ack_positions`), calling `Source::ack()` once per position, **in the same
  order Conduit sends them** — the protocol's own doc comment guarantees "acknowledgments will
  be sent back to the connector in the same order as the records produced," so this task is a
  straight `while let Some(req) = stream.message().await` loop, no reordering logic needed.

If any task errors, the `JoinSet` observes it, cancels the sibling task via a shared
`CancellationToken`, closes the response stream, and propagates the error so Conduit sees a
clean stream close rather than a hang — this is the concrete mechanism behind invariant 7's
"kill -9 at any instant must be recoverable" for the in-process (non-crash) error path; the
crash/`SIGKILL` path is necessarily handled by the _next_ process's `Open(position)` resuming
correctly, which is a connector-author responsibility this SDK can only document, not enforce
(same honest limit the Python doc names in its own Failure modes).

**Destination side**: mirror image — an **inbound task** reads `Streaming<Destination.Run.Request>`
(each message a `repeated Record` batch), calls `Destination::write(&records)` once per batch,
and an **ack-out task** pushes the resulting `Vec<Result<(), String>>` (mapped to `Ack{position,
error}`) onto the outbound stream. Because `write` takes the whole batch Conduit already sent,
no author-side batching primitive is needed (same conclusion Python reached) — the wire
protocol is already batch-shaped.

#### 2.5 Graceful shutdown (invariant 7)

The primary graceful-shutdown path is **not** an OS signal — it's go-plugin's own
`GRPCController.Shutdown` RPC, which Conduit's `Close()` calls **synchronously and with no
deadline**: `client.go`'s `doneCtx` is a plain cancellation context, not a timed one. As a
consequence, the ~2s force-kill fallback (`client.go:530-567`) **only** fires when `Shutdown`
returns an error quickly (e.g. the method is unimplemented) — it is **not** a rescue for a
`Shutdown` handler that hangs. A buggy connector-side drain (for instance, one blocked forever on
a stuck destination write) can therefore hang Conduit's own pipeline-stop path indefinitely, with
no upstream SIGKILL to save it. This is a hard requirement, not a graceful-shutdown nicety:

**The `Shutdown` RPC handler MUST return within a bounded time, unconditionally, no matter what
user code does.** Concretely: (1) trigger the `CancellationToken` shared with every active `Run`
call's task pair, (2) wrap the wait for in-flight `write`/`read`+`ack` calls to finish and
buffered acks to flush in a `tokio::time::timeout` with a fixed, documented budget (an SDK
default, not "however long the author's drain code takes"), (3) if the timeout elapses, return
the RPC anyway — reporting partial completion via a truncated-drain diagnostic — rather than
continuing to block on user code. The SDK's own adapter owns and enforces this timeout; it must
never call into `Destination::write` or `Source::read`/`ack` from the shutdown path without a
bound around it.

As defense-in-depth (some hosting environments send `SIGTERM` directly, independent of the
go-plugin path), the SDK also installs a `tokio::signal::unix::signal(SignalKind::terminate())`
handler (Unix) that triggers the identical bounded-drain path — same intent, second entry point,
one shared, always-time-bounded drain routine. This needs its own focused test in Phase 1 (see
Testing), including a "write that never returns" fault-injection case — not just sustained write
load — since the hang scenario, not the quick-error scenario, is exactly the one go-plugin's own
fallback does not cover.

### 3. Repo & packaging

**New repo: `ConduitIO/conduit-connector-sdk-rust`** — matches the naming convention already set
by `conduit-connector-sdk` (Go) and planned for `conduit-connector-sdk-python`.

```text
conduit-connector-sdk-rust/
  Cargo.toml                    # workspace root
  crates/
    conduit-connector-sdk/       # the public crate (published to crates.io)
      src/
        lib.rs                   # Source, Destination, Record, Operation, Change, serve, errors
        config.rs                 # ConnectorConfig derive re-export, Parameter/Validation mapping
        record.rs                 # Record/Change/Data/Operation/Metadata
        source.rs                 # Source trait, adapter, Run task pair (§2.4)
        destination.rs            # Destination trait, adapter, Run task pair (§2.4)
        serve.rs                  # handshake + tonic server bootstrap (§1)
        handshake.rs               # magic cookie, protocol negotiation, stdout line
        error.rs                   # ConnectorError, BackoffRetry
        grpc/                       # generated stubs (buf generate output, prost/tonic) +
                                     # adapters translating proto <-> Rust types; go-plugin's
                                     # own GRPCController/GRPCBroker stubs (§1.5) live here too
    conduit-connector-sdk-macros/ # the #[derive(ConnectorConfig)] proc-macro crate
    conduit-connector-testing/    # acceptance-equivalent harness (Phase 3, §4)
  examples/http-poll-source/       # Phase-1 worked example, runnable standalone
  buf.gen.yaml                       # codegen config (§1.5)
  proto/go-plugin/                    # vendored GRPCController/GRPCBroker/GRPCStdio .proto
                                        # (pinned to go-plugin v1.8.0, §1.5 Phase 0 task)
  .github/workflows/
    lint.yml  test.yml  release.yml  compat-nightly.yml
  CONTRIBUTING.md  README.md  CHANGELOG.md
```

- **`GRPCStdio` is vendored for codegen uniformity, not served**: Conduit's client dials
  `GRPCStdio.StreamStdio` on launch but treats `Unavailable`/`Unimplemented` as an expected,
  graceful degradation (logged, ignored) rather than a handshake failure. Stderr/stdout
  forwarding already works through go-plugin's own process-level stdout/stderr capture,
  independent of this RPC. This SDK vendors the `.proto` alongside `GRPCController`/`GRPCBroker`
  so the codegen pipeline is uniform, but implements no server for it — one less RPC surface to
  build and verify in Phase 0 (§1.1, §1.5).
- **Packaging**: a `cargo` workspace; the public crate published to crates.io as
  `conduit-connector-sdk`. Rust floor: current stable minus a small MSRV window (documented, not
  bleeding-edge) — this is a house-style question the repo's own `README` should pin explicitly
  once chosen; not load-bearing for this design.
- **Cross-compilation**: `cross` (Docker-based, simplest for a small maintainer surface) or
  `cargo-zigbuild` for the OS/arch matrix matching the Go template's
  (`darwin`/`linux`/`windows` × `amd64`/`arm64`, per `conduit-connector-template/.goreleaser.yml`)
  — connector authors get prebuilt binaries per platform the same way Go connectors do via
  `goreleaser`; a Rust binary's static/near-static linking (especially on Linux via `musl`)
  is a genuine advantage over Python here (no interpreter/venv to ship, no `PATH` resolution
  risk at all once the binary itself is the artifact — see Alternatives).
- **Lint/type**: `clippy` (`-D warnings` in CI) and `rustfmt`; `cargo audit` for the dependency
  vulnerability scan CLAUDE.md's automated gates require org-wide.
- **Test**: `cargo test` + `tokio::test`; `proptest` for the record (de)serialization round-trip
  properties (raw ↔ proto ↔ struct, and the `Struct`-fidelity boundary from §1.4) — same
  property-testing bar CLAUDE.md holds serialization code to, applied here even though this repo
  isn't the engine's own data path.
- **Acceptance-equivalent harness (Phase 3, designed now, §4)**.
- **CI matrix**: `ubuntu-latest`, `macos-latest`, `windows-latest`, against current stable + the
  pinned MSRV. A `compat-nightly.yml` job regenerates stubs from the current
  `buf.build/conduitio/conduit-connector-protocol` HEAD and runs the example connector against a
  current Conduit dev build — the same drift-detection discipline the Python doc's
  `compat-nightly.yml` establishes, and the same one `conduit-connector-template`'s own CI
  already holds Go to.
- **Release**: tag-triggered, conventional-commit changelog, crates.io publish via a
  trusted-publisher GitHub Action (no long-lived crates.io token in repo secrets) — same bar as
  the Python plan's PyPI trusted-publisher choice, for the same reason (a published package is a
  shared supply-chain surface, stricter than a per-connector binary release).
- **Example/template connector**: `examples/http-poll-source/` — deliberately the same worked
  example (HTTP polling source) the Python doc and the org's "same tutorial per language" plan
  (`20260705-sdk-and-embedding-dx.md:318-320`) use, so a reader compares Go/Python/Rust
  apples-to-apples. A dedicated `conduit-connector-template-rust` repo (mirroring
  `conduit-connector-template`'s CI/README-generation/release structure) is **Phase 3**, gated on
  `conduit connector new --lang rust` integration — building it before the SDK's API has
  stabilized through a couple of real connectors would lock in the wrong shape, same reasoning
  the Python plan gives for deferring its own template repo.

### 4. The acceptance-test harness — the honest version

Restating the finding from Context: the existing Go `AcceptanceTest` (`acceptance_testing.go`)
tests **in-process**, not over the wire — it wraps a Go `Connector`'s `NewSource`/
`NewDestination` factories directly, never spawning a subprocess or speaking the go-plugin
handshake. So "drive a Rust connector from the Go harness" is not an available shortcut; it
would require building subprocess-driving capability into the Go harness first, which is
**cross-cutting, not Rust-specific**, and is named in Related as a separate opportunity rather
than folded into this SDK's scope.

**Recommendation for this SDK: a Rust-native port**, structurally mirroring
`acceptance_testing.go`'s actual test boundary (in-process trait-level testing) rather than
inventing a stronger black-box harness unilaterally for one language:

- `conduit_connector_testing::AcceptanceTestDriver` trait an author implements — `write_to_source`,
  `read_from_destination`, `generate_record`, seed/config hooks — direct analogs of the Go
  driver's methods (`acceptance_testing.go:67-120`).
- `ConfigurableAcceptanceTestDriver` convenience wrapper (analog of `acceptance_testing.go:125+`)
  for the common case: supply the connector's `Source`/`Destination` types plus two valid configs
  pointing at the same resource.
- Same test categories the Go suite defines (`acceptance_testing.go:497-874`): specifier
  existence/validity, config parameter validation (success + required-param-missing), resume-at-
  position (snapshot and CDC — `t.Run("snapshot", ...)`/`t.Run("cdc", ...)` at lines 735/750),
  read/write round trip, read timeout behavior.
- **Versioned and published** per the org's stated principle ("the acceptance-test suite is
  versioned and published per language," `20260705-sdk-and-embedding-dx.md:66-73`) so a Rust
  connector author knows exactly which contract version they passed, matching the Python plan's
  identical commitment.

**Named but explicitly out of scope here**: a genuinely stronger, language-agnostic
subprocess/gRPC black-box acceptance harness (one that launches _any_ standalone connector
binary — Go, Python, or Rust — and drives it purely over the wire) would be strictly more
valuable than three separate in-process ports, and would for the first time actually test the
handshake and wire encoding as part of "acceptance." That harness does not exist for any
language today, including Go. This design doc proposes it as a candidate cross-SDK follow-on
(see Related), not as this SDK's own Phase 3 deliverable — building it here would silently
expand scope and duplicate an investment the Python SDK would need to make identically.

### 5. Build/release integration with scaffolding and the registry

- `conduit connector new --lang rust` (Phase 3 here) generates from `conduit-connector-template-
  rust`, using the same orchestration engine (`pkg/scaffold/`) the Go path already uses per
  `20260707-connector-processor-scaffolding.md` — no new scaffolding architecture, just a new
  template target and an SDK for it to wire against.
- Built binaries are `kind: "standalone"` in the connector registry index schema
  (`docs/design-documents/registry-index/index-schema.json:252-255`), identical to Go and the
  planned Python artifacts — **not** `kind: "wasm"`. This is concrete, load-bearing evidence for
  this doc's framing: a Rust connector built with this SDK is, to the registry and to Conduit's
  dispenser, indistinguishable from a Go one. That's the whole point of choosing gRPC-standalone
  over waiting on WASM.
- The registry's publish Action (being built, per the registry MVP doc) should treat Rust
  release artifacts the same as Go's `goreleaser` output — no Rust-specific publishing path to
  design here, only to plug into once it exists.

## Alternatives considered

**Trait dispatch shape — `dyn`+`async-trait` (current recommendation, flagged for
reconsideration) vs. a monomorphized generic entry point.** A Rust connector binary compiles
exactly **one** concrete `Source` or `Destination` implementation — there is no in-process
runtime-polymorphism requirement forcing `dyn` dispatch inside a single connector process at all;
Go's `dyn`-style adapter (`sourcePluginAdapter`) reflects Go's own interface model, not something
the protocol imposes on every language. An alternative surfaced in review: a generic
`serve<S: Source>(source: S)` entry point, monomorphized per author at their own `main()`, using
native stable `async fn` in traits (return-position `impl Trait`, stable since Rust 1.75) with
**zero boxing** and no `async-trait` dependency at all. This also resolves a real object-safety
wrinkle the current design had not previously named: `Source`/`Destination` carry an associated
`type Config` referenced in method signatures, so `dyn Source` is not actually usable as written
— it would need to be `dyn Source<Config = Concrete>`, which is exactly as concrete as the
generic alternative and gives up the polymorphism `dyn` exists to provide. **This design does not
resolve the question here** — it keeps `async-trait` as the working recommendation but
explicitly flags the trait-shape decision as one to revisit and settle deliberately before Phase
1 locks `Source`/`Destination` as a public, semver-governed contract (§2.1). Changing the
dispatch shape after connector authors depend on it is an SDK-breaking change to the trait
definitions themselves — unlike swapping `async-trait`'s internals for a future dyn-safe-async-
trait language feature under the same public signatures, which is a non-breaking implementation
swap.

**Async trait dispatch — `async-trait` crate (recommended) vs. hand-rolled boxed futures vs.
waiting for native dyn-safe async traits.** Hand-rolling `Pin<Box<dyn Future<...>>>` return
types everywhere is what `async-trait` already generates, so doing it manually buys nothing but
more boilerplate for the SDK's own adapter code (the boilerplate cost is the same; only who
writes it changes). Waiting for the language to make async trait methods object-safe without
boxing was considered and rejected for a v1 SDK — that capability isn't stable as of this
design, and gating a production SDK's public trait shape on an unshipped language feature is
exactly the kind of speculative bet CLAUDE.md's "no speculative generality" principle warns
against. `async-trait` is swappable later as an internal implementation detail without breaking
the public trait signatures, if and when dyn-safe async traits stabilize.

**Config codegen — proc-macro derive (recommended) vs. a Go-style external `build.rs`/CLI tool
vs. hand-written `Parameter` maps.** An external tool (mirroring Go's `paramgen` invoked via
`//go:generate`) was considered for consistency with Go. Rejected: Rust's proc-macro system
already runs derive macros as an ordinary part of `cargo build` with no extra invocation step
authors must remember — an external tool would be strictly worse ergonomics for no compatibility
benefit, since nothing about the wire format cares how the `Parameter` map was produced.
Hand-written maps (no macro at all) were rejected as the same "always in sync with the struct"
argument Python makes for its introspection approach: any manual mapping drifts from the struct
it describes the moment a field is added and the map isn't touched to match.

**Destination write result shape — exhaustive positional `Vec<Result<(), String>>` (recommended)
vs. a sparse `HashMap<usize, String>` (Python's shape) vs. Go's `(n, err)`.** The sparse-map
shape was considered for API-shape parity with the Python design. Rejected after naming the
Python review's B1 finding explicitly (§2.3): a sparse map has a "success by omission" state
that is easy to construct incorrectly and was exactly the data-loss-class bug the Python
review caught before Phase 1 build. The exhaustive positional `Vec` has no such state — every
record has an explicit `Result`, so there is nothing to omit. This is a case where the "same
shape across languages" instinct should lose to "the shape each language can make safest,"
matching the Python doc's own stated principle that behavioral parity (correct acks) is the
goal, not shape parity.

**Cross-compilation — `cross`/`cargo-zigbuild` (recommended) vs. native runners per OS/arch in
CI.** Building natively on a `windows-latest`/`macos-latest`/`ubuntu-latest` matrix (no
cross-compilation at all) was considered as the simplest option. Rejected only provisionally —
it's a reasonable Phase 1 fallback if `cross`/`zigbuild` prove flaky in CI, and is noted as the
practical de-load option if cross-compilation tooling becomes a time sink, but `cross`/
`cargo-zigbuild` is preferred because it produces all release artifacts from one CI job rather
than fanning out matrix builds, and static/`musl` linking on Linux specifically is easiest to
get right through `cross`'s prebuilt toolchain images.

**Acceptance harness — Rust-native in-process port (recommended) vs. building the black-box
subprocess harness now.** Covered in §4; repeated here because it's a real fork in scope: the
subprocess harness is more valuable long-term but is cross-SDK infrastructure this doc
deliberately does not claim as its own deliverable, to avoid quietly expanding a Rust-SDK design
doc into a rewrite of the Go acceptance suite's architecture.

## Failure modes

Analyzed against CLAUDE.md's data-integrity invariants, scoped to what an SDK (not the engine)
owns:

- **Handshake cookie mismatch / malformed stdout line** → process must exit non-zero with a
  clear stderr diagnostic before writing anything to stdout that isn't the handshake line.
  Mitigation: all SDK-internal logging defaults to stderr (mirroring Go's stdio-forwarding
  behavior); documented loudly, since a stray `println!()` in connector code corrupts the
  handshake exactly as a rogue `print()` would in Python.
- **`GRPCController.Shutdown` unimplemented, or returns an error quickly** → falls back to
  Conduit's ~2s force-kill (`client.go:530-567`, §2.5) — functional (no pipeline hang) but
  ungraceful; in-flight writes at kill time carry the same risk profile as any `SIGKILL`-mid-batch
  scenario the (Phase 2+, per Process maturity) chaos suite is meant to cover once it exists for
  this SDK's own test matrix.
- **`Shutdown` RPC handler hangs (HIGH — distinct from the case above)** → go-plugin's `Close()`
  calls `Shutdown` synchronously with **no deadline** (`doneCtx` is a plain cancel-context, not a
  timed one); the ~2s force-kill fallback only triggers on a quick error return, never on a hang.
  A connector-side drain that blocks unconditionally on user code — e.g. a stuck destination
  write — can hang Conduit's own pipeline-stop path indefinitely, with no upstream rescue. This
  is a correctness gap adjacent to invariant 7 (graceful shutdown by default), not merely an
  ungraceful-but-safe degradation. Mitigation: §2.5's hard requirement that the SDK's `Shutdown`
  handler always returns within a bounded time via `tokio::time::timeout`, regardless of what the
  author's `write`/`read`/`ack` implementations do — verified with a dedicated "write that never
  returns" fault-injection test (Testing), not assumed safe from the timeout code merely existing.
- **Crash mid-`Run` stream (source)** → in-flight, unacked records are lost from the SDK's view
  on restart unless the source's own `open(position)` correctly resumes from the last acked
  position (invariant 2). Phase 1's done-criterion does not yet include a resume-after-kill test
  (that's the acceptance harness's `resume-at-position` category, §4, arriving Phase 3) —
  flagged as a Phase 1 gap, not silently assumed safe, identical honesty to the Python doc's own
  equivalent flag.
- **Partial-batch write failure (destination)** → structurally mitigated by §2.3's exhaustive
  `Vec<Result<(), String>>` shape rather than merely documented as a rule to follow — the
  Python review's B1 finding is applied as a design constraint here, not a follow-up fix.
- **go-plugin internal service stub drift (§1.5)** → since these are hand-vendored from
  go-plugin's own `.proto` files rather than sourced from a maintained crate, a future go-plugin
  version bump on the Conduit/Go side could silently break `GRPCController`/`GRPCBroker`
  compatibility with no compiler error on the Rust side (the mismatch would only surface at
  runtime, as a hung or failed `Shutdown`/multiplex call). Mitigation: pin the vendored `.proto`
  files to the exact go-plugin version Conduit's `go.mod` uses, and make `compat-nightly.yml`
  assert the pinned version still matches `conduit-connector-protocol`'s `go.mod` — a version
  skew becomes a loud CI failure, not a silent runtime one.
- **Static-linking/`PATH` exec assumptions** → lower risk than Python's here (no interpreter
  resolution), but the release artifact must still be invoked by absolute path with a clean
  environment (§1.1.6) — Phase 1's done-criterion uses a real Conduit launch specifically to
  catch this class of packaging mistake immediately rather than assume it away.

## Upgrade/rollback & compatibility

- **Protocol version skew**: targets v2, negotiated via `PLUGIN_PROTOCOL_VERSIONS` (§1.1.2); if
  a future v3 lands, an older SDK still negotiates down to v2 as long as Conduit's client keeps
  v2 in `VersionedPlugins` — the standard go-plugin backward-compat mechanism already relied on
  for today's v1/v2 coexistence. A breaking protocol change is governed upstream by CLAUDE.md's
  "never change `conduit-connector-protocol` without an explicit versioning discussion" rule.
- **Handshake line field count**: §1.1.3's 6-field stdout line
  (`CORE-PROTOCOL-VERSION|APP-PROTOCOL-VERSION|NETWORK|ADDRESS|PROTOCOL|SERVER-CERT`) is correct
  as of today's go-plugin usage because Conduit's client does not set `GRPCBrokerMultiplex`. If
  Conduit ever enables broker multiplexing, go-plugin appends a 7th segment to that line; the
  vendored Rust handshake parser must be written to either tolerate an optional trailing segment
  or fail loudly and specifically on an unexpected field count — not silently misparse
  `SERVER-CERT` or a later field. Track this as a compatibility watch item alongside the
  go-plugin version pin (§Failure modes).
- **SDK API breaking changes**: semver from v1.0; breaking changes to `Source`/`Destination`/
  `ConnectorConfig` follow the standing announce → warn → remove policy (minimum two minor
  versions), same as every other public Conduit contract and the same commitment the Python SDK
  makes for its own surface.
- **go-plugin internal-service vendoring**: re-vendor and re-verify the `GRPCController`/
  `GRPCBroker`/`GRPCStdio` `.proto` files whenever `conduit-connector-protocol`'s `go.mod` bumps
  its `hashicorp/go-plugin` dependency — tracked as an explicit, CI-checked step (§Failure modes),
  not a manual "remember to check" task.
- **Rollback**: the SDK ships as a versioned crates.io package pinned per connector project, not
  vendored into Conduit — rolling back is "pin an older SDK version," no coordinated Conduit-side
  rollback needed, the same standalone-subprocess-architecture benefit the Python doc names.
- **Drift detection**: `compat-nightly.yml` (§3) regenerating stubs from BSR HEAD and running the
  example connector against a current Conduit dev build is the primary anti-drift mechanism,
  mirroring the org-wide prescription in the SDK DX doc.

## Testing

- **Handshake unit tests**: assert the exact stdout line format against go-plugin's own parser
  logic (`client.go:838-926`), independent of a real Conduit process — catches "subtly wrong in
  a way only Conduit's real parser would reject" bugs before the first real-launch test does.
- **Phase 1 done-criterion (the concrete, testable bar, mirroring the Python plan's)**: a fresh
  checkout of the example connector, built with `cargo build --release`, launched by a **real,
  unmodified Conduit binary** (no Conduit-side changes) as one side of a pipeline against an
  already-supported connector (e.g. `conduit-connector-file`) — records flow end-to-end, get
  correctly acked, and `conduit pipelines stop` tears the subprocess down via the graceful
  `GRPCController.Shutdown` path, not the force-kill fallback. Scripted and CI-timed via
  `compat-nightly.yml` from day one, not eyeballed once.
- **SIGTERM/Shutdown-RPC-mid-write test** (Phase 1): the example connector exercised under two
  distinct fault scenarios — (a) sustained write load, killed via both paths (direct `SIGTERM`
  and Conduit's normal `Close()`/Shutdown-RPC path), asserting no torn writes and a clean process
  exit; and (b) **a destination write that never returns** (a fault-injected hang, not merely
  slow load), asserting the `Shutdown` RPC still returns within its bounded timeout and the
  process still exits cleanly rather than hanging Conduit's pipeline-stop path indefinitely — the
  scenario go-plugin's own force-kill fallback does not cover (§2.5). Case (b) is the concrete
  verification behind §2.5's hard-requirement claim, not just a docs note.
- **Property tests** (`proptest`) on the record codec: raw ↔ proto ↔ struct round-trip, and an
  explicit test asserting the `Struct` int→float fidelity boundary (§1.4) fails loudly in a
  documented way rather than silently — same bar CLAUDE.md sets for serialization code, and the
  same specific gap the Python review's B3 finding named.
- **Acceptance-equivalent suite** (§4, Phase 3): specifier validity, config validation
  (success + missing-required, matching all six `ValidationType` variants including
  `Exclusion`), resume-at-position (snapshot + CDC), read/write round trip, read timeout.
- **No performance claim ships without a committed `benchi` result** — a Rust-vs-Go throughput
  comparison is explicitly **not** claimed anywhere in this document and should not be claimed
  in interim docs either, per CLAUDE.md's benchmarking discipline. A benchi comparison is
  scheduled as Phase 3 polish, not assumed favorable in advance (Rust's likely-lower per-call
  overhead vs. `async-trait`'s boxing cost is exactly the kind of thing that needs measuring, not
  guessing).

## Phased plan

### Phase 0 — protocol groundwork (the part with no existing precedent to copy)

Scope: vendor and verify go-plugin's `GRPCController`/`GRPCBroker`/`GRPCStdio` `.proto` files
against the pinned `go-plugin v1.8.0` (§1.5); stand up the `buf generate` pipeline
(`neoeinstein-prost`/`neoeinstein-tonic`) against `conduit-connector-protocol`; a minimal
handshake-only binary that Conduit's dispenser can launch, negotiate with, and cleanly shut
down via `GRPCController.Shutdown` — **no connector logic yet**, just proving the transport
layer end-to-end. This phase exists because, unlike Python, there is no off-the-shelf go-plugin
server crate to lean on; get this exactly right before building anything on top of it.

**Acceptance**: a real Conduit binary launches the bare handshake binary, sees it register (via
`grpc.health.v1.Health`), and tears it down via the graceful `Shutdown` RPC — verified in CI, not
by hand.

### Phase 1 — MVP: Conduit can run a Rust connector

Scope: `Source`/`Destination` traits (§2.1) with default-bodied lifecycle/optional methods +
`Configure`/`Specify` (§2.2, no schema middleware) + OpenCDC record (§1.4) + the full bidi `Run`
task-pair design (§2.4) + `BackoffRetry`/exhaustive-`Vec` write results (§2.3) + graceful
shutdown (§2.5) + the worked example connector (§3).

Explicitly **not** in Phase 1: acceptance-equivalent harness (hand-verified only), schema/Avro
middleware, `conduit connector new --lang rust` integration, crates.io trusted-publisher release
automation (manual release is fine for v0.x).

**Definition of done**: the Testing section's Phase 1 done-criterion, run as CI-timed
`compat-nightly.yml` from day one and kept green thereafter.

### Phase 2 — Destination hardening, schema deferral decision, docs

- Full property-test suite for the record codec, including the `Struct`-fidelity boundary test.
- Schema extraction/encoding middleware parity decision: either implement (matching
  `SourceWithSchemaExtraction`/`DestinationWithSchemaExtraction` semantics) or explicitly defer
  with a stated gap, matching the Python plan's own Phase 2 treatment — decide based on real
  connector demand at that point, not speculatively now.
- Full docs: the shared HTTP-poll worked-example tutorial (same one Go/Python/TS use, per the DX
  doc's "apples to apples" principle), rustdoc coverage on the public API, cookbook recipes.
- Golden record-shape fixture corpus shared with the Go/Python suites (raw, structured,
  tombstone, schema-carrying, error shapes) — the "one corpus" principle from the DX doc.

### Phase 3 — acceptance harness, scaffolding integration, benchmarks

- The Rust-native acceptance-equivalent harness (§4), versioned and published.
- `conduit connector new --lang rust` scaffolds from a new `conduit-connector-template-rust`
  repo, wired through the existing `pkg/scaffold/` engine — no new CLI architecture, a new
  template target plus this SDK to generate against, closing out the NO-GO branch's original
  "RFC + reference only" outcome for Rust connectors with an actual production path instead.
- `conduit connector test`/`conduit connector build` wire into the Rust acceptance/integration
  loop the same way they already do for Go.
- First `benchi`-committed Rust-vs-Go reference-pipeline comparison — reported, not assumed.
- Registry publish-Action integration once that Action exists (§5) — `kind: "standalone"`
  artifacts, no new registry work required.

## Consequences

- Conduit gains a second production, gRPC-standalone connector SDK, opening Rust as a real
  connector-authoring language without any change to Conduit itself or to the WASM track —
  the standalone-subprocess architecture (`20220121-conduit-plugin-architecture.md`) pays off
  for a second non-Go language exactly as its own rationale predicted, ahead of and independent
  of whatever the WASM component-model track eventually decides.
- **This decision changes what ROADMAP.md and `20260705-sdk-and-embedding-dx.md` currently say
  about Rust.** Both currently describe Rust as "the WASM component-model reference [language];
  proves the [in-process] WASM connector path before it's a supported product" — language that,
  read literally, says Rust connectors are not meant to be production-usable pre-WASM. Once this
  doc is accepted, those documents need an explicit update (not a silent contradiction sitting
  next to this one): Rust's SDK tier moves to parity with Python's ("gRPC-standalone first"),
  and the WASM reference-component work is re-scoped as pure interface-proving R&D, decoupled
  from "is Rust a connector language yet." `conduit-v019-plans/v019-execution-plan.md`'s
  Workstream 4 framing ("Rust connector scaffolding ships as an RFC + reference implementation…
  not a supported `--lang rust` scaffold path") is superseded by this doc for the connector half
  specifically — the processor half of that workstream (Rust/TS _processor_ WASM scaffolding) is
  untouched and continues as already planned.
- The org takes on a second SDK-plus-a-still-design-only-third (Python) maintenance surface,
  permanently, on a solo-maintainer-plus-Claude base — the same honest tradeoff the Python doc
  names for itself, now doubled. The `compat-nightly.yml` mechanism is the concrete mitigation
  in both cases; it does not remove the underlying cost.
- Phase 0's go-plugin internal-service vendoring (§1.5) is genuinely new, unproven work with no
  existing Rust crate to lean on, but it is small in absolute surface area: `GRPCController` is a
  single `Shutdown(Empty) -> Empty` RPC, and `GRPCBroker` is one bidi RPC whose `StartStream`
  error Conduit's own client discards, making a no-op stub sufficient. **Independent review found
  this is not this document's single largest execution risk** — that framing is corrected here.
  The bigger risks, re-ranked: (1) the shutdown-hang semantics in §2.5 — go-plugin's `Close()`
  calls `Shutdown` synchronously with no deadline, so an implemented-but-hanging handler gets no
  upstream rescue, unlike an unimplemented one, which is a correctness gap adjacent to invariant
  7; and (2) the `dyn`+`async-trait` vs. monomorphized-generic trait-shape decision in §2.1,
  which is a public API commitment that only gets harder to change the longer it ships unexamined.
  Both should be re-derisked/decided before Phase 1 locks its scope — ahead of, not instead of,
  the go-plugin vendoring spike.

## Related

- [Python connector SDK](20260707-python-connector-sdk.md) — the sibling gRPC-standalone SDK
  design this document mirrors in structure and, where the protocol is identical, in verified
  facts (handshake constants, wire record shape, env vars). Also the source of the B1/B3 review
  findings this design applies as constraints from the start rather than as follow-up fixes.
- `conduit-v019-plans/wasm-connector-go-no-go.md` — the NO-GO decision this document responds
  to; the Rust reference-component work it describes is unrelated to and unaffected by this SDK.
- `conduit-v019-plans/v019-execution-plan.md` — Workstream 4, whose "Rust connector scaffolding
  = RFC only" framing this document supersedes for the connector half.
- [SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md) — persona A1 language
  tiers; needs a Consequences-noted update once this doc lands.
- `ROADMAP.md` — "WASM everywhere" section names Rust as the component-model proving path; same
  update needed.
- [Connector/processor scaffolding](20260707-connector-processor-scaffolding.md) — the CLI
  integration this SDK's Phase 3 plugs into unmodified.
- [Connector registry index schema](20260714-connector-registry-index-schema.md) — `kind:
  "standalone"`, the artifact category this SDK's release binaries fall under.
- [WASM component model ADR](../architecture-decision-records/20260704-wasm-component-model.md)
  — the separate extension mechanism this SDK is not part of.
- `ConduitIO/conduit-connector-protocol` — the wire contract this SDK implements
  (`proto/connector/v2/*.proto`, `pconnector/`).
- `ConduitIO/conduit-connector-sdk` — the Go reference implementation this design mirrors
  semantically.
- **Follow-on opportunity, not this doc's scope**: a language-agnostic, subprocess/gRPC
  black-box acceptance harness (§4) that could replace three separate in-process ports (Go,
  Python, Rust) with one real wire-level compatibility contract — worth its own design doc if
  a second or third SDK reaches the point of actually building an acceptance suite.
