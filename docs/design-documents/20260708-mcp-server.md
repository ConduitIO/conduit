# `conduit mcp` — the agent-native MCP server

## Summary

`conduit mcp` exposes Conduit's operations to AI agents as MCP tools that are
**1:1 with the CLI verbs** and call the **exact same cobra-free engines** — no
divergent logic (the three-faces rule: human CLI, agent MCP, embedder library are
renderings of one operation set). It is the marquee v0.17 "agent-native"
deliverable (§2) and the north-star: an agent goes zero→running pipeline using
only MCP + `llms.txt`. Tier 2 for the read tools; the write tools (`apply`,
`scaffold`) inherit the tier of the operation they wrap, and are gated behind an
**operator-set** `--allow-mutations` flag that is not agent-passable.

## Context — everything the tools wrap already exists, cobra-free

Wave 1–2 deliberately built every engine decoupled from cobra so MCP wraps them
1:1:

| Tool | Engine (same call the CLI verb makes) | Side effects |
| --- | --- | --- |
| `validate` | `validate.Run(ctx, path)` | none (offline) |
| `lint` | `validate.RunWithOptions(ctx, path, {Warnings})` | none |
| `dry_run` | `validate.RunWithOptions(ctx, path, {Enriched, ResolvePlugins})` | none |
| `doctor` | `check.Run(ctx, checks)` + doctor's check set | none |
| `inspect` | API `GetPipeline`/`ListConnectors`/`GetDLQ` (needs a running server) | none |
| `deploy` | `provisioning.Service.Plan(ctx, desired)` → `Diff` + hash | **none** (preview only) |
| `apply` | `provisioning.Service.ApplyPlan(ctx, desired, hash)` | **mutates** |
| `scaffold_connector` / `scaffold_processor` | `scaffold.Generate(ctx, req)` | **writes files** |

All already return the §1.1 structured shapes (`validate.Report`, `check.Report`,
`provisioning.Diff`, `scaffold.Result`) with `conduiterr` codes — so the MCP
result mapping is a serialization, not new logic.

**One real adapter concern (path vs content) — decided.** The
validate/lint/dry-run/deploy engines take a filesystem _path_, but an MCP agent
naturally passes config _content_ (inline YAML), not a path on the server host.
The MCP tools take a `config` (content) argument and the adapter writes it to a
**private temp file** (0600, in an os.MkdirTemp dir, removed after the call) for
the path-based engine — done uniformly for all four offline tools rather than
forking each engine's signature (the review's recommendation: less surface, no
correctness gain from a content entry). The tools **never** accept a server path
from an agent — that would let an agent read/validate arbitrary host files.
Content-in is the only safe interface.

`repair` (§3) is not built yet — it is out of scope for this doc; add its MCP tool
when the CLI verb lands (same-engine rule).

## Decision

### 1. Command + package

- `conduit mcp [--http <addr>] [--allow-mutations] [--token-file <path>]` — a new
  ecdysis command under root. Default transport **stdio** (the primary agent
  channel); `--http <addr>` additionally serves the HTTP/SSE transport.
- Engine in `cmd/conduit/internal/mcp/` (a thin adapter layer): a `Server` that
  registers tools, each tool a small handler that (a) decodes args, (b) calls the
  shared engine, (c) maps the result/error to the MCP structured result. No
  business logic lives here — it is a transport shell over the Wave 1–2 engines.

### 2. Library (new dependency — justified)

Implementing the MCP JSON-RPC/tool protocol by hand is out of scope and
error-prone. Use the **official `github.com/modelcontextprotocol/go-sdk`**
(Anthropic-maintained, pin the current GA **v1.6.1** — v1.7.0 is not yet
released; requires go 1.25.0, which the repo already uses). This is the one
genuinely new dependency; everything else is reuse. Justified in the
implementation PR per the no-new-deps rule.

Confirmed SDK API the implementation uses (verified against v1.6.1
`github.com/modelcontextprotocol/go-sdk/mcp`):

- Transports: `mcp.StdioTransport{}` via `Server.Run(ctx, t)`; HTTP via
  `mcp.NewStreamableHTTPHandler(getServer, opts)` returning an `http.Handler` —
  both bind the same `*Server`, so stdio + `--http` serve one tool catalog.
- Tools: `mcp.AddTool[In, Out any](s, *Tool, handler)` auto-derives the input
  JSON schema (2020-12) from the `In` struct's `json`/`jsonschema` tags and
  validates before the handler runs.
