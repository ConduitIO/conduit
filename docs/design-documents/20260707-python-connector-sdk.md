# Python connector SDK

## Summary

Design and phased plan for `conduit-connector-sdk-python`, a new repo delivering an
idiomatic Python SDK for building Conduit **source and destination connectors** that
run as standalone (subprocess, gRPC, go-plugin-handshake) plugins — no Conduit code
changes required. This is the detailed follow-on to the Python tier of
[SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md) (persona
A1: "Python — gRPC-standalone first ... First `libconduit` consumer on the author
side"). This document is planning/design only; it does not implement the SDK.

The technical crux — and the part most likely to sink the project if under-specified
— is replicating HashiCorp go-plugin's subprocess handshake from a pure-Python
process with no Go runtime. That handshake is fully characterized below with exact
constants, cited to the Go source Conduit and the SDK actually run. It is a plain
stdout line + a standard gRPC server; nothing in it requires Go. The rest of the
document designs a Python API that mirrors the Go SDK's semantics (interfaces,
config validation, acceptance contract, batching) while being idiomatically
Python rather than a transliteration (async-native, pydantic-based config +
paramgen-by-introspection, `bytes | dict` records instead of a `Data` interface,
exceptions instead of Go's `(n, err)` partial-write convention).

## Context

### The problem

- The plugin author's SDK is Go-only today (`conduit-connector-sdk`, pinned at
  `v0.14.1` per the template's `go.mod`). Python — the dominant language for the
  data/AI audience Conduit is targeting — has no SDK, only an unrelated,
  experimental, **WASM-based** `conduit-processor-sdk-python` (see "Relationship to
  the existing Python processor prototype" below), which is a different extension
  mechanism for a different plugin kind and is not reusable here as-is.
- "A connector" is defined by conformance to the connector protocol
  (`conduit-connector-protocol`) and by passing the acceptance-test suite — not by
  language. Today that suite (`AcceptanceTest`,
  `conduit-connector-sdk/acceptance_testing.go:54-58`) exists only in Go, so there is
  no way for a Python-authored connector to prove conformance.
- Standalone connectors are launched by Conduit as **subprocesses** using
  HashiCorp go-plugin over gRPC (`conduit/pkg/plugin/connector/standalone/
  dispenser.go:55` → `conduit-connector-protocol/pconnector/client/client.go`). A
  Python process must replicate that launch protocol exactly, or Conduit cannot
  start it at all — this is a hard gate before any Python-authored business logic
  matters.

### Constraints (from CLAUDE.md, binding on this design)

- Language floor: Python 3.11+, matching the house style already used for
  tooling elsewhere in the org.
- Errors are API: every user-facing error needs a stable shape and an actionable
  message — same bar as Conduit's own CLI/API errors.
- No speculative generality: this SDK targets the gRPC-standalone connector path
  only. WASM is the processor extension mechanism (ADR `20260704-wasm-component-model.md`),
  not the connector one — see the relationship note below. Adding WASM-connector
  support is out of scope without a documented demand signal.
- Docs and template move with the code: Phase 1 ships an example connector, not
  just a library.
- Design doc before code for anything touching a public contract — this **is**
  that doc for the SDK's own public surface (the base classes, config model, wire
  behavior). Downstream connector authors will build against what's decided here,
  so treat the API shape as a public contract from v0.1.

### Relationship to the existing Python processor prototype

`ConduitIO/conduit-processor-sdk-python` already exists — description "[WIP]
Experimental Python SDK for Conduit processors" (`gh repo view
ConduitIO/conduit-processor-sdk-python`). Its file tree (`gh api repos/ConduitIO/
conduit-processor-sdk-python/git/trees/main`) shows: `world.wit` +
`componentize-py` + a hand-rolled `malloc`/pointer WASM ABI in `sdk.py:1-100`
matching the Go SDK's WASM ABI, and protobuf stubs generated via `buf generate`
with the **`protocolbuffers/python` + `protocolbuffers/pyi`** remote BSR plugins
(`proto/buf.gen.yaml`) — i.e., standard protoc-style `_pb2.py`/`_pb2.pyi`
output, not `betterproto`. Two takeaways:

1. **It is not prior art for this SDK's transport.** It targets the WASM
   component-model path for _processors_; connectors use go-plugin/gRPC
   subprocesses, a different mechanism entirely (per the repo map in CLAUDE.md:
   "`conduit-processor-sdk`: standalone processors compile to WASM" vs.
   `conduit-connector-sdk` which has no WASM story). Do not conflate the two —
   this design doc is gRPC-only.
2. **It is real precedent for codegen tooling**: the org already generates Python
   protobuf code via `buf generate` against BSR modules
   (`buf.build/conduitio/conduit-connector-protocol`, confirmed present at
   `conduit-connector-protocol/proto/buf.yaml:2`, depending on
   `buf.build/conduitio/conduit-commons`). This SDK should follow the same
   pattern for consistency across the org's Python surface (see Alternatives
   Considered, "Protobuf codegen tool").

## Decision

### 1. Protocol & handshake (the crux)

This is the make-or-break section. Every constant below is cited to the exact Go
source Conduit runs today.

#### 1.1 The handshake is not Go-specific

Conduit (client) and the Go SDK (server) both import the **same shared symbol**
for their `HandshakeConfig` — not just matching values by convention:

```go
// conduit-connector-protocol/pconnector/pconnector.go:19-22
var HandshakeConfig = plugin.HandshakeConfig{
    MagicCookieKey:   "CONDUIT_PLUGIN_MAGIC_COOKIE",
    MagicCookieValue: "204e8e812c3a1bb73b838928c575b42a105dd2e9aa449be481bc4590486df53f",
}
```

- Server (SDK) side: `conduit-connector-protocol/pconnector/server/serve.go:42`
  passes it into `plugin.ServeConfig`; `conduit-connector-sdk/serve.go:106` just
  calls `server.Serve(...)`.
- Client (Conduit) side: `conduit-connector-protocol/pconnector/client/client.go:48`
  passes the identical value into `plugin.ClientConfig`. Conduit itself never
  constructs its own `plugin.NewClient` — `conduit/pkg/plugin/connector/
  standalone/dispenser.go:55` delegates to `client.New(...)` in the protocol repo.

A Python plugin must:

1. Read `os.environ["CONDUIT_PLUGIN_MAGIC_COOKIE"]` and compare it to the value
   above at startup; exit non-zero with a diagnostic if it doesn't match or is
   absent (mirrors `go-plugin@v1.8.0/server.go:247-266`, which is enforced only by
   the server-side library, not by anything Conduit itself checks beyond
   requiring the line — a Python implementation must do this check itself, there
   is no free lunch from the Go runtime).
2. Negotiate protocol version via the `PLUGIN_PROTOCOL_VERSIONS` env var
   (`go-plugin@v1.8.0/client.go:642`) and pick the highest version it supports
   that the client also lists (`go-plugin@v1.8.0/server.go:148-221`). Two
   protocol versions exist today: v1 = `conduit-connector-protocol/pconnector/v1/
   version.go:19` (`const Version = 1`), v2 = `pconnector/v2/version.go:18`
   (`const Version = 2`). **Target v2** — v1 is the older, more verbose,
   single-service style (`proto/connector/v1/connector.proto`, 368 lines);
   v2 is current and what Conduit's dispenser actually speaks.
3. Print exactly one handshake line to **stdout** (not stderr — stdout is the
   channel go-plugin's client parses) once the gRPC server is listening:

   ```text
   CORE-PROTOCOL-VERSION|APP-PROTOCOL-VERSION|NETWORK|ADDRESS|PROTOCOL|SERVER-CERT
   ```

   Format and field semantics from `go-plugin@v1.8.0/server.go:426-445`; parse
   side at `go-plugin@v1.8.0/client.go:838-926` (splits on `|`, requires ≥4
   parts). Concretely: `CORE-PROTOCOL-VERSION` is always literal `1`
   (`server.go:33`, unrelated to the v1/v2 app-protocol negotiated above —
   confusingly, go-plugin has its own "core" protocol version separate from
   Conduit's connector-protocol version); `APP-PROTOCOL-VERSION` is `2` (or `1`);
   `NETWORK` is `tcp` or `unix`; `ADDRESS` is the listen address; `PROTOCOL` is
   literal `grpc` (NetRPC is explicitly disabled on both sides via
   `plugin.NetRPCUnsupportedPlugin`, `server/serve.go:125` and
   `client/client.go:93`); `SERVER-CERT` is only interpreted if longer than 50
   chars (AutoMTLS) — **Conduit's client never sets `AutoMTLS: true`**
   (absent from `pconnector/client/client.go`), so **leave this field empty**; no
   TLS is in play.

4. Choose **TCP**, not Unix domain socket, for the listen transport. go-plugin's
   own server defaults to a Unix socket on non-Windows and TCP only on Windows
   (`server.go:528-535`), but the client-side parser accepts either
   (`client.go:888-901`) — nothing requires matching the Go default. TCP on
   `127.0.0.1:0` (OS-assigned port) is simpler to implement correctly in Python
   (`asyncio`/`grpc.aio` both support it natively) and sidesteps Unix-socket
   temp-directory/permission bookkeeping. This also gets Windows support "for
   free" per CLAUDE.md's Windows CI requirement, rather than needing a
   platform branch.
5. Serve, on that listener, a **plain gRPC server** — nothing exotic. go-plugin's
   `GRPCServer.Init()` additionally registers `grpc.health.v1.Health` (service
   name `"plugin"`, `GRPCServiceName = "plugin"`, set to `SERVING`,
   `grpc_server.go:24-81`), gRPC reflection, a `GRPCBroker` service, and a
   `GRPCController` service (carries the `Shutdown` RPC). Of these, two matter in
   practice for a Python implementation and the rest are cosmetic:
   - **`grpc.health.v1.Health`** — register it (grpcio ships
     `grpc_health.v1` support); low effort, part of the documented contract.
   - **`GRPCController.Shutdown`** — Conduit's `Close()` RPCs this on teardown
     (`grpc_client.go:106-108`); if unimplemented, `Close()` errors and Conduit
     force-kills the process ~2s later instead (`client.go:530-567`) — the
     pipeline still tears down correctly, just not gracefully from go-plugin's
     point of view. **Implement it** (trivial: acknowledge and
     `os._exit(0)`/stop the server) so shutdown is clean rather than
     relying on the timeout fallback, per CLAUDE.md invariant 7 (graceful
     shutdown by default).
   - `GRPCBroker` exists for nested/multiplexed plugin scenarios (a plugin that
     itself dispenses sub-plugins over the same connection). The connector
     protocol's `SourcePlugin`/`DestinationPlugin`/`SpecifierPlugin` services run
     directly on the single primary gRPC connection — **a no-op broker stub is
     sufficient**; Python does not need to reimplement go-plugin's internal
     yamux-based multiplexer.
   - gRPC reflection is optional polish (aids `grpcurl`/debugging); include it,
     it's a few lines with `grpc_reflection`.
6. Exec with a **clean environment**. Conduit's dispenser sets
   `cmd.Env = make([]string, 0)` before appending its own vars
   (`pconnector/client/client.go:45`) — the subprocess gets _only_ go-plugin's
   own vars plus the Conduit-set ones below, **no inherited `PATH`**. Practical
   consequence for Python: the connector's launch shebang/entry point must be an
   absolute interpreter path (e.g. baked into a PyInstaller/zipapp artifact, or a
   wrapper script that resolves its own venv by absolute path) — anything that
   relies on `PATH`-based `python3` resolution at exec time will fail. This is
   flagged explicitly in Risks & Open Questions and drives the packaging
   decision in §3.

#### 1.2 Env vars a Python subprocess should read

| Var | Source | Purpose |
| --- | --- | --- |
| `CONDUIT_PLUGIN_MAGIC_COOKIE` | go-plugin | handshake cookie, must match §1.1 |
| `PLUGIN_PROTOCOL_VERSIONS` | go-plugin | client-supported protocol versions to negotiate against |
| `PLUGIN_MIN_PORT`/`PLUGIN_MAX_PORT` | go-plugin | only relevant if restricting the TCP port range; not required |
| `CONDUIT_CONNECTOR_UTILITIES_GRPC_TARGET` | `pconnutils/env_vars.go:18` | address of Conduit's connector-utilities gRPC service (schema registry access, etc.) |
| `CONDUIT_CONNECTOR_TOKEN` | `pconnutils/env_vars.go:19` | auth token for calling back into Conduit |
| `CONDUIT_CONNECTOR_ID` | `pconnector/env_vars.go:18-20` | this connector instance's ID |
| `CONDUIT_CONNECTOR_LOG_LEVEL` | same | log level to honor |
| `CONDUIT_CONNECTOR_MAX_RECEIVE_RECORD_SIZE` | same | gRPC max message size to configure |

Unused/irrelevant given the TCP choice: `PLUGIN_UNIX_SOCKET_DIR`,
`PLUGIN_UNIX_SOCKET_GROUP`, `PLUGIN_CLIENT_CERT` (AutoMTLS only).

#### 1.3 RPC surface to implement (protocol v2)

From `conduit-connector-protocol/proto/connector/v2/{source,destination,specifier}.proto`,
confirmed against `conduit-connector-protocol/proto/buf.yaml` (BSR module
`buf.build/conduitio/conduit-connector-protocol`, dep on
`buf.build/conduitio/conduit-commons`):

- **`SourcePlugin`** (`source.proto:16-73`): `Configure` (unary), `Open`
  (unary), `Run` (**bidirectional stream** — plugin streams record batches out,
  Conduit streams ack-position batches back, independently/concurrently), `Stop`
  (unary), `Teardown` (unary), `LifecycleOnCreated`/`OnUpdated`/`OnDeleted`
  (unary each).
- **`DestinationPlugin`** (`destination.proto:14-71`): same shape — `Run` is
  bidi, Conduit streams record batches in, plugin streams back per-record acks
  (each carrying an optional error string) — `Configure`, `Open`, `Stop`,
  `Teardown`, three lifecycle hooks.
- **`SpecifierPlugin`** (`specifier.proto:11-14`): `Specify` (unary) → name,
  summary, description, version, author, `source_params`/`destination_params`.

The `Run` bidi stream is the only structurally interesting RPC — everything else
is unary request/response. `grpc.aio`'s native bidi-stream support maps onto it
directly (see §2 for how this shapes the async-vs-sync recommendation).

#### 1.4 Wire record shape (confirmed against the .proto, not inferred)

`conduit-commons@v0.6.0/proto/opencdc/v1/opencdc.proto:91-131`:

```protobuf
message Record {
  bytes position = 1;
  Operation operation = 2;             // OPERATION_{CREATE,UPDATE,DELETE,SNAPSHOT} = 1..4
  map<string, string> metadata = 3;
  Data key = 4;
  Change payload = 5;
}
message Change {
  Data before = 1;   // optional; update/delete only
  Data after = 2;    // all ops except delete
}
message Data {
  oneof data {
    bytes raw_data = 1;
    google.protobuf.Struct structured_data = 2;
  }
}
```

This directly confirms the Go-side `opencdc.Data` interface
(`conduit-commons@v0.6.0/opencdc/data.go:29-34`, implementers `RawData []byte` /
`StructuredData map[string]interface{}`) is, at the wire level, nothing more
than a two-way oneof. Section 2.3 uses this to justify a simpler Python
representation than Go's interface indirection.

#### 1.5 Codegen: buf + protoc, not betterproto

**Recommendation: `buf generate` against
`buf.build/conduitio/conduit-connector-protocol`, using the
`protocolbuffers/python` + `protocolbuffers/pyi` remote plugins for messages plus
`grpc/python` for service stubs**, mirroring exactly what
`conduit-processor-sdk-python/proto/buf.gen.yaml` already does for messages
(`protocolbuffers/python:v31.1` + `protocolbuffers/pyi:v31.1`) plus a grpc
plugin for the service definitions it doesn't need (WASM has no gRPC service, so
that repo has no precedent for the grpc plugin specifically — this SDK adds it).
See Alternatives Considered for why `betterproto` was rejected.

### 2. Idiomatic Python API design

The public surface a connector author writes against. Design goal: feel like a
modern Python library (dataclasses/pydantic, `async`/`await`, exceptions), not a
Go interface transliterated field-for-field. Where Go needed indirection to work
around limitations Python doesn't have (an interface for `Data`, a code-gen step
for parameter specs, a `mustEmbedUnimplementedX()` seal for forward-compat), this
design collapses it.

#### 2.1 async, not sync — and why

The `Run` RPC is a bidirectional stream: a source must be able to emit records
_and_ receive ack callbacks concurrently on the same logical connection; a
destination must receive record batches _and_ emit ack batches concurrently.
`grpc.aio` models this natively as two independent `async for` loops over one
call object. A sync `grpcio` implementation could do this too (with a background
thread pulling one direction while the RPC thread pushes the other), but that
pushes hand-rolled thread-safety onto the SDK's core loop for no benefit — the
whole surface (HTTP clients, DB drivers, message-queue clients most connectors
wrap) is I/O-bound, which is precisely asyncio's target case. **Recommendation:
`asyncio` + `grpc.aio` as the SDK runtime.**

Connector authors, though, should not be forced into `async def` if their
target system's client library is sync-only (many DB drivers still are).
**Base class methods are declared `async def`, but the SDK detects a sync
override via `inspect.iscoroutinefunction` and runs it in a thread-pool executor
transparently** — the same dual-mode ergonomic FastAPI uses for path operations.
An author writing a sync `psycopg2`-based source just writes `def read(self):
...`; an author writing an `httpx.AsyncClient`-based source writes `async def
read(self): ...`. Both are first-class, not "sync as a fallback hack."

#### 2.2 Config: pydantic v2, with paramgen replaced by introspection

Go needs `paramgen` (`conduit-commons@v0.6.0/paramgen/`, driven by struct tags
like `validate:"required,gt=0,lt=100,inclusion=a|b"`,
`conduit-connector-sdk`'s template invoking it via `//go:generate conn-sdk-cli
specgen`, `conduit-connector-template/connector.go:1`) because Go has no runtime
reflection rich enough to turn a struct's field types and tags into a
`config.Parameter` map without a separate generation pass. **Python doesn't have
that problem: pydantic v2's `model_fields` already carries type, default, and
constraint metadata at runtime.** The SDK provides one function,
`to_parameters(config_cls: type[BaseConfig]) -> dict[str, Parameter]`, that
introspects a pydantic model and produces the `Specify` RPC's parameter map
directly — **no codegen step, no `//go:generate`, always in sync with the model
because it's the model.** This is a genuine simplification over the Go SDK, not
just a stylistic swap, and is worth flagging as such in author-facing docs.

```python
class Config(BaseConfig):
    url: str = Field(description="HTTP endpoint to poll.")
    poll_interval_ms: int = Field(default=1000, ge=100, description="Milliseconds between polls.")
    format: Literal["json", "csv"] = Field(default="json")  # -> Validation.inclusion
```

Mapping to `config.Parameter`/`Validation` (`conduit-commons@v0.6.0/config/
parameter.go:20-43`, `config/validation.go:29-45`): `Field(default=...)` →
`Default`; `ge=`/`le=` → `greater-than`/`less-than`; `Literal[...]` → `inclusion`;
`pattern=` → `regex`; a plain (no-default) field → `required`. `BaseConfig`
subclasses `pydantic.BaseModel` and additionally exposes `to_parameters()` as a
classmethod so the Specifier RPC handler is one line:
`Specify.Response(source_params=SourceConfig.to_parameters(), ...)`.

#### 2.3 The OpenCDC record: `bytes | dict`, not an interface

Go's `opencdc.Data` interface (`Bytes()`, `Clone()`, `ToProto()`) exists to give
two structurally different Go types (`RawData []byte`, `StructuredData
map[string]interface{}`) a common contract. Per §1.4, the wire format is simply
a two-way oneof (`raw_data: bytes` / `structured_data: Struct`). Python doesn't
need the interface: `bytes` and `dict` already have their own copy semantics
(`bytes` is immutable, `dict.copy()`/`copy.deepcopy()` handle `Clone()`'s job),
and `isinstance()` at the (de)serialization boundary — not author-facing code —
is enough to pick the right oneof branch. **`Data = bytes | Mapping[str, Any]`.**

```python
@dataclass
class Change:
    before: Data | None = None   # update/delete only
    after: Data | None = None    # all ops except delete

class Operation(enum.Enum):
    CREATE = 1
    UPDATE = 2
    DELETE = 3
    SNAPSHOT = 4

@dataclass
class Record:
    position: bytes
    operation: Operation
    metadata: dict[str, str] = field(default_factory=dict)
    key: Data = b""
    payload: Change = field(default_factory=Change)
```

Plain `dataclasses`, not pydantic, for `Record` — records are produced/consumed
at high frequency in the hot path; pydantic's validation overhead isn't wanted
there, and there's nothing to validate (the wire format already constrains the
shape). Well-known metadata keys (`opencdc.collection`, `opencdc.createdAt`,
etc. — confirmed exact strings in `opencdc.proto:9-20`, e.g.
`metadata_collection = "opencdc.collection"`) ship as `Metadata` string
constants plus typed helpers (`record.metadata.set_created_at(dt)`) mirroring
Go's `GetCreatedAt`/`SetCreatedAt` ergonomics on the underlying dict.

#### 2.4 Forward-compatible base classes without Go's seal hack

Go forces `mustEmbedUnimplementedSource()` (an unexported method only
`UnimplementedSource` provides) so a struct can't accidentally satisfy the
`Source` interface without embedding the default impl — protecting against the
interface growing a method later. Python's ABC + default-method pattern gets the
same guarantee for free: `Source`/`Destination` are `abc.ABC` subclasses with
`open`/`read`/`ack`/`teardown`/lifecycle hooks all having **default no-op (or
`NotImplementedError`-raising) bodies** except the couple of genuinely-required
ones (`read`/`write`, `config` typed by the generic parameter). Adding a new
optional method later is source-compatible automatically — no seal method
needed, no boilerplate for authors.

#### 2.5 Errors: exceptions, not `(n, err)`

Go's `Destination.Write(ctx, batch) (n int, err error)` requires the SDK to
_enforce_ an invariant that's easy to get wrong: `n == len(batch)` must imply
`err == nil`, and `n < len(batch)` must imply `err != nil`
(`conduit-connector-sdk/destination.go:345-350` logs it as a connector bug
otherwise). Python favors exceptions for error propagation and this SDK follows
that: `async def write(self, records: list[Record]) -> None`; full success is
"no exception raised"; a partial-batch failure raises
`BatchWriteError({index: exception, ...})`, which the SDK's adapter — not the
author — translates into the correct per-record ack/error entries on the `Run`
stream. This removes the invariant-violation footgun structurally instead of
relying on authors reading a doc comment.

For `Source.read()`: raising `BackoffRetry` signals "no record right now,
retry with backoff" (direct analog of Go's `ErrBackoffRetry`,
`conduit-connector-sdk/error.go:19-27`, consumed with a `Factor:2, Min:100ms,
Max:5s` backoff at `source.go:280-289` — the Python SDK reuses the same
constants for parity). Any other exception propagates as a gRPC `INTERNAL`
status with the exception's string as detail. The base `ConnectorError`
exception carries an optional `code: str | None` field, currently unused on the
wire — **the Go connector protocol has no stable error-code scheme today**
(confirmed: no sentinel/coded-error taxonomy beyond `ErrBackoffRetry`/
`ErrUnimplemented`, `conduit-connector-sdk/error.go:19-27`) — but a protocol
change adding plugin-originated `ConduitError` codes is already flagged as
landing around v0.16 per
[SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md:39-41,361).
Reserving the field now avoids a breaking SDK change when that lands; until
then it's always `None` and unused by the wire encoding.

