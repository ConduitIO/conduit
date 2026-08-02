# DLQ record visibility (v0.18 prerequisite P6)

## Summary

Recommendation, up front: **descope DLQ _record_ reading from v0.18.** The engine keeps no queryable
copy of dead-lettered records today — the DLQ is a destination connector, not a store — and building
one is a new, Tier-1, data-path subsystem (new serialized format, crash-safety, retention) with no
design doc, no sign-off, and no concrete operator ask backing it yet. That is out of scope for a v0.18
UI prerequisite.

What ships instead: the UI shows the DLQ's **configuration** (destination plugin, settings, window
size, nack threshold, and a plain-language statement of what that configuration currently does),
plus the existing per-pipeline DLQ volume metrics. It states plainly that dead-lettered record
_content_ is not queryable in-product, and — for the default configuration — that no record is even
written to a DLQ destination on the first failure (see the threshold-0 correction below). This
resolves prerequisite **P6** from `20260713-greenfield-built-in-ui.md` §Epic/PR plan: the DLQ view is
unblocked as a config-view, not gated on new engine work, matching the disposition of `pause`
(`20260715-pipeline-pause-semantics.md`) and `repair` (`20260715-pipeline-repair-api-semantics.md`)
in the same epic — the capability the UI needs already exists; a new store/primitive does not.

A queryable in-engine DLQ store remains real future work. It is named explicitly in
**Alternative A** below with its rough shape, so it is a scoped backlog item and not a vague "later."

## Context

### The DLQ is a destination connector, not a store (verified against the tree)

- **A pipeline's DLQ is configuration, not a running-record buffer.** `pipeline.Instance.DLQ` is a
  `DLQ` struct (`Plugin string`, `Settings map[string]string`, `WindowSize int`,
  `WindowNackThreshold int`) attached to the pipeline instance
  (`pkg/pipeline/instance.go:54,76-82`). The default is `builtin:log` at level `warn`, `WindowSize:
  1`, `WindowNackThreshold: 0` (`pkg/pipeline/instance.go:84-92`).
- **`GetDLQ`/`UpdateDLQ` are config-only RPCs.** `pkg/pipeline/service.go:191-219` (`UpdateDLQ`)
  validates and stores the `DLQ` struct on the pipeline instance; there is no `GetRecords`-style
  method anywhere in the package. Over the wire, `proto/api/v1/api.proto:650-662` defines
  `GetDLQRequest/Response` and `UpdateDLQRequest/Response` around `Pipeline.DLQ`
  (`proto/api/v1/api.proto:83-98`: `plugin`, `settings`, `window_size`, `window_nack_threshold`) —
  no record field exists in that message. The HTTP/gRPC handlers
  (`pkg/http/api/pipeline_v1.go:219-253`) just marshal `pipeline.Instance.DLQ` through
  `toproto.PipelineDLQ`/`fromproto.PipelineDLQ` — config in, config out.
