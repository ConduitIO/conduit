# WASM host-mediated network-egress capability for standalone processors

## Summary

This is the focused, security-scoped design for the **single core-engine-adjacent change** the
AI-pipeline subsystem requires: a host-mediated, allowlisted HTTP-egress capability for standalone
(WASM) processors. It is the expansion of Decision §1 of the AI-pipeline components design
(`20260724-ai-pipeline-components.md`, signed off, PR #2687), pulled into its own document because
DeVaris's Open Question #2 on that doc asked for exactly this: a **separate, focused sign-off on the
new security boundary before `conduit-processor-ai`'s embedding processor is built against it**. No
embedding-processor egress code merges until this doc is signed off.

The problem, restated precisely: `conduit-processor-sdk` standalone processors run under wazero +
WASI Preview 1, which has **no socket API**. An embedding processor (OpenAI, Voyage, Cohere,
local Ollama) must make outbound HTTP calls. Rather than granting the sandbox raw network access
(no SSRF control) or inventing a second processor transport, this design extends the **existing,
already-reviewed host-mediated capability pattern** — the same `command_request`/`command_response`
channel and per-method park/resize protocol that `pprocutils.SchemaService` already uses
(`pkg/plugin/processor/standalone/host_module.go`) — with one new host function: a bounded,
allowlisted, host-executed HTTP call.

**The three load-bearing decisions this doc fixes:**

- **ABI:** one new host-module export, `http_request` (mirroring `create_schema`/`get_schema`),
  carrying a versioned `HTTPRequest`/`HTTPResponse` protobuf pair over the existing buffer-and-resize
  protocol. Buffered request/response only — no streaming. The **guest never gets a socket**; the
  host (full Go `net/http`) performs the I/O.
- **Security boundary:** a host-side allowlist enforced in **two independent stages** — a coarse
  hostname/scheme pre-filter, then the **load-bearing resolved-IP check in the dialer's `Control`
  hook on every dial** (the DNS-rebinding / TOCTOU defense). Private, link-local, loopback, and
  reserved ranges are refused unless that exact IP is itself explicitly allowlisted (the local-Ollama
  case). Redirects to non-allowlisted targets are not followed. Per-call timeout and response-size
  cap are host-enforced, never guest-requestable.
- **Configuration:** default **deny-all**. Egress is opt-in via a **host-reserved** config key,
  clamped by an **engine-level ceiling** the Conduit operator sets — the guest can never widen its
  own allowlist, because the resolved policy is bound host-side into the per-processor host-module
  instance at `newWASMProcessor` time and is never mutable from guest code.

## Context

### Why WASM processors need egress, and why they have none today

Standalone processors are compiled to WASM and executed under wazero with `wasi_snapshot_preview1`
(`pkg/plugin/processor/standalone/registry.go`, `processor.go`). WASI Preview 1 exposes no sockets:
there is no `sock_connect` in the preview-1 surface Conduit instantiates, and the module config
(`processor.go`, `newWASMProcessor`) enables only wall/monotonic clocks, sleep, and stdio — nothing
network. This is why every processor that needs outbound HTTP today (`openai.textgen`,
`openai.embeddings`, `cohere.embed`, `ollama`) is a **built-in**, compiled into the core binary with
full `net/http`. The AI-pipeline design's "new dedicated repos" call rules out that built-in escape
hatch for the new embedding processor, so the new processor needs a network path out of the sandbox
that does not exist yet.

### The precedent this reuses (not a new mechanism)

The host module (`host_module.go`) already brokers host-executed I/O for the guest:
`create_schema`/`get_schema` let a guest marshal a request into a buffer, call a
`//go:wasmimport conduit <fn>` import, and receive a marshalled protobuf response the **host**
produced by doing the actual schema-registry I/O. The guest never touches the registry directly. The
shared machinery — `handleWasmRequest` (park/resize, request-bytes de-dup), the `pprocutils.Error`
numeric error-code band (`errors.go`, `ErrorCodeStart = MaxUint32 - 100`), the guest-side `hostCall`
2-try resize loop (`wasm/caller.go`), and the typed service interface
(`pprocutils.SchemaService`) wired through `hostModuleOptions` — is exactly the shape outbound HTTP
needs. This design adds one capability to that broker; it does not add a transport, a protocol
revision, or a second sandbox model.

### The SSRF risk this boundary exists to contain

Host-mediated egress means the **host** — which has full network reach, including to the cloud
instance metadata endpoint (`169.254.169.254`), the loopback interface, and the private VPC — makes
a request whose destination is influenced by a WASM guest that is, in the general case, **untrusted,
possibly-any-language, possibly-third-party code**. That is the textbook setup for Server-Side
Request Forgery. The specific, high-value target is credential theft from the metadata endpoint;
the specific, easy-to-get-wrong defense is DNS rebinding, where a hostname that passes a
string-based allowlist check resolves to a private address at connection time (either a genuine
TOCTOU DNS change or an attacker deliberately re-pointing a record they control). A hostname-string
allowlist alone is **not** a sufficient implementation of this capability — the parent doc states
this and this doc makes it a testable requirement (§ Security boundary, § Testing).

## Goals / Non-goals

### Goals

- Give standalone WASM processors a way to make outbound HTTP calls **through the host**, under a
  host-enforced policy the guest cannot influence.
- Make the allowlist, DNS-rebinding, redirect, timeout, and response-size defenses **load-bearing and
  testable**, not implementation-discretion — the security tests are the deliverable's point.
- Fit the **existing host-function ABI pattern** exactly (same channel, same park/resize protocol,
  same numeric error-code band) so the change is a small, reviewable, additive extension.
- Default **deny-all**; require an explicit, operator-authored opt-in; keep the resolved policy
  host-side and immutable from guest code.
- Make every egress attempt — allowed or denied — **auditable** (structured log + metrics), because
  egress from a sandbox is a security-sensitive event.
- Preserve backward compatibility: a processor/SDK that does not know about egress is unaffected; a
  newer guest on an older host fails **discoverably at load time**, never silently.

### Non-goals

- **Not a general-purpose outbound-HTTP escape hatch** for arbitrary WASM processors. The capability
  is scoped, deny-by-default, allowlisted, and justified by the embedding use case; it is not offered
  as a way to get around the sandbox for anything else.
- **Not streaming.** The ABI is single-shot buffered request/response. Server-sent-events / chunked
  streaming responses are out of scope (embedding endpoints return a small JSON body); a streaming
  ABI is a fundamentally different, cursor-stateful design and is not built here.
- **Not raw sockets, not WASI-P2 `wasi:http`, not a new plugin transport.** See Alternatives.
- **Not a secrets-management subsystem.** How the embedding processor's API key reaches the request
  is a real decision this doc surfaces as an open question (host-side injection vs. guest-supplied
  header), but the secret store itself is out of scope.
- **Not a change to `conduit-connector-protocol` or the processor command protocol.** The new host
  function is additive to the WASM host-module surface only.
- **Not TCP/UDP/arbitrary-protocol egress.** HTTP(S) only, with `https` the default and `http`
  permitted only for explicitly-allowlisted private/local targets.

## The host-function ABI

### Shape

One new export is added to the `conduit` host module's function map (`host_module.go`,
`hostModuleFunctions`), alongside `create_schema`/`get_schema`:

```go
var hostModule wazergo.HostModule[*hostModuleInstance] = hostModuleFunctions{
    "command_request":  wazergo.F1((*hostModuleInstance).commandRequest),
    "command_response": wazergo.F1((*hostModuleInstance).commandResponse),
    "create_schema":    wazergo.F1((*hostModuleInstance).createSchema),
    "get_schema":       wazergo.F1((*hostModuleInstance).getSchema),
    "http_request":     wazergo.F1((*hostModuleInstance).httpRequest), // new
}
```

The guest-side import mirrors the existing ones (`wasm/imports.go`):

```go
//go:wasmimport conduit http_request
func _httpRequest(ptr unsafe.Pointer, size uint32) uint32
```

A new typed service interface in `pprocutils` mirrors `SchemaService`, wired through
`hostModuleOptions` and the `Registry` exactly as `schemaService` is today:

```go
// Illustrative — exact signature is an implementation-time decision. The properties this doc
// fixes are stated below and in the Security boundary section, not the Go syntax.
type HTTPService interface {
    Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

type HTTPRequest struct {
    Method  string
    URL     string            // parsed + validated host-side; see Security boundary
    Headers map[string][]string
    Body    []byte
}

type HTTPResponse struct {
    StatusCode int
    Headers    map[string][]string
    Body       []byte           // fully buffered, host-capped
}
```

`HTTPRequest`/`HTTPResponse` get a protobuf representation under `proto/procutils/v1` with the same
`fromproto`/`toproto` converters `SchemaService` uses, so the wire encoding rides the identical
marshalling path.

### Call flow, memory ownership, and size limits

The guest side is a verbatim reuse of the schema pattern (`wasm/schema.go` + `wasm/caller.go`):

1. Guest marshals an `HTTPRequest` proto into a pooled buffer.
2. Guest calls `_httpRequest(ptr, len)`. The host reads the request from the guest's linear memory,
   validates it (Security boundary), performs the call, marshals the `HTTPResponse`, and writes it
   back into the **same guest-owned buffer**.
3. If the buffer is too small, the host parks the response and returns the required size (via the
   existing `handleWasmRequest` machinery); the guest's `hostCall` loop grows the buffer once and
   retries. Two tries maximum, identical to schema calls.

**Memory ownership:** the guest owns and allocates the linear-memory buffer; the host only writes
into the region the guest presented. No host memory is handed to the guest. This is why the
response-size cap matters twice over:

