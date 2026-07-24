# Embed client libraries (Python, Node): gRPC over the control-plane API + external connectors

## Summary

**This supersedes the C-ABI design previously drafted in this file** (see
[`docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md`](../architecture-decision-records/20260724-embed-bindings-via-grpc.md)
for the decision record). Python and Node embed bindings will be **gRPC client libraries** driving
two already-real surfaces — the control-plane API (`proto/api/v1/api.proto`) and a new
**external-connector** engine feature — not a `libconduit` C-ABI shared library.

The reason this flips, not just relabels, the earlier design: in an embedded pipeline, records
never cross the host-process boundary. Source → processor → destination all happens inside the
Conduit engine process; the embedding host only issues lifecycle and status calls. That traffic
already exists as gRPC — twice, in fact: the control-plane API (`ListPipelines`, `CreatePipeline`,
`StartPipeline`, `GetPipeline`, ...) and the connector protocol
(`conduit-connector-protocol`'s `pconnector` services, which the Python _connector_ SDK already
speaks per `docs/design-documents/20260707-python-connector-sdk.md`). A C-ABI's benefit — avoiding
a network hop on the hot data path — does not apply, because the hot data path never touches
either boundary. What a C-ABI would add is real cost with no matching benefit here: FFI
memory-ownership rules, a Go panic that is fatal to the _entire host process_ if it crosses the
cgo boundary uncaught, a C-toolchain-per-target build matrix, an ASAN gate, and JSON-marshaling
every structured value anyway. gRPC additionally spans a deployment shape the C-ABI structurally
cannot: driving an already-running, independently deployed Conduit service
(`conduit.connect(addr)`), not just an in-process one (`conduit.local()`).

Risk tier: **Tier 1** (public contract — a new client-facing API surface and a new engine
connector-acquisition path). **Design-ahead only: no code ships from this doc.** DeVaris sign-off
is required before any Slice below starts implementation.

## Problem

Conduit has a merged Go embedding API (`github.com/conduitio/conduit`, B1) and an in-review
pipelines-in-code builder (B2, `feat/embed-b2-pipeline-builder`), but nothing for a non-Go host.
A Python or Node application that wants to run — or drive — a Conduit pipeline today has exactly
one option: shell out to the `conduit` CLI and parse its output, or hand-roll a gRPC client
against `proto/api/v1/api.proto` from scratch. Neither is a supported, documented, idiomatic
surface. The embedding vision doc (`docs/design-documents/20260705-sdk-and-embedding-dx.md` §B3)
and a design-ahead draft on PR #2675 previously proposed closing this gap with a `libconduit`
C-ABI shared library plus per-language FFI bindings. This doc rejects that mechanism and proposes
the alternative: idiomatic gRPC client libraries over surfaces that already exist and are already
versioned.

## Alternatives considered

**(a) gRPC client library over the control-plane API + external connectors — chosen.** No new
wire protocol: the control-plane API (`proto/api/v1/api.proto`) already has full CRUD-plus-
lifecycle coverage for pipelines, connectors, and processors (see Context), and the connector
protocol already supports dialing a pre-known address instead of spawning a binary (go-plugin's
`ReattachConfig`, see Context). A gRPC client is idiomatic per language (`grpcio`/`grpc.aio` for
Python, `@grpc/grpc-js` for Node), needs no C toolchain, and works identically whether the engine
is a local subprocess or a remote deployed service — the same client code, a different address.
Cost: engine lifecycle/status calls now cross a real IPC boundary (loopback or network) instead of
a direct in-process Go call, and `conduit.local()` needs a binary-provisioning story (a real
Conduit executable must exist on the host).

