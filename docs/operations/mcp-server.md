# `conduit mcp`: the agent-native MCP server

`conduit mcp` exposes Conduit's operations to AI agents as [MCP](https://modelcontextprotocol.io)
tools. Every tool is a thin wrapper over the exact same engine the matching CLI verb calls — see
[`docs/design-documents/20260708-mcp-server.md`](../design-documents/20260708-mcp-server.md) for
the design rationale.

## Quick start

```console
conduit mcp
```

Starts the MCP server on stdio — the primary channel for an agent that spawns Conduit as a
subprocess (e.g. Claude Desktop, Claude Code, or any MCP-compatible client configured with a
`command`/`args` server entry). No authentication is needed on stdio: the agent that spawned the
process already owns it.

Point the `inspect` tool at a running Conduit:

```console
conduit mcp --api-address localhost:8084
```

## Tool catalog

| Tool | Mutates | Requires `--allow-mutations` | Same engine as |
| --- | --- | --- | --- |
| `validate` | no | no | `conduit pipelines validate` |
| `lint` | no | no | `conduit pipelines lint` |
| `dry_run` | no | no | `conduit pipelines dry-run` |
| `doctor` | no | no | `conduit doctor` |
| `deploy` | no (preview only) | no | `conduit pipelines deploy` |
| `inspect` | no | no | `conduit pipelines inspect` (requires `--api-address`) |
| `apply` | yes | **yes** | `conduit pipelines apply` |
| `scaffold_connector` | yes (filesystem) | **yes** | `conduit connector new` |
| `scaffold_processor` | yes (filesystem) | **yes** | `conduit processor new` |

`validate`/`lint`/`dry_run`/`deploy`/`apply` take pipeline configuration as a `config` string
(inline YAML content) — never a server-side file path. An agent naturally has content, not a path
on the machine `conduit mcp` runs on; accepting a path would let an agent read/validate arbitrary
files on that host.

Every tool result is a structured envelope: `{ok, summary, result, error}`. On a domain failure,
`error` carries `{code, message, suggestion, fix}` — the same machine-actionable fields the CLI's
`--json` output and error registry expose.

## `--allow-mutations`: the write-tool gate

`apply`, `scaffold_connector`, and `scaffold_processor` mutate the local pipeline store or
filesystem. They are **absent from the tool catalog** — not merely present-and-erroring — unless
the operator starts the server with `--allow-mutations`:

```console
conduit mcp --allow-mutations
```

This is a startup/process-level flag. There is no corresponding tool argument: an agent cannot
enable mutations by passing a parameter, only the human who launched `conduit mcp` controls it.

`apply` is additionally diff-first and token-bound: an agent must call `deploy` first (which
returns a `Diff` and a `hash`), then call `apply` with that exact `hash`. A stale or mismatched
hash is refused (`provisioning.plan_stale`) with nothing mutated.

`deploy`/`apply` prefer a live Conduit server: if one is reachable at the configured
`api.grpc.address`, they call its `PlanPipeline`/`ApplyPipeline` RPCs — which means `apply` **can**
mutate a genuinely running pipeline via a graceful stop-drain-restart (see
[`docs/operations/live-restart-apply.md`](live-restart-apply.md) for the separate, server-side
operator gate that requires). Without a reachable server, they fall back to the standalone engine
(see [`cmd/conduit/internal/deploy`](../../cmd/conduit/internal/deploy)), which keeps its
Badger-only structural gate and refuses outright to touch a running pipeline. Either way,
`--allow-mutations` above is the orthogonal gate deciding whether the `apply` tool is registered at
all; it says nothing about which transport a registered `apply` tool uses.

## HTTP transport (EXPERIMENTAL)

`--http <addr>` serves the streamable-HTTP transport **instead of** stdio — this is a
network-daemon mode (systemd/container) with no attached stdin, so it does not also serve stdio.
Use it for a remote agent, or a shared Conduit instance multiple agents connect to over the
network.