- **Guest linear memory is 32-bit** (WASM32, ≤ 4 GiB). A response larger than the guest can address
  cannot be delivered regardless of policy, so the host cap must be comfortably below that.
- **The host must never read an unbounded body into its own memory** before marshalling. The host
  reads the response body through an `io.LimitReader` bounded to the configured cap; exceeding the
  cap aborts the read, closes the connection, and returns `ErrorCodeHTTPResponseTooLarge`. This is
  the DoS defense against a malicious or misbehaving allowlisted server returning a huge body.

**Buffered, not streaming.** The whole (capped) response body is materialized before it crosses back
to the guest. Embedding responses are small JSON (a vector array + usage), so this is not a
constraint in practice; it is called out so no future contributor assumes a streaming contract the
ABI does not provide.

### Error codes (the ABI's numeric band vs. the product's `ai.*` codes)

Two distinct layers, deliberately kept separate:

- **ABI-level numeric codes** live in the `pprocutils.ErrorCodeStart` band (`errors.go`), returned
  as the `uint32` from `http_request` and mapped by the guest `hostCall` loop into a
  `pprocutils.Error`. New codes (illustrative — exact set is an implementation-time `errors.go`):

  | Code | Returned when |
  | --- | --- |
  | `ErrorCodeHTTPEgressDisabled` | The processor was not opted into egress (deny-all default). |
  | `ErrorCodeHTTPForbidden` | Host/scheme not in allowlist, **or** resolved IP in a refused range. |
  | `ErrorCodeHTTPInvalidRequest` | Unparseable URL, bad scheme, illegal header (CRLF, `Host` override). |
  | `ErrorCodeHTTPDNS` | Name resolution failed. |
  | `ErrorCodeHTTPTimeout` | Per-call deadline exceeded. |
  | `ErrorCodeHTTPResponseTooLarge` | Response body exceeded the host cap. |
  | `ErrorCodeHTTPTransport` | Connection reset / TLS failure / other transport error. |

- **Product-level `conduiterr` codes** are the embedding processor's concern, defined in the parent
  doc (`ai.embedding_host_not_allowed`, `ai.embedding_provider_error`, …). The processor maps an ABI
  numeric code into the appropriate `ai.*` code it surfaces to the operator. The host capability
  itself owns only the numeric band; it does not know about `ai.*`. This seam keeps the host module
  free of AI-pipeline-specific vocabulary.

An egress error is **never** silent: the guest always receives a specific numeric code, and the host
always logs the denial/failure with a reason (Observability).

## The security boundary (the core)

This is the load-bearing section. The mechanism has two independent enforcement stages plus a set of
hardening rules. Both stages run **host-side**, on data the host controls, bound at processor-open
time. None of it is influenced by guest code at runtime.

### Stage 1 — hostname + scheme pre-filter (coarse)

On each `Do`, the host parses the URL with `url.Parse` (strict) and checks:

- Scheme is `https`, or `http` **only** if the target host is an explicitly-allowlisted
  private/local entry (the Ollama case). All other schemes (`file`, `ftp`, `gopher`, `unix`, …) →
  `ErrorCodeHTTPInvalidRequest`.
- The URL host (host:port, IP literal, or hostname) matches an entry in the **resolved allowlist**
  bound to this processor instance. No match → `ErrorCodeHTTPForbidden`.

This stage is a fast reject and a usability aid; it is **not** the security guarantee on its own,
because a matched hostname can still resolve to a hostile address. That is Stage 2's job.

### Stage 2 — resolved-IP check at dial time (the load-bearing gate)

The host's `http.Client` uses a custom `net.Dialer` whose `Control` hook fires **immediately before
each `connect(2)`, for every candidate address the resolver returns**. The hook receives the actual
IP-and-port about to be dialed and refuses the connection unless:

- the IP is **not** in any refused range (below), **or**
- that exact IP is itself an explicit entry in this processor's allowlist (the local-Ollama /
  private-VPC-endpoint case).

Refused ranges (canonical set; final list is an open question, but must at minimum cover):

| Family | Ranges |
| --- | --- |
| IPv4 loopback / any / private | `127.0.0.0/8`, `0.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` |
| IPv4 link-local + metadata | `169.254.0.0/16` (includes `169.254.169.254`), `100.64.0.0/10` (CGNAT) |
| IPv6 loopback / unspecified | `::1/128`, `::/128` |
| IPv6 link-local / ULA | `fe80::/10`, `fc00::/7` |
| IPv4-mapped / -compatible IPv6 | `::ffff:0:0/96`, `::/96` — unwrapped to their v4 form, then re-checked |

Because the check runs on the **parsed `net.IP` about to be dialed**, it is immune to textual
encoding tricks (decimal/octal/hex IPs, `0x`-forms, IPv6-mapped literals) — those all resolve to the
same address bytes the hook inspects. Because it runs **per candidate address on every dial** (not
once, not cached across calls), it closes the TOCTOU/rebinding window: the coarse hostname check and
the actual connection can see different DNS answers, and only the connection-time answer governs.