**(b) `libconduit` C-ABI shared library — rejected, previously chosen in this doc's earlier
draft.** In-process, sharing the host's address space, no network hop on the lifecycle path. This
looked attractive when the design didn't distinguish the data path from the lifecycle path: the
whole point of "in-process" is avoiding a hop on the _hot_ path, but the hot path (record
movement) never crosses this boundary either way — only lifecycle calls do, and those are
low-frequency (create/start/stop/status, not per-record). Once that distinction is made, the
C-ABI's core justification evaporates and only its costs remain: cgo cannot pass Go pointers,
slices, or interfaces, so every structured value still crosses as a JSON string anyway (no
marshaling savings over gRPC's protobuf); a C-toolchain-per-target build (Go's
`-buildmode=c-shared` requires `CGO_ENABLED=1`) that the rest of Conduit's pure-Go, cross-compile-
for-free build never needed; a documented-but-compiler-unenforced memory-ownership and
single-writer-per-handle threading contract, strictly weaker than Go's own `-race`-detectable
guarantees; and a Go panic reaching the cgo boundary uncaught that is fatal to the _entire host
process_ — unlike a Go-to-Go panic, which any caller up the stack can `recover()`. It also has no
story for driving an already-deployed, remote Conduit service — a C-ABI is fundamentally
in-process-only, so a production "point our client library at our running Conduit fleet" use case
would need an entirely separate gRPC path anyway, at which point the C-ABI is solving only the
dev/local case while a second, unavoidable gRPC design solves production. Building two mechanisms
to cover what one gRPC client already covers is the opposite of CLAUDE.md's simplicity bar.

**(c) A local gRPC/UDS bridge to an out-of-process engine.** This is not really a third
alternative — it _is_ what (a) becomes for the `conduit.local()` case (dialing a spawned
subprocess over loopback gRPC, previously enumerated as alternative (b) in the superseded C-ABI
draft and dismissed there as "not in-process embedding, so it doesn't compete with the C-ABI").
That framing was backwards: once the C-ABI's in-process benefit is understood to not apply to the
lifecycle-only path this design covers, there is no separate "in-process" competitor to prefer
over it — this _is_ the chosen design, not a fallback.

**(d) WASM (compile Conduit itself to a WASM module).** Still rejected, unchanged from the
superseded draft's reasoning: Conduit's own engine (goroutine scheduling, real file/network I/O, a
database driver, an HTTP/gRPC server) is a poor fit for any WASM sandbox today — WASI Preview 1
has no engine-scale concurrency model, and the component model needed for anything richer is
NO-GO on a pure-Go host per `docs/architecture-decision-records/20260722-wasm-component-model-deferred.md`.
This doc's problem — "run the whole engine inside someone else's process" — is the opposite
direction from WASM's fit (a plugin running inside Conduit), and the two should never be
conflated.

## Context

### The control-plane API is already a complete lifecycle/status surface

`proto/api/v1/api.proto` defines, with full CRUD-plus-lifecycle coverage, each exposed over HTTP
via `grpc-gateway` (`google.api.http` annotations, e.g. lines 320–325):

- **`PipelineService`** (lines 318–602): `ListPipelines`, `CreatePipeline`, `GetPipeline`,
  `UpdatePipeline`, `DeletePipeline`, `StartPipeline`, `StopPipeline`, `GetDLQ`, `UpdateDLQ`,
  `ExportPipeline`, `ImportPipeline`, `PlanPipeline`, `ApplyPipeline`.
- **`ConnectorService`** (lines 767–927): `ListConnectors`, `InspectConnector` (server-streaming
  record inspection), `GetConnector`, `CreateConnector`, `ValidateConnector`, `UpdateConnector`,
  `DeleteConnector`, `ListConnectorPlugins`.
- **`ProcessorService`** (lines 1003–1138): `ListProcessors`, `InspectProcessorIn`/
  `InspectProcessorOut`, `GetProcessor`, `CreateProcessor`, `UpdateProcessor`, `DeleteProcessor`,
  `ListProcessorPlugins`.
- Plus `GetInfo`/`ListPlugins` (lines 1203, 1225).

`message Pipeline` embeds `message State { Status status; string error; StoppedReason
stopped_reason; }` (lines 41–75), returned inline by `GetPipeline`/`ListPipelines` — status is not
a separate call. **Gap, stated honestly:** there is no metrics RPC. Metrics are a separate
Prometheus `/metrics` HTTP endpoint outside the gRPC/OpenAPI surface
(`pkg/conduit/runtime.go:797–808`, mounted at `runtime.go:969`). The client library's `status()`
maps to `GetPipeline`; a metrics story is out of scope for v1 (see Observability).

### The connector acquisition model already supports dial-by-address underneath

