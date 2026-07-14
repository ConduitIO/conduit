# Greenfield built-in UI: observe + operate (`conduit-ui`)

## Summary

v0.18's headline is a new built-in web UI — a **greenfield React app in a new `conduit-ui` repo**,
embedded into the `conduit` binary and served by `conduit run`. Its scope is **observe + operate**,
not authoring:

- **Observe:** live record flow (over the already-built Inspect streaming RPCs), per-stage
  inspection with **before/after diffs**, a pipeline graph, and throughput/lag/health.
- **Operate (v0.18 core):** **start / stop** an existing pipeline — these map to the existing
  `StartPipeline`/`StopPipeline` RPCs.

The UI is **not** an authoring surface: pipelines are created/edited as config (YAML →
`deploy`/`apply`, MCP, or the library), which stays the single source of truth. This is the line
that keeps Conduit config-as-code and agent-native rather than a second, drifting source of truth
(the Airbyte/Fivetran model — see Alternatives).

**Honesty note (this revision).** An earlier draft claimed `pause`, `repair`, and server-side
sampling were existing verbs the UI would just call, and mis-stated the pipeline status enum. Two
independent reviews verified against the code: those were wrong. `pause`, a pipeline-scoped
`repair`, DLQ-record inspection, Inspect drop-metrics, and configurable CORS are **prerequisite
Tier-2 work on `conduit`** (§ Prerequisite API work), each its own PR — not UI wiring. The UI epic
does not start against a verb until its API prerequisite has landed.

## Context

### What exists (verified against the tree) — and what does not

- **No UI in the tree.** No `ui/`, no submodule, no `conduit-ui` in `go.mod` — only the retired
  external Ember UI's `http://localhost:4200` CORS fingerprint. Greenfield; no parity obligation.
- **The Inspect streaming RPCs exist and are unused.** `InspectConnector`,
  `InspectProcessorIn`, `InspectProcessorOut` (`proto/api/v1/api.proto`), grpc-gateway-forwarded
  and websocket-proxied (`pkg/foundation/grpcutil/websocket.go`, wired in
  `pkg/conduit/runtime.go`). No client consumes them today. The UI consumes them; it builds no new
  streaming primitive. **Correction from the earlier draft:** the proxy lives under
  `pkg/foundation/grpcutil`, not `pkg/web/api/grpcutil`.
- **But the Inspector has no sampling or drop-visibility.** `pkg/inspector/inspector.go` fans out
  with `select { case s.C <- rec: default: /* log a warning */ }` against a fixed 1000-record
  buffer — **uncontrolled drop-on-full, no rate control, drop counts not exposed** (only a log
  line). So "the hard server half already exists" is true for _transport_ but **not** for the
  backpressure story: honest drop-reporting is server work (§ Prerequisite API work).
- **The pipeline status enum has 4 wire-visible values, not 5.** Internally there are five states
  (`pkg/pipeline/instance.go`: `StatusRunning`, `StatusSystemStopped`, `StatusUserStopped`,
  `StatusDegraded`, `StatusRecovering`), but `toproto.PipelineStatus`
  (`pkg/http/api/toproto/pipeline.go`) **collapses** `SystemStopped`/`UserStopped` into one
  `Pipeline_STATUS_STOPPED`. Over the wire the UI (like `conduit pipelines list`) sees exactly
  **running / stopped / degraded / recovering**. There is no "created/provisioned" state — a fresh
  instance initializes to `StatusUserStopped`. Consequence: the UI cannot distinguish "an operator
  stopped this" from "the engine restarted and this hasn't been resumed yet" — those are opposite
  answers to "should I worry," and closing the gap needs an additive API change (§ Prerequisite).
- **`start`/`stop` exist as RPCs** (`StartPipeline`/`StopPipeline`, behind
  `conduit pipelines start`/`stop`). These are the real operate verbs for v0.18.
- **`repair` is NOT pipeline-scoped and has no RPC.** `cmd/conduit/internal/repair` operates on
  **YAML text** (a file path / an MCP `Config string`); `repair.Apply` "edits the config file only
  — it never touches the pipeline store or a running pipeline." Making repair a UI operate action
  needs new server work (§ Prerequisite), not a call to an existing verb.
