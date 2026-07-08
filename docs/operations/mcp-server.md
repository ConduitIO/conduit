# `conduit mcp`: the agent-native MCP server

`conduit mcp` exposes Conduit's operations to AI agents as [MCP](https://modelcontextprotocol.io)
tools. Every tool is a thin wrapper over the exact same engine the matching CLI verb calls — see
[`docs/design-documents/20260708-mcp-server.md`](../design-documents/20260708-mcp-server.md) for
the design rationale.

## Quick start

```console
$ conduit mcp
```

Starts the MCP server on stdio — the primary channel for an agent that spawns Conduit as a
subprocess (e.g. Claude Desktop, Claude Code, or any MCP-compatible client configured with a
`command`/`args` server entry). No authentication is needed on stdio: the agent that spawned the
process already owns it.

Point the `inspect` tool at a running Conduit:

```console
$ conduit mcp --api-address localhost:8084
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
$ conduit mcp --allow-mutations
```

This is a startup/process-level flag. There is no corresponding tool argument: an agent cannot
enable mutations by passing a parameter, only the human who launched `conduit mcp` controls it.

`apply` is additionally diff-first and token-bound: an agent must call `deploy` first (which
returns a `Diff` and a `hash`), then call `apply` with that exact `hash`. A stale or mismatched
hash is refused (`provisioning.plan_stale`) with nothing mutated. `apply` also inherits the
standalone deploy/apply engine's Badger-only structural gate (see
[`cmd/conduit/internal/deploy`](../../cmd/conduit/internal/deploy)): it can only run against a
stopped BadgerDB store, so it structurally cannot mutate a pipeline a live `conduit run` process
has open.

## HTTP transport

`--http <addr>` additionally serves the streamable-HTTP transport (in addition to stdio, not
instead of it) — for a remote agent or a shared Conduit instance multiple agents connect to over
the network.

HTTP refuses to start without **both**:

- `--token-file <path>`: a file containing a bearer token. Every request's `Authorization: Bearer
  <token>` header is compared against it in constant time (`crypto/subtle.ConstantTimeCompare`).
- `--tls-cert <path>` / `--tls-key <path>`: a TLS certificate/key pair. HTTP is only ever served
  over TLS — there is no plaintext HTTP path.

```console
$ conduit mcp --http :8443 \
    --token-file /etc/conduit/mcp-token \
    --tls-cert /etc/conduit/tls/cert.pem \
    --tls-key /etc/conduit/tls/key.pem
```

A request with a missing or incorrect bearer token is rejected with `401 Unauthorized` before it
ever reaches the MCP protocol handler.

## Known limitations (Wave 3)

- **`repair` tool**: not built — the CLI `repair` verb doesn't exist yet. It will ship as an MCP
  tool in the same PR as the CLI command (same-engine rule).
- **`llms.txt` tool-catalog generation**: the full multi-source `llms.txt` generator (config
  schema, connector list, error registry, and this tool catalog) is deferred; the tool catalog
  itself is stable and documented here in the interim.
- **North-star E2E** (a scripted agent going zero → running pipeline using only MCP + `llms.txt`):
  deferred pending the `llms.txt` generator. Today, an agent can reach a *valid, provisioned*
  pipeline via `dry_run` → `deploy` → `apply`; running it requires a `conduit run` over the
  applied store until the live-server RPC path (tracked follow-up, referenced in the design doc as
  #2588) lands, at which point `apply` gains apply-to-a-running-server for the CLI and MCP
  simultaneously.