`pkg/plugin/connector/standalone/dispenser.go:39–62` dispenses a connector by calling
`conduit-connector-protocol`'s `pconnector/client.New(logger, path, opts...)`
(`pconnector/client/client.go:34–78`), which builds `plugin.ClientConfig{Cmd: exec.CommandContext(...,
path), AllowedProtocols: [ProtocolGRPC], VersionedPlugins: {...clientv1/clientv2 gRPC stubs...}}` —
HashiCorp go-plugin's standard spawn model: Conduit execs the binary, the child writes a handshake
line to stdout, go-plugin parses the advertised address and dials it over gRPC. The registry
(`pkg/plugin/connector/standalone/registry.go:37–56`) keys each plugin by filesystem `Path`.

go-plugin (`github.com/hashicorp/go-plugin@v1.8.0`, `client.go:299–315`, wired at `client.go:596–616`
and `973–1016`) already has a second, built-in acquisition path that skips spawning entirely:
`ClientConfig.Reattach *ReattachConfig{Addr net.Addr, Pid int, Protocol, ProtocolVersion}` dials a
**pre-known address** directly, using the identical `VersionedPlugins` gRPC client stubs as the
spawn path. **Nothing in `conduit-connector-protocol`'s wire messages changes between the two
paths** — `Reattach` only changes how the client _finds_ the server. The Python connector SDK
design independently confirms the handshake and gRPC server aren't Go-specific
(`docs/design-documents/20260707-python-connector-sdk.md:1–17, 163`): a Python-authored connector
already writes the handshake line and serves a plain gRPC server any `clientv1`/`clientv2` stub can
dial.

**External connector = a new dispenser variant, sibling to `standalone.Dispenser`, that builds
`plugin.ClientConfig{Reattach: &plugin.ReattachConfig{Addr: ...}}` instead of `{Cmd: cmd}`, and a
registry entry keyed by address instead of path.** No protocol change. New engine-side code:
Slice 2 below.

### The pipeline config model and state layer already exist

`pkg/provisioning/config/parser.go:30–38` defines `Pipeline{ID, Status, Name, Description,
Connectors, Processors, DLQ}` — the target shape for any code-first builder — re-exported
unmodified at the embed API root: `conduit.go:37`, `type PipelineConfig =
provisioningconfig.Pipeline` (a type alias, not a copy; B1, merged, commit `e6a8b9f`).

The local KV store is Badger (`github.com/conduitio/conduit-commons/database/badger`), wired at
`pkg/conduit/runtime.go:336`: `badger.New(logger.Logger, cfg.DB.Badger.Path)`. `Options.DB`
(`conduit.go:82–104`) defaults to `DBTypeInMemory` when `Type == ""` — its own doc comment already
warns "every pipeline configuration is lost once the Engine stops." Positions persist inside
`connector.Instance.State` (`pkg/connector/source.go:227`), written through
`pkg/connector/store.go`'s `Store`, itself a thin layer over the generic `database.DB` interface —
Badger-durable, or in-memory and gone on process exit, depending on `Options.DB.Type`. This is not
theoretical: `tests/chaos` (added in commit `045f283`, the just-landed sev-0 fix for `Source.Ack`'s
ack-before-persist ordering, `pkg/connector/source.go:207–238`) already SIGKILLs mid-snapshot and
mid-stream against a **real on-disk Badger DB** to verify recovery — the same store `local()` uses.

### What already ships, unaffected by this design

B1 (`conduit.New`, `Engine.Run`/`Handle.Stop`/`Engine.Close`, `Engine.Import`,
`Engine.StartPipeline`/`StopPipeline`) is merged at the repo root (`conduit.go`). `Options.API`
(`conduit.go:107–117`, `APIOptions{Enabled, GRPCAddress, HTTPAddress}`) already turns on the exact
gRPC/HTTP surface this design's client libraries dial — `conduit.local()`'s "spawn a `conduit`
process with its API enabled on loopback, then speak gRPC to it" needs **zero new engine code**.
B2 (`builder.go`, in flight, not yet merged) is unaffected — it's Go-side plumbing each language's
own builder mirrors in its own idiom, never crossing a boundary.

## Decision — client-library API design

### Pipeline builder

Each binding exposes a fluent builder idiomatic to its language, producing the same
`config.Pipeline`/`PipelineConfig` shape the YAML provisioner parses (mirroring B2's `Build()`
contract — raw, unenriched output, validated the same way `conduit pipeline validate` validates):

```python
# Python (illustrative)
pipeline = (
    conduit.Pipeline("orders-sync")
    .source("postgres", url="postgres://...", tables=["orders"])
    .destination("kafka", brokers=["localhost:9092"], topic="orders")
    .process("orders", "filter.field", condition="orders.deleted == false")
)
```