See [`docs/design-documents/20260712-mcp-http-transport.md`](../design-documents/20260712-mcp-http-transport.md)
for the full threat model and hardening record. **This transport is experimental**: the
fail-closed/auth/TLS fundamentals below are solid, but it is a single shared token with no
rotation short of a restart, no per-agent identity, and no rate limiting — see "Not yet built"
below before exposing it beyond a trusted network.

HTTP refuses to start without **both**:

- `--token-file <path>`: a file containing a bearer token. Every request's `Authorization: Bearer
  <token>` header is compared against it in constant time (`crypto/subtle.ConstantTimeCompare`).
  The token is a **file path**, never an inline flag value — it never lands in `ps`/argv/shell
  history. An empty (or whitespace-only) token file is refused at startup.
- `--tls-cert <path>` / `--tls-key <path>`: a TLS certificate/key pair. HTTP is only ever served
  over TLS (`MinVersion: TLS1.2`) — there is no plaintext HTTP path.

```console
$ conduit mcp --http :8443 \
    --token-file /etc/conduit/mcp-token \
    --tls-cert /etc/conduit/tls/cert.pem \
    --tls-key /etc/conduit/tls/key.pem
```

A request with a missing or incorrect bearer token is rejected with `401 Unauthorized` before it
ever reaches the MCP protocol handler — including `tools/list`, so an unauthenticated caller learns
nothing about the catalog, not even whether `--allow-mutations` is on.

### Operational behavior

- **Startup log line:** on success, a structured `info` line names the bound address and auth mode
  (`serving MCP over streamable HTTP`).
- **Non-loopback bind warning:** if `--http` resolves to an address that is not restricted to
  loopback (e.g. `:8443`, `0.0.0.0:8443`, or a public hostname), a `warn`-level log line calls out
  the exposure at startup. TLS + the bearer token already make this safe; the warning exists so an
  operator who meant `--http localhost:8443` notices if they typed (or defaulted to) something
  wider.
- **Auth-failure logging:** every rejected request logs `method`, `path`, `remote_addr`, and
  `outcome=unauthorized` at `warn` level — so brute-force/probing attempts against the token are
  observable. **The token itself, and whatever credential the caller presented, are never logged**
  — only request metadata.
- **Timeouts:** `ReadHeaderTimeout: 10s` (Slowloris guard) and `IdleTimeout: 120s` (bounds an idle
  keep-alive connection). No blanket `WriteTimeout` — streamable HTTP can legitimately hold a
  response open while streaming a tool result.
- **Body cap:** requests are capped at 4 MiB (`http.MaxBytesReader`); an oversized body fails the
  read instead of being buffered in full.
- **Graceful shutdown:** on `SIGTERM`/context cancellation, in-flight requests drain via
  `http.Server.Shutdown` within a 5s window before the process exits.

### Not yet built (v0.18 candidates)

- Rate limiting / lockout on repeated auth failures.
- Per-agent tokens with revocation; optional mTLS (client-cert) auth.
- Token hot-reload / rotation without a restart.
- Cipher-suite pinning and a TLS 1.3 floor.

Rotating the shared token today means restarting `conduit mcp` with a new `--token-file`.

## Known limitations (Wave 3)

- **`repair` tool**: not built — the CLI `repair` verb doesn't exist yet. It will ship as an MCP
  tool in the same PR as the CLI command (same-engine rule).
- **`llms.txt` tool-catalog generation**: the full multi-source `llms.txt` generator (config
  schema, connector list, error registry, and this tool catalog) is deferred; the tool catalog
  itself is stable and documented here in the interim.
- **North-star E2E** (a scripted agent going zero → running pipeline using only MCP + `llms.txt`):
  deferred pending the `llms.txt` generator. The live-server RPC path (#2588) has landed — `apply`
  now reaches a genuinely running pipeline when a server is reachable, gated by the server's
  `--api.allow-live-restart-apply` operator flag (see
  [`docs/operations/live-restart-apply.md`](live-restart-apply.md)) for restart-class changes — so
  what remains for the north-star E2E is the `llms.txt` generator itself, not the apply-to-running
  capability.
