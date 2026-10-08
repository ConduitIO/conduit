# SDK parity automation and the open plugin protocol

## Summary

Conduit will have six officially supported SDK languages in two tiers, plus a community tier:

| Tier | Languages | Surfaces the tier must pass |
| --- | --- | --- |
| Full | Go, Python, TypeScript, Rust | connector (gRPC), processor (WASM in-process and/or gRPC out-of-process), state API, embedded client |
| Enterprise | Java, C# | connector (gRPC), processor (gRPC out-of-process), state API (gRPC), embedded client. WASM deferred until the language's component toolchain is production-grade |
| Community | anything else | whatever surfaces the author declares; listed as community-maintained |

Six SDKs maintained by one maintainer plus Claude only works if parity is a property the build checks, not something
people remember to do. This document proposes how:

1. **One versioned spec repo** (`ConduitIO/conduit-plugin-spec`) holds the protobuf for the connector, processor and
   state protocols, the WIT for WASM components, the feature list, the conformance scenarios and the shared test
   vectors. `buf breaking` becomes a required check there, with no skip label.
2. **Generated bindings, thin hand-written layer.** Wire types and stubs are generated per language
   (`buf generate`; `wit-bindgen`/`componentize-*` where WASM applies). The hand-written part is the ergonomic layer:
   handshake, lifecycle, batching, ack tracking, config, errors.
3. **A language-neutral conformance suite is the parity contract.** Black-box scenarios run by the engine over the
   real protocol against a reference "kitchen-sink" plugin in each language. Anyone can run it:
   `conduit plugins conformance <artifact>`.
4. **Feature manifests and a release gate.** Each SDK ships a machine-readable manifest. A generated parity matrix is
   published in docs and in registry metadata. An official SDK cannot publish a release that claims a spec version
   it did not pass. Official SDKs reach parity within one minor release of a spec change (full tier) or two (enterprise
   tier), or the feature is labelled "Go-only preview".
5. **Change propagation is automated.** A spec release opens tracking issues in every official SDK repo and can start
   an agent that drafts the port from the Go reference. Conformance and human review gate the result.
6. **One repo per language** for non-Go SDKs (connector, processor, state and client packages together). Go keeps its
   existing repos.
7. **The protocol is public and documented** (handshake, lifecycle, message flows, ack/position semantics, error codes,
   versioning), with a raw-gRPC walkthrough and the same conformance kit used for registry listing and a
   "Conduit Certified" mark.