- Results: `CallToolResult{Content, StructuredContent, IsError}` — engine result
  → `StructuredContent`; a `conduiterr.ConduitError` → `Content` text +
  `IsError:true`.
- Conditional registration: `Server.AddTool` is a plain pre-`Run` method, so the
  `--allow-mutations` gate is just an `if` around the write-tool `AddTool` calls —
  no runtime tool-hiding API needed.

### 2a. How MCP tools reach their target — same engines, same constraints

`conduit mcp` is a **standalone command that wraps the exact same engines the CLI
verbs call** — the three-faces rule taken literally, so an MCP tool and its CLI
verb can never diverge:

- **Offline tools** (`validate`, `lint`, `dry_run`, `doctor`, `scaffold_*`) run
  their engine locally — no server, no network.
- **`inspect`** dials a running Conduit's API (the same `GetPipeline`/
  `ListConnectors`/`GetDLQ` the CLI `inspect` uses) — it needs a running server,
  as its CLI peer does.
- **`deploy`/`apply`** use the same standalone provisioning path the CLI verbs use
  (`provisioning.Service.Plan`/`ApplyPlan` via `NewLocalService`), and therefore
  **inherit the identical Wave-2 constraint**: default-deny to a BadgerDB store,
  refusing when a live server holds the store (fail-closed). The API today exposes
  only CRUD (`CreatePipeline`/`UpdatePipeline`), no Plan/Apply endpoint.

**The consequence for the north-star (stated honestly):** because MCP wraps the
same engine, when #2588 (the live-server RPC / API Plan-Apply path) lands, the MCP
`apply` tool gains apply-to-a-running-server **for free**, simultaneously with the
CLI verb — MCP does not need its own server-connection design. Until then, the
full in-session "agent applies and the pipeline is immediately running in the same
process" requires either #2588 or an operator `conduit run` over the applied
Badger store. Wave 3 delivers the complete agent-facing harness + tool catalog
over today's engines; the live-apply upgrade rides on #2588 for CLI and MCP
together. This is the same reasoning that made deploy/apply safe to ship in
Wave 2.

### 3. Read/write separation — the safety gate is real, not theater

- **Read tools** (`validate`, `lint`, `dry_run`, `inspect`, `doctor`, `deploy`)
  are always registered. `deploy` is read-only: it returns the `Diff` + hash and
  performs no mutation.