```javascript
// Node (illustrative)
const pipeline = new Pipeline("orders-sync")
  .source("postgres", { url: "postgres://...", tables: ["orders"] })
  .destination("kafka", { brokers: ["localhost:9092"], topic: "orders" });
```

`Settings` is a flat `map<string, string>` today (`config.Connector.Settings`,
`config.Processor.Settings`) — v1 bindings pass and validate strings; typed config
(`postgres.Source(url=..., tables=[...])` with real parameter types) is a fast-follow (see Build
slices) generated from connector param specs, not hand-written per connector.

### `conduit.local()` and `conduit.connect(addr)`

Both return the same client object; only construction differs:

- **`conduit.local(state_dir=..., binary=None)`** spawns and supervises a `conduit` subprocess:
  `conduit run --api.enabled --api.grpc-address=127.0.0.1:0 --db.type=badger
  --db.badger.path=<state_dir>` (or the equivalent `Options` if a future Go shim embeds B1
  directly instead of shelling to the released binary — an open question, see Open questions).
  Discovers the OS-assigned port from the subprocess's structured startup log line, then dials it.
  Owns the subprocess lifecycle: killed on client `close()`/`__exit__`, or on host-process exit via
  an atexit/finalizer hook — best-effort, not guaranteed (see Failure modes).
- **`conduit.connect(addr)`** dials an already-running, independently deployed Conduit service at
  `addr`. No process ownership — `close()` just closes the gRPC channel.

Both expose the same async/idiomatic surface: `pipeline.deploy()` → `CreatePipeline`/`Import`,
returning a **run handle** — `run.status()` → `GetPipeline` (reads `Pipeline.State`), `run.stop()`
→ `StopPipeline`, `run.wait(timeout)` polling or streaming status until terminal. Errors surface
as the existing `conduiterr.ConduitError` shape (code, message, config path, suggestion, docs URL)
already crossing the CLI/API/MCP boundary today via `google.rpc.Status`/`ErrorInfo` — reused
as-is, not reinvented, translated into an idiomatic exception per language
(`conduit.ConduitError` in Python, `class ConduitError extends Error` in Node).

### `inline_source` / `inline_destination`

```python
pipeline.source(MyPythonSource())  # a conduit-connector-sdk-python Source, not a plugin name
```

The host process runs the Python-SDK connector's gRPC server in-process (the exact server shape
`docs/design-documents/20260707-python-connector-sdk.md` already designs), and the client library
registers its address with the (local or remote) engine as an **external connector** — see below.
This is Case B; plain named connectors (Case A) never touch this path.

## External-connector feature (Slice 2)

Address-based connector acquisition: the engine dials a pre-known `host:port` instead of spawning
a binary, reusing go-plugin's `ReattachConfig` path (Context) — no `conduit-connector-protocol`
wire change. Concretely: a new sibling to `pkg/plugin/connector/standalone.Dispenser` that accepts
an address instead of a path, and a pipeline-config extension so `config.Connector` can reference
`address: host:port` instead of `plugin: <name>@<version>`. **Reachability requirement, stated
plainly:** the engine process must be able to open a TCP connection to the given address. For
`conduit.local()` (Case A/B, engine co-located with the host on the same machine) this is
trivially loopback. For `conduit.connect(addr)` with an inline connector (Case B against a remote
engine), the **remote engine** must be able to reach back to the **host process** — a real
network-reachability requirement (open inbound port, no NAT/firewall in the way) that does not
exist in the local case and must never be glossed over in docs or error messages: a connector
registration should fail fast with an actionable error if the engine cannot reach the given
address at registration time, not time out silently mid-run.

## Deployment modes — framed honestly

**Mode 1 — `conduit.local()`: engine co-located, lifecycle tied to the host application.** Fits
dev, notebooks, one-off jobs, single-host batch/ETL. Requires an explicit, non-ephemeral
`state_dir` (backing `Options.DB.Badger.Path`) or at-least-once breaks the moment the host process
exits and the temp dir goes with it (Invariant 2, tied to the just-fixed sev-0 above). **Do not
sell Mode 1 as a production-pipeline story** — it has no independent lifecycle, no restart-without-
the-host-restarting, and no fleet management; a crash of the host application takes the engine
down with it (or leaves an orphaned subprocess if supervision is buggy — see Failure modes).