- **CORS is a single hardcoded origin.** `pkg/conduit/runtime.go` calls
  `allowCORS(gwmux, "http://localhost:4200")` and reflects `Access-Control-Allow-Origin` only for
  an exact match. "Runs against a remote engine" and dev-mode (a Vite dev server on another port)
  can't work in a browser until this is configurable (§ Prerequisite).
- **`/openapi/`, `/v1/*`, `/healthz`, `/readyz`, metrics are already-claimed routes.** The SPA's
  route must be registered so these still match first.
- **Observability primitives (v0.16):** `/healthz`, `/readyz`, Prometheus metrics, structured
  `ConduitError` (code/path/suggestion). **Config-as-code authoring:**
  `conduit run --pipelines <dir>` imports YAML at startup; `deploy`/`apply` gate changes behind a
  reviewed plan+hash — the discipline the UI must not undercut with a blind write path.

### Why now

A running pipeline is a black box unless you scrape logs or wire Prometheus. The Inspect streams —
already paid for — turn that into a first-class visual: watch records move, see where they stall,
diff a record before/after a processor, stop a runaway. That is the "visual wow" the Phase 1 plan
scheduled for v0.18, and it is _mostly_ a client-side build once the prerequisite API gaps close.

## Goals / Non-goals

### Goals

- **React** app consuming **only documented HTTP APIs** + metrics/health, with a **TypeScript
  client generated from the served OpenAPI schema** (`proto/api/v1/api.swagger.json`) and
  contract-tested — provably "just another API client," runnable against a remote engine.
- **Observe:** health-first fleet view; per-pipeline graph; live record flow with **freeze** and
  **per-stage before/after diffs**; throughput + destination lag; honest drop-rate display.
- **Operate:** **start / stop** an existing pipeline (existing RPCs), with confirmation on stop,
  optimistic-but-authoritative feedback, and fast reversibility.
- **Errors render identically to the CLI** (`code`/`path`/`suggestion`) and are actionable.
- **Accessible + resilient:** keyboard nav, WCAG AA, reduced-motion; a live table that does not
  spam a screen reader; a keyboard/SR-navigable linear equivalent of the graph; a defined
  reconnect/loading spec; works against a read-only or unreachable engine.
- **Embedded + served by `conduit`** (`go:embed`), gated by config; observe on by default, operate
  guarded (§ Decision 7).

### Non-goals (v0.18 — deliberate scoping lines)

- **No pipeline authoring in the UI.** No create/edit/delete of pipelines/connectors; no drag-drop
  DAG editor; no schema-driven config forms; no in-browser secrets. Authoring is CLI/MCP/YAML;
  config stays the single source of truth. (A future _config-first_ authoring flow — UI composes
  YAML → `apply` plan-review → export to git — is v0.19+, demand-gated, and explicitly **not** the
  Airbyte "UI is the source of truth" model.)
- **No UI-owned state.** The UI holds no pipeline definition the config import doesn't already own.
- **`pause`, pipeline-scoped `repair`, DLQ inspection are not committed to v0.18's UI** until their
  API prerequisites land (§ Prerequisite). If a prerequisite slips, its UI slice descopes with it.
