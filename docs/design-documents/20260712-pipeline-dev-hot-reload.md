# Live in-place hot-reload (processor + metadata) and `conduit run --dev`

## Summary

Deliver a fast pipeline development loop: edit a config file, and the change is
live in the running engine in seconds. The headline requirement — set by review —
is that **in-place-class changes apply without restarting the pipeline**. That
makes the core of this work a new capability in the stream runtime: **live,
at-a-record-boundary reconfiguration of a running node**, not merely a file
watcher over the existing restart-based apply path.

Scope is deliberately drawn along an invariant-safety line (decided in review):

- **Live-swappable (applied in place, no restart):** processor config changes, and
  pipeline Name/Description changes. These nodes carry **no source position, no
  ack/durability state, and no external connection** — so swapping them at a clean
  record boundary cannot skip a position, lose an ack, or drop a record.
- **Restart-class (still drain-and-restart via the existing `ApplyPlanLive`):**
  source/destination connector settings, the DLQ (a live destination with ack
  semantics), and every topological change (add/remove connector or processor,
  plugin change, delete). These carry position/ack/connection state whose live
  mutation is genuinely dangerous (invariants 1/2/3) and belongs behind the chaos
  harness (a Phase 2 gate), not this milestone.

This split delivers the common dev-loop win — tuning a transform without dropping
the pipeline — while keeping the dangerous live-connector-swap work explicitly out
of scope. `conduit run --dev` (plus a `conduit pipelines dev` alias) is the
consumer that ties a file watcher to this capability.

Roadmap: Phase 1 (Developer Experience Core), execution-plan §3 (verb parity) and
§4 (hot-reload). This is the real §4 — the in-place half the plan asked for — not
the always-restart shortcut, which review rejected.

## Context

### What exists (and what does not)