**Mode 2 — `conduit.connect(addr)`: a deployed, long-running Conduit service.** Fits production.
The client library is a thin, stateless gRPC client; the engine's lifecycle, restart policy, and
persistence are managed independently (systemd, Kubernetes, the fleet console once it ships), not
by whatever process happens to import the client library. This is the mode a production pipeline
should use.

**The Case-B host-reachability wrinkle applies only when combining Mode 2 with
`inline_source`/`inline_destination`**: a remote engine must be able to open a connection back to
the host process running the connector server. This is a real operational constraint (the host
needs a reachable, stable address — awkward behind NAT, in a serverless function, or in a
short-lived job) that Case A (named connectors only) never encounters. Docs must state this
plainly: **`inline_*` connectors are a Mode-1 (local) feature first; Mode-2 support is explicitly
gated on solving this reachability problem**, not assumed to work out of the box.

## Failure modes

1. **`conduit` subprocess crash/restart (Mode 1).** The client library's supervisor detects the
   dead process (exit code / closed gRPC channel) and surfaces a `ConduitError` on the next call
   rather than hanging — it does not auto-restart transparently (auto-restart would silently
   re-run `Import` against a corrupted or partial view of the previous state; the caller decides).
   Mitigation: `run.status()` distinguishes "engine unreachable" from "pipeline degraded."
2. **Ephemeral state dir → position loss (Mode 1), tied to Invariant 2 and the just-fixed sev-0
   (`Source.Ack` ordering, `pkg/connector/source.go:207–238`, commit `045f283`).** `conduit.local()`
   without an explicit `state_dir` defaults to in-memory (`Options.DB` zero value,
   `DBTypeInMemory`) — every position is lost on process exit, silently, unless the client library
   makes this failure mode impossible to reach by accident: `local()` should require an explicit
   `state_dir` argument (no silent in-memory default) for any pipeline expected to survive a
   restart, and the docs must say so before the first code sample, not in a footnote.
3. **Binary/version mismatch between the client library and the `conduit` engine (Mode 1).** The
   library bundles or downloads a specific `conduit` version; if a user's system has an
   incompatible or stale binary on `PATH` that gets picked up instead, behavior drifts silently.
   Mitigation: the library never relies on `PATH` lookup for the binary it spawns (same lesson
   `20260707-python-connector-sdk.md` already learned for connector subprocesses — no inherited
   `PATH`); it pins and verifies the exact binary/version it provisioned, and `GetInfo` (already in
   the control-plane API, line 1203) is queried at connect-time to confirm the running engine's
   version is compatible with the client library's minimum-supported API version, failing fast
   with an actionable error on mismatch — never a version-skewed protobuf field silently ignored.