For multi-answer DNS (a hostname returning several A/AAAA records, or happy-eyeballs dual-stack),
each candidate connection triggers its own `Control` invocation, so a public+private mixed answer set
cannot smuggle a private dial through on a fallback attempt.

### Hardening rules

- **Redirects are not followed** by default. The client's `CheckRedirect` returns an error, so a 3xx
  from an allowlisted server pointing at a non-allowlisted (or private) `Location` becomes a failed
  call (`ErrorCodeHTTPForbidden`/`ErrorCodeHTTPTransport`), never an automatic hop out of the
  allowlist. Embedding APIs do not need redirects; making the guest handle a 3xx explicitly keeps the
  policy honest. (If a future API needs follow-redirects, it re-runs Stages 1+2 on each hop — flagged
  as an open question, default off.)
- **The `Host` header is derived from the validated URL, never a guest override.** A guest-supplied
  `Host`/`:authority`/connection-control header is rejected (`ErrorCodeHTTPInvalidRequest`) so the
  guest cannot present one hostname to the allowlist and another to the server (Host/SNI confusion).
- **Header names/values are validated** — CRLF and control characters are rejected (header/response
  splitting). `net/http` rejects most of these already; the host asserts it rather than relying on it.
- **Decompression is bounded.** If the host lets the transport transparently decompress (`gzip`,
  etc.), the response-size `io.LimitReader` wraps the **decompressed** stream, so a decompression
  bomb (small body, huge inflation) still trips `ErrorCodeHTTPResponseTooLarge`. Alternatively the
  host disables transparent decompression and caps the raw bytes; either is acceptable, a cap on the
  decompressed size is not.
- **Per-call timeout** (host-set, guest cannot extend) bounds dial + TLS + response-header + body
  read, so a slow/hung server fails deterministically to `ErrorCodeHTTPTimeout` rather than wedging
  the `Process` call.

### Enumerated attack scenarios and their defense

| # | Attack | Defense | Result |
| --- | --- | --- | --- |
| 1 | Guest requests a non-allowlisted host directly | Stage 1 hostname allowlist | `ErrorCodeHTTPForbidden` |
| 2 | Guest requests `http://169.254.169.254/…` (metadata) as an IP literal | Stage 1 (IP not allowlisted) then Stage 2 (link-local refused) | `ErrorCodeHTTPForbidden` |
| 3 | Allowlisted hostname resolves to a private IP at dial time (DNS rebinding / TOCTOU) | Stage 2 resolved-IP check on every dial | `ErrorCodeHTTPForbidden` |
| 4 | Allowlisted server 3xx-redirects to a non-allowlisted / private `Location` | Redirects not followed | failed call, no hop |
| 5 | Encoded metadata IP (`0xA9FEA9FE`, octal, `::ffff:169.254.169.254`) | Stage 2 inspects parsed `net.IP`, v4-mapped unwrapped | `ErrorCodeHTTPForbidden` |
| 6 | Multi-answer DNS: one public + one private A record | Stage 2 fires per candidate address | private candidate refused |
| 7 | Non-HTTP scheme (`file://`, `gopher://`, `unix://`) | Stage 1 scheme check | `ErrorCodeHTTPInvalidRequest` |
| 8 | Header injection / response splitting (CRLF in a header) | Header validation | `ErrorCodeHTTPInvalidRequest` |
| 9 | `Host` header override to bypass the allowlist string | `Host` derived from URL, override rejected | `ErrorCodeHTTPInvalidRequest` |
| 10 | Oversized response body → host OOM | `io.LimitReader` at the cap | `ErrorCodeHTTPResponseTooLarge` |
| 11 | Decompression bomb | cap applied to decompressed stream | `ErrorCodeHTTPResponseTooLarge` |
| 12 | Slowloris / hung connection to wedge `Process` | per-call timeout | `ErrorCodeHTTPTimeout` |
| 13 | Guest tries to widen its own allowlist via config it controls | policy bound host-side at instance open; guest copy not trusted | opt-in ignored, deny-all holds |
| 14 | Exfiltration: guest POSTs records to an allowlisted-but-hostile host | **not prevented** — operator trust decision; bounded + audited | logged; see residual risk |