The one load-bearing conflict this doc surfaces: [ADR 20260722](../architecture-decision-records/20260722-wasm-component-model-deferred.md)
made WASM _connectors_ NO-GO because no pure-Go runtime can host WASI Preview 2 components, and that is still true on
2026-10-08. WIT-based processors share the same blocker. Resolved: Rust and TypeScript connectors ship over gRPC, the
custom wasip1 processor ABI continues, and the host choice is recorded in an ADR at v0.23 (see
[Decisions](#decisions-devaris-2026-10-08)).

## Context: what exists today (verified 2026-10-08)

### Protocols and specs

- **Connector protocol** — `ConduitIO/conduit-connector-protocol` v0.9.5. Protobuf under `proto/connector/v1`,
  `proto/connector/v2` (`SourcePlugin`, `DestinationPlugin`, `SpecifierPlugin`) and `proto/connutils/v1` (the schema
  service the engine serves back to connectors). `buf.yaml` uses `breaking: FILE`. `buf-validate.yaml` runs
  `buf breaking` against `main` on PRs that touch `proto/**`, but honours a `Buf Skip Breaking` label. `buf-push.yaml`
  publishes to `buf.build/conduitio/conduit-connector-protocol`. The OpenCDC record and config parameter types come
  from `buf.build/conduitio/conduit-commons`.
- **Handshake** — HashiCorp go-plugin. The engine sets `CONDUIT_PLUGIN_MAGIC_COOKIE`
  (`pconnector.HandshakeConfig`), `CONDUIT_CONNECTOR_UTILITIES_GRPC_TARGET`, `CONDUIT_CONNECTOR_TOKEN` and
  `CONDUIT_CONNECTOR_ID`. The plugin prints `CORE|APP|NETWORK|ADDR|grpc` on stdout, serves `grpc.health.v1` with
  service name `plugin`, and go-plugin's `GRPCController.Shutdown`. None of this is written down outside Go source and
  the Python SDK's `_handshake.py`, which reimplements it from go-plugin line numbers.
- **Error codes on the plugin protocol** — none. `pconnector/errors.go` defines only `ErrUnimplemented`. Plugin errors
  are gRPC status plus free text. The engine itself already has a code scheme: `pkg/foundation/cerrors/conduiterr`
  encodes errors as `google.rpc.Status` with a `google.rpc.ErrorInfo` detail (domain `conduit`, dotted reasons such as
  `connector.plugin_not_found`).
- **Processor protocol** — `ConduitIO/conduit-processor-sdk` v0.6.0. Protobuf `processor/v1` (`CommandRequest`/
  `CommandResponse` wrapping `Specify`, `Configure`, `Open`, `Process`, `Teardown`) plus `procutils/v1` (schema, HTTP).
  The WASM ABI is a **custom wasip1 host-function ABI, not the component model**: a core module imports module
  `conduit` functions `command_request`, `command_response`, `create_schema`, `get_schema` and, since v0.6.0,
  `http_request` (the operator-gated egress capability, see
  [20260726-wasm-host-egress-capability](20260726-wasm-host-egress-capability.md)). Protobuf bytes move through
  linear memory. The engine hosts it on wazero with `wasi_snapshot_preview1`
  (`pkg/plugin/processor/standalone/registry.go`). There is no gRPC processor runtime.
- **Control API** — `proto/api/v1/api.proto` in this repo, published as `buf.build/conduitio/conduit`, `breaking: FILE`.
- **WIT** — no `.wit` file exists in this repo. The stale `conduit-processor-sdk-python` repo (last push 2025-06) has an
  experimental `world.wit`.

### SDKs and their test suites

| Repo | Language | State | Test contract |
| --- | --- | --- | --- |
| `conduit-connector-sdk` v0.14.2 | Go | production | `sdk.AcceptanceTest(t, driver)` — drives the connector **in-process through Go interfaces** (`AcceptanceTestDriver.Connector() Connector`), not over the wire |
| `conduit-processor-sdk` v0.6.0 | Go (wasip1) | production | Go unit tests |
| `conduit-connector-sdk-python` 0.1.0.dev1 (PyPI) | Python, gRPC | pre-release | `conduit.testing.AcceptanceTestSuite`, `CONTRACT_VERSION = "2026-08.v2"`, also **in-process** through the servicer adapters; the real-binary launch job in `compat-nightly.yml` is a TODO |
| `conduit-client-python` 0.1.0.dev1 | Python, control-API client | pre-release | unit + one integration test |
| `conduit-processor-sdk-python` | Python | stale, experimental | none |

Three findings matter for this design:

1. **The two acceptance suites have already drifted.** Python tests stable config-error codes and partial batch-write
   correctness; Go tests neither. Python's "stable code" is pydantic's own `"missing"` identifier, which no other
   language will produce, because the protocol has no wire code for it. Both suites run in-process, so neither proves
   the plugin speaks the protocol correctly.
2. **The hand-written layer is most of the code.** Python connector SDK: about 5,500 hand-written lines against about
   1,800 generated. Go connector SDK: about 8,600 hand-written lines (excluding tests and mocks). Codegen removes the
   message-type busywork. It does not remove the maintenance tax, so the plan has to target the hand-written layer.
3. **Both Python distributions ship a top-level `conduit/__init__.py`.** Installing `conduit-connector-sdk` and
   `conduit-client` in one environment makes one overwrite the other. This has to be fixed before either goes GA at
   v0.21.

### Registry

The index schema (`docs/design-documents/registry-index/index-schema.json`) gives every connector and processor version
`minConduitVersion` and `minProtocolVersion`. A connector's `minProtocolVersion` is a `conduit-connector-protocol`
semver. A processor's is a `conduit-processor-sdk` semver. A processor artifact is pinned to `os: wasip1`. Every object
sets `additionalProperties: false`.

### WASM component tooling (checked against upstream releases, 2026-10-08)

| Language | Component tool | Latest | Assessment |
| --- | --- | --- | --- |
| Rust | `wit-bindgen` | 0.62.0 (2026-09-10) | Mature. `wasm32-wasip2` is a standard target |
| Go | `componentize-go` + `wit-bindgen go` | 0.5.0 (2026-10-07), requires Go 1.27.1+ | Young. The TinyGo generator in `go-modules` is unmaintained, per the wit-bindgen README |
| TypeScript | `componentize-js` + `jco` | 0.23.0 (2026-09-21) | Works. README: "experimental project, no guarantees". About 8 MB StarlingMonkey engine per component |
| Python | `componentize-py` | 0.25.1 (2026-09-11) | Works for pure Python. Native extensions need WASI builds, so tokenizers, numpy-heavy code and model clients are out, which is why the gRPC processor runtime exists |
| C# | `componentize-dotnet` | v0.8.0-preview00011 (2026-06-12, prerelease) | Not production-grade. Depends on the experimental NativeAOT-LLVM feed |
| Java | none | — | wit-bindgen removed TeaVM-WASI support as unmaintained. TeaVM 0.16 targets Wasm GC for browsers. No WASI P2 component path verified |

Hosting is the bigger issue. WASI 0.3.0 shipped 2026-06-11 (0.3.1 on 2026-08-11), but wazero v1.12.0 (2026-05-29)
still has no component model, and both tracking issues (#2200, #2289) are closed. The pure-Go hosts that ADR 20260722
said to watch have **no tagged releases**: `arcjet/gravity` (pushed 2026-10-07) and `partite-ai/wacogo` (pushed
2026-09-25). The ADR's flip conditions have not been met.

## Problem

1. **Parity is manual and already failing at N=2.** Each SDK carries its own idea of what "a connector" is. With six
   official SDKs, every protocol change becomes six hand-ports and six hand-written test updates. Nothing detects a
   missed one.
2. **The protocol is not public.** A Ruby or Elixir author would have to reverse-engineer go-plugin, the magic cookie,
   the health service name, `GRPCController`, ack ordering and position semantics from Go source. The community tier
   cannot exist until those are written down.
3. **There is no language-neutral proof of correctness.** An in-process acceptance suite cannot catch a wrong
   handshake, a misordered ack on the wire, or a plugin that drops a batch on `Teardown`. Those are the bugs that
   corrupt pipelines (Invariants 1, 3, 7).
4. **New surfaces are coming in every language at once.** The gRPC processor runtime (v0.24) and the state API (v0.25,
   in four languages at once) are Tier 1 data-path contracts. Without a single spec and one conformance oracle, six
   implementations will mean six slightly different semantics.

## Goals and non-goals

Goals:

- A single, versioned, machine-checked source of truth for every plugin-facing contract.
- Parity measured by one black-box suite that every official SDK passes in CI, scoped by tier.
- A published, versioned protocol that a third party can implement from docs and verify with the same suite.
- Make every per-SDK step that can be automated automatic; leave humans the semantic review.

Non-goals:

- Designing the state API itself (get/put/TTL/timers semantics, storage, checkpoint integration). That needs its own
  design doc before v0.25. This doc only fixes how the state API is specified, transported and conformance-tested.
- Designing the gRPC processor runtime internals (supervision, batching policy). Also its own doc. This doc fixes its
  wire contract and test contract.
- Reversing ADR 20260722. This doc names the decision. It does not take it.
- Native SDKs for community languages. They get the protocol and the kit. We do not maintain their SDKs.

## Constraints

- **Invariants 1–7 hold for every language.** The engine owns acks, positions, checkpoints and state. Plugins never
  own durable state. Every scenario that can be tested over the wire is.
- **No CGO in the engine binary** (ADR 20260722, single-static-binary principle).
- **No breaking wire change without a versioning plan.** Existing Go connectors built against protocol v0.9.x and
  existing wasip1 processors built against processor-sdk v0.6.x must keep working, unmodified, through the deprecation
  window.
- **Deprecation policy:** announce, then warn, then remove, with at least two minor Conduit releases between announce
  and remove.
- **Solo maintainer.** Any process that needs a human per SDK per change does not scale to six. The design has to
  reduce that cost, not just describe it.
- **Monthly release train.** Spec minors cannot ship faster than SDKs can follow.

## Design

### 1. Single source of truth: `ConduitIO/conduit-plugin-spec`

#### Contents

```text
conduit-plugin-spec/
  proto/                      # buf module buf.build/conduitio/plugin-spec
    connector/v2/             # moved from conduit-connector-protocol, package names unchanged
    connutils/v1/
    processor/v1/             # moved from conduit-processor-sdk, package names unchanged
    processor/v2/             # new: gRPC ProcessorPlugin service (reuses processor/v1 messages)
    procutils/v1/
    state/v1/                 # new (v0.25): StateService the engine serves to plugins
    plugin/v1/                # new: capabilities, ErrorInfo reason registry, go-plugin GRPCController (vendored)
  wit/                        # WIT packages conduit:processor@x.y.z, conduit:state@x.y.z, conduit:connector (unstable)
  abi/wasip1/                 # normative description of the legacy custom ABI ("WASM ABI v1"), frozen
  features.yaml               # every feature ID: surface, introduced-in, status, scenario IDs
  tiers.yaml                  # which surfaces each tier must pass
  conformance/
    scenarios/                # declarative scenarios (YAML), one file per feature area
    vectors/                  # shared test vectors (OpenCDC records, configs, error cases)
    kitchen-sink.md           # behaviour every reference plugin must implement
  docs/                       # the public protocol reference (section 7)
  CHANGELOG.md
```

The proto **package names stay exactly as they are** (`connector.v2`, `processor.v1`, …). gRPC method paths are
derived from package names (`/connector.v2.SourcePlugin/Run`), so moving files between repos changes nothing on the
wire. `conduit-connector-protocol` and `conduit-processor-sdk` stop owning `.proto` files. They become generated
consumers of `buf.build/conduitio/plugin-spec`, and a sync bot keeps their checked-in Go code current. Their Go import
paths do not change.

The **control API stays in this repo** (`proto/api/v1`). It changes in the same PRs as engine features, and splitting
it out would make every API field a two-repo change. The spec repo pins a control API version per spec release and
documents it. The embedded-client conformance scenarios drive that pinned version. (Decided; see
[Decisions](#decisions-devaris-2026-10-08).)

#### Versioning

- One spec semver, `MAJOR.MINOR.PATCH`, independent of Conduit's version. The spec starts at **1.0.0**, which is the
  connector protocol as of v0.9.5 plus processor v1 as of processor-sdk v0.6.0, plus two additive pieces: stable
  error codes (below) and the `spec_version`/`capabilities` fields in `Specify`.
- **Stable error codes are part of spec 1.0.0.** Today the plugin protocol has none (`pconnector/errors.go` defines
  only `ErrUnimplemented`). Spec 1.0.0 adds `plugin/v1` with a reason registry in the engine's existing format: a
  `google.rpc.Status` carrying a `google.rpc.ErrorInfo` detail, domain `conduit`, dotted reasons (`plugin.config.missing`,
  `plugin.config.invalid`, `plugin.capability_unsupported`, `plugin.record.too_large`, …) and the failing config path in
  `metadata`. Generated constants per language replace ad hoc codes such as pydantic's `"missing"`. Additive on the
  wire: an older engine still reads the status message. Reasons follow the same announce → warn → remove policy as
  fields.
- The registry's `minProtocolVersion` keeps its field name and its semver pattern. Values below `1.0.0` mean the legacy
  per-repo versions, and every legacy version sorts below 1.0.0, so ordering stays correct with no schema change.
- **MINOR** adds features (new RPCs, fields, WIT functions behind a version gate). **MAJOR** means a new proto package
  (`connector.v3`) and a new go-plugin app protocol version. The engine keeps serving the old one for the deprecation
  window. Because go-plugin already negotiates `VersionedPlugins` (v1 and v2 today), this is the existing mechanism,
  not a new one.
- **Runtime negotiation.** The `Specify` response gains a `spec_version` string and a `capabilities` list of feature
  IDs from `features.yaml` (an additive field). The engine enables a feature only when the plugin declares it. A plugin
  without the field is treated as legacy 0.9.x. A plugin that needs a capability the engine lacks fails `Configure`
  with `plugin.capability_unsupported`, never silently degrades.

#### Breaking-change checks

- `buf breaking --against` the last release tag, `FILE` rules, **required** on the spec repo's `main`, with **no**
  skip label. A real break goes through a MAJOR (new package), not an override.
- WIT has no equivalent of `buf breaking` that is mature enough to gate on. Instead the spec repo commits **golden
  components**: tiny Rust components built against every released WIT minor. CI instantiates every golden component
  against the current host bindings. If an old component fails to link or behaves differently, the change was
  breaking. It is a compatibility test by construction, not static analysis, and the doc says so.
- A **proto/WIT mapping check**. Every WIT record that mirrors a proto message is listed in `wit/mapping.yaml`. A
  generator round-trips each vector through both encodings in CI. The same scenarios run over gRPC and over WASM. Two
  IDLs is a real drift risk; this check is how it gets caught.

#### Deprecation

1. **Announce:** the field or RPC is marked `deprecated = true` in proto, or `@deprecated(version = x.y.z)` in WIT,
   recorded in `features.yaml` and `CHANGELOG.md`, and released in a spec minor.
2. **Warn:** the engine logs once per plugin and increments
   `conduit_plugin_deprecated_feature_use_total{feature, plugin}` whenever a plugin uses it. The conformance report
   lists it as a warning.
3. **Remove:** no earlier than two minor Conduit releases after the warn release, and only in a spec MAJOR if the
   change is wire-breaking.

#### Migrating the custom wasip1 ABI to WIT (dual-ABI host period)

The existing ABI becomes **"WASM ABI v1"**. It is documented normatively in `abi/wasip1/`, frozen (no new host
functions), and supported. The engine tells the two artifact kinds apart before instantiating anything, by binary
format: a core module starts with the preamble `\0asm` followed by version `0x01`, and a component uses a different
layer/version value. A core module that imports from module `conduit` takes today's path, unchanged. A component takes
the component path.

The component path does not exist yet, and it cannot exist in-process without a pure-Go component host (ADR 20260722).
So the migration runs in phases, and each phase is useful without the next:

1. **Spec now (v0.21–v0.24).** Publish `wit/processor` and `wit/state` as versioned WIT, marked `@unstable` until a host
   exists. The WIT is hand-authored, and the mapping check above round-trips it against the proto in CI, so the two
   cannot drift silently.
2. **New WASM SDKs build on ABI v1 until a host exists.** Rust processors (v0.23) target `wasm32-wasip1` core modules
   with the `conduit` imports. That needs prost and a dozen lines of hand-written import glue, and it is the same thing
   TinyGo does today. Rust is not blocked.
3. **Host decision (ADR at v0.23, decided 2026-10-08).** The ADR chooses between (a) waiting for gravity or wacogo to
   reach a tagged, full-type-coverage release and (b) an **out-of-process component host**, a separate
   `conduit-wasm-host` binary on wasmtime that loads a component and speaks the existing gRPC plugin protocol to the
   engine. Option (b) keeps the engine CGO-free and keeps the single sandboxed artifact, but gives up in-process speed.
   (c) CGO wasmtime in the engine stays rejected per ADR 20260722. The v0.23 ADR supersedes ADR 20260722 in part. The
   same choice unblocks WIT-based processors and WASM connectors, and until it ships the custom wasip1 ABI v1 is the
   only WASM processor ABI.
4. **Dual-ABI period.** Once a component host ships, both ABIs are supported. ABI v1 is announced deprecated no earlier
   than the release where the component host is GA, warned for at least two minors, and removed only in a spec MAJOR.
   `conduit processor-plugins describe --json` reports `abi: wasip1-v1 | component` so operators can see what they run.

### 2. Codegen per language

Two layers per SDK. **Generated** code is never edited by hand and is regenerated in CI with a drift check, the
pattern `compat-nightly.yml` already uses in the Python SDK. **Hand-written** code is the ergonomic layer.

| Language | gRPC codegen | WASM codegen | Viability |
| --- | --- | --- | --- |
| Go | `protoc-gen-go`, `protoc-gen-go-grpc` (existing) | ABI v1: existing `go:wasmimport`. Component: `componentize-go` (Go 1.27.1+) | Production for gRPC and ABI v1; component tooling young |
| Python | `grpcio-tools` + `protobuf` (existing, `buf generate`) | `componentize-py` (pure-Python only) | gRPC production-ready; WASM limited to pure Python |
| TypeScript | `@bufbuild/protobuf` + Connect for Node (gRPC over HTTP/2, can serve the plugin) | `jco` + `componentize-js` | gRPC fine; WASM experimental upstream |
| Rust | `prost` + `tonic` | ABI v1: `prost` + hand glue. Component: `wit-bindgen` | Best component story of the six |
| Java | `protoc-gen-java` + `grpc-java` | none viable | gRPC only (enterprise tier) |
| C# | `Grpc.Tools` + `grpc-dotnet` | `componentize-dotnet` preview | gRPC only (enterprise tier) |

**Generated:** message types, service stubs, WIT bindings, the error-reason constants (from `plugin/v1`), the feature
ID constants (from `features.yaml`), and the config-parameter schema types.

**Hand-written, per language:**

- go-plugin handshake (stdout line, magic cookie check, health service, `GRPCController.Shutdown`, stdio).
- Lifecycle state machine (`Configure` → `Open` → `Run` → `Stop` → `Teardown`, lifecycle events).
- Source ack tracking and position handling, destination batching and partial-batch results.
- Config parsing and validation that emits `ErrorInfo` reasons with the failing config path.
- Logging bridge, schema client (`connutils`/`procutils`), state client, egress client.
- Ergonomic record type, idiomatic error types, `serve()` entry point, scaffold template.
- Embedded client: `local()` and `connect()`, pipeline builder.

Rough size, calibrated on the existing Python and Go code: about 5,000 hand-written lines for a connector SDK, 2,000
for a gRPC processor SDK, 1,000 for the state client, 1,500 for the embedded client, with tests roughly 1:1. The
section on [maintenance cost](#maintenance-cost-of-six-official-sdks) uses these numbers.

Rule: **the hand-written layer contains no wire knowledge that is not in the spec.** If an SDK has to know something
(for example, that acks must arrive in emission order), that fact lives in `docs/` in the spec repo and has a
conformance scenario. This rule is what makes AI-assisted porting reviewable.

### 3. Language-neutral conformance suite

#### Shape

- **Runner.** Written in Go inside the engine, exposed as `conduit plugins conformance <artifact>`, with `plugin` and
  `plugins` aliases to match the existing `connector-plugins`/`processor-plugins` alias style. It launches the artifact
  exactly as production does: go-plugin spawn for gRPC connectors and processors, wazero for ABI v1, and the chosen
  component host later. It then plays the engine's role over the real protocol. Being in the engine is the point. The
  oracle is the code that will actually talk to the plugin in production.
- **Scenarios** are declarative YAML in the spec repo, versioned with the spec. Each has an ID
  (`connector.source.ack.in-order`), a surface, the `features.yaml` IDs it covers, a mode (`kitchen-sink` or `any`), and
  steps. Steps are a small fixed vocabulary interpreted by the runner (configure, open, read N, ack positions, kill,
  restart, assert). They are not a scripting language. Anything the vocabulary cannot express goes into the runner as Go
  and is referenced by ID.
- **Kitchen-sink plugins.** Each official SDK keeps one reference plugin in its repo (source, destination, processor and
  state user) whose behaviour is driven by config: emit these vectors, fail at record k with this reason, block for
  T seconds, expose a configurable position format. `kitchen-sink.md` specifies it. It is how scenarios get
  deterministic behaviour without an external system.
- **Two modes.**
  - `--mode sdk` runs every scenario against a kitchen-sink. This is the parity contract for official and community
    SDKs.
  - `--mode plugin` runs against any real connector or processor: specification validity, config validation and error
    codes, lifecycle and teardown, wire-level ack/position contract, and graceful shutdown. Read/write scenarios run
    when the author supplies a fixture config, as with the Go `ConfigurableAcceptanceTestDriver`. This is what the
    registry uses.
- **Output.** `--json` produces a versioned report: spec version, tier claimed, every scenario's ID, result, duration,
  seed, and failure detail with a stable reason. Exit codes follow the deterministic exit-code ADR. Human output is a
  summary table.

#### Scenario coverage (initial set)

| Area | What is asserted |
| --- | --- |
| Handshake | cookie mismatch exits non-zero with a message; health `plugin` SERVING; `GRPCController.Shutdown` exits within the deadline |
| Specification | `Specify` returns a valid spec; `spec_version` and capabilities parse; parameters are well-formed |
| Config validation | missing, wrong-type and out-of-range values fail with `ErrorInfo` reason and config path; valid config passes |
| Records in/out | each vector round-trips byte-for-byte after canonicalisation (see vectors); raw, structured, tombstone, all four operations, metadata, keys |
| Ack and position | acks arrive in emission order; positions are opaque bytes returned unchanged; after SIGKILL and restart the source resumes at the last acked position with no gap (at-least-once, Invariants 1–3) |
| Ordering | records within a partition key are emitted and written in order (Invariant 4) |
| Destination | ack only after write returns; partial batch failure acks the written prefix and errors the rest, never acks unwritten records |
| Errors | plugin errors carry `ErrorInfo` (domain `conduit`, reason `plugin.*`); unknown reasons fall back to a generic code, never crash the runner |
| Lifecycle and teardown | `Stop` drains; `Teardown` after an error returns cleanly; no goroutine, thread or process leak (checked from outside: child exits, ports close) |
| Backpressure | engine stalls reads for T seconds; plugin does not error, drop or reorder. Memory growth is reported, not gated, because RSS is not comparable across runtimes |
| Schema | schema create/get through `connutils`/`procutils`; unknown schema version fails with a reason, never silently coerces (Invariant 6) |
| State API (v0.25) | get/put/delete; TTL expiry by processing time; timers fire once each; staged writes are discarded on crash before ack and visible after ack; kill mid-batch then restart shows no torn state (Invariant 5) |
| Egress (WASM) | denied by default; allowlisted call succeeds; out-of-policy call fails with the documented reason |
| Embedded client | create, start, stop, get and delete a pipeline against a running engine; error codes surfaced unchanged |

#### Shared test vectors

`conformance/vectors/opencdc/*.json` defines each record twice: as canonical JSON, and as protobuf bytes produced by
Go's deterministic marshaller. Protobuf binary encoding is **not** canonical across languages (map field order is
unspecified, and `metadata` is a `map<string,string>`), so "byte-compare" means: the runner decodes what the plugin
sent, re-encodes it deterministically, and compares bytes with the vector. Each SDK also runs the vectors in its own
unit tests (decode, wrap in the ergonomic type, unwrap, encode, decode, compare), which catches drift before the slower
black-box run.

The vectors deliberately include the cases where languages disagree:

- Integers above 2^53 inside structured data. `google.protobuf.Struct` stores numbers as doubles, so they lose
  precision in every language. The vector pins the documented behaviour rather than letting each SDK pick one.
- Empty vs. absent payloads, `NaN`, non-BMP Unicode, `opencdc.*` reserved metadata keys.
- A record just under and just over 4 MiB (the default inbound gRPC message limit in Go, Python, Java and .NET). The
  spec states the limit and the error reason for exceeding it.

**Property-based scenarios.** The runner generates records from a seed, pushes them through the kitchen-sink echo
processor and destination, and checks equality, ordering and ack completeness. The seed is printed so any failure can
be replayed. This would add `pgregory.net/rapid` as a dependency. It is justified as the first property-based framework
in the tree, which the Process maturity table already lists as missing.

#### Testing the oracle

A conformance suite that passes broken plugins is worse than none. The spec repo ships **mutant kitchen-sinks** in
Go, each violating one rule: acks out of order, ack before write, drop on teardown, wrong error domain, torn state
write. CI requires the runner to fail every mutant. A new invariant scenario must come with its mutant.

### 4. Feature manifest, parity matrix and release gate

Each SDK repo commits `conduit-sdk.yaml`:

```yaml
sdk: conduit-sdk-python
language: python
tier: full                      # full | enterprise | community
version: 0.3.0
spec: 1.4.0                     # highest spec version claimed
surfaces:
  connector.grpc: 1.4.0
  processor.grpc: 1.4.0
  processor.wasm: null          # not offered
  state.grpc: 1.4.0
  client.control: 1.4.0
features:
  state.timers: preview         # ga | preview | absent; IDs from features.yaml
```

- **Release gate.** Every official SDK's release workflow calls a reusable workflow from the spec repo. It builds the
  kitchen-sink from the release commit, runs `conduit plugins conformance --mode sdk --spec <claimed> --json` with the
  matching Conduit release, and compares results with the manifest. Publishing to PyPI, npm, crates.io, Maven Central
  or NuGet is a later job that depends on it. **A claimed surface version with any failing scenario fails the release.**
  A surface the tier requires (from `tiers.yaml`) but the manifest leaves `null` also fails, unless every feature in it
  is labelled `preview` under the parity policy.
- **Parity policy.** When a spec minor adds a feature, official SDKs in the tiers whose surfaces include it must reach
  `ga` for it within **one Conduit minor (full tier) or two Conduit minors (enterprise tier)**. The window is recorded
  per tier in `tiers.yaml`, and the release gate reads it from there. Until they do, the feature appears in docs and in
  the matrix as
  "Go-only preview" (or "Go, Python preview"). The engine does not gate it; the label is for users.
- **Parity matrix.** A scheduled job in the spec repo collects every SDK's manifest and its latest conformance report,
  then renders a matrix (feature × language, with the status and the report link) into the docs site and `llms.txt`.
  It never hand-edits the page. The same data feeds the registry.
- **Registry metadata.** Connector and processor versions gain an optional `conformance` object: spec version, mode,
  tier, report digest and report URL. Index CI verifies the digest. This is additive within schemaVersion 1: the
  shipped client verifies signatures over the canonical bytes of the whole payload, then unmarshals into typed structs
  with plain `json.Unmarshal` (`pkg/registry/index/verify.go`, no `DisallowUnknownFields` anywhere in `pkg/`), so older
  clients ignore the nested field. The JSON Schema's `additionalProperties: false` on `connectorVersion` and
  `processorVersion` still has to be widened for index CI, in the same change.

### 5. Change propagation

1. **Spec PR template and checks.** A spec PR must name its feature IDs and surfaces. CI fails if a new `features.yaml`
   entry has no scenario, if a scenario references an unknown feature, or if a Tier 1 feature (ack, position, state,
   lifecycle, serialization) has no mutant.
2. **Go reference first.** The Go SDK implements the feature against the unreleased spec in the same cycle. The spec
   release requires Go to pass the new scenarios. Go is the reference because the engine and runner are Go, not
   because other languages matter less.
3. **Fan-out on spec release.** A reusable workflow in the spec repo uses a GitHub App with issue and PR scope on the
   official SDK repos only. For each SDK whose tier covers the affected surfaces, it opens one tracking issue with the
   spec diff, the new scenario IDs, the Go reference PR, and the parity deadline (the next Conduit minor). It is
   idempotent: one issue per (SDK, spec version), updated rather than duplicated on re-runs.
4. **Optional agent-drafted port.** Labelling the issue `port:draft` starts an agent run in that SDK repo. Its inputs
   are the spec diff, the Go reference diff, the SDK's existing code and the new scenarios. It opens a **draft** PR. The
   PR's CI runs conformance at the new spec version.
5. **Parity dashboard.** The matrix page plus a GitHub Project view of open tracking issues by age against their
   deadline.

**Review bar, stated honestly.** Passing conformance shows the port satisfies the enumerated scenarios and nothing
more. An agent can overfit, for example by special-casing kitchen-sink config. Three controls:

- Conformance-only behaviour is confined to the kitchen-sink package. A lint rule fails the build if SDK core code
  references kitchen-sink config keys.
- Property-based scenarios use fresh seeds on every run, so a port cannot memorise expected outputs.
- **Every port that touches a Tier 1 surface (ack, position, state, lifecycle, serialization) needs DeVaris's review in
  a session separate from the authoring one, per CLAUDE.md.** Ergonomic-only ports (new optional config field, a
  docstring) need one approval and green conformance. In solo reality that approval is also DeVaris. The automation
  removes the typing, not the review.

### 6. Repository layout

Recommendation: **one repo per non-Go language**, holding every surface for that language as separate packages, built
and released together with independent package versions. Go keeps `conduit-connector-sdk` and
`conduit-processor-sdk` as they are, because their module paths are a public contract.

```text
conduit-sdk-python/     one distribution: conduit.connector, conduit.processor, conduit.client
conduit-sdk-typescript/ packages: @conduitio/connector, @conduitio/processor, @conduitio/client
conduit-sdk-rust/       crates:   conduit-connector, conduit-processor, conduit-client
conduit-sdk-java/       artifacts: io.conduit:connector-sdk, processor-sdk, client
conduit-sdk-dotnet/     packages: Conduit.Connector, Conduit.Processor, Conduit.Client
```

The existing `conduit-connector-sdk-python` and `conduit-client-python` merge into `conduit-sdk-python` before v0.21 GA.
They ship as **one distribution**, which removes the `conduit` package collision: the code moves under
`conduit.connector` and `conduit.client` (and `conduit.processor` at v0.24). That changes import paths for 0.1.0.dev
users, which is acceptable before GA and not after. The other languages ship one package per surface because their
ecosystems have no single-namespace collision problem and users install only what they need.

Shared CI lives in the spec repo as reusable workflows: `conformance.yml` (build kitchen-sink, run the runner, upload the
report), `release-gate.yml`, `regen-check.yml` (`buf generate` and WIT bindgen, then fail on diff), and `propagate.yml`.

Alternatives considered:

- **Polyrepo per (language × surface)**, the current pattern. Up to 15 non-Go repos. Every spec change fans out to
  three times as many places, and cross-surface types (record, errors, config) are duplicated per repo or published as
  yet another package. Lost on fan-out cost.
- **One multi-language monorepo.** One place for the bot, but five toolchains in one CI, release tooling that has to
  understand five registries, and contributors who must clone everything to fix a Python typo. Package registries and
  community expectations are per-ecosystem. Lost on toolchain coupling.

### 7. The open protocol for community languages

Published from `conduit-plugin-spec/docs/` to the docs site under "Plugin protocol":

- **Reference:** handshake (environment variables, stdout line grammar, protocol version negotiation, health service,
  `GRPCController`, stdio, exit behaviour); lifecycle state machine with sequence diagrams for source, destination and
  processor; message flows for `Run` streams; ack and position semantics (opaque positions, in-order acks, what
  "durably handled" means, at-least-once, restart behaviour); error model (`ErrorInfo` domain and reason registry,
  config path metadata); limits (message size, timeouts); versioning, capabilities and deprecation; state API; schema
  and egress callbacks; WASM ABI v1 and WIT worlds.
- **Raw-gRPC walkthrough:** a minimal source plugin built with nothing but generated stubs and about 150 lines of
  hand-written code, in a language we do not officially support (Ruby, so nobody mistakes it for an SDK). It passes
  `conduit plugins conformance --mode sdk` for the connector surface. It lives in the spec repo and runs in its CI, so
  the walkthrough cannot rot.
- **Conformance kit:** the same `conduit` binary plus `kitchen-sink.md`. No separate download.
- **Registry acceptance:** any-language plugins are accepted when their release attaches a `--mode plugin` report
  signed in their own CI (same provenance model as artifacts), and index CI re-verifies the digest. Plugins in an
  officially supported language follow the same rule.
- **"Conduit Certified":** a listing badge for plugins whose `--mode plugin` report passes at a supported spec version
  with read/write fixtures supplied, re-checked on each new version. It certifies protocol behaviour, not the quality of
  the external integration, and the docs say exactly that.
- **Community-maintained tier rules:** a community SDK is listed when it publishes a manifest with `tier: community`,
  passes `--mode sdk` for the surfaces it declares, and names a maintainer. It is de-listed (marked unmaintained, not
  removed) when it falls two spec minors behind or fails conformance on a supported spec version for 90 days. We do not
  promise fixes, releases or support for community SDKs.

## Enterprise tier: Java and C\#

- **Surfaces:** connector over gRPC, processor over the gRPC out-of-process runtime, state API over gRPC, and the
  embedded client. Generated stubs plus a thin layer. Exactly the same scenarios as the full tier for those surfaces.
- **Placement:** Java and C# embedded clients at v0.25 (mostly generated). Java connector and processor SDK across
  v0.25–v0.26, after the Kafka Connect JAR host (v0.23) and the gRPC processor runtime (v0.24). Java is also the
  forward path for Kafka Connect connector authors. C# SDK at v0.27, with scope set together with early adopters. There
  is no maintainer precondition.
- **Parity window:** two Conduit minors, against one for the full tier.
- **WASM re-evaluation criteria, checked yearly (first check 2027-10):** a Java or C# guest toolchain becomes eligible
  when all of these hold: (1) a tagged non-preview release that produces WASI P2 or P3 components from ordinary library
  code; (2) no experimental package feeds required; (3) the kitchen-sink processor compiles and passes `--mode sdk` on
  the engine's component host; (4) the artifact size and cold start measured with benchi are recorded in the repo. Today
  C# fails (1) and (2), and Java fails all four.

## Maintenance cost of six official SDKs

Per-SDK hand-written code to reach full parity, using the calibration in section 2:

| Language | Connector | Processor | State | Client | Hand-written total | Exists today |
| --- | --- | --- | --- | --- | --- | --- |
| Go | exists | exists (WASM) + ~2k gRPC | ~1k | exists | ~3k new | most |
| Python | exists (~5.5k) | ~2k gRPC | ~1k | exists (~1.6k) | ~3k new | connector, client |
| TypeScript | ~5k | ~2k gRPC + WASM glue | ~1k | ~1.5k | ~10k | none |
| Rust | ~5k | ~2k (ABI v1 + gRPC) | ~1k | ~1.5k | ~10k | none |
| Java | ~5k | ~2k gRPC | ~1k | ~1.5k | ~9.5k | none |
| C# | ~5k | ~2k gRPC | ~1k | ~1.5k | ~9.5k | none |

That is roughly 45,000 new hand-written lines, plus a similar amount of tests, against perhaps 30–40% of that volume in
generated code that costs nothing to maintain.

The ongoing tax is the larger number. If each spec minor carries about four SDK-visible changes and spec minors ship
every other Conduit release, that is about 24 port tasks per spec minor across six SDKs. At half a day for a small
additive change and three days for a semantic one, that is three to six weeks of work per spec minor if done by hand,
which one maintainer cannot absorb.

What must be automated for this to be sustainable:

| Work | Automated | Human |
| --- | --- | --- |
| Wire types, stubs, WIT bindings, reason and feature constants | 100% (codegen + drift check) | none |
| Knowing what changed and where it must go | 100% (fan-out issues, deadlines, dashboard) | none |
| Test updates per SDK | 100%: scenarios are written once in the spec repo; SDKs only update the kitchen-sink when new behaviour must be driven | kitchen-sink behaviour, rarely |
| Parity claims and docs matrix | 100% (manifest + report → matrix) | none |
| Release correctness | 100% (release gate) | release approval |
| Dependency bumps (grpc, protobuf, toolchains) | Dependabot/Renovate per repo, merged on green conformance | Tier 3 approval |
| The port itself | agent-drafted | **Tier 1 review always**; ergonomic API design |
| New language idioms, docs prose, examples | partly (generated reference docs) | yes |

Two rules keep the tax bounded: **spec minors ship at most every other Conduit release** unless a fix is urgent, and a
spec change that touches a Tier 1 surface must be worth six ports. The enterprise tier's two-minor parity window
spreads Java and C# ports across two releases. If the gate still goes red and stays red, the response is to narrow
scope, not to let the gate be ignored.

## Alternatives considered

### Hand-maintained parity per SDK (status quo)

Each SDK keeps its own acceptance suite and follows Go by reading changelogs. Lost because it is already failing with
two SDKs (the Go and Python suites have drifted), it never tests the wire, and it scales linearly in maintainer time
with every language added.

### Go-only conformance: other SDKs embed or link the Go acceptance suite

Run `sdk.AcceptanceTest` against non-Go plugins through a Go adapter that speaks gRPC. Lost because the Go suite is
built around in-process Go interfaces (`AcceptanceTestDriver.Connector() Connector`). Making it wire-level is the same
work as building the runner, and leaves the scenarios as Go code that only Go developers can read or extend. The
declarative-scenario runner keeps the oracle in Go, where the engine is, without making the contract Go-shaped.

### Make the WASM ABI v1 the permanent WASM contract and skip WIT

Document the custom wasip1 ABI as the open standard and never move to components. It works today without a component
host, and Javy (for JavaScript) and Rust can both produce wasip1 core modules with custom imports. Lost as the
long-term answer because every language needs hand-written import glue and manual protobuf marshalling through linear
memory, and the upstream toolchains (`componentize-*`, `wit-bindgen`) are converging on components. Kept as the
supported bridge (ABI v1) for as long as there is no host.

### Specification by IDL only, without a conformance suite

Rely on proto and WIT plus prose. Lost because the most important rules (ack ordering, positions, teardown, staged
state) are behavioural and cannot be expressed in an IDL.

## Failure modes

| Failure | Effect | Detection | Mitigation |
| --- | --- | --- | --- |
| Moving protos to the spec repo registers the same file twice in one Go binary (old `conduit-connector-protocol` code plus new generated code) | protobuf-go registration conflict at startup | engine and SDK CI start-up tests | one generated Go package per proto file: `conduit-connector-protocol` re-exports generated code from the spec, it does not generate a second copy |
| Proto and WIT drift | WASM and gRPC plugins behave differently | mapping check; same scenarios over both transports | `wit/mapping.yaml` round-trip in CI |
| Breaking proto change merged | old plugins fail at runtime | `buf breaking` required, no skip label | MAJOR via new package only |
| Conformance passes a broken plugin | false parity claim; possible data loss in production | mutant kitchen-sinks must fail | every Tier 1 scenario ships with a mutant |
| Flaky timing scenario | release gate blocks six repos | report durations; flake-hunt workflow already exists in this repo | no wall-clock asserts below 1 s; retries recorded in the report, never silent |
| Fan-out bot runs twice or loops | issue storm in SDK repos | idempotency key per (SDK, spec version) | update in place, never create a duplicate |
| Agent port overfits to scenarios | passes CI, wrong in production | lint against kitchen-sink keys in core; fresh seeds | Tier 1 human review |
| Registry `conformance` field rejected by older clients | `install` fails for every user | fixture test: a v0.21 client parses an index carrying the field | client decoder verified lenient today; keep it lenient (test pins it) |
| Dual-ABI host misclassifies an artifact | processor fails to load | load error with reason `processor.abi_unknown` | classify on binary format before instantiation; fuzz the classifier |
| ABI v1 removed while users still run v1 processors | pipelines fail after upgrade | deprecation counter metric | removal only after two warned minors and in a spec MAJOR |
| gRPC processor crashes mid-batch | batch not acked | supervisor exit status, restart counter | batch is retried after restart; staged state writes discarded (Invariants 1, 3, 5) |
| State callback per record over gRPC | throughput collapse | benchi run in the runtime's slice | batch-scoped prefetch and multi-get in `state/v1`; measured, not assumed |
| Engine older than plugin's spec | feature silently missing | capability negotiation | plugin fails `Configure` with `plugin.capability_unsupported` |
| Message size limits differ per language | large records fail in one SDK only | 4 MiB boundary vectors | limit stated in spec, negotiated in `Specify` |

## Upgrade and rollback

- **Spec repo introduction** changes no wire bytes (package names unchanged). Rolling back means pointing the Go repos
  back at their own proto directories.
- **Capabilities and `spec_version` in `Specify`** are additive. Old engines ignore them. New engines treat their
  absence as legacy 0.9.x.
- **`ErrorInfo` on plugin errors** is additive. Older engines still read the status message.
- **Release gate** is a workflow job. It can be disabled per repo if it is wrong, and the PR that disables it must say
  why. This is not a route around a failing gate before a release.
- **gRPC processor runtime** is a new plugin type next to WASM. Pipelines that do not use it are unaffected. Rollback is
  removing the runtime; existing WASM processors are untouched.
- **ABI v1 to component** follows announce → warn → remove with at least two minors, and an upgrade test (an ABI v1
  processor built with processor-sdk v0.6.0, run on every new engine) gates releases for as long as ABI v1 is
  supported.

## Observability

- `conduit_plugin_info{plugin, kind, sdk, sdk_version, spec_version, abi}` gauge, and the same fields in
  `conduit connector-plugins describe --json` and `conduit processor-plugins describe --json`.
- `conduit_plugin_deprecated_feature_use_total{feature, plugin}` counter.
- gRPC processor runtime: `conduit_processor_grpc_call_duration_seconds` histogram (by processor, batch-size bucket),
  restart counter, and in-flight batch gauge.
- Conformance JSON report schema, versioned, with scenario IDs, seeds and durations. Attached to every SDK release and
  every registry entry that claims conformance.
- Parity dashboard (matrix plus tracking-issue ages).
- Runbooks under `docs/operations/` for a gRPC processor crash loop, a capability mismatch at `Configure`, and
  deprecated-feature warnings, written in the slices that add those failure modes.

## gRPC processor runtime vs WASM: what to measure

No numbers are claimed here. The v0.24 runtime slice commits a benchi config that measures, for a no-op and a
JSON-transform processor, over WASM ABI v1 (Go), gRPC (Go) and gRPC (Python):

- Added latency per batch, p50/p99, at batch sizes 1, 100 and 1,000, with 1 KiB and 64 KiB records.
- Throughput in records/s at saturation, and CPU seconds per million records (engine and plugin process together).
- Resident memory at steady state, per processor instance.
- Cold start: process spawn to first `Process` vs. module instantiation.
- The same runs with one state `get` and one `put` per record, with and without batch prefetch.

Reported as medians with variance across at least five runs on a documented machine. The result decides the docs
guidance ("use gRPC processors for native-library workloads; WASM otherwise"), not a release gate. The benchi
regression gate is still not live (Process maturity table).

## Phasing

Brief placements, with the parity work this doc adds. Estimates are focused maintainer-plus-Claude weeks.

| Release | Brief placement | Parity and protocol work | Estimate |
| --- | --- | --- | --- |
| v0.21 | Python connector SDK GA; Python embedded client GA | spec repo at 1.0.0 (move protos, `buf breaking` required, `features.yaml`, `tiers.yaml`); stable error codes (`plugin/v1` reason registry) and capabilities in `Specify`; runner v0 for the connector surface; Go and Python kitchen-sinks; Python repos merged into one distribution (`conduit.connector`, `conduit.client`); manifests for Go and Python | 5–6 |
| v0.22 | — | vectors and property scenarios; mutants; release gate on Go and Python; fan-out workflow; protocol reference for connectors plus the Ruby walkthrough; registry `conformance` field | 4–5 |
| v0.23 | Rust SDK; TypeScript embedded client | Rust SDK: gRPC connectors, ABI v1 processors; TS client on generated stubs; component-host ADR (pure-Go runtime vs out-of-process wasmtime host), superseding ADR 20260722 in part | 5–6 |
| v0.24 | gRPC processor runtime; Python processor SDK; processor spec (protobuf + WIT) published; conformance suite and parity matrix live | `processor/v2` gRPC service; runner processor surface; public matrix; benchi runtime comparison | 6–8 (runtime is Tier 1) |
| v0.25 | state API in Go, Python, TypeScript and Rust at once; Java and C# embedded clients | `state/v1` proto and WIT; state scenarios with crash recovery; Java and C# clients; Java SDK starts | 6–8 (state is Tier 1, separate design doc first) |
| v0.26 | TypeScript connectors; pipelines-as-code builders in Python and TypeScript; one-call local mode for non-Go clients | TS connectors over gRPC on Node (WASM connectors follow the v0.23 host ADR, not this release); Java SDK GA | 5–6 |
| v0.27 | C# SDK | C# connector, processor and state on gRPC, scoped with early adopters; enterprise-tier gate for C# | 4–5 |

## Risks

- **Maintenance tax.** Six SDKs is about 45,000 hand-written lines before tests and a recurring port load per spec
  minor. Mitigated by the automation table, the cadence rule and the enterprise tier's two-minor window. If the gate
  goes red and stays red, the honest response is to narrow scope, not to waive the gate.
- **Component-model maturity.** Host side: no pure-Go component host, so in-process WASM components are blocked.
  Guest side: Rust is mature, Go and TypeScript work but are young or labelled experimental, Python is pure-Python
  only, C# is preview and Java has nothing. The phasing does not depend on any of these: connectors ship over gRPC,
  and WASM processors stay on ABI v1 until the v0.23 host ADR is implemented.
- **gRPC processor latency.** Out-of-process adds a serialization round trip per batch and per state call. Measured in
  v0.24 before docs recommend anything.
- **Conformance as a bottleneck.** If the runner is wrong, every SDK is wrong in the same way. Mitigated by the mutants,
  by keeping the runner in the engine, and by putting every runner change through Tier 1 review.
- **Go reference bias.** Features land in Go first, so other SDKs always lag by up to one minor. That is acceptable and
  visible ("Go-only preview"), not hidden.

## Decisions (DeVaris, 2026-10-08)

Recorded on PR #2948. The options considered are kept beside each decision.

1. **Rust and TypeScript connectors ship over gRPC now.** The WASM connector host choice is recorded in an ADR at
   v0.23: wait for a pure-Go component runtime, or run a separate wasmtime host process that speaks gRPC to the engine.
   That ADR supersedes ADR 20260722 in part. WIT-based processors have the same blocker, so the custom wasip1 processor
   ABI (ABI v1) continues until that host exists. _Considered:_ keeping the v0.23 and v0.26 WASM connector placements
   and waiting on gravity or wacogo, both untagged on 2026-10-08. That would have made two releases depend on upstream
   projects we don't control.
2. **`proto/api/v1` stays in this repo, pinned per spec release.** _Considered:_ moving it into the spec repo so every
   contract lives in one place. Rejected because every API field would then need PRs in two repos.
3. **The two Python repos merge into one distribution, with `conduit.connector` and `conduit.client`, before v0.21
   GA.** Users of the pre-GA 0.1.0.dev releases take the import break. _Considered:_ two distributions sharing a
   PEP 420 namespace. Rejected because it keeps two release trains for one language and makes users install two
   packages.
4. **C# stays at v0.27, scoped with early adopters. There is no co-maintainer precondition.** _Considered:_ gating the
   C# start on a co-maintainer. Rejected: no maintainer gates.
5. **Parity window: one Conduit minor for the full tier, two for the enterprise tier (Java, C#).** It is recorded in
   `tiers.yaml` and enforced by the release gate. _Considered:_ one minor for every tier, the brief's original policy.
6. **Spec minors ship at most every other Conduit release**, unless an urgent fix requires otherwise.
7. **Stable error codes are added in spec 1.0.0**, through the `plugin/v1` reason registry (`ErrorInfo`, domain
   `conduit`, config path in metadata), because the protocol has none today. _Considered:_ deferring codes to a later
   minor. Rejected because every SDK would ship ad hoc codes first and then have to break them.

## Risk tier and related

This document is Tier 3 (docs only). The slices it proposes include Tier 1 work: the gRPC processor runtime, the state
API, the dual-ABI host, and any runner scenario touching ack, position or state. Each needs its own PR with a
failure-mode analysis and human sign-off. The spec repo and the two-tier language policy should also get an ADR when the
first slice lands.

- [ADR 20260704 — WASM component model](../architecture-decision-records/20260704-wasm-component-model.md)
  (superseded)
- [ADR 20260722 — gRPC standalone primary, WASM component model deferred](../architecture-decision-records/20260722-wasm-component-model-deferred.md)
- [ADR 20260724 — embed bindings via gRPC](../architecture-decision-records/20260724-embed-bindings-via-grpc.md)
- [ADR 20260704 — local state only](../architecture-decision-records/20260704-local-state-only.md)
- [Python connector SDK](20260707-python-connector-sdk.md), [Rust connector SDK](20260722-rust-connector-sdk.md)
- [Embedded gRPC client libraries](20260724-embed-grpc-client-libraries.md)
- [WASM host egress capability](20260726-wasm-host-egress-capability.md)
- [Registry index schema](20260714-connector-registry-index-schema.md),
  [registry processor artifacts](20260727-registry-processor-artifacts.md)