4. **External-connector disconnect (Slice 2, Case B).** The host process running an inline
   connector's gRPC server crashes or the connection drops mid-run. The engine observes this as an
   ordinary connector-plugin failure (the same path an ordinary standalone plugin crash already
   takes — Conduit's existing plugin-failure handling, not new machinery) and the pipeline
   transitions to `STATUS_DEGRADED` with the error surfaced in `Pipeline.State.error`, per the
   existing at-least-once floor (Invariant 3) — it is a DLQ/halt/degrade situation, never a silent
   drop.
5. **Host↔engine control-channel loss mid-run (either mode).** If the gRPC channel between the
   client library and the engine drops (network blip, engine restart) while a pipeline is running,
   the _pipeline itself_ is unaffected — it is the engine's responsibility, not the client
   library's, and continues running (or fails per its own invariants) independent of whether
   anyone is watching. The client library's `run` handle must reconnect and re-fetch `GetPipeline`
   state rather than assume the pipeline stopped just because the channel did — conflating "I lost
   the connection" with "the pipeline stopped" would be a client-library bug, not an engine one.

## Upgrade / rollback

No new serialized/persisted format: `PipelineConfig` on the wire is the same JSON/protobuf-derived
shape the control-plane API and YAML provisioning already use, governed by the same announce →
warn → remove policy `parser.go:22–29` already documents. A client-library version bump is an
ordinary package upgrade (`pip install --upgrade`, `npm update`); rollback is reinstalling the
prior version. The one real compatibility surface is **client-library-minimum-API-version vs.
engine-`GetInfo`-version** (Failure mode 3) — each client library release documents its minimum
supported engine version, checked at `connect()`/`local()` startup, not discovered as a runtime
protobuf-decode error later.

## Observability

`run.status()` (`GetPipeline`) and log streaming are the v1 story. Metrics are explicitly deferred
to the existing `/metrics` Prometheus endpoint (`pkg/conduit/runtime.go:797–808`) — scraped
directly by the host's own monitoring, or via the host's per-language Prometheus client — not a
new client-library API. `InspectConnector`/`InspectProcessorIn`/`InspectProcessorOut` (existing
server-streaming RPCs, lines 775, 1011, 1018) are directly usable by the client library for a
`pipeline.inspect()`-style debugging affordance without any new engine surface.

## Build slices

- **Slice 1 — Case-A Python client library. No core (Go) changes.** Uses only the existing
  control-plane API and `Options.API` (already merged). Builder, `local()`/`connect()`, run handle,
  `ConduitError` translation, named (non-inline) connectors only.
- **Slice 2 — External-connector engine feature → unlocks Case B.** New address-based dispenser
  variant (Context), pipeline-config `address:` field, reachability validation at registration.
  Tier 1 (touches connector acquisition, a data-path-adjacent surface) — needs its own design-doc
  sign-off pass at implementation time, not rubber-stamped by this doc alone.
- **Slice 3 — Node client library.** Mirrors Slice 1 in `@grpc/grpc-js`/idiomatic JS/TS, once
  Python has proven the pattern.
- **Fast-follow — typed-config codegen.** Generate typed builder methods
  (`postgres.Source(url=..., tables=[...])`) from each connector's param spec
  (`connector.yaml`/registry `Specification`), replacing the flat `Settings` map with real
  per-connector types and IDE-visible parameters — not hand-maintained per connector.

## Open questions for DeVaris

1. **Repo/packaging home.** A new `conduit-sdk-python` (embedding), distinct from
   `conduit-connector-sdk-python` (connector authoring, a different persona)? What PyPI name — the
   superseded C-ABI draft proposed `conduit-embed`; does that still read right for a gRPC-based
   client library, or does something like `conduit-client`/`conduit` (if unclaimed) fit better?
2. **Release target.** Does Slice 1 (Python client library, no core changes) target a v0.19
   fast-follow, or land as the v0.20 anchor alongside the Python connector SDK?
3. **Binary-provisioning mechanism for `conduit.local()`.** Bundle a `conduit` binary inside the
   Python wheel per platform (heavier package, no network dependency, mirrors the C-ABI draft's
   wheel-packaging discipline), or download-on-first-use with version pinning and a checksum
   (lighter package, needs network access and a trust story)? Either needs the version-check
   discipline in Failure mode 3 regardless of which is chosen.

## Related

- `docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md` — the ADR this design
  doc implements.
- `docs/design-documents/20260705-sdk-and-embedding-dx.md` §B3 — the original embedding vision,
  reframed here from C-ABI to gRPC.
- `docs/design-documents/20260722-embed-libconduit-v1.md` — the merged B1 (+ in-review B2) Go
  embedding API this design's client libraries sit on top of via gRPC.
- `docs/design-documents/20260707-python-connector-sdk.md` — confirms the connector-protocol
  handshake and gRPC server aren't Go-specific; the precedent `inline_source`/`inline_destination`
  and the external-connector feature build on; also the source of the "no inherited `PATH`" /
  self-contained-artifact packaging lesson reused in Failure mode 3.
- `proto/api/v1/api.proto` — the control-plane API surface this design's client libraries drive.
- `pkg/plugin/connector/standalone/dispenser.go`, `registry.go` — the spawn-by-path dispenser
  Slice 2's external-connector feature adds a dial-by-address sibling to.
- `pkg/connector/source.go:207–238`, `tests/chaos` (commit `045f283`) — the ack/position
  durability behavior Failure mode 2's `state_dir` guidance is grounded in (Invariant 2).
- `pkg/foundation/cerrors/conduiterr/conduiterr.go` — the `ConduitError` shape this design's error
  propagation reuses unchanged.
- `ROADMAP.md`, "Embedded v1" — Phase 1/Phase 2 mapping for B1/B2 (Go) vs. this design's client
  libraries (Python/Node); the client-library work is a distinct workstream, not a Phase-2-only
  gate the way the superseded C-ABI draft assumed.
