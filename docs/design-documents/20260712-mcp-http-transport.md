# MCP HTTP transport: token auth + TLS

## Summary

`conduit mcp` gained an optional network transport: `--http <addr>` serves the MCP tool catalog over
**streamable HTTP**, guarded by a **bearer token** (`--token-file`, constant-time compared) and
**TLS** (`--tls-cert`/`--tls-key`), and refuses to start unless both are present (fail-closed). This
advances Phase-1 execution plan §2: "HTTP transport security: authn (token) + TLS for the HTTP MCP
surface; documented."

This document is the **dedicated security design record** for that surface. The umbrella MCP design
doc ([`20260708-mcp-server.md`](./20260708-mcp-server.md)) covered it in a single paragraph (§5) and
one acceptance line (AC-8); the transport then shipped bundled into the Wave-3 MCP PR
(`46bbf7c`, #2591). A network-exposed authentication surface — the **first** one in the entire
Conduit codebase — deserves more than a paragraph. This doc records the as-built design, does the
threat model §5 skipped, hardens the acceptance criteria into a testable checklist, enumerates the
gaps, and makes an explicit **ship-in-v0.17-vs-defer-to-v0.18** recommendation (the maintainer
flagged `--http` as a defer candidate).

**Status: documents already-merged code (#2591) plus a hardening plan.** This is not a greenfield
plan — the implementation exists at `cmd/conduit/root/mcp/http.go` and `cmd/conduit/root/mcp/mcp.go`
with tests at `cmd/conduit/root/mcp/http_test.go`. Nothing below asks to re-implement what is there;
it asks to (a) ratify the design, (b) close a documented set of gaps before the v0.17 stable cut, and
(c) caveat the surface as experimental until they close.

## Context

### The transport that exists today

- **Command wiring:** `MCPFlags` (`cmd/conduit/root/mcp/mcp.go:51-62`) adds `--http`, `--token-file`,
  `--tls-cert`, `--tls-key` alongside the pre-existing `--allow-mutations` and `--api-address`.
- **Dispatch:** `MCPCommand.Execute` (`mcp.go:158-198`) branches on `c.flags.HTTP == ""`. Empty →
  `srv.Run(ctx, &sdkmcp.StdioTransport{})` (unchanged stdio default). Non-empty → build an
  `*http.Server` and `ListenAndServeTLS("", "")` with cert/key already loaded into `TLSConfig`.
- **Server construction:** `newHTTPServer` (`http.go:39-69`) validates the flag combination, loads
  the token, loads the cert, wraps the MCP handler in a body-size limiter, and returns an
  `*http.Server` with `MinVersion: tls.VersionTLS12` and `ReadHeaderTimeout: 10s`.
- **Auth:** `requireBearerToken` (`http.go:144-154`) rejects a missing/malformed `Authorization`
  header or a non-matching token with `401` + `WWW-Authenticate` **before** the request reaches the
  MCP handler. `constantTimeEqual` (`http.go:160-165`) uses `crypto/subtle.ConstantTimeCompare`.
- **Fail-closed gate:** `validateHTTPConfig` (`http.go:90-107`) refuses `--http` without a token
  and/or TLS pair, with a `conduiterr.CodeInvalidArgument` error and a suggestion.
- **Body limit:** `limitRequestBody` (`http.go:79-84`) caps request bodies at 4 MiB via
  `http.MaxBytesReader`.
- **SDK:** `github.com/modelcontextprotocol/go-sdk v1.6.1` (the official Anthropic-maintained SDK),
  pinned in `go.mod:38`. HTTP is served via `sdkmcp.NewStreamableHTTPHandler` (`http.go:136`).

### Which SDK, and what HTTP transport does it actually offer

The SDK is the **official `github.com/modelcontextprotocol/go-sdk` v1.6.1**, not `mark3labs/mcp-go`
or any fork. Inspecting the module cache confirms it ships **both** HTTP transports:

- `mcp.NewStreamableHTTPHandler(getServer, opts)` — `mcp/streamable.go:194`. The current MCP HTTP
  transport ("Streamable HTTP", spec revision 2025-03-26 and later): a single endpoint that upgrades
  to a server-sent-events stream only when a response needs to stream.
- `mcp.NewSSEHandler(getServer, opts)` — `mcp/sse.go:83`. The **legacy** HTTP+SSE transport
  (2024-11-05 spec), deprecated in the MCP spec in favor of Streamable HTTP.

Both return an `http.Handler` bound to the same `*mcp.Server`, so the HTTP surface serves the exact
same tool catalog as stdio (no divergent logic — the three-faces rule holds within MCP too). **The
implementation chose `NewStreamableHTTPHandler`, which is correct** (see §"Transport choice").

### What we can and cannot reuse from Conduit's own HTTP/gRPC API

This is the load-bearing finding for "reuse the API's TLS/auth, don't invent new patterns":

**There is nothing to reuse — Conduit's own HTTP/gRPC API has neither TLS nor authentication
today.** The API config (`pkg/conduit/config.go:73-97`) exposes only `api.enabled`,
`api.http.address`, `api.grpc.address`, and `api.allow-live-restart-apply`. There are no TLS or
token fields. The API is served by a plain `http.Server` with `ListenAndServe` (no TLS) at
`pkg/conduit/runtime.go:781` / `:864-885`. So the MCP HTTP transport is the **first TLS-terminating,
first authenticated network surface in the codebase**. It cannot inherit an API TLS config that does
not exist; if anything, the API should later borrow *this* transport's pattern.

What the implementation **did** correctly reuse is the *authorization-gate pattern*, not a TLS stack:
`--allow-mutations` and `--token-file`/TLS are **process-level flags set at startup, never agent-
passable tool arguments** — the same shape as `--api.allow-live-restart-apply`
(`config.go:83-96`, [`docs/operations/live-restart-apply.md`](../operations/live-restart-apply.md)).
An agent driving a tool call has no lever to flip mutations on, present a different token, or disable
TLS. That is the invariant this design most needs to preserve.

## Problem / use case — why HTTP MCP at all

stdio is the primary and default agent channel: the agent spawns `conduit mcp` as a subprocess, owns
its stdin/stdout, and no other principal can reach it — so it needs no authentication (whoever can
talk to the pipe already controls the process). That covers the local-copilot case entirely.

HTTP exists for the case stdio structurally cannot serve: a **remote agent** — a hosted assistant, a
CI job, a fleet controller, an agent runtime on another host — that must reach a long-lived Conduit
MCP endpoint it did not spawn and does not own. Over a network, "owning the process" no longer
implies authorization, so the transport must supply what stdio got for free: confidentiality (TLS)
and caller authentication (a bearer token). The design goal is narrow and explicit: **make the
remote surface no less safe than the local one**, and refuse to run if it cannot.

Non-goals: replacing stdio (it stays the default), per-agent identity/RBAC, OAuth/OIDC flows,
multi-tenant isolation, or a public internet-facing deployment story. Those are v0.18+ if demand
appears.

## Constraints

- **Fail-closed, always.** A network MCP surface with mutations available (`--allow-mutations`) is a
  remote code/effect surface. It must be impossible to serve HTTP in plaintext or without a token —
  not "warned about", refused.
- **Non-agent-passable authorization.** Token, TLS, and mutation-gating are all process-level, set by
  the operator at startup, mirroring `--api.allow-live-restart-apply`. No tool argument may weaken
  any of them.
- **stdio unchanged and still default.** The common path (no `--http`) must be byte-for-byte the
  prior behavior; adding the flag must not regress it.
- **One tool catalog.** HTTP and stdio serve the identical `*sdkmcp.Server`; the mutation gate
  (`--allow-mutations`) decides *whether* a write tool is registered, orthogonal to *which* transport
  serves it.
- **No new heavyweight dependency.** TLS and bearer auth are `crypto/tls` + `crypto/subtle` +
  `net/http` from the standard library. (The go-sdk was already added by the MCP PR.)

## Decision

### 1. Transport choice — Streamable HTTP, not SSE

Use `mcp.NewStreamableHTTPHandler`. Reasons:

- It is the **current** MCP HTTP transport; `NewSSEHandler` implements the deprecated 2024-11-05
  HTTP+SSE transport the spec has superseded. New clients target Streamable HTTP; building on the
  deprecated one would be born-legacy.
- Single endpoint, simpler operational surface (one path to auth-guard, one to TLS-terminate) versus
  SSE's two-endpoint (GET stream + POST message) model.
- It degrades to a plain request/response for the small JSON-RPC calls the Conduit tool catalog makes
  (validate/lint/deploy/apply/…), streaming only when a response needs it.

This choice is already in the code (`http.go:136`). The doc ratifies it.

### 2. Auth model — static bearer token, process-level

- A single bearer token, read once at startup from `--token-file` (trimmed; empty-after-trim is
  refused, `http.go:113-127`), compared against `Authorization: Bearer <token>` in constant time.
- The token is a **file path, not an inline flag value**, so the secret never lands in the process
  argv (visible in `ps`, shell history, container inspect). This is deliberate and must stay.
- Auth is enforced by middleware wrapping the *entire* MCP handler, so **tool enumeration
  (`tools/list`) is authenticated too** — an unauthenticated caller learns nothing about the catalog,
  not even that `--allow-mutations` is on.
- Non-agent-passable: there is no `ApplyPipelineRequest`-style field, no tool argument, and no
  MCP-session parameter that carries or overrides the token, TLS, or mutation gate.

Scope boundary (v0.17): one shared static token, no per-agent identity, no revocation-per-client,
no rotation-without-restart. Rotating the token means restarting the process. Accepted for v0.17;
revisit in v0.18 if multi-agent demand appears.

### 3. TLS — operator-provided cert/key, TLS 1.2 floor, required

- `tls.LoadX509KeyPair(--tls-cert, --tls-key)` at startup; a load failure refuses to start with a
  `conduiterr` error (`http.go:49-52`). The server is handed the loaded certificate directly and
  called as `ListenAndServeTLS("", "")` — **there is no plaintext `ListenAndServe` path for `--http`
  at all** (`mcp.go:190`). TLS is not optional; it is the only way HTTP is ever served.
- `MinVersion: tls.VersionTLS12` (`http.go:63`) prevents a TLS 1.0/1.1 downgrade.
- Operator supplies the cert (their own CA, an internal PKI, or a self-signed cert the agent host
  trusts). Conduit does not generate or manage certs — matching the "operator-provided" line in the
  umbrella doc §5.

### 4. Exact flag / config surface

```text
conduit mcp
  --http <addr>          also serve streamable HTTP on <addr>; requires --token-file + --tls-cert/--tls-key
  --token-file <path>    file containing the bearer token (constant-time compared)
  --tls-cert <path>      TLS certificate for --http
  --tls-key <path>       TLS key for --http
  --allow-mutations      (pre-existing) register write tools; process-level, never agent-settable
  --api-address <addr>   (pre-existing) gRPC address the inspect tool dials
```

Behavior matrix:

| `--http` | token | cert+key | Result |
| --- | --- | --- | --- |
| unset | — | — | **stdio only** (default, unchanged) |
| set | missing | — | **refuse to start** (`CodeInvalidArgument`, fail-closed) |
| set | present | missing/partial | **refuse to start** (no plaintext HTTP) |
| set | present | present | serve streamable HTTP over TLS with bearer auth |

### 5. Failure modes

- **`--http` with no token / no TLS →** refuse to start before binding a listener
  (`validateHTTPConfig`, proven by `TestValidateHTTPConfig_RefusesWithoutTokenOrTLS` and
  `TestNewHTTPServer_RefusesWithoutTokenOrTLS`).
- **Empty token file →** refuse to start (an empty required token would make every empty
  `Authorization` header "match"; `loadBearerToken` rejects it).
- **Unreadable token file / unparseable cert →** refuse to start with a `conduiterr` + suggestion.
- **Request with no / wrong / wrong-scheme Authorization →** `401` before the MCP handler runs
  (`TestRequireBearerToken_AuthenticatesGoodRejectsBad`, `TestNewMCPHTTPHandler_EndToEndAuth`).
- **Oversized request body →** `http.MaxBytesReader` fails the read (413-shaped) instead of buffering.
- **Slow-header (Slowloris) client →** `ReadHeaderTimeout: 10s` cuts it.
- **Graceful shutdown:** on `ctx` cancel, the errgroup calls `httpSrv.Shutdown` with a 5s timeout
  (`mcp.go:182-195`); `http.ErrServerClosed` is treated as clean.
- **TLS handshake failure (bad client, plaintext client hitting the TLS port) →** handled by the
  stdlib TLS server; the connection is dropped, no handler runs.

## Acceptance criteria (testable checklist)

Each item names how it is verified. Items marked **[have]** are covered by existing tests in
`cmd/conduit/root/mcp/http_test.go`; items marked **[gap]** are must-add before the stable cut (see
§Hardening).

- **AC-1 — stdio unchanged and still default. [have, partial]** `conduit mcp` with no `--http`
  serves stdio (`mcp.go:165-168`); an MCP client lists/calls the read tools identically to before.
  *Add:* an explicit regression test asserting the no-`--http` path selects `StdioTransport` and
  never constructs an `http.Server`.
- **AC-2 — fail-closed without token+TLS. [have]** `--http` set with any missing piece
  (nothing / token-only / TLS-only / cert-without-key / key-without-cert) refuses to start;
  all-present succeeds. `TestValidateHTTPConfig_RefusesWithoutTokenOrTLS` (6 cases) +
  `TestNewHTTPServer_RefusesWithoutTokenOrTLS`.
- **AC-3 — TLS actually terminates. [have, partial]** With valid token+cert+key, `newHTTPServer`
  builds a server whose `TLSConfig` carries exactly one loaded certificate and `MinVersion >=
  TLS1.2` (`TestNewHTTPServer_WithTokenAndTLS_Succeeds` checks the cert). *Add:* an end-to-end test
  that a **plaintext** `http://` request to the `--http` port fails (no plaintext fallback), and that
  a TLS 1.1 client is refused.
- **AC-4 — bearer auth: reject bad, admit good. [have]** No header → `401`; `Basic <token>` → `401`;
  `Bearer <wrong>` → `401`; `Bearer <right>` → reaches the handler; every `401` carries
  `WWW-Authenticate`. `TestRequireBearerToken_AuthenticatesGoodRejectsBad`,
  `TestNewMCPHTTPHandler_EndToEndAuth`.
- **AC-5 — auth guards enumeration. [have, implied → make explicit]** `tools/list` over HTTP without
  a valid token → `401` (the middleware wraps the whole handler). *Add:* an explicit test issuing a
  `tools/list` JSON-RPC with no token and asserting `401` (not an empty catalog, not a `200`).
- **AC-6 — empty token file refused. [gap]** A `--token-file` that is empty or whitespace-only
  refuses to start. Logic exists (`loadBearerToken`); *add* the direct test.
- **AC-7 — `--allow-mutations` still gates writes over HTTP. [gap]** With `--http` **and**
  `--allow-mutations`, `apply`/`scaffold_*` appear in the HTTP catalog; with `--http` and **no**
  `--allow-mutations`, they are absent over HTTP exactly as over stdio. Asserts the gate is transport
  -independent. *Add* this test (currently only the stdio gate is directly tested).
- **AC-8 — token never logged. [gap]** With `--http` up and `log.level=trace`, neither the token nor
  any `Authorization` header value appears in server logs on success or on a `401`. *Add* a test
  scanning captured log output for the token string.
- **AC-9 — token not in argv. [have by construction]** The token is passed as a *file path*; assert
  no flag accepts an inline token value. (Design invariant; enforced by the flag set, no runtime
  test needed, but stated so a future flag addition doesn't violate it.)
- **AC-10 — auth failures are observable. [gap]** A `401` emits a log line (method, remote addr,
  outcome) so brute-force attempts are detectable. *Add* the log emission and its test. (See
  Hardening H-2.)
- **AC-11 — help text matches behavior. [gap — see self-review SR-1]** The `--http` usage/Docs text
  must not claim a co-served stdio that the implementation does not provide. *Fix the text*, then a
  doc test / manual check confirms consistency.
- **AC-12 — graceful shutdown. [gap]** On SIGTERM/ctx-cancel while serving HTTP, in-flight requests
  drain within the 5s `Shutdown` window and the process exits 0. *Add* a test driving cancel against
  a live listener.

## Threat model

Surface class: **network-exposed, authenticated, potentially mutation-capable.** Highest-risk surface
in the product. Threats and current posture:

- **Auth bypass.** *Posture: mitigated.* Middleware wraps the entire MCP handler; there is no route
  that skips it, no unauthenticated health/metadata path on the `--http` server. Residual: any future
  addition of a second route on this server must go through the same middleware — noted as an
  invariant, not enforced by a linter. *Action: keep the handler composition single-entry.*
- **Plaintext / TLS downgrade.** *Posture: mitigated.* No `ListenAndServe` path exists for `--http`;
  only `ListenAndServeTLS`. `MinVersion: TLS1.2` blocks 1.0/1.1. Residual: cipher-suite selection is
  the Go default (reasonable); we do not pin suites. Acceptable.
- **Token in logs / error messages / argv.** *Posture: mostly mitigated, one gap.* Token is a file
  path (not argv), and no current code logs it. **Gap: there is no test guaranteeing it stays out of
  logs** (AC-8), and no structured audit line on auth events (AC-10). A careless future log statement
  could leak it. *Action: add AC-8/AC-10 tests + a redaction-conscious auth log.*
- **Unauthenticated tool enumeration.** *Posture: mitigated.* `tools/list` requires the token (AC-5).
  An unauthenticated attacker cannot even discover whether mutations are enabled.
- **Brute-force / credential stuffing on the token.** *Posture: partial.* Constant-time compare
  prevents a timing oracle on token *content*. **Gaps:** (a) no rate limiting or lockout — an
  attacker can try tokens as fast as the network allows; (b) no logging, so attempts are invisible
  (AC-10); (c) `constantTimeEqual` short-circuits on length mismatch, leaving a *length* timing
  oracle (an attacker could learn the token's length). The length oracle is low-value against a
  high-entropy token and is a deliberate, documented trade-off (`http.go:156-165`), but it is a
  residual. *Action: add auth-failure logging now (H-2); rate limiting is v0.18 (H-5).*
- **DoS — body flood.** *Posture: mitigated.* 4 MiB `MaxBytesReader` cap. Blast radius limited to
  token holders.
- **DoS — slow clients / connection exhaustion.** *Posture: partial.* `ReadHeaderTimeout` blocks
  Slowloris on headers. **Gap: no `IdleTimeout` and no `ReadTimeout`/`WriteTimeout`**, so an
  authenticated (or handshaking) client could hold connections open. Streamable HTTP uses long-lived
  responses, so a blanket `WriteTimeout` is wrong, but an `IdleTimeout` is safe and should be set
  (H-3).
- **Mutation via the network by an unauthorized principal.** *Posture: mitigated by composition.*
  Writes require both a valid token (network auth) **and** operator-set `--allow-mutations` (process
  gate) **and**, for a running pipeline, `--api.allow-live-restart-apply` (the live-apply gate).
  Three independent process-level gates, none agent-passable.
- **Binding to a public interface unintentionally.** *Posture: gap.* `--http :8443` binds all
  interfaces; nothing warns when the address is non-loopback. TLS+token make this *safe*, but a
  surprised operator is a real failure mode. *Action: log a warning on non-loopback bind (H-4).*
- **Compromised/stolen token → full catalog access.** *Posture: accepted for v0.17.* A single shared
  token means one leak grants everything the catalog exposes, with no per-client revocation short of
  rotating (restart). This is the strongest argument for the "experimental" caveat. v0.18: per-agent
  tokens / revocation (H-6).

## Hardening plan (gap → action)

Must-fix before the v0.17 stable cut (small, high-value, no new deps):

- **H-1 (AC-11 / SR-1): fix the help-text/behavior mismatch.** `--http` says "in addition to stdio"
  but HTTP mode serves network only (`mcp.go:176-181` comment vs. `mcp.go:54`/`:90-92` text). Correct
  the flag usage and `Docs.Long` to state HTTP mode does **not** co-serve stdio (the daemon has no
  attached stdin). Text-only change.
- **H-2 (AC-10): auth-event logging.** Emit a structured log line on `401` (method, remote addr,
  outcome) and on server start ("serving MCP over HTTPS on <addr>, auth: bearer"), being careful to
  never log the token itself (AC-8 guards this).
- **H-3: set `IdleTimeout`.** Add `IdleTimeout` (e.g. 120s) to bound idle connections without
  breaking streamable responses.
- **H-4: warn on non-loopback bind.** If `--http` resolves to a non-loopback address, log a warning
  naming the exposure.
- **AC-1, AC-5, AC-6, AC-7, AC-8, AC-12 test additions** as listed above (behavioral coverage that
  already-correct code currently lacks a guard for).

Defer to v0.18 (larger, demand-gated):

- **H-5:** rate limiting / lockout on repeated auth failures.
- **H-6:** per-agent tokens with revocation; optional mTLS (client-cert) auth.
- **H-7:** token hot-reload / rotation without restart.
- **H-8:** cipher-suite pinning and a TLS 1.3 floor once client support is universal in the target
  agent runtimes.

## Test plan

Unit / handler-level (extend `cmd/conduit/root/mcp/http_test.go`): AC-1, AC-5, AC-6, AC-7, AC-8,
AC-10 as above; keep the existing AC-2/AC-3/AC-4 tests.

Integration (a real `httptest.NewTLSServer` or a bound `ListenAndServeTLS` on `:0`): drive a real MCP
client (or raw JSON-RPC) over TLS with a valid token through a full `initialize` → `tools/list` →
`tools/call validate` round-trip; assert a bad token is `401` at the same endpoint; assert a
plaintext `http://` client fails (AC-3); assert graceful shutdown drains (AC-12).

Security regression: the AC-8 log-scan test is the anchor — it must fail if any future change logs
the token. Add the auth-failure-logging assertion (AC-10) in the same file so the two move together.

Out of scope for automated CI (manual / documented): real-CA cert chains, mTLS, load/rate-limit
behavior (those arrive with H-5/H-6).

## Upgrade / rollback

- **Additive and opt-in.** Absent `--http`, behavior is identical to the pre-#2591 stdio server;
  there is no serialized-state or protocol change, so no migration. Rollback = don't pass `--http`
  (or ship a build without it). No compatibility surface is affected.
- **If deferred (see recommendation):** hide `--http` behind an explicit experimental gate or mark it
  clearly in help; the flags stay parsed so configs don't break, but the surface is labeled
  not-yet-stable. No data path touched either way.

## Observability

Today: none specific to the transport (a gap — H-2/AC-10). After hardening: a start-up line naming
the bound address and auth mode, and a per-`401` line for auth failures (token-redacted). No metrics
in v0.17; a `mcp_http_auth_failures_total` counter is a reasonable v0.18 add alongside rate limiting.

## Recommendation: ship in v0.17 as **experimental**, gated on the H-1..H-4 must-fixes

The maintainer flagged `--http` as a defer candidate. The honest call, given the code already
exists and is more careful than most first cuts:

**Ship it in v0.17, but labeled experimental, and only after the four must-fix hardening items
(H-1..H-4) and the missing-coverage tests (AC-1/5/6/7/8/12) land. Do not pull it; do not present it
as production-grade.**

Reasoning:

- **The security fundamentals are correct, not aspirational.** Fail-closed refusal, no-plaintext-path,
  constant-time compare, authenticated enumeration, body cap, Slowloris guard, token-out-of-argv,
  and three independent non-agent-passable gates for writes. There is **no auth bypass, no plaintext
  hole, no token-leak-in-argv** — the gaps are hardening (logging, rate limiting, idle timeout, a
  doc-string mismatch), not correctness holes. Pulling well-built, correct-at-the-core code would
  waste it and delay the one thing §2 explicitly asks for.
- **But it is the first network auth surface in the product and shipped under a one-paragraph design
  note.** Presenting it as stable overstates maturity: no audit logging (brute-force is invisible),
  no rate limiting, a single shared token with no revocation, and a user-facing help string that
  contradicts the behavior. Those are exactly the things an operator exposing a mutation-capable
  endpoint needs, and their absence is why "experimental" is the honest label.
- **The must-fix set is cheap.** H-1 is text. H-2/H-3/H-4 are a few lines each of standard-library
  code. The test additions guard already-correct behavior. This is a days-not-weeks hardening pass,
  not a redesign — so "defer the whole feature to v0.18" buys little and costs the §2 deliverable.
- **The deep items genuinely are v0.18.** Per-agent identity, mTLS, rotation, and rate limiting are
  real work with design questions of their own and no demonstrated v0.17 demand (stdio covers the
  default local-agent case). Deferring *those* is right; deferring the whole transport is not.

Net: v0.17 ships `--http` as an **experimental remote transport** with the fail-closed guarantees it
already has plus H-1..H-4; v0.18 graduates it to stable once H-5..H-8 and field feedback land. This
is a Tier-2 surface (transport/command), but because it can carry Tier-1 `apply` calls, the
`--allow-mutations` + live-apply gates and the human-sign-off discipline on those data-path
operations are unchanged and remain the real protection for writes.

## Adversarial self-review

- **SR-1 (found a real bug): the help text lies about co-serving stdio.** `--http`'s usage string
  (`mcp.go:54`) and `Docs.Long` (`mcp.go:90-92`) both say HTTP is served "in addition to stdio," but
  `Execute`'s own comment and code (`mcp.go:176-181`) deliberately serve **HTTP only** in `--http`
  mode (a daemon has no stdin; co-running stdio would EOF immediately and could tear down the
  errgroup). The behavior is *right*; the docs are *wrong*. Captured as H-1/AC-11 — must fix before
  the cut. This is the kind of mismatch that makes an operator think a channel is available when it
  isn't.
- **SR-2: is the fail-closed gate truly before any listener binds?** Yes — `Execute` calls
  `newHTTPServer` (which calls `validateHTTPConfig`, `loadBearerToken`, `LoadX509KeyPair`) and returns
  the error *before* the errgroup that calls `ListenAndServeTLS` (`mcp.go:171-197`). A misconfigured
  server never opens a socket. Verified by `TestNewHTTPServer_RefusesWithoutTokenOrTLS`.
- **SR-3: can an agent flip any gate over the wire?** No. Token, TLS, `--allow-mutations`, and
  `--api.allow-live-restart-apply` are all process flags read at startup; none has a corresponding
  request/tool field. Confirmed against `config.go` and the tool signatures — same invariant as
  live-restart-apply, which is the pattern being mirrored.
- **SR-4: does the length short-circuit in `constantTimeEqual` leak the token?** It leaks the token's
  *length* via timing, not its content. Documented and deliberate (`http.go:156-165`). Against a
  high-entropy token this is negligible; flagged in the threat model as a residual, not a blocker.
- **SR-5: is enumeration really authenticated, or does the SDK expose an unauthenticated
  metadata/health route?** The bearer middleware wraps the single `NewStreamableHTTPHandler`, and the
  `--http` server mounts nothing else — there is no separate health/metadata listener on that port
  (unlike the main API, which has `health_server.go`, but that is a different, unauthenticated
  surface not served here). AC-5 makes this an explicit test rather than an implicit property.
- **SR-6: did I claim any gate is live that isn't?** No. Per CLAUDE.md's process-maturity honesty
  rule: the fail-closed/auth/TLS behavior *is* implemented and tested today; the audit logging, rate
  limiting, idle timeout, non-loopback warning, and several ACs are **gaps I am flagging as
  not-yet-present**, not claiming as done. This doc is a hardening plan precisely because those are
  missing.
- **SR-7: is "reuse the API's TLS" satisfied?** It cannot be — the API has no TLS/auth
  (`config.go:73-97`, `runtime.go:781`). I verified this rather than assuming a shared stack exists.
  The reused pattern is the process-level non-agent-passable *gate* (live-restart-apply), which is the
  correct thing to mirror; the TLS/bearer plumbing is necessarily new (stdlib), and the API is a
  future *consumer* of this pattern, not its source.

## Related

- Execution plan §2 — HTTP transport security line:
  [`20260704-phase-1-execution-plan.md`](./20260704-phase-1-execution-plan.md).
- Umbrella MCP design (this doc expands its §5 / AC-8):
  [`20260708-mcp-server.md`](./20260708-mcp-server.md).
- The non-agent-passable process-gate pattern being mirrored:
  [`20260708-live-server-deploy-apply.md`](./20260708-live-server-deploy-apply.md),
  [`docs/operations/live-restart-apply.md`](../operations/live-restart-apply.md).
- As-built code: `cmd/conduit/root/mcp/http.go`, `cmd/conduit/root/mcp/mcp.go`, tests in
  `cmd/conduit/root/mcp/http_test.go`; landed in `46bbf7c` (#2591).
- Operator docs: [`docs/operations/mcp-server.md`](../operations/mcp-server.md) (§"HTTP transport").
- SDK: `github.com/modelcontextprotocol/go-sdk v1.6.1` (`go.mod:38`);
  `mcp.NewStreamableHTTPHandler` (`mcp/streamable.go:194`), legacy `mcp.NewSSEHandler`
  (`mcp/sse.go:83`, not used).