- **Write tools** (`apply`, `scaffold_connector`, `scaffold_processor`) are
  registered **only if the operator started the server with `--allow-mutations`.**
  This flag is a **startup/process** flag set by the human operator, NOT a tool
  argument — an agent cannot enable mutations by passing a parameter (§2: "else
  the safety gate is theater"). When mutations are disabled, the write tools are
  absent from the catalog (not merely erroring), so an agent's tool discovery
  reflects what it can actually do.
- **`apply` is diff-first + token-bound:** an agent must call `deploy` (get the
  `Diff` + `hash`), then `apply` with that `hash`. `ApplyPlan` refuses a stale
  hash (`provisioning.plan_stale`). So mutation is always an explicit,
  plan-authorized second call — never a one-shot.
- **Data-path human sign-off is enforced structurally, not by a flag.** The
  review correctly flagged that a "requires human review" _field_ in the result
  is theater for an autonomous agent that can just proceed. In Wave 3 the
  enforcement is real and structural: MCP `apply` inherits the Wave-2
  Badger-not-running gate — it can only apply to a **stopped** store (a live
  `conduit run` makes `OpenStore` fail closed), so **Wave-3 MCP `apply` cannot
  mutate a running pipeline at all.** There is no live data-path change to
  sign off on. The `Diff`'s per-change `Effect` (restart/in_place) is surfaced to
  the agent as _information_ ("applying this will require a restart"), not as an
  honor-system gate. When #2588 adds apply-to-a-running-server, that new path is
  where the enforced human-sign-off gate must live (apply-to-running of a
  data-path diff hard-refuses at the tool layer without an explicit operator
  authorization) — it lands **with** #2588, not as an unenforced promise now.
  Same for the future `repair` tool.

### 4. Structured results (§1.1)

Every tool returns the shared result shape: the engine's report/diff/result as
structured JSON content, plus, on failure, an error carrying `code` +
`suggestion` + structured `fix` (mapped from `conduiterr.ConduitError`). This is
the same envelope the CLI `--json` emits, so an agent sees identical semantics
across MCP and CLI. Tool input schemas are declared (JSON Schema) so agents get
typed arguments.

### 5. Transport security (HTTP)

stdio needs no auth (the agent owns the process). The optional HTTP transport
requires **a bearer token** (`--token-file`, compared constant-time) **and TLS**
(operator-provided cert, or refuse to serve HTTP without one). Documented in
`docs/operations/`. The default (stdio) is the secure-by-default path.

### 6. `llms.txt`

`llms.txt` / `llms-full.txt` are regenerated in CI from source (config schema,
connector list, error registry, and **this MCP tool catalog**) — never
hand-maintained (§2). Wave 3 ships the generator hook for the MCP tool catalog
section; the full multi-source generator can land incrementally. The north-star
test (below) depends on `llms.txt` being accurate, so the tool catalog it lists
must be generated from the registered tools, not duplicated by hand.

## Failure modes

- **Mutation via parameter:** an agent tries to pass `allow_mutations: true` — it
  is not a tool arg; write tools simply aren't in the catalog. Tested.
- **Stale/forged apply hash:** `ApplyPlan` recomputes + compares; stale → refused,
  no mutation.
- **`inspect`/`apply` with no server / unreachable:** the underlying engine's
  error (Unavailable → env) maps to a structured tool error, not a hang.
- **Long-running scaffold (network tool install):** bounded/timed; a failure
  returns the structured `scaffold` error, never a partial half-written dir
  (the engine already writes-to-temp-then-renames).
- **Concurrent tool calls:** read tools are stateless; `apply` takes the
  provisioning path's per-pipeline serialization (as the CLI does).

## Acceptance criteria

1. `conduit mcp` starts a stdio MCP server; an MCP client lists the read tools
   (validate/lint/dry_run/inspect/doctor/deploy) and can call each, getting the
   §1.1 structured result identical to the CLI `--json`.
2. **Write tools absent without `--allow-mutations`:** with the flag off, tool
   discovery does NOT list `apply`/`scaffold_*`; with it on, they appear.
3. An agent cannot enable mutation by any tool argument (no `allow_mutations`
   parameter exists) — asserted.
4. `deploy` tool returns a `Diff` + hash and mutates nothing (store unchanged).
5. `apply` tool with a matching hash mutates; with a stale hash → `plan_stale`,
   no mutation.
6. Every tool error carries `code` + `suggestion` (mapped from `conduiterr`).
7. `doctor` MCP tool == the CLI `doctor` result (same `check.Run`), proving the
   shared-engine rule.
8. HTTP transport refuses to serve without a token + TLS; with both, authenticates
   a bearer token (constant-time), rejects a bad one.
9. **North-star (the headline):** a scripted agent session, given only the MCP
   tools + `llms.txt`, produces a valid running pipeline from zero: `scaffold`
   or author a builtin-connector config → `dry_run` (passes) → `deploy` (diff) →
   `apply` (with the hash) → the config is provisioned. Because Wave-3 `apply`
   uses the standalone Badger path (see §2a), "the pipeline is _running_" is
   completed by a `conduit run` over the applied store (or, once #2588 lands,
   in-session via the live-server path — same engine). The gate for Wave-3 "done"
   is: the agent reaches a **valid, provisioned** pipeline using only MCP +
   `llms.txt`; the fully-in-session running upgrade is #2588.
10. The MCP tool catalog section of `llms.txt` is generated from the registered
    tools (not hand-maintained) — a test asserts they match.

## Risk tier & scope

Read tools: Tier 2. `apply`/`scaffold` write tools: inherit the wrapped
operation's tier (apply is Tier-1-adjacent — hence `--allow-mutations` +
diff-first + the human-sign-off flag on data-path diffs). Wave 3 delivers: the
`conduit mcp` command, stdio transport, the read + (gated) write tool catalog over
the existing engines, structured results, HTTP+token+TLS, and the llms.txt
tool-catalog hook. Deferred: `repair` tool (until the CLI verb exists), the full
multi-source llms.txt generator, `conduit generate` (v0.19).

## Related

- Execution plan §2 (agent-native), §3 (the CLI verbs these mirror).
- `20260707-cli-output-conventions.md` (the §1.1 result shape reused verbatim).
- The wrapped engines: `cmd/conduit/internal/validate`, `pkg/conduit/check`,
  `pkg/scaffold`, `pkg/provisioning` (`Plan`/`ApplyPlan`), the API client.
- North-star also referenced in CLAUDE.md's MCP session workflow.
