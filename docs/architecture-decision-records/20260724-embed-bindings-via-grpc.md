# Embed language bindings (Python, Node) are gRPC clients, not a C-ABI

## Summary

Conduit's non-Go embed bindings — Python first, Node next — will be **gRPC client libraries
driving the existing control-plane API, plus a new "external connector" engine feature**, not a
`libconduit` C-ABI shared library. This supersedes the C-ABI framing in
[`docs/design-documents/20260705-sdk-and-embedding-dx.md`](../design-documents/20260705-sdk-and-embedding-dx.md)
§B3 and the design-ahead doc drafted on PR #2675, originally
`docs/design-documents/20260723-libconduit-c-abi-bindings.md` — renamed and rewritten, in the
same change that introduces this ADR, to
`docs/design-documents/20260724-embed-grpc-client-libraries.md`.

The load-bearing reason: **in an embedded pipeline, the data path never crosses the host-process
boundary.** Records flow source → processors → destination entirely inside the Conduit engine
(`pkg/connector`, `pkg/pipeline`, `pkg/lifecycle-poc/funnel`); the embedding host only issues
lifecycle and status calls (create/start/stop/get/list a pipeline, read its state) — traffic that
already exists as a control-plane RPC (`proto/api/v1/api.proto`). A C-ABI's central benefit —
avoiding a network hop on the hot data path — does not apply here, because the hot data path
never touches the ABI at all under either design. What a C-ABI would add is cost without a
matching benefit for this workload: FFI memory-ownership rules the host must honor exactly, a Go
panic that is fatal to the _entire host process_ if it ever crosses the cgo boundary uncaught
(unlike a Go-to-Go panic, which any Go caller can recover), a C-toolchain-per-target build matrix
for a codebase that otherwise cross-compiles for free, an ASAN gate on top of the Go race
detector, and JSON-string marshaling for every structured value crossing the boundary anyway
(the C-ABI draft's own D1.1: "cgo cannot safely pass Go pointers, slices, or interfaces").

gRPC already exists twice in this codebase for exactly this kind of boundary: the control-plane
API (`proto/api/v1/api.proto`, HTTP-gateway'd via `google.api.http` annotations) and the
connector protocol (`conduit-connector-protocol`'s `pconnector` gRPC services, which the Python
_connector_ SDK already speaks per `docs/design-documents/20260707-python-connector-sdk.md`).
A gRPC client library spans **both** deployment shapes a C-ABI cannot: a local subprocess
(`conduit.local()`, dialing a loopback address) and a remote, already-deployed Conduit service
(`conduit.connect(addr)`) — the same client code, a different address. A C-ABI is fundamentally
in-process-only; it has no story for "drive a Conduit that's already running somewhere else,"
which is the production shape this project cares about most.

## Context

### The control-plane API is already a complete lifecycle/status surface

`proto/api/v1/api.proto` defines three gRPC services with full CRUD-plus-lifecycle coverage,
each exposed over HTTP via `grpc-gateway` (`google.api.http` options, e.g. lines 320–325 for
`ListPipelines`/`CreatePipeline`):

- **`PipelineService`** (lines 318–602): `ListPipelines`, `CreatePipeline`, `GetPipeline`,
  `UpdatePipeline`, `DeletePipeline`, `StartPipeline`, `StopPipeline`, `GetDLQ`, `UpdateDLQ`,
  `ExportPipeline`, `ImportPipeline`, `PlanPipeline`, `ApplyPipeline`.
- **`ConnectorService`** (lines 767–927): `ListConnectors`, `InspectConnector` (server-streaming),
  `GetConnector`, `CreateConnector`, `ValidateConnector`, `UpdateConnector`, `DeleteConnector`,
  `ListConnectorPlugins`.
- **`ProcessorService`** (lines 1003–1138): `ListProcessors`, `InspectProcessorIn`/
  `InspectProcessorOut` (server-streaming), `GetProcessor`, `CreateProcessor`, `UpdateProcessor`,
  `DeleteProcessor`, `ListProcessorPlugins`.
- Plus `GetInfo` (line 1203) and `ListPlugins` (line 1225).

Status is not a separate RPC: `message Pipeline` embeds `message State { Status status;
string error; StoppedReason stopped_reason; }` (lines 41–75), returned inline by
`GetPipeline`/`ListPipelines`. **Gap worth naming honestly:** there is no metrics RPC in this
proto. Metrics are a separate Prometheus `/metrics` HTTP endpoint wired outside the gRPC/OpenAPI
surface (`pkg/conduit/runtime.go:797–808`, mounted at `pkg/conduit/runtime.go:969`, alongside
`/healthz`/`/readyz` per `pkg/conduit/ui.go:30–41`). A client library's `status()` call maps to
`GetPipeline`; anything metrics-shaped means scraping `/metrics` separately, or nothing, in v1 —
not a new RPC invented for this doc.

**Conclusion: the control-plane API RPC surface is sufficient for a lifecycle/status-driving
client library today, with the metrics gap noted above as an explicit non-goal for v1.**

### The connector acquisition model is spawn-by-path today, and already supports dial-by-address underneath

`pkg/plugin/connector/standalone/dispenser.go:39–62` (`NewDispenser`) calls
`conduit-connector-protocol`'s `pconnector/client.New(logger, path, opts...)`
(`pconnector/client/client.go:34–78`), which builds:

```go
cmd := exec.CommandContext(context.Background(), path)
clientConfig := &plugin.ClientConfig{
    HandshakeConfig:  pconnector.HandshakeConfig,
    VersionedPlugins: map[int]plugin.PluginSet{ /* v1, v2 gRPC client stubs */ },
    Cmd:              cmd,
    AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
}
return plugin.NewClient(clientConfig), nil
```

This is HashiCorp go-plugin's standard spawn model: Conduit execs the binary at `path`; the child
writes a handshake line (`CORE_PROTOCOL_VERSION|APP_PROTOCOL_VERSION|NETWORK|ADDRESS|PROTOCOL`)
to stdout; go-plugin's client parses that line and dials the advertised address over gRPC.
`pkg/plugin/connector/standalone/registry.go:37–56` stores each discovered plugin as a
`blueprint{FullName, Specification, Path}` — keyed by filesystem path, one dispenser per binary.

Critically, go-plugin (`github.com/hashicorp/go-plugin@v1.8.0`, `client.go:299–315`) already
supports a second acquisition path that does **not** spawn a process at all:

```go
type ReattachConfig struct {
    Protocol        Protocol
    ProtocolVersion int
    Addr            net.Addr
    Pid             int
    ReattachFunc    runner.ReattachFunc
    Test            bool
}
```

`ClientConfig.Reattach`, wired at `client.go:596–616` and `973–1016`, lets a client dial a
**pre-known `Addr`** directly, skipping `Cmd`/spawn entirely, using the exact same
`VersionedPlugins` gRPC client stubs (`clientv1`/`clientv2`) as the spawn path. Nothing in
`conduit-connector-protocol`'s wire messages (`pconnector.SourceRunRequest`,
`SpecifierSpecifyRequest`, etc.) changes between the two paths — `Reattach` only changes how the
client _finds_ the server, not what it says to it once connected. The Python connector SDK design
(`docs/design-documents/20260707-python-connector-sdk.md:1–17, 163`) independently confirms the
handshake and gRPC server are not Go-specific: a Python-authored connector already writes the
handshake line and serves a plain gRPC server that any `clientv1`/`clientv2` stub can dial.

**Conclusion, stated definitively: an "external connector" — Conduit dialing a pre-known address
instead of spawning a binary — requires no `conduit-connector-protocol` wire change.** It needs a
new dispenser variant (sibling to `standalone.Dispenser`) that constructs `plugin.ClientConfig{
Reattach: &plugin.ReattachConfig{Addr: ...}}` instead of `{Cmd: cmd}`, and a registry entry keyed
by address instead of path. This is new engine-side code (Slice 2, Tier 1/Tier 2 depending on
final shape — see the design doc), not a protocol change.

### The pipeline config model and the state layer already exist and already anticipate this

`pkg/provisioning/config/parser.go:30–38` defines `Pipeline{ID, Status, Name, Description,
Connectors, Processors, DLQ}` — the exact structure a code-first builder targets. It is already
re-exported, unmodified, at the embed API's root: `conduit.go:37`,
`type PipelineConfig = provisioningconfig.Pipeline` — a type alias, not a copy, per B1
(`github.com/conduitio/conduit`, merged, commit `e6a8b9f`). `parser.go:22–29`'s own doc comment
already states the deprecation discipline this ADR does not need to re-litigate: a field added,
removed, or renamed here is subject to the announce → warn → remove policy.

The local KV store is Badger (`github.com/conduitio/conduit-commons/database/badger`), wired at
`pkg/conduit/runtime.go:336`: `badger.New(logger.Logger, cfg.DB.Badger.Path)`. At the embed API,
`Options.DB` (`conduit.go:82–104`) defaults to `DBTypeInMemory` when `Type == ""` — the doc
comment on `Options.DB` already warns "every pipeline configuration is lost once the Engine
stops." Positions persist inside `connector.Instance.State` (`pkg/connector/source.go:227`,
`s.Instance.State = SourceState{Position: p[len(p)-1]}`), written through
`pkg/connector/store.go`'s `Store`, which wraps the generic `database.DB` interface — Badger-file
durable, or in-memory and gone on process exit, depending on `Options.DB.Type`. This is not
theoretical: `tests/chaos` (added in commit `045f283`, the just-landed sev-0 fix for
`Source.Ack`'s ack-before-persist ordering) already exercises SIGKILL-mid-snapshot and
SIGKILL-mid-stream against a **real on-disk Badger DB** to verify recovery — the same store an
embedded `local()` engine uses. An embedder that runs `local()` with the default in-memory store,
or a temp directory it deletes on exit, does not violate any _engine_ invariant, but it does
forfeit Invariant 2 (crash-safe, monotonic positions) at the _embedding_ layer, silently, unless
the client library is explicit about requiring a persistent `Options.DB.Badger.Path` for anything
beyond a throwaway job. See the design doc's failure-modes section for the concrete guidance this
implies for `conduit.local()`.

### What already ships, so this decision changes nothing about it

B1 (`conduit.New`, `Engine.Run`/`Handle.Stop`/`Engine.Close`, `Engine.Import`,
`Engine.StartPipeline`/`StopPipeline`) is merged Go code at the repository root (`conduit.go`).
`Options.API` (`conduit.go:107–117`, `APIOptions{Enabled, GRPCAddress, HTTPAddress}`) already
turns on the exact gRPC/HTTP control-plane surface this ADR's client libraries will dial — meaning
`conduit.local()`'s "spawn a `conduit` process with its API enabled on a loopback address, then
speak gRPC to it" story requires **no new engine code** to stand up a first Python client. B2
(the pipelines-in-code builder, `builder.go`) is in flight (`feat/embed-b2-pipeline-builder`,
not yet on `main`) and is unaffected by this decision — it is Go-side plumbing the Python/Node
builders will mirror in each language's own idiom, not something that crosses an ABI.

## Decision

1. **Python and Node embed bindings are gRPC client libraries**, not a `libconduit` C-ABI shared
   library. They drive the existing control-plane API (`proto/api/v1/api.proto`) for pipeline/
   connector/processor lifecycle and status, and a new **external-connector** engine feature for
   host-implemented inline sources/destinations.
2. **`conduit.local()`** spawns and supervises a `conduit` subprocess (binary provisioning
   TBD — bundle vs. download-on-first-use, an open question for the design doc), with
   `Options.API.Enabled = true` on a loopback address and an explicit, non-ephemeral
   `Options.DB.Badger.Path`, then drives it exactly like `conduit.connect(addr)` drives a remote
   instance — same client code, different process ownership.
3. **`conduit.connect(addr)`** dials an already-running, independently deployed Conduit service.
   This is the production long-running shape; `local()` is dev/job/single-host shaped and must
   never be sold as the production story (see the design doc's honest Mode 1 vs. Mode 2 framing).
4. **External connectors** (Slice 2) let Conduit dial a pre-known address for a connector instead
   of spawning a binary, reusing go-plugin's existing `Reattach`/`Addr` path — zero
   `conduit-connector-protocol` wire changes. This is the mechanism `inline_source`/
   `inline_destination` (a host-language connector server the client library runs in-process) is
   built on.
5. **C-ABI / shared-memory transport is reserved as a demand-gated escape hatch**, not built now:
   only revisited if two real, specific asks surface a use case gRPC structurally cannot serve —
   a proven zero-copy requirement, or a single-binary-with-no-subprocess deployment constraint
   that `local()` cannot meet. Absent that, per CLAUDE.md's no-speculative-generality rule, the
   C-ABI does not get built on a hunch.

## Consequences

- **Positive.** No new build artifact class (`libconduit.{so,dylib,dll}`), no C toolchain per
  target platform, no ASAN gate alongside the existing Go race detector, no FFI ownership/
  threading contract for the binding author to get right with weaker guardrails than Go's own
  `-race` gives. The client library reuses two already-hardened, already-versioned surfaces
  (control-plane API, connector protocol) instead of inventing a third wire encoding. It has an
  honest story for both local and remote/production deployment, which the C-ABI design never did
  (PR #2675's own alternative (b) analysis already conceded gRPC is the right answer for a
  "managed sidecar" embedder — this ADR extends that concession to the primary path, not a
  secondary one).
- **Negative / new costs.** Needs a real client library per language (not "for free" the way a Go
  embedder importing `github.com/conduitio/conduit` is) and the external-connector engine feature
  (Slice 2 — new dispenser variant, address-keyed registry entry, reachability requirements
  documented). `local()` needs binary provisioning (a packaging problem the C-ABI draft's own D6
  already flagged as nontrivial, now inherited by a different artifact: the `conduit` executable
  instead of a shared library). A deployed-remote `conduit.connect(addr)` with an
  `inline_source`/`inline_destination` requires the remote engine to be able to reach back to the
  host process running the Python/Node connector server — a real network-reachability
  requirement absent in the local case, called out explicitly in the design doc rather than
  glossed over.
- **Process.** This ADR supersedes the C-ABI framing in
  `docs/design-documents/20260705-sdk-and-embedding-dx.md` §B3 (Phase-1/2 mapping corrected in
  the same change) and replaces PR #2675's original design-ahead draft
  (`docs/design-documents/20260723-libconduit-c-abi-bindings.md`) with the gRPC-embed design,
  renamed and rewritten in place as `docs/design-documents/20260724-embed-grpc-client-libraries.md`
  — reusing that draft's alternatives analysis (gRPC/UDS bridge, WASM) where it already reached
  the right conclusion.
  `docs/design-documents/20260722-embed-libconduit-v1.md` (B1/B2, merged/in-review) is unaffected:
  its AC-8 C-ABI sketch is no longer the plan this doc's successor design builds toward, but
  nothing in the shipped B1 Go API needs to change as a result — the constraints AC-8/D8 placed on
  `PipelineConfig` (explicit JSON tags, etc.) become optional nice-to-haves for a future JSON-
  emitting surface rather than load-bearing ABI requirements, and are re-evaluated, not assumed,
  in the new design doc.

## Related

- `docs/design-documents/20260724-embed-grpc-client-libraries.md` — the design doc this ADR's
  decision authorizes (client-library API, deployment modes, failure modes, build slices). It
  replaces `docs/design-documents/20260723-libconduit-c-abi-bindings.md` (PR #2675's original
  C-ABI design-ahead draft), renamed and rewritten in the same change that adds this ADR — the
  old filename no longer exists in the tree.
- `docs/design-documents/20260705-sdk-and-embedding-dx.md` §B3 — the original embedding vision
  this ADR reframes from C-ABI to gRPC.
- `docs/design-documents/20260722-embed-libconduit-v1.md` — the merged B1 (+ in-review B2) Go
  embedding API this ADR's client libraries sit on top of via gRPC, not via cgo.
- `docs/design-documents/20260707-python-connector-sdk.md` — confirms the connector-protocol
  handshake and gRPC server are not Go-specific; the precedent the external-connector feature and
  `inline_source`/`inline_destination` build on.
- `proto/api/v1/api.proto` — the control-plane API surface this ADR's client libraries drive.
- `pkg/plugin/connector/standalone/dispenser.go`, `registry.go` — the spawn-by-path dispenser the
  external-connector feature adds a dial-by-address sibling to.
- `pkg/connector/source.go:207–238`, `tests/chaos` (commit `045f283`) — the ack/position
  durability behavior `conduit.local()`'s state-dir guidance is grounded in (Invariant 2).