- **No bulk operate** in v0.18 (would be N sequential calls to the per-pipeline verb — deferred;
  stated so it's a decision, not an omission).
- **Not a metrics/tracing replacement** — Prometheus stays the long-horizon system of record.

## Prerequisite API work on `conduit` (each its own Tier-2 PR, gates the matching UI slice)

These are the "already exists" claims that did not survive review. The UI epic depends on them;
they ship first, with `--json` + error codes, and (where they touch a public contract) a versioning
note.

1. **Inspect drop-metric.** Expose a per-stream dropped-record count/rate (a field on the Inspect
   stream or a companion metric) so the UI can honestly show "N records/sec dropped" instead of
   pretending sampling exists. _Precondition for the live-flow drop indicator._
2. **Configurable CORS allowlist** (replace the single hardcoded `localhost:4200`). _Precondition
   for remote-engine use and dev-mode._
3. **Stop-reason on pipeline state** (additive — a `stopReason`/`stopped_by` field alongside the
   existing `Error`, not a renumbered enum) so the fleet view can distinguish operator-stopped from
   engine-stopped-pending-resume. _Precondition for the fleet view's "should I worry" accuracy._
   Breaking-change discipline: additive field, documented, no enum renumber.
4. **Pause — its own design doc first.** `pause` is a data-path decision (invariant 7 / ack-safety):
   define quiesce semantics (graceful source-quiesce + drain in-flight + checkpoint, resumable via
   start with no loss) and whether it is a distinct status or `UserStopped` + a resumable flag,
   _before_ any verb or UI. Not committed to v0.18 unless that doc lands.
5. **Pipeline-scoped `repair` API + `Pipeline`→`PipelineDocument` export.** A server surface that
   exports a running pipeline's config as the YAML shape `repair.Collect` consumes, runs
   `repair.Collect`/`Apply`, and routes the result back through `PlanPipeline`/`ApplyPipeline` (so a
   repair is a reviewed apply, consistent with plan+hash). Its own design doc — it changes a public
   contract. Not committed to v0.18's UI unless it lands.
6. **DLQ-record inspection.** DLQ connectors are not persisted or listed
   (`pkg/connector/service.go` deletes persisted DLQ connectors on startup; `ProvisionTypeDLQ` is
   excluded from the normal lifecycle), so there is no ID to `InspectConnector` and no
   list-DLQ'd-records RPC. Either scope a "last-N DLQ'd records + cause" read API, or **explicitly
   descope DLQ visibility from v0.18** and say so. _Precondition for the degraded-path DLQ view._
7. **List pagination (only if needed).** `ListPipelines` has a name filter but no
   `page_size`/`page_token`. Client-side virtualization covers hundreds; if unfiltered
   `ListPipelines` p95 at 500+ pipelines is unacceptable, file the pagination gap rather than
   discover it in production.

## Decision

### 1. New `conduit-ui` repo, generated + contract-tested client

React + TypeScript. The client is **generated from the served OpenAPI schema** and contract-tested
in CI (drift → red), so the UI can never depend on an undocumented endpoint. **The 3 Inspect
endpoints are excluded from generation** — grpc-gateway emits them as `{result: Record}` with
"(streaming responses)", which a generator would mis-model as a single object; they are consumed by
a small hand-written websocket wrapper, and covered by a schema-shape assertion that the `result`
wrapper hasn't drifted.

### 2. Two zoom levels, not one surface

- **Fleet view (landing): health-first.** Not an alphabetical table (the CLI already wins at that).
  Degraded + recovering pinned to the top, always above the fold, with the error's first line
  inline; running collapsed into a light strip; stopped de-emphasized. A persistent header count
  ("12 running · 1 degraded · 2 recovering"). Top user task = "is anything wrong?" answered in zero
  clicks.
- **Pipeline detail (DAG) view.** One pipeline's source→processors→destination graph; node =
  connector/processor with per-node status + live throughput; this is where record flow and
  per-stage inspection live. Fleet and detail are **separate views**, not one component.

### 3. Observe over the existing Inspect streams — honestly

Subscribe via the existing websocket proxy. Because the Inspector drops-on-full with no rate
control, the UI shows the **drop rate** (from prerequisite #1) as "N records/sec dropped," never a
false "sampled at N/sec." Per-stage inspection opens at most _stage-count_ inspect connections for
the _currently-viewed_ pipeline only, all torn down on unmount; it never opens streams for
pipelines not in view.

### 4. Live record flow: freeze-first, with per-stage diffs

The signature feature, specified at the UX level, not just transport:

- **Freeze-by-default-usable.** A bounded ring buffer (~last 50–100 records) updating in place
  (highlight-flash, not append-scroll); a **Live/Freeze** toggle (Live on, one key to Freeze) that
  stops _rendering_ new rows without dropping the stream. Without freeze, inspecting a record on a
  busy pipeline is impossible.
- **Click a record → side drawer** (not a context-stealing modal): key/payload/metadata/position,
  syntax-highlighted, large payloads capped with an explicit "show full record."
- **Per-stage before/after diff — the differentiating feature.** For a record's position, show it
  at each stage (source Inspect → each processor In/Out → destination Inspect) as a **field-level
  diff**: exactly which fields a processor added/changed/dropped. This is the thing Kafka Connect
  cannot give you; it must ship as a real diff, not four raw-JSON tabs compared by eye.
- **Filtering:** by key (exact/prefix) and an "errors-only" toggle (per-record processing
  errors/nacks, not just DLQ'd). Finding record X matters more than seeing everything.
- **Sampling-policy transparency:** state and show whether the visible slice is representative
  (uniform vs per-key), so "I don't see key X" is interpretable.

### 4b. Degraded / error path

- **Show _when_ and _how long_,** not just current status: render "degraded since HH:MM (Xm ago)"
  from the state's `UpdatedAt`. (The engine's `Instance.Error` holds only the latest error — no
  history; flapping-detection is an explicit stretch/descope, not a silent absence.)
- **`Recovering` gets its own affordance:** stop/repair during automatic recovery is risk-flagged
  or disabled with a tooltip, not silently raced.
- **`repair` shows the config diff before apply** (contingent on prerequisite #5): the `Fix`
  (`ConfigPath`/`Op`/`Value`) renders as "set `/connectors/1/config/servers`: `localhost:9092` →
  `kafka:9092`"; the commit click is on the diff, never a blind "Repair" button — consistent with
  the plan+hash discipline the same class of change gets everywhere else.
- **DLQ view** contingent on prerequisite #6 (last-N DLQ'd records + cause), else descoped.

### 5. Content states — enumerated, each designed

| State | Design |
|---|---|
| Zero pipelines | Not a blank table: name the authoring path (`conduit pipelines deploy` / `--pipelines <dir>`), pre-empting a hunt for a nonexistent "create" button. |
| All stopped, fresh boot, none resumed | Needs prerequisite #3 (`stopReason`) or a "server just started, N pending resume" banner from reconciliation; else indistinguishable from "operator stopped all." |
| Engine unreachable at first load | Connection-attempt state naming the target URL — not an infinite skeleton. |
| Stream dropped mid-session | Freeze + "reconnecting" banner + last-known timestamp; **never clear** rows (clearing reads as a false "no records"). |
| Huge graph (100s of nodes) | Collapsed-by-default beyond ~20 nodes, grouped/searchable, **degraded/recovering nodes force-expanded** regardless of collapse — a problem is never hidden in a folded group. |
| Healthy-but-idle vs stalled | Surface lag/backpressure **at the node**, so genuine idle ≠ stalled. |
| Partial availability (observe up, mutate down) | "Read-only" here means the mutate path is unavailable (not the future-auth case): operate disables with a reason, observe continues. |

### 6. Operate safety

- **Stop confirms; pause (when it exists) does not.** Stop uses a lightweight inline "Stop — click
  again to confirm," not a modal. (This asymmetry is a stated decision so components don't diverge.)
- **Fast reversibility, not undo:** after Stop, a one-click "Start again" appears where the action
  was taken.
- **Bounded optimistic window:** show "stopping…" immediately; replace with the authoritative
  status only on the API response; if it takes >~2s, keep "stopping…" — never silently revert.
- **Multi-actor reconciliation:** status reflects changes from _any_ actor (this tab, another tab,
  the CLI, MCP, another operator) via poll/stream, not just this tab's own in-flight response.

### 7. Embedding, routing, auth exposure

- **`go:embed` the built SPA**, served at `/` by the existing HTTP server, **mux-ordered so
  `/v1/*`, `/openapi/*`, `/healthz`, `/readyz`, metrics match first** (a route-collision test
  asserts their content-type/status is unchanged).
- **Disable mechanism + binary-size are an explicit choice:** either route-disable (assets stay in
  the binary, config gates the route — the `swagger-ui` precedent) or a build tag that drops them;
  the epic picks one and states the acceptable binary-size delta (release-engineering impact, not
  just a flag).
- **Observe default-on; operate guarded.** The HTTP API has no auth today (bind-to-localhost /
  front with a proxy — same as the API's own posture). Because "operate" lets anyone reaching the
  port stop pipelines, either operate is behind an explicit opt-in flag, or the UI shows a
  persistent "no auth configured — anyone who can reach this can stop pipelines" banner whenever the
  API is unauthenticated. Auth itself is a tracked dependency, not solved here.

### 8. Shared visual language with the registry UI — tokens, not a package (v0.18)

The registry web UI is a static site; this is an embedded SPA — two deployment models. For v0.18,
share **design tokens (CSS variables) + a small set of duplicated primitives**, _not_ a published
component package (which, with two consumers and one maintainer, is speculative machinery — CLAUDE.md
YAGNI). Real package extraction is a v0.19+ decision with its own repo/version-pinning plan.

## Alternatives considered

- **Airbyte/Fivetran click-to-create (UI as source of truth).** Rejected: a second source of truth
  `conduit run --pipelines <dir>` can't reconcile (restart re-imports the dir — UI-only pipelines
  orphan/conflict); fights agent-native + config-as-code; a full CRUD builder is the largest
  never-finished surface for a small team whose moat is the engine, not a click-ops catalog.
- **Pure read-only (no operate).** Rejected: start/stop are safe existing verbs; an operator who
  can _see_ a runaway but must switch to a terminal to stop it is worse for no safety gain (the UI
  grants nothing the API didn't).
- **Revive/parity the Ember UI.** Not executable — not in the tree.
- **Separate-deploy SPA.** Rejected as the v0.18 default (built-in means `conduit run` serves it);
  the generated client makes a remote dashboard a trivial _later_ option.
- **A shared component _package_ now.** Rejected for v0.18 (YAGNI, two deployment models) — tokens +
  duplicated primitives instead.

## Failure modes (the 80%)

- **Stream drop (WS/SSE):** backoff reconnect, visible "reconnecting," re-subscribe with no
  double-count; prolonged loss → last-known snapshot + staleness banner, rows never cleared.
- **Render backpressure:** ring-buffer + in-place update; virtualize/collapse large tables/graphs so
  the DOM never unbounded-grows; show the server drop rate.
- **Pipeline backpressure (the operational signal):** surface destination lag/queue depth (feeds
  `/readyz`) at the node, so a slow destination reads as lag, not "fewer records."
- **Engine unreachable / partial availability:** operate disables with a reason; observe degrades to
  last-known; no crash.
- **Operate races / multi-actor:** authoritative status reconciliation from any actor (Decision 6).
- **Per-stage fan-out cost:** bounded to the viewed pipeline's stage count; torn down on unmount.
- **CORS / route collision:** configurable origin (prerequisite #2); SPA mux-ordered last with a
  collision test.
- **Contract drift:** generated-client contract test fails CI on schema/client divergence.
- **Scale:** fleet virtualized for 500+ pipelines; graph collapses >~20 nodes with forced-expand of
  problem nodes.

## Testing

- **Contract test** the generated client against the served OpenAPI (drift → red); the 3 Inspect
  endpoints excluded from generation but covered by a `result`-wrapper schema-shape assertion.
- **Run against a remote engine** in CI (proves "just another API client," and exercises the
  configurable-CORS prerequisite).
- **Accessibility:** axe/WCAG AA + keyboard-nav + reduced-motion, **plus** a scripted screen-reader
  pass over the live record table (no per-record `aria-live` spam) and the graph's linear
  equivalent.
- **Reconnect/backpressure/scale:** simulate drop (reconnect, no dup/miss, "reconnecting" ≠ "no
  data"), a firehose (drop-rate shown, bounded DOM), 500+ pipelines, a 200+-node graph.
- **Operate:** start/stop reflect authoritative status; delayed-response test proves the optimistic
  window holds "stopping…" and never silently reverts; multi-actor status change is reflected.

## Upgrade / rollback

Additive and reversible. The UI is served behind a config flag; disabling removes the surface with
no data-format impact. No serialized-format changes. The UI↔API contract is the OpenAPI schema; the
contract test is the compatibility gate. `conduit-ui` versions independently but its embedded build
is pinned per `conduit` release (a build-time asset). The one additive engine change that touches a
public contract — `stopReason` (prerequisite #3) — is a documented, non-renumbering field addition.

## Epic / PR plan and acceptance criteria

Prerequisite API PRs on `conduit` (each Tier-2, `--json` + error codes, land before the matching UI
slice): (P1) Inspect drop-metric · (P2) configurable CORS · (P3) `stopReason` · (P4) pause design
doc _then_ verb · (P5) pipeline-scoped repair API + Pipeline→YAML export design doc · (P6) DLQ-record
read API _or_ explicit descope. P4–P6 are gated on their own design docs and are **not** on the
v0.18 UI critical path — the UI ships observe + start/stop without them.

UI epic (`conduit-ui` + embed in `conduit`), with **user-testable** ACs:

1. **Bootstrap.** Repo, React+TS, CI, generated client + contract test (Inspect endpoints excluded
   from generation, their `result`-wrapper shape-asserted instead), design tokens shared with the
   registry UI. _AC: `make` builds a static bundle; the generated client round-trips every
   non-streaming schema path in CI._
2. **Fleet view (health-first).** _AC: every non-`running` pipeline with an error (degraded,
   recovering) is above the fold with zero scroll for fleets ≤50; a persistent status count header;
   a degraded pipeline's `code`/`configPath`/`suggestion` is reachable in **≤2 clicks**; the
   zero-pipelines state names the authoring command (copy-reviewed)._
3. **Pipeline detail + graph.** _AC: node states render the 4 wire values 1:1 (no invented 5th); a
   200+-node graph stays interactive and never hides a degraded/recovering node in a collapsed group
   by default; a keyboard/SR-navigable linear equivalent covers every node and edge._
4. **Live record flow + per-stage diff.** _AC: a record can be frozen, opened, and fully inspected
   without dropping the stream or losing records on un-freeze; the per-stage diff highlights exactly
   the fields a field-mutating test processor added/changed/removed; reconnect after a simulated
   drop shows no dup/miss and a "reconnecting" state distinct from "no data"; the server drop rate is
   displayed under a firehose._
5. **Metrics + node-level lag/backpressure.** _AC: a healthy-idle pipeline is visually
   distinguishable from a stalled one via node-level lag._
6. **Operate: start/stop.** _AC: stop requires a confirming second action and start does not; the
   optimistic "stopping…" holds until the authoritative response (tested with a delayed response)
   and never silently reverts; a status change from another actor is reflected; a "no auth" banner
   shows when the API is unauthenticated (or operate is behind an opt-in flag)._
7. **Embed + serve + a11y pass.** _AC: `conduit run` serves the SPA at `/`; a route-collision test
   asserts `/v1/*`, `/openapi/*`, `/healthz`, `/readyz`, metrics are unchanged; disabled cleanly by
   config; WCAG AA + the live-table/graph screen-reader ACs from §Testing pass; the chosen
   disable-mechanism and binary-size delta are documented._

Degraded-path richness (transition timestamp, `Recovering` affordance) rides with #2/#3; **repair
diff-apply (#4b) and DLQ view are gated on prerequisites P5/P6** and descope cleanly if those slip.

## Related

- Phase 1 Execution Plan (v2): `docs/design-documents/20260704-phase-1-execution-plan.md` (§6, §7).
- Connector registry MVP (shares design tokens): `docs/design-documents/20260713-connector-registry-mvp.md`.
- Hot-reload / `--dev`: `docs/design-documents/20260712-pipeline-dev-hot-reload.md`.
- Deploy/apply (the authoring path the UI deliberately doesn't duplicate):
  `docs/design-documents/20260708-cli-pipeline-deploy-apply.md`.
- Structured errors the UI renders:
  `docs/design-documents/20260705-conduit-error-and-structured-output.md`.
- `repair` (today: YAML-text, not pipeline-scoped): `docs/design-documents/20260712-repair-command.md`.