#### 2.6 Batching and schema: what Phase 1 defers

Go's `ReadN`/`Write([]Record)` batch APIs and `sdk.batch.size`/`sdk.batch.delay`
middleware (`source_middleware.go:651-657`, purely local/config-driven — no
protocol-level batch negotiation, confirmed) are useful but not required for a
minimally-working connector, since the wire protocol's `Run` stream is already
batch-shaped at the message level regardless of SDK-side buffering. **Phase 1**
implements the direct mapping only: `async def read(self) -> Record` (SDK wraps
single records into single-record batches on the wire) and `async def
write(self, records: list[Record]) -> None` (destination `Run` messages are
naturally batched, so no author-side batching primitive is needed there at all
— `write` already receives whatever batch Conduit sent). A `read_batch`
override point and `sdk.batch.size`/`delay`-equivalent config are **Phase 2**,
added as an optional base-class override the SDK falls back away from if
unimplemented (mirroring Go's `ReadN` cascading to `Read`). Schema
extraction/encoding middleware (Avro registration, `SourceWithSchemaExtraction`
et al.) is **Phase 2** as well — Phase 1 records carry raw `bytes`/`dict` only,
no schema subject/version metadata is auto-populated.

#### 2.7 End-to-end example (illustrative — not final API)

```python
"""A minimal Conduit source connector: polls an HTTP endpoint for new rows."""
from __future__ import annotations

import asyncio
from datetime import UTC, datetime

import httpx
from conduit import BackoffRetry, Change, Operation, Record, Source, Specification, serve
from conduit.config import BaseConfig, Field


class Config(BaseConfig):
    url: str = Field(description="HTTP endpoint to poll, expects ?since=<cursor>.")
    poll_interval_ms: int = Field(default=1000, ge=100, description="Delay between empty polls.")


class HTTPPollSource(Source[Config]):
    async def open(self, position: bytes | None) -> None:
        self._client = httpx.AsyncClient()
        self._since = position.decode() if position else "0"

    async def read(self) -> Record:
        resp = await self._client.get(self.config.url, params={"since": self._since})
        rows = resp.json()
        if not rows:
            await asyncio.sleep(self.config.poll_interval_ms / 1000)
            raise BackoffRetry()

        row = rows[0]
        self._since = str(row["id"])
        return Record(
            position=self._since.encode(),
            operation=Operation.CREATE,
            key={"id": row["id"]},
            payload=Change(after=row),
            metadata={"opencdc.readAt": str(int(datetime.now(UTC).timestamp() * 1e9))},
        )

    async def teardown(self) -> None:
        await self._client.aclose()


if __name__ == "__main__":
    serve(Specification(name="http-poll", version="0.1.0", author="you"), source=HTTPPollSource)
```

~38 lines, no boilerplate beyond what the connector's own logic needs. `Ack`,
lifecycle hooks, and config validation all have working defaults from the base
class and don't need overriding for this connector.

### 3. Repo & packaging

**New repo: `ConduitIO/conduit-connector-sdk-python`** — matches the naming
pattern already established by `ConduitIO/conduit-processor-sdk-python` and the
name anticipated in
[SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md:57-58,338).

Proposed layout:

```text
conduit-connector-sdk-python/
  pyproject.toml            # uv-managed; hatchling build backend
  src/conduit/
    __init__.py              # public API: Source, Destination, Record, Operation, Change, serve, errors
    config.py                 # BaseConfig, Field, to_parameters()
    record.py                 # Record/Change/Operation/Metadata
    source.py                 # Source ABC, default middleware hook points
    destination.py            # Destination ABC
    serve.py                  # handshake + gRPC server bootstrap (the §1 implementation)
    _handshake.py              # magic cookie, protocol negotiation, stdout line
    _grpc/                     # generated stubs (buf generate output) + adapters translating
                                # proto <-> Python dataclasses/pydantic models
    testing/
      acceptance.py            # the acceptance-test harness (Phase 2)
      fixtures.py               # golden record-shape fixtures shared with other-language SDKs
  examples/http-poll-source/    # the Phase-1 worked example, runnable standalone
  buf.gen.yaml                  # codegen config (see §1.5)
  tools/                         # pinned dev-tool versions (ruff, mypy, buf) — mirrors conduit-connector-template's tools/go.mod pattern
  .github/workflows/
    lint.yml  test.yml  release.yml  compat-nightly.yml   # see below
  CONTRIBUTING.md  README.md  CHANGELOG.md
```

- **Packaging**: `pyproject.toml`, `uv` for dependency management/locking
  (fast, single-tool, increasingly the default in the Python ecosystem the
  python-pro persona targets), `hatchling` build backend. Publish to PyPI as
  `conduit-connector-sdk`.
- **Python floor**: 3.11+ (per house style; also the first version with mature
  `tomllib`, exception groups, and `asyncio.TaskGroup` — the latter is directly
  useful for running the two directions of a bidi `Run` stream concurrently
  with structured error propagation).
- **Lint/type**: `ruff` (format + lint, replacing black/isort/flake8 in one
  tool) and `mypy --strict` on the public API surface (`pyright` as a
  documented alternative for authors, not required in this repo's CI).
- **Test**: `pytest`, `pytest-asyncio`, `hypothesis` for the record
  (de)serialization round-trip properties (raw ↔ proto ↔ dataclass), mirroring
  CLAUDE.md's property-testing bar for serialization code even though this repo
  isn't the core data path — the record codec is exactly the kind of
  round-trip-sensitive code that bar is meant to cover.
- **Acceptance-test harness (Phase 2, designed now)**: mirrors
  `sdk.AcceptanceTest(t, driver)`. A `conduit.testing.AcceptanceTestDriver`
  Protocol an author implements (`write_to_source`, `read_from_destination`,
  seed-data hooks — direct analogs of the Go driver's custom-hook methods,
  `acceptance_testing.go:67-120`), and a `ConfigurableAcceptanceTestDriver`
  convenience wrapper (analog of `acceptance_testing.go:125-177`) for the common
  case (just supply the connector class + two valid configs pointing at the
  same resource). Test categories are the same list the Go suite defines
  (`acceptance_testing.go:506-874`): specifier existence/validity, config
  parameter validation (success + required-param-missing), resume-at-position
  (snapshot and CDC), read/write round trip, read timeout behavior — kept
  **version-numbered** per
  [SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md:66-73)
  ("the acceptance-test suite is versioned and published per language") so an
  author knows exactly which contract version they passed, and so
  cross-language parity (a Python connector is "the same kind of thing" as a Go
  one from Conduit's perspective) is a checkable claim, not an assertion.
- **CI matrix**: ubuntu-latest, macos-latest, windows-latest (Windows because
  the TCP-transport handshake choice in §1.1 makes it free — no Unix-socket
  platform branch to skip on Windows), against the two most recent CPython
  minors on the 3.11+ floor. A separate `compat-nightly.yml` job regenerates
  the gRPC stubs from the **current** `buf.build/conduitio/conduit-connector-protocol`
  HEAD and runs the acceptance suite against the current Conduit dev build —
  the drift-detection mechanism from Risks & Open Questions, and the same
  "templates track the SDK" discipline CLAUDE.md already holds the Go
  templates to
  ([SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md:350-353)).
- **Release**: tag-triggered, conventional-commit changelog, published to PyPI
  via a trusted-publisher GitHub Action (no long-lived PyPI token in repo
  secrets) — this is a stricter bar than the Go template's `goreleaser`
  approach (`conduit-connector-template/.goreleaser.yml`) because a PyPI
  package, unlike a per-connector binary release, is a shared supply-chain
  surface every downstream Python connector depends on.
- **Example/template connector**: `examples/http-poll-source/` is the Phase-1
  worked example from §2.7, made fully runnable (its own `pyproject.toml`,
  README, and a `conduit.yaml` pipeline definition proving Conduit can run it
  standalone) — the Python analog of what `conduit connector new` scaffolds for
  Go from `conduit-connector-template`. A dedicated
  `conduit-connector-template-python` repo (mirroring
  `conduit-connector-template`'s CI/README-generation/release-workflow
  structure) is **Phase 3** work, gated on `conduit connector new --lang
  python` integration — building it before the SDK's API has stabilized
  through a couple of real connectors would lock in the wrong shape.

### 4. Phased plan

#### Phase 1 — MVP: Conduit can run a Python connector

Scope: handshake (§1) + `Source`/`Destination` ABCs (§2.1, §2.4) + `Configure`/
`Specify` (§2.2, no schema/middleware) + OpenCDC record (§2.3) + basic
lifecycle (`Open`/`Run`/`Stop`/`Teardown`, unary lifecycle hooks as no-ops
unless overridden) + `BackoffRetry`/`BatchWriteError` (§2.5) + the worked
example connector (§2.7, §3).

Explicitly **not** in Phase 1: acceptance-test harness (hand-verified only),
schema/Avro middleware, `sdk.batch.*`-equivalent config, `conduit connector new
--lang python` integration, PyPI trusted-publisher release automation (manual
release is fine for a v0.x).

**Definition of done (the acceptance criterion):** a fresh checkout of
`conduit-connector-sdk-python`'s example connector, installed with `uv sync`,
launched by a **real, unmodified Conduit binary** (no Conduit-side code
changes) as the source of a pipeline whose destination is `conduit-connector-
file` (or another already-supported connector) — records flow end-to-end, get
acked, and `conduit pipelines stop` triggers a clean subprocess exit via the
`GRPCController.Shutdown` path (§1.1.5), not the 2-second force-kill fallback.
This is scripted and CI-timed, not eyeballed once — it becomes the
`compat-nightly.yml` job from day one and stays green thereafter.

#### Phase 2 — acceptance harness, schema/middleware, paramgen parity, docs

- Acceptance-test harness (§3), versioned, published.
- `read_batch` override + batch-size/delay config (§2.6).
- Schema extraction/encoding middleware, matching `SourceWithSchemaExtraction`/
  `DestinationWithSchemaExtraction` semantics (registering key/payload schemas,
  attaching subject/version metadata — the exact metadata keys are already
  confirmed at the wire level, `opencdc.proto:23-30`).
- Full docs: per-connector-author tutorial (same worked example as the Go/Rust/
  TS tutorials per
  [SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md:318-320),
  so a reader compares apples to apples across languages), godoc-equivalent
  (`ruff`-enforced docstring coverage + `pydoc`/`mkdocstrings`-generated
  reference), cookbook recipes.
- Golden record-shape fixture corpus shared with the Go/other-language suites
  (raw, structured, tombstone, schema-carrying, error shapes) per the "one
  corpus" principle in the embedding-DX doc.

#### Phase 3 — parity polish, CLI integration

- `conduit connector new --lang python` scaffolds from a
  `conduit-connector-template-python` repo (§3).
- `conduit connector test`/`conduit connector build` wire into the Python
  acceptance/integration loop the same way they do for Go
  ([SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md:107-128)).
- Performance parity pass against the Go SDK on a benchi-committed reference
  pipeline (see Risks & Open Questions — "performance vs Go").
- `conduit connector generate` (AI-assisted scaffolding) targets Python as a
  `--lang` option once the MCP/generate tooling exists for Go.

## Alternatives considered

**Protobuf codegen tool — `grpcio-tools`/`buf generate` (recommended) vs.
`betterproto`.** `betterproto` produces more idiomatic Python (dataclasses,
`async` client stubs, no `_pb2.py` boilerplate) and was seriously considered for
that reason. Rejected because: (a) it lags official protobuf releases and has
had long stretches without releases historically, a maintenance-burden risk for
a protocol that must track `conduit-connector-protocol` closely; (b) the org
already has working precedent for `buf generate` with the official
`protocolbuffers/python`+`pyi` plugins in `conduit-processor-sdk-python`
(confirmed, §Context) — matching it avoids a second, divergent Python-codegen
toolchain in the org for no strong reason; (c) the generated `_pb2.py`/`_pb2_grpc.py`
stubs are treated as an internal implementation detail behind `_grpc/` adapters
(§3) — authors never see them, so betterproto's ergonomic advantage doesn't
reach the public API anyway, since §2 already wraps everything in idiomatic
dataclasses/pydantic models regardless of the underlying codegen choice.

**Transport — TCP (recommended) vs. Unix domain socket.** Matching go-plugin's
own non-Windows default (Unix socket) was considered for "do what Go does."
Rejected: the client-side parser is transport-agnostic (§1.1.4), Unix sockets
need temp-directory and permission handling with no corresponding benefit here
(this is a loopback-only, single-host, single-connection channel — no
performance case for sockets over loopback TCP at this scale), and TCP gets
Windows support without a platform branch, which Unix-socket-by-default would
have needed anyway since go-plugin itself falls back to TCP on Windows.

**Async-only vs. dual sync/async author API (recommended: dual).** Forcing
`async def` everywhere (simplest to implement, matches `grpc.aio` directly) was
considered. Rejected as author-hostile: a large fraction of Python's
data-ecosystem client libraries (many DB drivers, some vendor SDKs) are
sync-only, and forcing authors to wrap them in `asyncio.to_thread` themselves
just moves boilerplate from the SDK into every connector. The thread-pool
dispatch (§2.1) costs a small amount of SDK-internal complexity once, in
exchange for removing it from every connector author's code, every time.

**Config — pydantic v2 (recommended) vs. stdlib `dataclasses` + hand-written
validators.** Dataclasses avoid a dependency and match the `Record` type's
choice (§2.3). Rejected for `Config` specifically because the paramgen-by-
introspection design (§2.2) is the whole point of the Pythonic simplification
over Go's codegen step, and that requires rich, already-structured field
metadata (type, default, constraints) at runtime — reimplementing that on top
of bare dataclasses would mean hand-rolling a worse version of what pydantic
already provides, for the sake of one fewer dependency in an ecosystem where
pydantic is close to ubiquitous.

**Error model — exceptions (recommended) vs. mirroring Go's `(n, err)`
contract.** Matching Go's `Write(ctx, batch) (n, err)` return shape verbatim
was considered, for API-shape parity across languages. Rejected: that shape
exists in Go specifically because Go lacks structured exceptions with attached
data cleanly separable from control flow, and it requires the SDK to defend an
invariant (`destination.go:345-350`) that a well-designed Python API can make
structurally unrepresentable instead of enforced-at-runtime (§2.5). Parity of
_behavior_ (correct per-record acks on partial failure) is kept; parity of
_shape_ is not a goal in itself.

## Failure modes

Analyzed against CLAUDE.md's data-integrity invariants, scoped to what an SDK
(not the engine) is responsible for:

- **Handshake cookie mismatch / malformed stdout line** → process must exit
  non-zero with a clear stderr diagnostic before printing anything on stdout
  that isn't the handshake line itself (stdout is a structured channel go-plugin
  parses — any stray `print()` before the handshake line corrupts it). SDK
  design mitigation: redirect all logging to stderr by default (§ mirrored from
  Go SDK's stdio-forwarding behavior) and document this loudly for authors, since
  a rogue `print()` in connector code is an easy way to break the handshake.
- **Crash mid-`Run` stream (source)** → in-flight, unacked records are lost from
  the SDK's perspective on restart unless the source's own `Open(position)`
  contract correctly resumes from the last acked position (invariant 2). Phase
  1's acceptance criterion doesn't yet include a resume-after-kill test (that's
  Phase 2's acceptance harness, mirroring
  `TestSource_Open_ResumeAtPositionCDC`); flagged as a Phase 1 gap, not silently
  assumed safe.
- **Partial-batch write failure (destination)** → `BatchWriteError` (§2.5) must
  be raised with an entry for every failed index; the SDK adapter treats a
  missing index as "silently assumed successful," which is exactly the kind of
  silent-coercion failure CLAUDE.md's invariant 6 rules out for schema handling
  and this design extends to ack accuracy — the adapter should treat an
  **incomplete** `BatchWriteError` mapping as itself an error (fail closed, not
  fail open) rather than guessing.
- **`GRPCController.Shutdown` not implemented correctly** → falls back to
  Conduit's 2-second force-kill (§1.1.5) — functional (no pipeline hang) but
  not graceful; in-flight writes at kill time are the same risk profile as any
  `SIGKILL` mid-batch scenario the chaos suite (Phase 2+ per Process maturity)
  is meant to cover once it exists for this SDK's own test matrix, not just the
  engine's.
- **`PATH` not inherited (§1.1.6)** → connector fails to launch at all if
  packaged naively (e.g. a `#!/usr/bin/env python3` shebang script relying on
  `PATH` resolution). Mitigated by packaging guidance in §3 and Phase 1's done
  criterion explicitly using a real Conduit launch (not a hand-run process),
  which would surface this failure immediately if unaddressed.

## Upgrade/rollback & compatibility

- **Protocol version skew**: the SDK targets protocol v2 and negotiates via
  `PLUGIN_PROTOCOL_VERSIONS` (§1.1.2) — if a future protocol v3 lands, an older
  SDK still negotiates down to v2 as long as Conduit's client keeps v2 in its
  `VersionedPlugins` map (the standard go-plugin backward-compat mechanism,
  already relied on for the existing v1/v2 coexistence). No SDK-side action
  needed for additive protocol changes; a breaking protocol change is already
  governed by CLAUDE.md's "never change `conduit-connector-protocol` without an
  explicit versioning discussion" rule, upstream of this SDK.
- **SDK API breaking changes**: this repo commits to semver from v1.0; breaking
  changes to the `Source`/`Destination`/`Config` public surface follow the
  standing announce → warn → remove policy (minimum two minor versions),
  matching the org-wide deprecation policy CLAUDE.md sets for serialized
  formats and public contracts generally, extended here to this SDK's own API
  surface since downstream connector code depends on it exactly like a
  protocol.
- **Rollback**: since the SDK ships as a versioned PyPI package pinned per
  connector project (not vendored into Conduit), rolling back is "pin an older
  SDK version" — no coordinated Conduit-side rollback needed, which is the main
  practical benefit of the standalone-subprocess architecture over anything
  in-process.
- **Drift detection**: the `compat-nightly.yml` job (§3) regenerating stubs
  from BSR HEAD and running the acceptance suite against a current Conduit dev
  build is the primary mechanism preventing silent protocol drift — this is the
  same mechanism the embedding-DX doc already prescribes org-wide ("a protocol
  bump is additive-and-versioned ... a nightly job regenerates + runs each
  SDK's template CI",
  [20260705-sdk-and-embedding-dx.md:76-79](20260705-sdk-and-embedding-dx.md)).

## Observability

- Connector logs cross the process boundary as structured records on stderr
  (JSON lines), consumed by Conduit the same way the Go SDK's logs are —
  matching CLAUDE.md's cross-boundary structured-error/log principle from the
  embedding-DX doc rather than inventing a Python-specific log format.
  **Never write to stdout** except the single handshake line (§ Failure modes).
- The SDK exposes a `conduit.testing` record inspector hook (Phase 2, shared
  with the "record inspector + dry-run" tooling described in the embedding-DX
  doc's A3 local-testing-loop section) so an author can pipe sample records
  through their connector locally without standing up a full pipeline.
- Errors raised by connector code surface through the gRPC status + detail
  string today (§2.5); once the protocol's plugin-originated `ConduitError`
  codes land, this SDK's `ConnectorError.code` field starts being populated and
  serialized — designed for that transition now rather than bolted on later.

## Risks & open questions

1. **The handshake is the single biggest execution risk**, not because it's
   Python-hostile (it isn't — §1 shows it's a plain stdout line plus a
   standard gRPC server) but because it's easy to get subtly wrong in ways that
   only surface as "Conduit hangs waiting for the plugin to start" with a poor
   error message. Mitigation: Phase 1's done-criterion is a real Conduit launch
   from day one (not a hand-rolled harness that might pass while the real
   client-side parser would reject the line), and the handshake implementation
   ships with its own focused unit tests asserting the exact line format
   against the go-plugin source's parser logic (`client.go:838-926`).
2. **gRPC/codegen choice locks in early.** `buf generate` + protoc stubs (§1.5)
   is the recommendation, but it's the one decision hardest to reverse later
   (the internal `_grpc/` adapter layer insulates the public API from this, per
   §Alternatives, which is precisely designed to make a later swap possible
   without an author-facing breaking change if the choice turns out wrong).
3. **Async runtime + subprocess lifecycle interaction.** `asyncio`'s
   interaction with process-exit signal handling (SIGTERM from Conduit's
   graceful-shutdown path, invariant 7) needs care — an `asyncio` event loop
   that's mid-`await` when SIGTERM arrives must still let in-flight writes
   drain before the process exits, the same drain-before-exit discipline
   CLAUDE.md requires of the engine itself, now also required of every SDK
   author's `teardown()`. This needs its own focused test in Phase 1, not just
   a docs note — a straightforward chaos-style "SIGTERM mid-write" test using
   the example connector.
4. **Performance vs. Go is unknown and unclaimed.** No benchmark exists yet;
   per CLAUDE.md, no performance claim should be made about this SDK (parity or
   otherwise) without a `benchi` run committed to the repo. Phase 3 explicitly
   schedules that benchmark; until then, "how much throughput does a Python
   connector cost vs. Go" is an open question, not an assumed answer, and
   should be described that way in any interim docs.
5. **Second-SDK maintenance burden.** Every future connector-protocol change
   now has two SDKs (at least) to update, tested, and released in lockstep, on
   top of the existing solo-maintainer reality. The `compat-nightly.yml`
   mechanism (§3, §Upgrade/rollback) is the concrete mitigation, but it doesn't
   remove the underlying cost — this is an honest tradeoff being made for
   Python-audience reach, not a free win, and should be named as such when
   this doc gets a maintainer sign-off.
6. **Windows subprocess launch specifics.** TCP transport removes the
   Unix-socket asymmetry, but Windows process launching (job objects, signal
   delivery — Windows has no SIGTERM) has its own quirks for a subprocess
   expected to shut down gracefully; Conduit's own Windows CI story is called
   out generally in CLAUDE.md §5 but this SDK's specific Windows
   graceful-shutdown behavior is untested until the CI matrix in §3 actually
   runs it — flagged, not assumed fine.
7. **`google.protobuf.Struct` round-trip fidelity for `StructuredData`.**
   `Struct` only supports JSON-like values (no bytes, no distinguishing int
   from float in all cases); a Python `dict` used as `StructuredData` that
   contains, say, raw bytes in a nested field would silently lose fidelity
   through the `Struct` encoding. This needs an explicit documented boundary
   (what's a valid `StructuredData` payload) in Phase 1 rather than discovered
   by a connector author later — a case of CLAUDE.md's "schema handling never
   silently mangles data" invariant applying to the SDK's own record codec,
   even though the SDK isn't the engine's data path per se.

## Acceptance criteria

**For the SDK overall:**

- A connector written against this SDK, passing the versioned acceptance suite
  (Phase 2+), is treated by Conduit as fully interchangeable with a Go
  connector — no capability gap silently assumed away; any protocol feature the
  Python SDK can't yet express is documented, not hidden (per the "document the
  gap per language" principle in
  [20260705-sdk-and-embedding-dx.md:80-82](20260705-sdk-and-embedding-dx.md)).
- Every doc example compiles/runs in CI; a non-running example is a build
  failure, not a stale doc.
- No performance claim ships without a committed `benchi` result.
- The `compat-nightly.yml` job stays green continuously post-Phase-1; a red
  result is treated as a protocol-drift incident, not routine noise.

**For Phase 1 specifically (the concrete, testable bar):**

- A real, unmodified Conduit binary launches the example connector as a
  subprocess via the exact handshake in §1, with no Conduit-side code changes.
- Records flow end-to-end through a real pipeline (Python source →
  already-supported destination, or vice versa), get correctly acked, and
  `conduit pipelines stop` tears the subprocess down via the graceful
  `GRPCController.Shutdown` path, not the force-kill fallback.
- This is scripted and CI-timed (matching the "measured, not claimed"
  discipline the embedding-DX doc holds every other DX number to), and remains
  the nightly compat check going forward.

## Consequences

- Conduit gains a second, officially maintained connector SDK, opening the
  Python data/AI ecosystem as a first-class connector-authoring audience
  without any change to Conduit itself — the standalone-subprocess
  architecture (ADR `20220121-conduit-plugin-architecture.md`) pays off exactly
  as designed: "it allows us to easily generate code for other programming
  languages and potentially provide SDKs" (`...conduit-plugin-architecture.md:325`).
- The org takes on a second SDK's maintenance surface, permanently, on top of
  an already-solo maintainer reality — named explicitly in Risks, not
  understated.
- The Python API's deliberate departures from Go's shape (§2, §Alternatives)
  mean "port the Go connector" is not a mechanical translation for authors
  crossing languages — an acceptable, deliberate tradeoff for idiomatic
  ergonomics per this doc's brief, but worth flagging so cross-language
  connector porting guides (Phase 2 docs) address it explicitly rather than
  implying a 1:1 mapping that doesn't exist.
- This design doc's API surface (base classes, config model, record shape)
  becomes a public contract the moment Phase 1 ships an example connector
  against it — future changes to it are governed by the same
  announce-warn-remove policy as any other public Conduit contract.

## Related

- [SDK & embedding developer experience](20260705-sdk-and-embedding-dx.md) —
  the org-wide DX plan this doc is the detailed Python-connector follow-on to
  (persona A1).
- [Conduit Plugin Architecture](../architecture-decision-records/20220121-conduit-plugin-architecture.md)
  — the original ADR establishing gRPC-defined, SDK-decoupled plugin
  interfaces; this doc's §1 supersedes its stale `Start`/`Run`-split
  description with the current v2 protocol (`Configure`/`Open`/`Run`/`Stop`/
  `Teardown`) actually running today.
- [WASM component model](../architecture-decision-records/20260704-wasm-component-model.md)
  — the _other_ extension mechanism (processors, not connectors); referenced
  in Context to explain why `conduit-processor-sdk-python` is not transport
  prior art for this SDK.
- `ConduitIO/conduit-connector-protocol` — the wire contract this SDK
  implements (`proto/connector/v2/*.proto`, `pconnector/`).
- `ConduitIO/conduit-connector-sdk` — the Go reference implementation this
  design mirrors semantically.
- `ConduitIO/conduit-connector-template` — the Go scaffold this design's
  Phase 3 template repo is the Python analog of.
- `ConduitIO/conduit-processor-sdk-python` — existing, unrelated (WASM/
  processor) Python SDK prototype; codegen-tooling precedent only.
- `github.com/conduitio/conduit-commons` (`opencdc/`, `config/`) — the OpenCDC
  record and config-parameter types this SDK's Python equivalents are modeled
  on.

## Review outcome (2026-07-07) — SOUND (technical) / SOUND-WITH-CONCERNS (API)

Handshake independently byte-verified against go-plugin v1.8.0 — the crux is sound.
One BLOCKER before Phase-1 build:

- **B1 (data-loss blocker) — partial-batch ack.** `BatchWriteError({index: exc})`
  with "absence = success" can silently ACK records that were never written (the
  natural Python loop raises on the first error and never attempts the rest, which
  are then absent-and-acked → invariant 1/3 violation). Go's `(n, err)` mechanically
  nacks everything past the successful prefix. **Fix:** any exception nacks the WHOLE
  batch UNLESS the SDK is given an explicit contiguous `written: int` (Go's `n`) or
  an exhaustive success set — success by omission is banned.
- **B3** — the silent `Struct` fidelity case is **int→float** (`{"count":5}`→`5.0`,
  precision loss > 2^53), not bytes (bytes fails loud). Name it; add Hypothesis
  round-trip tests asserting IDENTITY.
- **A-gaps** (pre-Phase-1, non-blocking): pydantic mapping for
  `ParameterTypeDuration` (Go `"5s"` syntax, not ISO-8601) and `ValidationTypeExclusion`;
  the worked example double-backoffs (manual sleep + `raise BackoffRetry()` — the SDK
  already paces; just raise).

Source ack/position flow verified correct (no early-ack risk). API idiom (async +
dual-mode, pydantic, `bytes|dict`, exceptions, ABCs, uv/pyproject) confirmed sound.
