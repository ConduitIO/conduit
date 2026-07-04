# Conduit Phase 1 — Developer Experience Core: Execution Plan (v2)

_Goal: the best first-hour and first-week of any data-integration tool — for humans and for their agents._

> This plan was revised against two independent reviews (technical accuracy/optimization and
> UX cohesion), both grounded in the actual codebase. The load-bearing conclusions: SIGTERM drain
> is not a "cheap" feature — it's a live invariant-7 gap in v0.15.0 today, shipped as the **v0.15.1**
> patch below; the structured-error work is a CLI-wide refactor with a Go-error-vs-proto
> dual-representation problem, not a CLI-scoped add; and the UI rebuild is greenfield (no UI in the repo).
> The per-feature designs (SIGTERM drain, `ConduitError`, hot-reload, WASM connectors, partition
> claims) each get their own ADR/design doc as they're built.

Phase 1 is ~4 months, sequenced into four monthly releases (v0.16 → v0.19), each independently shippable.

---

## 0. Prioritization & release sequencing

Three tests per item: **foundation-first** (does other work depend on it?), **wow-per-effort**, and the
**three-faces rule** (human CLI/UI, agent MCP, embedder library are renderings of one operation set —
anything not cohesive across all three isn't done). Tiers: **P0** foundational · **P1** core
differentiator · **P2** bet (can slip to Phase 2 without breaking the first-hour promise).

| Release | Theme | Headline |
| --- | --- | --- |
| **v0.15.1** | Correctness patch | **SIGTERM graceful drain** (invariant-7 fix, §0.1) |
| **v0.16** | Foundations & the 5-min wow | Structured I/O + `ConduitError` (P0, re-scoped), `init` reconciliation (P0), 12-factor essentials (P0) |
| **v0.17** | Agent-native & CLI-as-product | MCP server (P1), validate/lint/dry-run (P1), doctor (P1), **CLI verb parity** (P1), Go+Python scaffolding (P1) |
| **v0.18** | UI & extensibility | **Greenfield React UI** (P1) + connector registry MVP (P2) — _templates & Rust/TS moved out_ |
| **v0.19** | WASM, generation & embedding | WASM connectors (P2, go/no-go gate), `conduit generate` (P2), embedded v1 (P2), templates gallery, Rust/TS scaffolding, partition-claims RFC |

**v0.18 de-loaded:** v1 crammed the greenfield UI (the single largest item) with registry +
templates + Rust/TS in one solo month. Templates gallery and Rust/TS scaffolding move to v0.19.

---

## 0.1 SIGTERM graceful drain — pull out first, treat as a bug

**This is a live correctness gap in v0.15.0, not a v0.16 feature.** `pkg/conduit/entrypoint.go:70`
registers only `os.Interrupt` (SIGINT). Raw **SIGTERM** — what `docker stop`, `kubectl delete pod`,
and `systemctl stop` send — gets Go's default disposition: immediate death, **zero drain**. A second
SIGINT calls a bare `os.Exit` (`entrypoint.go:77-78`) that bypasses graceful stop. And `ForceStop`
escalation (`runtime.go:499-519`) force-stops after a fixed fraction of `exitTimeout` without
verifying no un-acked record was already forwarded downstream (invariant-1 adjacency).

**Severity, honestly:** with at-least-once (inv. 3) + crash-safe positions (inv. 2) holding, an
un-drained SIGTERM behaves like a crash — recoverable on restart, **not silent data loss** — but it
violates documented **invariant 7** ("SIGTERM drains in-flight and checkpoints before exit"), causes
duplicate storms and unclean checkpoints on every K8s pod recycle, and undermines the entire
container/12-factor story Phase 1 is selling.

**Plan:**

- Design doc first (CLAUDE.md requires it for data-path work): signal set, drain sequence, grace

  deadline, force-stop-respects-checkpoint semantics.

- Register SIGTERM; make the drain path the default; remove the second-signal `os.Exit` bypass; fix

  V2 force-stop to escalate only after checkpoint or hard deadline, never mid-ack.

- Build the first **chaos test** (none exists today): SIGTERM/SIGKILL at random points under load →

  assert no double-delivery beyond at-least-once, no lost/torn checkpoint. Seeds `tests/chaos`.

- **Decided: ship as v0.15.1** (out-of-band patch). It's small, it's a documented-invariant

  violation, it's pre-existing (not a v0.15.0 regression), and every container deployment hits it —
  patching now signals K8s-readiness rather than waiting a month for v0.16.

---

## 1. Cross-cutting foundation (P0, v0.16)

### 1.1 Structured output & `ConduitError` — re-scoped

**Reality check (from the technical review):** `cerrors` is a thin forwarding layer with no code/type
(`cerrors.go:23`); there are **~720 `cerrors.Errorf`/`New` + ~94 `fmt.Errorf` sites**; the HTTP API
already emits an OpenAPI-documented `google.rpc.Status` error shape (`grpcutil/gateway.go`,
`http/api/status/status.go`) — so this is a change to a **versioned public API contract** needing a
compat plan, and CLI commands **interleave logic and output** today (e.g. `pipelines/list.go:62-68`,
`initialize/init.go:139-150`, `run/run.go` has no result object). "One shape, four renderings" has a
real architectural crux the v1 plan missed.

**The crux — dual representation:** `ConduitError` must be **both** a native Go `error` (for
library/embedder callers, and `Import` which works on Go types — `import.go:33`) **and** a structured
proto/`status.Status` payload (for CLI-via-client, MCP, UI). These are different representations;
design them together or the "one error" claim silently breaks at the embedding boundary.

**Acceptance criteria:**

- A `ConduitError` type: `{ code, message, configPath?, suggestion?, fix?, docsUrl? }` where `fix` is

  **structured** `{configPath, op, value}` so MCP `repair` and CLI `repair`
  are thin appliers of the _same_ data, not divergent capabilities. Native-Go and proto encodings of
  it are defined together with a documented mapping.

- Extract a `Result` type from each of the ~20 leaf CLI commands so human/JSON output share one path.

  **Investigate the framework route first:** `ecdysis` already has a dead `json`
  flag hook (`ecdysis@v0.4.2/decorators.go:434`) — if the `Command` interface can carry a generic
  "return a Result, render it" decorator, `--json` is an O(1) framework change, not O(n) per command.

- Stable error-code enum in one registry file; CI guard fails a user-facing error with no code

  (mirror the release-build guards added in v0.15).

- **Reuse, don't invent:** `configPath` from the YAML parser's existing

  line/column tracking (`config/yaml/linter.go:93-141`, currently log-only); `schemaVersion` from the
  YAML parser's proven version/per-version-model/changelog compat mechanism (`config/yaml/parser.go`).

- Documented deterministic exit codes (0 ok · 1 runtime · 2 config/validation · 3 environment).
- **Compat plan** for the public HTTP/gRPC error-shape change (announce → additive → migrate), per

  CLAUDE.md public-contract rules.

**Edge cases & mitigation:**

- Wrapped errors lose the code → error-boundary lint requiring `ConduitError` at user-facing returns.
- JSON schema drift breaks agent/UI → contract-test snapshots; `schemaVersion` bumps are deliberate.
- Secrets in errors/logs → redaction pass keyed on connector param sensitivity; test with a secret fixture.
- **Fuzz the JSON error encode/decode + any new schema boundary** (CLAUDE.md: fuzz every parser boundary).

### 1.2 `init` reconciliation + the 5-minute wow

**Reality:** `conduit init` (`initialize/init.go`) writes `conduit.yaml` + dirs and does **not** run a
pipeline — its own help says "run `conduit pipelines init`." `conduit pipelines init`
(`pipelines/init.go`) is the one that scaffolds a runnable pipeline. v1 conflated them.

**Acceptance criteria:**

- **Decided:** `conduit init` becomes the single 5-min-wow entry that scaffolds a _running_

  generator→file pipeline. Project scaffolding moves under `conduit init --project`; the current
  `conduit pipelines init` is aliased to `conduit init` and deprecated (warn) for one release, then
  removed. One obvious entry point, no "run this other command" hand-off.

- `conduit init` → `conduit run` shows records flowing in < 60s on a clean machine, zero required flags.
- Generated config is commented, valid, passes `conduit pipeline validate`.
- **Bundled templates use the registry template-manifest schema from day one**,

  even though the registry (v0.18) isn't live — so v0.16 templates aren't rewritten later.

**Edge cases:** port/file collisions → free defaults, never overwrite without `--force`; non-empty dir
→ confirm + `--dry-run`; template needs an uninstalled connector → error points at `connectors install`
(bundle built-ins pre-registry).

### 1.3 12-factor essentials (the genuinely cheap ones)

**Acceptance criteria:**

- `/healthz` (liveness) + `/readyz` (readiness: DB open, pipelines loaded) → documented JSON; Prometheus

  endpoint (reuse the metrics registry from #2268). `readyz` = "engine can serve," degraded pipelines
  reported in body, not as not-ready.

- Every engine setting env-configurable; documented precedence (flag > env > file); zero-config `run`.
- `conduit run --pipelines <dir>` — **already exists as `--pipelines.path`**; this is a

  rename/alias, size accordingly.

---

## 2. Agent-native (P1, v0.17)

**Wider dependency than "§2.1 only":** `deploy_pipeline`'s diff-first needs a **preview mode over
provisioning's `actionsBuilder.Build`** (`import.go:113`) that doesn't exist — `Import` only executes.
`repair` needs fix-synthesis. And MCP can't cleanly wrap CLI until §1.1's logic/output separation lands.
So MCP rides the foundation but inherits its refactor.

**Acceptance criteria:**

- `conduit mcp` (stdio + optional HTTP) exposing tools that are **1:1 with CLI verbs** (see §3): a diff

  returned by `deploy` includes the **hot-swappable-vs-restart classification** so an agent knows a
  change will drop the pipeline before applying.

- Every tool returns the §1.1 structured result; errors carry `code` + `suggestion` + structured `fix`.
- **Rename MCP `diagnose` → `doctor`** to match the CLI verb (v1 violated its own shared-vocab rule).
- Read tools vs write tools separated; **`--allow-mutations` is an operator-set startup flag, not

  agent-passable** — else the safety gate is theater. `deploy`/`repair` are
  diff-first (plan returned; apply is a second, explicitly-authorized call).

- **HTTP transport security:** authn (token) + TLS for the HTTP MCP surface; documented.
- **Agent `repair` touching ack/position/checkpoint-adjacent config still requires the human Tier-1

  sign-off path** — diff-first is not a substitute for review on data-path changes.

- `llms.txt` + `/llms-full.txt` regenerated in CI from source (config schema, connector list, error

  registry, MCP tool catalog) — never hand-maintained.

`conduit generate "<nl>"` → **v0.19** (needs MCP surface + templates + llms.txt as grounding). AC:
≥ 90% of a 25-request benchmark set produce a config that passes `validate`; every output is
`validate`-gated before display; unknown connector → closest-match + install suggestion, never a
fabricated plugin name.

---

## 3. CLI as product + verb parity (P1, v0.17)

The UX review found the CLI missing peers for operations MCP/UI have. **Close the gap so a bare-terminal
operator can do everything an agent can.** Add, with the same names the MCP tools use:

- `conduit pipeline validate | lint | dry-run` — static schema/refs/types; all `--json`, all §1.1 errors.
- `conduit pipeline inspect` — CLI peer of MCP `inspect_pipeline` + the UI per-stage

  view (live runtime/throughput). Distinct from the existing `describe` (static config) — state that
  both exist and what each does.

- `conduit pipeline deploy|apply <file>` — diff-first, apply-is-a-second-call, the

  same safety UX the MCP `deploy` tool gets. (Today only `run --pipelines <dir>` / dev hot-reload exist.)

- `conduit pipeline repair [--apply]` — thin applier of the structured `fix` field,

  sharing MCP `repair`'s engine so the human isn't a second-class citizen to the agent.

- `conduit pipeline start|stop|pause` — the status enum already has User/System-Stopped;

  give humans and agents verbs for the transitions that already exist.

- `conduit doctor` — env/config/plugin-compat diagnostics; pass/warn/fail with `suggestion`s.
- `conduit open ui` (or `run` prints the UI URL) — the literal CLI→UI bridge; today

  `open` only has `open docs` and the UI has no discoverable entry point.

- `conduit pipeline dev` + hot-reload — see §4 (its own subsystem, Tier-1-adjacent).
- **Pin the status-enum display table as a §1.1 AC:** the enum is **5 values**

  (`Running, SystemStopped, UserStopped, Degraded, Recovering`) — one table mapping each to CLI text,
  JSON string, and UI badge/color, so the UI can't collapse `System/UserStopped` into one "Stopped"
  while the CLI keeps them distinct.

---

## 4. Hot-reload / `conduit pipeline dev` (P1, v0.17) — from-scratch, Tier-1-adjacent

**Reality:** v1 claimed this "reuses the provisioning update-action classification" — **false.**
`import_actions.go` has only create/update/delete for startup diffing; `updatePipelineAction.Do`
(`:217-241`) calls `Update` with **no running-state check**, and there's **no** hot-swap-vs-restart
concept anywhere. `Init` runs exactly once (`runtime.go:880`); no file-watching exists. This is a new
subsystem: watch loop + config diff + **hot-swappable-vs-requires-restart classification** + partial
restart preserving position.

**It touches the data path** (a wrong swap of a source's position/offset → duplicate or skipped records,
invariant 2/3). **Gets the same Tier-1 review + chaos-test treatment as SIGTERM drain.** Design doc first.

**Edge cases:** essential-config change (source table/position semantics) → classified `requires-restart`,
refused/prompted with a clear reason, never silently hot-swapped; classification surfaced on the diff so
CLI/UI/agent all see "this will drop the pipeline" before apply. `dry-run` side effects → see §6 note.

---

## 5. Plugin scaffolding (P1: Go/Python v0.17 · Rust/TS v0.19)

- `conduit connector new --lang go|python|rust|ts`, `conduit processor new` → full repo: SDK wiring,

  passing tests, CI, release workflow, acceptance harness.

- **Target is a CI test, not a hope:** a scripted, timed run proves **first working custom connector

  < 30 min** per language on a clean machine.

- Generated repos pass their own CI on first commit. Nightly regenerate-and-test to catch SDK/protocol

  drift. `doctor`-style toolchain preflight in `new`. Template CI matrix includes Windows.

---

## 6. Greenfield React UI (P1, v0.18)

**Reality:** no `ui/`, no submodule, no `conduit-ui` in `go.mod` — only the `localhost:4200` CORS
fingerprint of the old external Ember UI. So "feature-parity with the old UI / flag-gate it for one
release" is not executable. **Decided:** this is a **greenfield React build in a new `conduit-ui`
repo**; the "old UI behind a flag" criterion is dropped. No parity obligation to a UI that isn't in
the tree — the bar is the acceptance criteria below, not matching the retired Ember app.

**Big de-risk:** the hard server half of "live record flow" **already exists** —
`InspectConnector` / `InspectProcessorIn/Out` streaming RPCs (`proto/api/v1/api.proto:617,853,860`),
grpc-gateway-forwarded, websocket-proxied (`grpcutil/websocket.go`, `runtime.go:713-717`), **unused by
any client today.** The UI consumes these, doesn't build new streaming primitives.

**Acceptance criteria:**

- **React**, consuming only documented APIs + the metrics/health endpoints; **TS API client generated

  from the served OpenAPI schema** (contract-tested) — provably just another API client (run it against
  a remote engine).

- Live record flow (over the existing Inspect streams), per-stage inspection, pipeline graph whose node

  states map 1:1 to the 5-value status enum.

- **Read/inspect-only in Phase 1 — state this explicitly:** the UI does not create/edit pipelines

  yet (CLI/MCP/library write; UI reads). It's a deliberate scoping line, not an omission.

- **One shared React component library / design tokens for the built-in UI AND the registry web UI

 ** — both are React, both land ~v0.18; commit them to one visual language now or
  ship "two different-looking React apps," defeating the cohesion goal.

- Errors render identically to the CLI (code/path/suggestion) **and are actionable** — one-click copy of

  the fixing command or trigger `repair` via the API.

- In-UI empty state teaches the CLI verb (`conduit init`, shown + copyable) instead of a blank screen.
- Accessibility: keyboard nav, WCAG AA contrast, **reduced-motion for the animated throughput view**,

  DOM-accessible (not canvas) record tables; works against a read-only engine.

- **Reconnect/loading spec** for a dropped WS/SSE mid-session.

**Edge cases:** render backpressure (server sampling + "sampled at N/sec") **and** surface _pipeline_
backpressure — lag/queue depth when a destination is slow, the operationally important signal, already
feeding `/readyz`; large graphs virtualized/collapsible.

---

## 7. Extensibility bets (P2)

**Connector registry MVP (v0.18):** `connectors install <name>` pulls a **signed** artifact, signature
verified before execution (refuse unsigned unless `--allow-unsigned`); GitHub-Action publishing + signing;
React web UI (shared component lib per §6) with search/verified-badges/stats. Registry records min
Conduit+protocol version; incompatible → clear refusal. Cache + documented offline install. **Signing
infra is a full surface, not a bolt-on — sized as its own workstream.**

**Templates gallery (v0.19):** ships with the registry; each template CI-tested
end-to-end (spin up infra, run, assert records land). Registry-UI templates need the identical
"not-installed → click to install" flow as CLI `init --template` — one flow, not a UI reinvention.

**WASM connectors (v0.19) — explicit go/no-go gate:** wazero (v1.11.0) wires only
`wasi_snapshot_preview1` (`registry.go:32,92`) — there is **no** WASI Preview 2 / component-model support
in the current dependency at all. This is an **external-dependency** risk outside our control. **Gate:**
if wazero/tooling lacks the needed component-model surface by month 4, **v0.19 ships the RFC + a
gRPC-path reference connector only; WASM connector implementation moves to Phase 2.** Rust SDK proves the
path first; opt-in/preview flag (mirror `preview.pipeline-arch-v2`) until a **committed benchi config**
(WASM vs gRPC throughput) clears a stated bar. Structured logs across the boundary; `doctor` WASM-runtime check.

**Embedded v1 / `libconduit` (v0.19):** stable, semver-committed Go embedding API — `Import`
(`import.go:33`, already takes a parsed `config.Pipeline`, the natural programmatic entry point)
is part of the contract, as is the #1274 fix. C ABI `libconduit` → minimal Python + Node examples. Clear
`embed` package boundary; internals stay internal; document C-ABI ownership/threading; ASAN/race tests.

---

## 8. Long-lead protocol design (v0.19 design; build Phase 3)

**Partition-claims RFC:** sources declare partitionable units so a future scheduler runs one hot pipeline
across instances. Ship the **protocol seam** into `conduit-connector-protocol` early to avoid a breaking
rev later — but **additive-only, backward-compatible proto evolution, per the required versioning
discussion** (CLAUDE.md: never change the protocol without it). Reviewed against
invariants (a claim must not let two instances double-read a partition). Design/seam only in Phase 1.

---

## 9. UX cohesion — the through-line

CLI, UI, MCP, library are **four renderings of one operation set**:

- **One result model + one `ConduitError` (code/path/suggestion/structured-fix)**, rendered as text

  (CLI), JSON (`--json`/MCP), and React (UI) — no surface invents vocabulary.

- **Shared verbs, now with full CLI parity** (§3): `init, validate, dry-run, inspect, deploy, repair,

  start/stop/pause, doctor` mean the same thing in CLI, MCP, and UI. `diagnose`→`doctor` fixed.

- **One 5-value status vocabulary**, pinned to a display table.
- **Errors teach** — what/where/how-to-fix — and are **actionable** (CLI applies `fix`; UI one-click;

  MCP `repair`), all sharing one fix engine.

- **React for all UI, one component library**, client generated from the served schema.
- **The write asymmetry is explicit:** CLI/MCP/library write; the Phase-1 UI reads.

---

## 10. Sequencing rationale & top risks

1. **SIGTERM drain first** (§0.1) — live invariant-7 gap; likely v0.15.1.
2. **§1.1 foundation is the linchpin AND the biggest hidden cost** — it's a CLI-wide Result-type refactor
   - a Go-error-vs-proto dual-representation design + a public-API compat plan. Name it, estimate it

   separately, investigate the ecdysis framework route to make `--json` O(1).

3. **Agent-native rides the foundation** but inherits its refactor and needs a new provisioning

   preview/diff mode + fix-synthesis — wider than "wiring."

4. **v0.18 = greenfield UI (largest item) + registry**, templates/Rust-TS pushed to v0.19.
5. **WASM last, behind a go/no-go gate** — the WASI-P2 gap is presently _total_, not partial.
6. **Two data-path items get Tier-1 rigor:** SIGTERM drain and hot-reload classification (both need a

   design doc + chaos tests; the chaos harness itself is new).

7. **ADRs are deliverables:** React-for-UI, `ConduitError` as the cross-surface contract,

   WASI-P2 for WASM, partition-claims protocol — each gets an immutable ADR.

8. **First-hour claim, honestly scoped:** CLI + agent wow by v0.17; **visual (UI) wow is v0.18** —

   competitors lead with UI, so consider a minimal live-tail slice earlier if the terminal-only month reads thin.

**Top risks:** (a) §1.1 slips → the quarter slips (mitigate: ship the error registry + framework-level
`--json` even if some commands lag); (b) solo bandwidth across the greenfield UI + registry + WASM
(mitigate: all three are P2/slippable except the UI, and the first-hour promise is fully met by
v0.16–v0.17 for CLI+agent); (c) WASM external-dependency risk (mitigate: the explicit go/no-go gate).