- **`provisioning.Service.Plan` / `ApplyPlanLive`** (`plan.go:166` / `:354`,
  shipped in the #2588 arc): Plan computes a `Diff` of `Change`s, each classified
  `EffectInPlace` or `EffectRestart` (`import_actions.go`). `ApplyPlanLive` applies
  a diff to a running pipeline by **StopAndWait → transactional import → Start** —
  a full graceful restart. It is Tier-1-reviewed and is exactly the right path for
  restart-class changes; we keep using it for those.
- **The classification already exists** but is coarser than we now need:
  `updateConnectorAction`, `updateProcessorAction`, and metadata-only
  `updatePipelineAction` are all `EffectInPlace` (`import_actions.go:411,487,277`).
  We need a finer notion — "live-swappable" — that is a _subset_ of `EffectInPlace`
  (processor + Name/Description), because connector-settings and DLQ changes are
  `EffectInPlace` yet **not** safe to swap live.
- **There is no live-reconfiguration mechanism.** `connectorService.Update` and
  the update actions write only the **store** (`connector/service.go:239`
  `s.store.Set`); they do not touch a running node. A running pipeline is a static
  graph of `stream.Node` goroutines built from instances at `Start`
  (`lifecycle/service.go:488` `buildNodes`). The only trace of the idea in the data
  path is a _comment_ warning against reconfiguring a connector mid-flight
  (`lifecycle/service.go:450`). Building the live-swap primitive is the core new
  work here.
- **No file watcher** exists (`fsnotify` is a transitive dep, unused).

### The community prior art (#2236) — validated intent, superseded mechanism

Open PR #2236 (`UpsertYaml`: `Stop(...,false)` → `Delete` → recreate) validated
the demand but is superseded: it uses the fire-and-forget `Stop` the #2588 review
rejected, nukes-and-repaves instead of diffing, and — most relevant here — does
**not** apply anything in place. Close it with thanks and a pointer here.

### Why the ProcessorNode is swappable and a connector is not

`ProcessorNode.Run` (`stream/processor.go:53`) is a goroutine that processes
**exactly one record per loop iteration**: `trigger()` pulls one message,
`Processor.Process` transforms it, `base.Send` forwards it. The top of the loop —
after `Send`, before the next `trigger()` — is a **clean record boundary**: no
record is in-flight inside the node, and the processor holds no position or ack
state (positions are owned by the source; a processor that alters a record's
position is already an error, `processor.go:157`). A source/destination, by
contrast, owns the position cursor, the open plugin connection, and (destination)
the ack/durability path — mutating those live is precisely the invariant-1/2/3
hazard we are deferring.

## Goals / Non-goals

### Goals

- A processor-config edit applies to the running pipeline **without a restart** —
  the source keeps reading, positions never rewind, no record is lost or
  duplicated beyond the at-least-once floor.
- A Name/Description edit applies with no restart.
- A restart-class edit (connector settings, DLQ, topology) applies via the
  existing `ApplyPlanLive` graceful drain-and-restart, clearly labeled as such.
- An invalid edit never takes the pipeline down: a bad new processor config that
  fails to `Open` leaves the **old** processor running; parse/validate failures
  never reach the engine.
- `conduit run --dev` watches the config and applies changes on save, printing
  which mode each change used (in-place vs restart) and why.

### Non-goals

- **No live source/destination connector swap.** Connector-settings changes remain
  restart-class for live apply. Deferred to a Phase 2 project with the chaos
  harness.
- **No live DLQ swap.** The DLQ is a live destination with ack semantics; treated
  as restart-class.
- **No new serialized format / protocol / config-schema change.** The live-swap
  path reads the same `config.Pipeline` and writes the same store the restart path
  does; the only new persisted effect is the connector/processor config the store
  already holds. Zero upgrade surface (see Upgrade/Rollback).
- **No partial apply.** A given apply is all-in-place or all-restart (see the
  apply-decision rule); we do not half-swap-half-restart one diff.

## Decision

### 1. Classification: a `LiveSwappable` predicate over the diff

Keep the public `Effect` enum (`in_place`/`restart`) unchanged — it is a
`--json`/UI contract. Add an internal predicate, `Change.liveSwappable()`, true iff
the change is:

- a processor config update (`updateProcessorAction`), or
- a pipeline update touching **only** Name and/or Description.

Everything else — connector create/update/delete, processor create/delete, DLQ
change, pipeline membership change, pipeline delete — is not live-swappable.

An entire diff is **live-eligible** iff it is non-empty and **every** change in it
is `liveSwappable()`. Otherwise the diff is restart-eligible. This all-or-nothing
rule keeps the apply atomic and reasoning simple: a diff that touches a processor
_and_ a source is a restart, because the source change needs one anyway.

Surface this on the diff for CLI/MCP/UI/agent consumption via a new, additive
boolean field `Change.LiveSwappable` (defaults false; additive to the JSON
contract) and a diff-level `Diff.LiveEligible()`. The existing `Effect` field is
untouched.

### 2. The live-swap primitive in `pkg/lifecycle/stream`

Add to `ProcessorNode` the ability to swap its `Processor` at a record boundary,
driven from outside the node goroutine. Grounding: `ProcessorNode.Run`'s
`trigger()` bottoms out in `nodeBase.Receive` (`stream/base.go:318`), a `select`
over _ctx.Done / errChan / the inbound_ `in` channel. The architecture **already
has an in-band control-signal idiom**: `pubNodeBase.InjectControlMessage`
(`stream/base.go:198`) injects a control message into the stream at a record
boundary — used today to deliver the last-position message when stopping a
source. A live reconfigure is the same shape of problem, so we build on that idiom
rather than invent a foreign mechanism.

- **Delivering the swap (two feasible mechanisms; PR1 picks one).**
  (a) _In-band control message_ — model "reconfigure processor X to config C" as a
  new `ControlMessageType` carrying the target processor ID and new config,
  injected into the stream like the source-stop signal. It reaches the processor
  in-band at a natural boundary; because it is a control message, **no data record
  is in-flight or displaced** when the swap happens; and it flows even on an idle
  source (injection goes straight into the message channel), solving the
  idle-stall case. (b) _Per-node control channel_ — the `ProcessorNode` loop
  `select`s over the message receive and a dedicated swap channel; requires a small
  helper so the blocking `Receive` and the swap channel are selected together.
  Both are viable and neither can drop or double-pull a **data** message (a control
  message is not a data record; the swap-channel select is mutually exclusive with
  the receive). Mechanism (a) is preferred for consistency with the existing
  control-message design; the final choice and its race analysis are PR1's, gated
  by the interruptible-wait unit test below.
- **The idle-pipeline constraint (the correctness bar the mechanism must meet).**
  If no records flow, a naive design would block in `Receive` and apply the swap
  only on the next record — an unbounded stall. Whichever mechanism PR1 picks must
  apply the swap promptly on an idle pipeline; both candidates above do.
- **Open-new-before-teardown-old.** On a swap request carrying the new
  `Processor`: build and `Open` the **new** processor _first_; only if `Open`
  succeeds, atomically switch the node's `Processor` reference and then `Teardown`
  the **old** one. If `Open` fails, keep the old processor running, discard the
  new, and report the error back to the caller. The pipeline never drops on a bad
  edit.
- **Boundary guarantee.** The switch happens only between loop iterations, when no
  record is mid-`Process`. Records already forwarded went through the old config;
  records pulled after the switch go through the new config; ordering within the
  partition is preserved (invariant 4). During the swap, inbound records back up in
  the channel → backpressure → the source pauses reading → no position advances and
  nothing is lost (invariants 1/2/3).
- **Synchronous completion.** The caller (the apply path) blocks until the node
  reports swap-done or swap-failed, with a bounded timeout; on timeout the apply
  reports failure and does **not** leave the node half-swapped (the node only ever
  switches atomically).

Name/Description live-apply needs no node swap: update the pipeline instance's
metadata fields in the store and in memory. (It is deliberately the _only_ other
live-swappable change; DLQ is excluded precisely because it _would_ need a live
destination swap.)

### 3. The apply path: `ApplyPlanLiveInPlace` decides swap vs restart

Add a live-capable entry point (name TBD; either a new
`ApplyPlanLiveInPlace(ctx, desired, hash, allowRestartOnRunning)` or a mode on
`ApplyPlanLive`) that, under the existing per-pipeline lock and hash check:

1. `Plan(desired)`. If empty → no-op (as today).
2. If the pipeline is **not running** → identical to `ApplyPlanLive`'s `!running`
   branch (transactional import; dev's ensure-running handles start — see §4).
3. If **running** and the diff is **live-eligible** (every change swappable):
   apply **in place** — for each processor change, drive the node swap (§2); for
   Name/Description, update metadata; persist the new config in the **same
   transaction** (invariant 5) so the store and the live nodes commit together.
   **No StopAndWait, no restart.** The operator gate still applies (see below).
4. If **running** and the diff is **not** live-eligible → delegate to the existing
   `ApplyPlanLive` restart path (StopAndWait → import → Start). Unchanged, already
   reviewed.

Transaction/rollback for the in-place path: if any node swap fails (e.g. new
processor `Open` fails), roll back the swaps already applied in this diff (re-swap
to old config — the old `Processor` was still running for the failed one, and
successfully-swapped ones re-swap back), discard the store transaction, and return
the error. The pipeline keeps running its prior config throughout. This is the
in-place analogue of the restart path's reverse-order rollback.

### 4. Surface: `conduit run --dev` (+ `conduit pipelines dev` alias)

`conduit run --dev [--pipelines.path <dir>]` runs the engine as `run` does and
additionally starts a directory watcher that, on change, drives
`ApplyPlanLiveInPlace`.

- **Where it lives:** not the CLI layer — `run.Execute` calls `e.Serve(cfg)`, which
  owns engine bootstrap; the `ProvisionService` exists only after
  (`runtime.go:289`). `--dev` sets `Config.Dev = true`; the **runtime** starts the
  watcher goroutine after `ProvisionService.Init`, hands it `r.ProvisionService` +
  `r.lifecycleService`, and ties its lifetime to the serve context so `Ctrl-C`
  cancels it (invariant 7).
- **Startup vs. deltas:** startup provisioning runs normally (provisions + starts
  existing pipelines); the watcher handles subsequent edits only. An empty dir is
  valid — dev still watches so the first new file works.
- **The gate = the interactive invocation.** `ApplyPlanLive*` refuses to touch a
  running pipeline unless `allowRestartOnRunning` is true. Dev passes `true` **for
  applies it drives, and only those**. The human typed `--dev`, saved the file, and
  watches each diff scroll by — that is the operator authorization the gate exists
  to require. Dev does **not** set the process-level `--api.allow-live-restart-apply`;
  the API/MCP surface stays independently gated. The CI/non-interactive edge is not
  a hole: `--dev` authorizes only local-file applies on this box (same trust as
  startup provisioning from those files) and grants nothing to a remote agent.
- **Ensure-running (dev only).** `ApplyPlanLive*`'s `!running` branch imports
  without starting (correct for prod). Dev means "keep my pipelines running," so
  after a successful apply, if the config wants the pipeline running and it is
  stopped (e.g. a prior apply failed post-StopAndWait, or a brand-new file), dev
  calls `lifecycleService.Start`. If the config declares it stopped, dev respects
  that. This is a dev-mode layer over the apply path, not a change to its
  semantics.
- **Debounce/coalesce:** a 300ms debounce collapses save-storms; a save during an
  in-flight apply queues at most one follow-up. Atomic-save editors (rename over
  the file) are handled by watching the **directory** and tolerating a transient
  truncated/absent read (reparse on the completing event). A **deleted** watched
  file leaves its pipeline **running** and logs — never auto-deletes running
  infra.
- **Output:** each apply prints the diff, whether it was **in-place** or
  **restart** and why, the outcome, and timing — human text and `--json` events.
- **Three-faces rule:** dev is legitimately CLI-only. The _operation_ (apply) has
  full CLI/MCP/API parity from #2588; `dev` is a CLI-side automation of it. An agent
  calls `ApplyPipeline` directly rather than "watching files."
- **Alias:** ship `conduit pipelines dev [dir]` as thin sugar over `run --dev`
  (decided in review), carrying no logic of its own.

## Alternatives considered

1. **Always-restart dev loop** (watcher → `ApplyPlanLive` for everything). Rejected
   in review: classification would be cosmetic; every save drops the pipeline. Does
   not deliver §4's in-place intent.
2. **Full true in-place, including live source/destination swap.** Rejected for
   this milestone: live connector reconfiguration mutates position/ack/connection
   state (invariants 1/2/3) and needs the chaos harness (Phase 2 gate). Deferred as
   an explicit follow-up.
3. **Re-implement reload in provisioning (#2236: Stop→Delete→recreate).** Rejected:
   unsafe `Stop`, no diff, no in-place, superseded by `ApplyPlanLive`.
4. **A new `Effect` enum value for "live-swappable" instead of an additive
   predicate/field.** Rejected: `Effect` is a published `--json`/UI contract;
   changing its value set is a breaking change for consumers. An additive
   `LiveSwappable` boolean is backward-compatible.
5. **Rebuild only the changed node's subtree (a "partial restart") instead of an
   in-place swap.** Rejected for processors: tearing down and rebuilding the
   processor subtree still interrupts flow through that path and is more disruptive
   than a boundary swap, for no safety gain — the boundary swap is strictly better
   for the stateless-processor case. (Partial-subtree rebuild may be the right tool
   later for the deferred connector case; out of scope here.)

## Failure modes (the 80%)

| Scenario | Behavior | Invariant / risk |
| --- | --- | --- |
| Edit a processor's config (running) | Live node swap at record boundary; source never pauses beyond the swap; no restart | Inv 1/2/3/4 hold via boundary + backpressure + open-before-teardown |
| New processor config fails to `Open` (bad WASM/config) | Old processor keeps running; new discarded; error printed; pipeline never drops | Open-new-before-teardown-old; no data-path interruption |
| Edit a source/dest connector setting (running) | Restart-class → `ApplyPlanLive` graceful drain-restart, labeled "restart" | Inherited #2588 guarantees |
| Edit touches both a processor and a source | Whole diff restart-eligible → one drain-restart | All-or-nothing apply rule |
| YAML syntax / validation error on save | Parse/validate fails pre-apply; running pipeline untouched; error printed | No engine touch |
| Apply fails after StopAndWait (restart path) | Pipeline left stopped; dev's ensure-running restarts it on next good save | Without ensure-running it would stay down (the `!running` branch never starts) |
| Brand-new pipeline file while dev runs | Provisioned via `!running` branch, then started by ensure-running | Same gap/fix |
| Swap requested on an **idle** pipeline (no records flowing) | Interruptible wait applies the swap promptly, not "on next record" | Avoids indefinite stall; the reason `Run`'s wait becomes a select |
| `kill -9` mid-swap | Recoverable on restart from last checkpoint. The store commit is transactional (inv 5); the node swap is in-memory only and is lost on crash, so on restart the pipeline rebuilds from the **committed** config. Ordering: the swap either committed (restart sees new config) or did not (sees old); never a torn half-config. | **New crash surface vs #2588 — see below.** Requires a targeted mid-swap fault test |
| Rapid saves / save-storm | Debounced; at most one queued follow-up | No swap thrash |
| Atomic-save rename shows truncated file | Transient read/parse error tolerated; reparse on completion | No apply from a half-written file |
| Watched file deleted | Pipeline left running, logged; never auto-deleted | Never destroy running infra on file vanish |

### Crash-safety: an honest accounting of the new surface

Unlike the always-restart alternative, the in-place path **does** add a crash
surface #2588 did not have: the window between committing the store transaction and
completing the in-memory node swaps. The design keeps this safe by construction —
**the store commit is the single source of truth; node swaps are in-memory and
carry no durable state.** On crash at any instant:

- If the transaction committed → restart rebuilds nodes from the new config
  (whether or not the in-memory swap had finished).
- If it did not → restart rebuilds from the old config.

Either way the pipeline comes back consistent, and because a processor holds no
position/ack state, no record is lost or duplicated beyond the at-least-once floor.
The source's position is untouched by a processor swap, so restart resumes exactly
where it was.

This is a real claim that must be **tested, not asserted**: this feature ships with
a **targeted mid-swap fault test** — cancel/kill during a processor swap under
load, assert (a) no record loss beyond at-least-once, (b) no torn config on
restart, (c) the pipeline resumes from the correct position. Per CLAUDE.md's
process-maturity table the full nightly chaos suite is Phase 2 and not live; this
feature does **not** claim that gate. It commits to a feature-specific
fault-injection test now as the safety bar for the new surface, which is what
review required ("needs its own chaos tests"). If review wants that test to block
merge (it should), say so and it gates PR1.

## Invariants (1–7) against the live swap

- **1 (ack after durable):** the in-flight record completes `Process`+`Send` before
  any swap; ack propagation is unaffected; the swap adds no early ack.
- **2 (positions monotonic/crash-safe):** a processor swap never touches the source
  position; crash mid-swap resumes from the committed checkpoint.
- **3 (at-least-once):** backpressure pauses the source during the swap; no record
  is dropped; worst case a record in the channel is reprocessed after a crash
  (at-least-once floor).
- **4 (ordering per partition):** the switch is a clean boundary; pre-swap records
  (old config) strictly precede post-swap records (new config); no reorder.
- **5 (atomic state writes):** the store write is one transaction; node swaps are
  in-memory and non-durable.
- **6 (schema policy):** unchanged; a processor config change goes through the same
  validate path.
- **7 (graceful shutdown):** the watcher is tied to the serve context; `Ctrl-C`
  cancels it and drains normally.

The invariant most worth a reviewer's skepticism is **4** (a config change
mid-stream is _intended_ to change behavior partway through the record sequence —
confirm that is acceptable dev semantics, which it is: the operator asked for the
new transform to take effect now) and **3** under the interruptible-wait change
(confirm the new `select` cannot drop or double-pull a message).

## Testing

- **Unit (stream):** processor swap at a boundary — old config applied to record N,
  new to N+1; open-before-teardown ordering; `Open`-fails-keeps-old; swap on an idle
  node applies promptly; the interruptible wait never drops/double-pulls a message
  (table-driven, race detector on).
- **Unit (provisioning):** `liveSwappable`/`LiveEligible` classification for every
  action type; the apply-decision rule (all-swappable → in-place; any restart-class
  → delegate); in-place rollback on a mid-diff swap failure.
- **Integration:** real generator→processor→file pipeline; edit the processor
  config; assert output reflects the new transform, the source never restarted
  (position continuous, no dupes beyond floor), and no restart log. Then edit a
  source setting; assert a labeled restart. Then a syntax error; assert the pipeline
  keeps running.
- **Ensure-running regressions:** post-StopAndWait failure → next good save starts
  it; new file → started; `status: stopped` config → not force-started.
- **Targeted mid-swap fault test** (the new-crash-surface gate; see above).
- **Property test** (rapid, per CLAUDE.md for transforms): random sequences of
  processor-config swaps interleaved with record flow preserve ordering and
  at-least-once.

## Upgrade / rollback

None. `--dev` is a runtime mode; the live-swap path persists only the same
connector/processor config the restart path already persists, in the same store
format. No serialized-format, protocol, or config-schema change. Reverting the
feature affects only dev-time convenience and the in-place fast path; running
pipelines, stored state, and configs are byte-identical to what `run` produces. The
additive `LiveSwappable` JSON field defaults false and is ignorable by old
consumers.

## PR plan

Mirrors the #2588 split:

- **PR1 (Tier 1, engine):** the `ProcessorNode` live-swap primitive + interruptible
  wait (`pkg/lifecycle/stream`), the `liveSwappable`/`LiveEligible` classification +
  additive diff field (`pkg/provisioning`), and the `ApplyPlanLiveInPlace` apply
  decision (in-place vs delegate-to-restart), with unit + property + the targeted
  mid-swap fault test. Human sign-off + failure-mode analysis required; the fault
  test gates merge.
- **PR2 (Tier 2, surface):** `conduit run --dev` + the runtime watcher +
  ensure-running + the `conduit pipelines dev` alias + `--json` output + docs
  (CLI reference, `docs/operations`, llms.txt, changelog). Consumes PR1.

## Related

- `docs/design-documents/20260708-live-server-deploy-apply.md` — the #2588 arc
  (`ApplyPlanLive`/`StopAndWait`, the operator gate) reused for the restart path.
- `docs/operations/live-restart-apply.md` — the operator gate this authorizes per
  dev session.
- `docs/design-documents/20260704-phase-1-execution-plan.md` §3, §4.
- Community PR #2236 — validated demand; superseded.
- **Deferred follow-up (Phase 2):** live source/destination connector-settings swap,
  behind the chaos harness.