- **The DLQ connector is intentionally non-inspectable and non-persistent.**
  `pkg/connector/service.go:182-185`: when a connector is created with `ProvisionedBy ==
  ProvisionTypeDLQ`, the service returns it without writing it to the store or adding it to
  `s.connectors` ("do not persist the instance, just return the connector"). On top of that,
  `pkg/connector/service.go:66-77` actively deletes any DLQ connector found persisted from an older
  Conduit version at startup (referencing issue #1016), logging a warning. Net effect: a DLQ
  connector is never listed, never inspectable via the connector API, and never present after a
  restart as a queryable object — by design, not by omission.
- **Records are written out, synchronously, through `DLQDestination`.** `pkg/lifecycle/dlq.go:44-67`
  (`DLQDestination.Write`) writes the record to the configured destination connector and blocks for
  its ack before returning; a nack or write error from the _DLQ's own_ destination is itself an
  error. There is no side buffer kept in the engine — once the write succeeds, the only copy of that
  record lives wherever the configured destination plugin put it (a log line, a Kafka topic, an S3
  object, whatever the operator configured).
- **Correction to the "default writes to `builtin:log`" framing: threshold 0 means the DLQ does not
  fire at all.** `pkg/lifecycle/stream/dlq.go:148-162` (`DLQHandlerNode.Nack`): a nack first calls
  `n.window.Nack()`; if that returns not-ok _and_ `WindowNackThreshold > 0`, the pipeline fails fatally
  (`cerrors.FatalError`, threshold exceeded). But if `WindowNackThreshold` is not greater than 0 —
  which is the **default** (`0`) — the function takes the `else` branch and returns
  `nackMetadata.Reason` directly, **without ever calling `n.Handler.Write`**. The code comment there
  is explicit: `// DLQ is disabled, we don't need to wrap the error message`. This is corroborated by
  the window implementation (`pkg/lifecycle/stream/dlq.go:232-236`,
  `newDLQWindow`/`Nack`: `return w.nackThreshold >= w.nackCount`, which is false on the very first
  nack when `nackThreshold == 0`) and by a test's own comment:
  `pkg/lifecycle/stream/dlq_test.go:182` — `WindowNackThreshold: 0, // nack threshold is 0,
  essentially the DLQ is disabled`. **Practical consequence:** with the shipped default
  (`WindowNackThreshold: 0`), the first record-delivery failure stops the pipeline immediately and
  _nothing is ever written to `builtin:log` or any other configured DLQ destination_ — the DLQ only
  starts actually receiving records once an operator raises `WindowNackThreshold` above 0 (with a
  correspondingly larger `WindowSize`). Any UI/doc language claiming "failed records are logged by
  default" is wrong today and must not ship uncorrected.
- **What a DLQ record contains, when one is written.** `pkg/lifecycle/stream/dlq.go:186-203`
  (`dlqRecord`) builds an `opencdc.Record` with `Position` set to the original message ID,
  `Operation: OperationCreate`, `Payload.After` set to the failed record's structured data
  (`msg.Record.Map()`), and metadata `SetConduitDLQNackError` (the failure reason) and
  `SetConduitDLQNackNodeID` (which pipeline node produced the nack), plus a `CreatedAt` timestamp.
  This is the shape a future store would need to preserve — see Alternative A.
- **Metrics already give volume/rate visibility without record content.** `conduit_dlq_bytes`
  (histogram, bytes written per pipeline+plugin) and `conduit_dlq_execution_duration_seconds`
  (histogram, write latency per pipeline+plugin) are defined in
  `pkg/foundation/metrics/measure/measure.go:76,97` and documented at `docs/metrics.md:28,32`. These
  exist today, require no new engine work, and are enough to answer "is my DLQ receiving traffic and
  how much" — just not "what failed."
- **No redaction on the `GetDLQ`/`UpdateDLQ` wire path today.** `toproto.PipelineDLQ`
  (`pkg/http/api/toproto/pipeline.go:82-91`) copies `in.Settings` straight into the response with no
  masking. Log output elsewhere in the codebase goes through `log.RedactAll`
  (`pkg/foundation/log/redact.go:26-44`, which redacts every config value until per-parameter
  sensitivity metadata exists — see issue #2566 referenced there), but that redaction is a
  log-call-site concern and is not applied to this RPC response. A UI DLQ-config panel built on top
  of `GetDLQ` inherits this pre-existing gap; see Failure modes.
- **A second, parallel implementation confirms this is the intended model, not a bug.**
  `pkg/lifecycle-poc/funnel/dlq.go` (a proof-of-concept successor lifecycle engine) reimplements the
  same window/threshold DLQ mechanic against a destination task, with no side store either. The
  "DLQ is a connector with a nack-tolerance window, not a queryable buffer" design is being carried
  forward, not something this doc proposes to patch around.
- **No inspect RPC targets the DLQ node.** The existing `InspectConnector`, `InspectProcessorIn`,
  `InspectProcessorOut` streaming RPCs (`proto/api/v1/api.proto`, catalogued in
  `20260713-greenfield-built-in-ui.md` §Context) attach to source/destination connectors and
  processors. None of them attaches to `DLQHandlerNode`; there is no live-tap equivalent for
  dead-lettered records either.

### Why this matters under the data-integrity invariants

CLAUDE.md invariant 3: "At-least-once is the floor. Any path that could drop a record without
delivering it or routing it to a DLQ is a data-loss bug." That invariant is about the _write_ path
(the engine must not silently drop what it can't process) — it does not require the engine to make
dead-lettered records queryable after the fact. Conflating the two would justify building a brand new
durable, crash-safe, versioned record store as an off-hand UI prerequisite, which is exactly the
speculative-generality/YAGNI pattern CLAUDE.md flags and exactly the class of change ("new serialized
format," "state layer discipline," "no pluggable state backends without sign-off") that requires its
own design doc and human sign-off before a line of code — not a side effect of a UI epic.

## Goals / Non-goals

### Goals

- Resolve prerequisite P6 for the v0.18 UI epic: decide whether DLQ record content is visible
  in-product, with the decision backed by verified current behavior.
- If descoped (it is), specify exactly what the UI/API surface instead, so "DLQ view" in the UI epic
  has a concrete, buildable-now definition.
- Name the deferred Tier-1 "DLQ store" as a real backlog item: rough shape, invariants it must
  satisfy, and what its own design doc must cover — not just "later."

### Non-goals

- Not building an in-engine DLQ record store, ring buffer, or read RPC in this doc or in v0.18.
- Not changing DLQ write-path behavior, window/threshold semantics, or defaults — including the
  threshold-0-disables-the-DLQ behavior documented above, which is out of scope to "fix" here (it may
  itself be worth a UX/defaults follow-up, but that is a separate, narrower decision).
- Not adding a new Inspect-style streaming RPC for the DLQ node.
- Not addressing the `GetDLQ` settings-redaction gap beyond flagging it as a failure mode; fixing it
  is tracked separately (issue #2566 is the umbrella for per-parameter sensitivity metadata).

## Decision

**Descope DLQ record reading from v0.18.** Ship a **config-only DLQ view**:

1. **Configuration panel**, built on the existing `GetDLQ` RPC (no new engine surface): destination
   plugin, settings (see the redaction caveat in Failure modes), `window_size`,
   `window_nack_threshold`, rendered with a **plain-language statement of current semantics** —
   e.g., "threshold 0: any delivery failure stops the pipeline; no record is sent to the DLQ
   destination" versus "threshold N (window M): up to N failures per M attempts are tolerated and
   forwarded to `<plugin>`." This directly prevents the misconfiguration trap described in Failure
   modes, where an operator assumes the default is "logging failures" when it is actually "stop on
   first failure, log nothing."
2. **Link-out, not a live view.** The UI does not attempt to render DLQ record content. It names the
   configured destination (plugin + resolved settings) so an operator knows _where_ to look — their
   own Kafka topic, S3 bucket, file, or (for the default) the Conduit process log stream — and links
   to it where a URL/target is derivable from settings (e.g., a topic name), otherwise shows the
   plugin and settings as plain text.
3. **Existing metrics, no new instrumentation.** Surface `conduit_dlq_bytes` and
   `conduit_dlq_execution_duration_seconds` (per pipeline+plugin) on the pipeline detail view as a
   volume/rate signal — "is the DLQ being hit, how much" — without claiming record-level insight.
4. **Explicit honesty copy.** The panel states, verbatim in spirit: "Dead-lettered record content is
   not queryable in Conduit. Configure a DLQ destination you can inspect directly (e.g., a topic or
   table), or raise the nack threshold and check destination-side tooling." This is a product-honesty
   requirement, not decoration — CLAUDE.md's process-maturity table is explicit about never claiming
   a capability that isn't there.

This is buildable now: it needs zero engine changes (both RPCs already exist), zero new serialized
formats, and zero Tier-1 review. It is Tier-2 UI/docs work.

## Alternatives considered

### Alternative A — in-engine last-N DLQ ring/store + read RPC — Tier-1, deferred

**Sketch.** A bounded, crash-safe store (ring buffer or small embedded-KV-backed structure, in the
same family as the existing state-layer discipline) living alongside `DLQHandlerNode`, retaining the
last N dead-lettered records — `Position`, `Payload.After`, `ConduitDLQNackError`,
`ConduitDLQNackNodeID`, `CreatedAt` (the same fields already assembled in `dlqRecord`,
`pkg/lifecycle/stream/dlq.go:186-203`, just persisted instead of forwarded-and-discarded) — exposed
through a new `ListDLQRecords`-style RPC, paginated, scoped per pipeline.

**Rough shape a real design doc would need to fix:**

- **Bounded ring buffer vs. pluggable store.** Per CLAUDE.md's state-layer discipline ("no
  distributed snapshots, no pluggable state backends"), this should almost certainly be a small,
  fixed-capacity, embedded structure — not a new backend abstraction. Capacity (count- or
  byte-bounded?) needs a number and a memory/disk budget.
- **Retention.** Last-N by count, by age, or both; what happens at eviction (silently drop oldest —
  document as a known, bounded-visibility gap, not "we keep everything").
- **Crash-safety (invariant 5).** Torn writes on `kill -9` mid-write must be impossible — a
  write-ahead-log-then-rename scheme or a transactional store API, with a kill-mid-write recovery
  test, exactly like any other state feature.
- **Read RPC.** Shape (`ListDLQRecords(pipelineID, limit, cursor)`), auth/exposure posture consistent
  with the rest of the API (today unauthenticated — see the greenfield-UI doc's "no auth configured"
  banner requirement), and whether it exposes raw payload content at all given the redaction gap
  noted above (a DLQ record embeds the _original_ failed record, which may itself carry secrets: this
  is a harder redaction problem than the DLQ config settings case).
- **Upgrade/rollback.** A new serialized format needs a versioned migration path and an
  upgrade/downgrade test (CLAUDE.md: "never change a serialized format... without a versioned
  migration path and an upgrade test").
- **Ordering/duplication.** Whether the stored view can duplicate or reorder relative to what the
  configured DLQ destination itself received (the two writes — to the store and to the destination —
  are not part of the same transaction) needs its own failure-mode analysis.

**Why it loses for v0.18 (declined, not merely deferred for time reasons).** This is a brand-new
Tier-1 data-path subsystem: new serialized format, crash-safety obligations, retention design, and its
own upgrade test — everything CLAUDE.md's Tier-1 bar and the data-integrity invariants demand — with
no design doc, no ADR, and critically **no concrete signed-off operator need** driving it yet beyond
"the UI epic wants a DLQ tab." It also duplicates, less durably, what a properly configured real
destination (a Kafka topic, a Postgres table) already does — the state-layer discipline explicitly
warns against growing engine-owned storage machinery without sign-off. If a concrete need emerges
(e.g., a documented incident-response workflow that specifically requires in-product record
inspection), it gets its own design doc with the full failure-mode enumeration above, plus human
sign-off, before any code — matching how `20260715-pipeline-repair-api-semantics.md` treats
store-mutating repair.

### Alternative B — config-only view + link-out — recommended for v0.18

Described in full under Decision. **Why it wins:** zero new engine surface (`GetDLQ` already exists
and is already wired through HTTP/gRPC), zero data-path risk, zero migration, ships within the v0.18
UI epic's existing Tier-2 posture, and is honest about the actual gap instead of hiding it behind
promised functionality. It matches the disposition already reached for `pause`
(`20260715-pipeline-pause-semantics.md`) and `repair`
(`20260715-pipeline-repair-api-semantics.md`) in the same epic: the UI need is real, but the existing
API surface already serves it once framed correctly — no new engine primitive required.

**Cost, stated plainly.** For the default configuration (`WindowNackThreshold: 0`), this alternative
cannot show "what failed" at all, because nothing is written anywhere — the pipeline just stops. That
is not a limitation of the UI; it is the engine's actual default behavior, and Alternative B's job is
to surface that fact clearly (Decision #1/#4), not to paper over it.

### Alternative C — tee DLQ to a queryable sink "by convention" — docs/recipe complement, not a separate path

**Sketch.** No code change: document a pattern where operators configure a real, independently
inspectable destination as the DLQ plugin (a Kafka topic, a Postgres table, a local file) instead of
`builtin:log`, so _they_ get queryability through that system's own tooling, and raise
`WindowNackThreshold` above 0 so the DLQ actually receives records.

**Disposition.** This does not compete with B — it is what makes Decision #2 ("link-out, not a live
view") actionable, and belongs in the cookbook/DLQ docs rather than as a distinct architectural
option. It is folded into the Decision's guidance copy rather than kept as a standalone alternative,
because on its own it answers "what should operators do" but not "what does the UI show," which is
what this doc is scoped to resolve.

## Failure modes

Enumerated for the recommended path (config-view + link-out); Alternative A's own failure modes
(crash-mid-write, retention-eviction losing signal mid-incident, dual-write ordering against the real
destination) are explicitly deferred to that store's own future design doc, not analyzed here, since
no code for it exists or is proposed.

- **Operator misreads "shows DLQ config" as "shows DLQ records."** Mitigated by the explicit honesty
  copy (Decision #4) — the panel never implies record-level visibility.
- **Threshold-0 misconfiguration trap.** An operator sets `window_size > 0` expecting some buffering
  of failures but leaves `window_nack_threshold` at its default (0) and is surprised the DLQ "never
  fires" — because, per the verified behavior above, threshold 0 means the very first failure stops
  the pipeline without a DLQ write at all. Decision #1's plain-language semantics statement exists
  specifically to prevent this being a debugging session; this is the single most important thing
  this doc's UI surface must get right, since it is a real, verified, non-obvious behavior.
- **Settings exposure.** `GetDLQ` returns `Settings` unredacted today
  (`pkg/http/api/toproto/pipeline.go:82-91` — no masking applied), which can include DLQ-destination
  credentials (e.g., a database DSN with an embedded password). This is a **pre-existing** gap in the
  RPC, not introduced by this decision, but a UI panel built on it makes the exposure more visible
  (rendered in a browser instead of only reachable via direct API calls). The panel should not
  aggravate it: at minimum, mask values for settings keys matching common secret-name heuristics
  client-side until `conduit-commons` ships per-parameter sensitivity metadata (tracked upstream,
  referenced at `pkg/foundation/log/redact.go:36-43` as issue #2566); this is a required part of
  shipping Decision #1, not an optional nice-to-have.
- **Metrics give a false "all clear."** `conduit_dlq_bytes` reading zero can mean either "no failures"
  or "failures are occurring but threshold 0 means nothing is ever written" — indistinguishable from
  the metric alone. The UI copy (Decision #4) must not let a zero-metric read as "healthy" without
  the same threshold-semantics caveat.
- **Link-out has no target for the default plugin.** `builtin:log` has no external system to link to
  — the "where do dead letters go" answer for the default is "the Conduit process's own log stream,"
  which the UI should state as such rather than rendering a broken or empty link.

## Upgrade / rollback

- **Recommended path (config-view + link-out):** purely additive on the UI side; reuses the existing
  `GetDLQ`/`UpdateDLQ` RPCs with no proto change, no serialized-format change, and no migration. Fully
  reversible by hiding the panel — there is no persisted state introduced by this decision to roll
  back.
- **If Alternative A is ever pursued:** any new ring-buffer/store format is a versioned migration
  concern from day one — an upgrade test (run N, dead-letter records, upgrade to N+1, verify the
  stored records are still readable or the format bump is explicit and documented) is a release
  blocker for that feature, not an afterthought, per CLAUDE.md's serialized-format rule and invariant
  5.

## Related

- `20260713-greenfield-built-in-ui.md` — names DLQ-record visibility as prerequisite **P6**; this doc
  resolves it as a config-view descope, matching the disposition already reached for P4 (pause) and
  P5 (repair) in the same epic.
- `20260715-pipeline-pause-semantics.md`, `20260715-pipeline-repair-api-semantics.md` — the same
  "the existing API already serves the real need; do not build a new Tier-1 primitive for a UI
  convenience" pattern applied to the same v0.18 epic.
- `docs/metrics.md` — `conduit_dlq_bytes` / `conduit_dlq_execution_duration_seconds`, the
  observability surface this decision reuses rather than duplicating.
- ROADMAP v0.18 (P6). If accepted, "no DLQ record store for v0.18, config-view only" is a candidate
  line for the roadmap; the deferred Tier-1 DLQ store, if a concrete need emerges, gets its own design
  doc, ADR, and human sign-off before any code, per Alternative A.