**Residual risk (#14), stated plainly.** If an operator allowlists a host the attacker controls, the
egress mechanism will faithfully allow calls to it — this design cannot distinguish a legitimate
embedding vendor from a hostile one the operator listed. What it _does_ guarantee is that (a) egress
only ever reaches hosts the operator explicitly listed, (b) every destination is recorded in the
audit log and metrics (Observability), and (c) the guest cannot add a destination the operator did
not. Preventing an operator from trusting a bad host is a policy/review problem, not an enforcement
one; the design makes the trust decision explicit and observable rather than implicit.

### The local-Ollama exception

Local Ollama is served from `127.0.0.1:11434` (or a private VPC address). Stage 2 refuses
loopback/private by default, so this case is handled by the **explicit-IP-allowlist carve-out**: an
operator who lists `127.0.0.1:11434` (or a specific private endpoint) as an allowlist entry has that
exact IP admitted through Stage 2. The distinction that keeps this from reopening the SSRF hole: an
operator deliberately listing a loopback target in reviewed config is a decision; an
`api.some-vendor.com` entry silently _resolving_ to `127.0.0.1` is not — and Stage 2 tells the two
apart because the carve-out matches the **explicitly-listed IP**, not "any private address once a
hostname is listed."

## Configuration

### Trust model: deny-all, opt-in, engine-clamped

Three properties, in order of precedence:

1. **Default deny-all.** A processor gets zero egress unless explicitly opted in. A `Do` call from a
   non-opted-in processor returns `ErrorCodeHTTPEgressDisabled`.
2. **Per-processor opt-in (pipeline config).** The pipeline author enables egress for a specific
   processor by supplying an allowlist under a **host-reserved** key in that processor's config entry
   (illustrative: a reserved `sdk.egress.allow` / top-level `egress` block — exact key is an
   implementation decision). This is operator-authored: in Conduit the pipeline YAML author _is_ the
   operator, so this is a deploy-time, reviewed artifact, not runtime input.
3. **Engine-level ceiling (Conduit instance config).** The operator running the Conduit instance can
   set an outer bound (`Config.Processors.Egress`, env-overridable) that no pipeline can exceed. If a
   pipeline's per-processor allowlist names a host outside the engine ceiling, the effective allowlist
   is the **intersection**; the excess entries are dropped with a startup warning, never silently
   honored. The engine ceiling's own default is deny-all — so on a stock instance, egress requires an
   affirmative operator action at both levels (defense in depth for shared/multi-tenant instances).
   Whether the ceiling defaults to deny-all or to "whatever pipelines request" is Open Question #1.

### Why the guest cannot influence the policy

This is the architectural crux and it is a small but real departure from the schema pattern.
`schemaService` is a **process-global singleton**: `createServices` (`pkg/conduit/runtime.go`)
constructs one `procSchemaService` and hands it to `standalone.NewRegistry`, which shares it across
every processor instance. Egress policy **cannot** be global — each processor's allowlist differs.

So the plumbing is: the resolved, clamped allowlist + timeout + size-cap for a given processor is
computed by the engine from (pipeline processor config ∩ engine ceiling) **at the moment the
processor is opened**, and bound into that processor's host-module instance via `hostModuleOptions`
(the same seam that injects `schemaService` in `newWASMProcessor`). It lives in `hostModuleInstance`,
host-side, for the life of the processor. The guest never receives a mutable copy and the host never
re-reads policy from guest-supplied data. This requires threading the per-processor egress config
through `Registry.MakeProcessor` → `newWASMProcessor` → `hostModuleOptions` — slightly more than the
stateless schema wiring, and the one genuinely new bit of engine plumbing this capability adds.

### Allowlist entry syntax (proposed)

- Entries are `host` or `host:port`; scheme defaults to `https`. `http` is permitted **only** for an
  entry that is (or resolves to, via the explicit-IP carve-out) a private/loopback target.
- **No broad wildcards.** `*.example.com` is a footgun (`*.` can over-match); the default is
  exact-host matching. A single left-most-label wildcard (`*.api.openai.com`) may be permitted if
  DeVaris wants it, but the default recommendation is exact hosts only. Open Question #2.
- Port defaults: `443` for https, the entry's explicit port otherwise.

## Failure modes

Per CLAUDE.md "think in failure modes first." Each has a defined, coded, non-silent behavior.

1. **Allowlist miss (host or scheme).** Stage 1 rejects → `ErrorCodeHTTPForbidden` /
   `ErrorCodeHTTPInvalidRequest`. The embedding processor maps this to `ai.embedding_host_not_allowed`
   and routes the batch per the pipeline's processor-error policy (retry/DLQ) — never a silent drop
   (parent doc §4/§7). This is deterministic and config-caused, so it fails fast, not per-record
   flakily.
2. **Egress not enabled.** A processor that calls `Do` without an opt-in → `ErrorCodeHTTPEgressDisabled`
   at the first call. Surfaced as a clear, actionable config error ("egress is not enabled for this
   processor; add an allowlist entry"), not a generic transport failure.
3. **DNS failure.** Resolution returns no usable address → `ErrorCodeHTTPDNS`. Retryable at the
   processor's discretion (transient), distinct from a forbidden/allowlist error (permanent) so the
   processor's backoff logic does not burn its retry budget on a misconfiguration.
4. **Resolved-IP refused (rebinding / private target).** Stage 2 refuses the dial →
   `ErrorCodeHTTPForbidden`, logged as a **security-relevant denial** (distinct log/metric reason from
   a plain allowlist miss). This is the DNS-rebinding defense firing; it must be visible in the audit
   trail, not just returned to the guest.
5. **Timeout.** Per-call deadline exceeded → `ErrorCodeHTTPTimeout`. The `Process` call cannot hang
   indefinitely; the connection is torn down.
6. **Oversized response / decompression bomb.** `io.LimitReader` trips →
   `ErrorCodeHTTPResponseTooLarge`, connection closed, no partial body delivered to the guest.
7. **Transport / TLS error.** Connection reset, TLS handshake failure, etc. → `ErrorCodeHTTPTransport`,
   retryable-transient classification.
8. **Malicious guest (SSRF / exfil attempt).** Covered by the attack table; every attempt is
   refused-and-logged (scenarios 1–13) or bounded-and-audited (14). The host never trusts the guest's
   URL beyond using it as an input to Stages 1+2.
9. **Backward/forward compat mismatch.** See next section — a load-time, discoverable failure, never
   a silent one.

## Capability negotiation and backward compatibility

- **Additive to the host module.** `http_request` is a new entry in the `conduit` host module's
  function map. Existing processors that never call it are entirely unaffected — no protocol version
  bump, no change to `create_schema`/`get_schema`, no change to the command channel.
- **Import presence is naturally tied to use.** Go's WASM linker only emits a `//go:wasmimport` for a
  symbol that is reachable. A chunking processor that never touches `HTTPService` will not import
  `conduit.http_request`; only a processor that actually uses egress links it. So the ABI surface a
  guest declares reflects what it actually needs.
- **Old host + new guest → discoverable load-time failure.** wazero resolves imports at module
  instantiation. A guest that imports `conduit.http_request` running on a Conduit binary too old to
  export it fails instantiation with an unresolved-import error at plugin load — the same "clear,
  discoverable compatibility failure at plugin-load time" the parent doc describes for the whole
  capability. Not silent, not a runtime surprise.
- **New host + old guest → fully compatible.** An older standalone processor that never imports the
  function runs unchanged on a newer host.
- **ABI versioning discipline.** The function **name** is the version handle. If the
  `HTTPRequest`/`HTTPResponse` contract ever needs a breaking change, add `http_request_v2` rather
  than mutating the wire shape of `http_request` — the same additive rule the rest of the host module
  follows. The proto messages live under the already-versioned `proto/procutils/v1`.
- **No separate capability handshake (proposed).** Rather than a "does egress exist / is it enabled"
  negotiation call, the capability is _present_ iff the host exports the function (discovered at load
  time as above) and _enabled_ iff the processor opted in (the first `Do` returns
  `ErrorCodeHTTPEgressDisabled` if not). This avoids an extra round-trip and extra ABI surface. Open
  Question #4 asks whether a lightweight capability-probe is worth adding anyway.

## Alternatives considered

**(a) Host-mediated, allowlisted egress (chosen).** Smallest new surface: reuses the reviewed
host-broker pattern, adds no transport or protocol rev, keeps the guest socketless, and puts the
security policy exactly where the engine already owns config trust. Its cost is that the host must
implement the SSRF/rebinding defenses correctly — which is precisely why this doc exists and why the
defenses are testable requirements rather than guidance.

**(b) Grant the WASM sandbox raw network access (WASI sockets / a socket host-call). Rejected.**
Preview 1 has no sockets; adding them (or moving to a preview that has them) would give
untrusted, any-language guest code unrestricted outbound network from inside the host process, with
**no allowlist, no SSRF control, no rebinding defense** unless we rebuild all of the above around a
socket API anyway. It maximizes the attack surface to solve a problem (one JSON POST to an embedding
API) that needs a keyhole, not a door. This is the option the whole design exists to avoid.

**(c) A separate sidecar / forward-proxy process the guest talks to. Rejected.** The guest still has
no sockets, so it would still need a host-brokered channel to reach the sidecar — i.e. this design
plus an extra always-on process to operate, secure, and version. It also moves allowlist enforcement
out of the engine's config trust boundary into a separate component with its own config surface and
its own way to be misconfigured. More moving parts for an identical guarantee. A sidecar (e.g. an
egress proxy like a locked-down Squid) is a fine _operational_ deployment choice an operator can make
independently, but it is not a substitute for the in-engine capability, and it is not something the
engine should require.

**(d) WASI Preview 2 `wasi:http/outgoing-handler` (Component Model). Rejected for now.** This ties
egress to the Component Model, which the pending ADR `20260722-wasm-component-model-deferred.md`
explicitly defers. More to the point, `wasi:http` gives a guest an HTTP client — it does **not**
provide an allowlist, a resolved-IP rebinding defense, redirect suppression, or per-call caps. We
would have to wrap it in exactly the host-side policy this design specifies, so it buys nothing today
and couples us to a deferred runtime model. Revisit if/when the Component Model lands.

## Observability

Egress from a sandbox is security-sensitive, so **every** call — allowed and denied — is auditable.

- **Structured log per call** (host-side): pipeline ID, processor ID, method, request host, **the
  resolved IP actually dialed**, HTTP status, latency, request bytes, response bytes, decision
  (`allow`/`deny`), and on deny the **reason** (`allowlist_miss`, `ip_refused_rebinding`,
  `scheme_rejected`, `redirect_blocked`, `size_cap`, `timeout`, …). Deny events are logged at a level
  that survives normal log filtering — they are the audit trail for an attempted SSRF.
- **Redaction is mandatory.** Request and response **bodies are never logged** (they contain pipeline
  records and may contain PII), and the `Authorization` header (and any header matching a
  secret-name denylist) is redacted. Logging a bearer token would itself be a security bug.
- **Metrics** (Prometheus, alongside the existing pipeline metrics):
  - `conduit_processor_egress_requests_total{processor,host,decision}` — counter.
  - `conduit_processor_egress_denied_total{processor,reason}` — counter; a spike here is an alertable
    security signal (attempted egress to refused destinations).
  - `conduit_processor_egress_request_duration_seconds{processor,host}` — histogram (distinct from the
    embedding processor's own `conduit_embedding_call_duration_seconds` in the parent doc; this is the
    host-transport view, that is the processor-logic view).
  - `conduit_processor_egress_response_bytes{processor,host}` — histogram, for capacity/cap tuning.
- **A runbook entry** under `docs/operations/` for the alertable case: a rising
  `egress_denied_total{reason="ip_refused_rebinding"}` means either a misconfigured allowlist entry
  whose DNS points somewhere private, or an actual rebinding attempt — symptom → diagnosis →
  remediation.

## Testing

The security tests are the deliverable's point; "it compiles and does a happy-path POST" is
explicitly insufficient. All of the following are required host-side unit/integration tests for the
capability (in the engine repo, at the `host_module.go` wiring layer), independent of the embedding
processor that consumes it.

- **Allowlist enforcement (Stage 1).** Table-driven: allowed host passes; non-allowlisted host,
  non-allowlisted port, and rejected scheme each return the correct numeric code.
- **DNS-rebinding / resolved-IP check (Stage 2) — the headline test.** Inject a fake resolver that
  returns a **public** IP for an allowlisted hostname on the first lookup and a
  **private/loopback/link-local** IP on a subsequent lookup (simulating a rebind or a plain
  private-resolving host). Assert the dial is **refused at the `Control` hook** regardless of the
  hostname having passed Stage 1 — proving the check is at dial time against the actual address, not
  once against the string. Parameterize across the full refused-range table (RFC 1918, `127/8`,
  `169.254/16` incl. `169.254.169.254`, `::1`, `fe80::/10`, `fc00::/7`, v4-mapped-v6, `0.0.0.0`).
- **The local-Ollama carve-out.** An explicitly-allowlisted `127.0.0.1:11434` entry is **not** blocked
  by the Stage-2 private-range refusal — proving the refusal is explicit-IP-allowlist-aware, not a
  blanket ban that would break the zero-key local path.
- **Redirect handling.** An allowlisted server returning a 3xx to a non-allowlisted / private
  `Location` results in a failed call, not an automatic hop.
- **Header / request hardening.** CRLF in a header, a guest-supplied `Host` override, and a
  non-http(s) scheme each return `ErrorCodeHTTPInvalidRequest`.
- **Timeout.** A slow test server (delayed headers, then delayed body) trips `ErrorCodeHTTPTimeout`
  within the configured deadline.
- **Response-size cap + decompression bomb.** An oversized body and a small-but-hugely-inflating gzip
  body both trip `ErrorCodeHTTPResponseTooLarge`, with the cap enforced on the **decompressed** size.
- **Config clamping.** A per-processor allowlist naming a host outside the engine ceiling yields the
  intersection (excess dropped with a warning); a non-opted-in processor gets
  `ErrorCodeHTTPEgressDisabled`; the guest cannot widen the policy via any config it supplies.
- **Backward/forward compat.** New guest on old host → instantiation fails with an unresolved-import
  error (discoverable); old guest on new host → unaffected.
- **Fuzzing (per CLAUDE.md "fuzz every parser boundary").** Fuzz the URL parser + allowlist matcher +
  the refused-range classifier — these are the parser/decision boundaries where an encoding or
  edge-case bug becomes an SSRF bypass. Native Go fuzzing, CI-short + scheduled-long.
- **ABI round-trip (property).** `HTTPRequest`/`HTTPResponse` proto marshal/unmarshal round-trips
  under the park/resize protocol, including the buffer-too-small resize path, mirroring the existing
  schema-call tests.

## Open questions for DeVaris

1. **Engine-ceiling default and two-tier model.** Confirm the deny-all engine ceiling + per-pipeline
   opt-in (intersection) model, and specifically whether the **engine ceiling defaults to deny-all**
   (so egress needs an affirmative operator action at the instance level even if a pipeline YAML
   requests it — safest for shared instances) or defaults to "honor whatever the pipeline requests"
   (simpler for single-tenant / laptop use). Recommendation: deny-all engine default, overridable.
2. **Allowlist entry syntax — wildcards.** Exact-host-only (recommended default) vs. permitting a
   single left-most-label wildcard (`*.api.openai.com`). Broad wildcards are out regardless. Confirm.
3. **Secret / API-key handling — the most consequential question.** The embedding processor needs an
   `Authorization: Bearer <key>` header. Two options: **(a)** the key flows from pipeline config into
   the guest, and the guest puts it in `HTTPRequest.Headers` — simple, but the key lives in WASM guest
   memory; or **(b)** the guest references a secret _by name_ and the **host injects** the actual
   header from Conduit's secret store, so the key **never enters the sandbox**. Option (b) is
   materially stronger (a compromised/malicious guest never sees the credential) but adds a
   secret-resolution + reserved-header-injection step to the ABI. Recommendation leans (b); this
   changes the ABI, so it needs a decision before implementation.
4. **Capability-probe.** Ship with no explicit negotiation (present-iff-exported, enabled-iff-opted-in,
   as designed) or add a lightweight `egress_available`-style probe the guest can call first?
   Recommendation: none for v1; the load-time import failure + `ErrorCodeHTTPEgressDisabled` cover it.
5. **Redirect policy default.** Recommend **never follow**. Confirm no v0.20 embedding provider needs
   redirect-following (none of OpenAI/Voyage/Cohere/Ollama embeddings endpoints do).
6. **Defaults to freeze vs. leave to implementation.** Per-call timeout (proposed 30s, matching the
   parent doc's provider timeout) and response-size cap (proposed a few MiB — embedding responses are
   small; the cap is a DoS bound, not a functional limit). Freeze now or leave as implementation-time
   constants like the parent doc did for batch size?
7. **Refused-range set extensibility.** The canonical block-list (RFC 1918 / link-local / loopback /
   ULA / v4-mapped) is fixed and non-negotiable as a floor. Should operators be able to **add** ranges
   to the refusal set (tighter), and — the harder call — should the explicit-IP carve-out generalize
   from "a single loopback/private IP" to "an operator-declared private CIDR" for private-VPC embedding
   endpoints (the Ollama case at datacenter scale)? Recommendation: allow adding to the refusal set;
   keep the carve-out to explicit IPs (not CIDRs) in v1 to keep the bypass surface minimal.
8. **Ship gated as experimental?** Given this is a new security boundary, do we land it behind an
   explicit `experimental`/feature-flag posture for the first release, with the flag removed once the
   embedding processor has exercised it in CI end-to-end?

## Related

- `docs/design-documents/20260724-ai-pipeline-components.md` (signed off, PR #2687) — the parent
  design; this doc is the focused expansion of its Decision §1 and its Open Question #2 (the
  standalone sign-off for the host capability). The `ai.*` product error codes, the embedding
  processor's batching/backpressure semantics, and the DNS-rebinding note all originate there.
- `pkg/plugin/processor/standalone/host_module.go` — the wazero host module this capability extends
  (`create_schema`/`get_schema` are the pattern `http_request` follows); `handleWasmRequest` is the
  park/resize protocol reused verbatim.
- `pkg/plugin/processor/standalone/processor.go`, `registry.go`, and `pkg/conduit/runtime.go`
  (`createServices`) — the `schemaService` wiring path this capability's per-processor egress policy
  threads through, with the one difference that egress policy is per-instance (bound at
  `newWASMProcessor`) rather than a process-global singleton.
- `conduit-processor-sdk` `pprocutils/schema.go`, `pprocutils/errors.go`, `wasm/imports.go`,
  `wasm/schema.go`, `wasm/caller.go` — the guest-side and interface-side pattern
  (`SchemaService`, the numeric error-code band, the `hostCall` 2-try resize loop) the new
  `HTTPService` mirrors.
- `docs/architecture-decision-records/20260722-wasm-component-model-deferred.md` (pending
  ratification) — defers WASI Preview 2 / Component Model, which is why Alternative (d)
  (`wasi:http`) is rejected for now and why standalone processors remain WASI-P1/socketless.
- `docs/design-documents/20260722-conduit-generate.md` — the per-call-timeout shape and the
  bounded-not-quantified stance the embedding processor (the capability's first consumer) inherits.
- `docs/postmortems/20260723-source-ack-persist-ordering.md` — the sev-0 whose "never trust the fast
  path with a data-integrity guarantee" lesson this doc applies to a security boundary: the
  hostname-string fast path is never trusted as the guarantee; the dial-time resolved-IP check is.
