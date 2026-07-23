# Source.Ack persist-ordering fix

## Summary

**Confirmed sev-0, not hypothetical:** `Source.Ack` (`pkg/connector/source.go:207-238`) sends the
ack to the plugin (`s.stream.Send`, line 218) **before** the resulting position reaches durable
storage (`s.Instance.persister.Persist`, lines 223-235, which only enqueues into an in-memory batch
— see `pkg/connector/persister.go:137-170`). The batch is flushed asynchronously by `flushNow`
(`persister.go:238-271`) on a debounce: `DefaultPersisterDelayThreshold` (1s, `persister.go:28`) or
`DefaultPersisterBundleCountThreshold` (10k items, `persister.go:29`), whichever comes first. Any
crash inside that window loses the position write while the plugin has already been told "you may
commit."

For a plugin whose upstream commit is reversible/idempotent from Conduit's point of view (Kafka:
retention-based, replay from an older offset just redelivers), the crash window degrades to a
**benign duplicate** — consistent with the at-least-once floor (invariant 3). For a plugin whose
upstream commit **prunes** data it considers committed (Postgres logical replication: acking a
position drives `confirmed_flush_lsn` forward, and WAL segments behind that point can be recycled),
the identical crash window produces a **structural gap**: Conduit resumes from the last _durably
persisted_ position, which is now known to be behind data the upstream has already discarded.
Confirmed empirically by the DBZ-1 chaos test (`gh pr diff 2677`, `tests/chaos/`) against a
synthetic `upstreamStore` with a `prune` toggle — identical engine code, identical crash timing,
`prune=false` → duplicate delivery, `prune=true` → `OPEN_GAP_ERROR` on restart.

This violates invariants 1 (ack only after durable downstream handling), 2 (crash-safe, gap-free
position resume), and 3 (at-least-once floor) for every source connector backed by a
pruning/retention-limited upstream. It is **latent**: present in every shipped release since
`Source.Ack`'s current form, not introduced by DBZ-1 — DBZ-1 is simply the repo's first test that
looks at this window at all.

**This PR is documentation only.** It records the bug, proposes and recommends a fix approach
(**A: ack-follows-durable-flush**), and requests DeVaris's Tier-1 sign-off on the _approach_ before
any code changes. The code fix is a separate, subsequent PR.

## Context

### The mechanism, traced end to end

1. `funnel.Worker.Ack` (`pkg/lifecycle-poc/funnel/worker.go:491-496`) is called once a batch has
   been acked by every downstream node (including the DLQ, for partial failures —
   `worker.go:507-531`, `Nack`, acks the successfully-DLQ'd prefix). It calls
   `w.Source.Ack(ctx, originalBatch.positions)`.
2. `connector.Source.Ack` (`source.go:207-238`):
   - Line 218: `s.stream.Send(pconnector.SourceRunRequest{AckPositions: p})` — sends the ack over
     the plugin gRPC stream. For a real connector (e.g. the Debezium/Kafka-Connect wrapper,
     `DefaultSourceStream.onNext`), this is what triggers the plugin's own upstream commit
     (`task.commitRecord`/`task.commit`, which for Debezium-Postgres advances the replication
     slot's confirmed LSN — an irreversible signal to Postgres that it may recycle WAL behind that
     point).
   - Lines 223-227: updates `s.Instance.State` in memory, under `s.Instance`'s lock.
   - Lines 228-235: calls `s.Instance.persister.Persist(ctx, s.Instance, callback)` — this
     **enqueues** the connector's new state into the persister's in-memory batch
     (`Persister.batch`, keyed by connector ID) and returns immediately. It does not wait for a
     flush.
3. `Persister.Persist` (`persister.go:137-170`) arms a debounce timer
   (`DefaultPersisterDelayThreshold` = 1s) the first time a batch starts accumulating, or triggers
   an immediate flush if `bundleCount` hits `DefaultPersisterBundleCountThreshold` (10k). Either
   path eventually calls `triggerFlush` → `flushNow` (`persister.go:214-271`), which opens one
   `badger` transaction, writes every connector's queued state, and commits.
4. **The gap:** step 2's plugin-ack (irreversible for pruning upstreams) can be observed by the
   outside world microseconds after it happens. Step 3's durable write can lag it by up to ~1s (or
   until 10k records accumulate across _all_ connectors sharing that debounce cycle — see "Blast
   radius" below). A crash anywhere in that window loses step 3 while step 2 already happened.

### Why this stayed hidden

`Source.Ack`'s ordering predates any chaos test in this repo (`tests/chaos` did not exist before
DBZ-1, PR #2677). The persister's debounce was designed for throughput (batch position writes
instead of an fsync per record) and reviewed for the failure modes visible at the time — torn
writes (invariant 5, upheld: `badger`'s transaction commit is atomic, confirmed by the chaos test's
restart path never hitting its `CORRUPT_POSITION` marker) — but not for the ack-before-persist
ordering itself, because no test exercised a plugin whose upstream _prunes_ on ack. Every existing
integration/acceptance test targets connectors (Postgres native, Kafka, S3, generator, file) where
either there is no comparable irreversible commit, or the test never runs a `kill -9` in the
window. DBZ-1's synthetic `upstreamStore` with an explicit `prune` toggle is the first thing in
this repo that isolates the one variable that actually decides gap-vs-duplicate.

### Constraints this design is bound by (from `CLAUDE.md`)

- No connector-protocol change without an explicit versioning discussion — a fix that requires
  every connector plugin to adopt new protocol semantics is a last resort, not a first choice.
- No trading ack-correctness/ordering/crash-safety for throughput without a design doc and
  explicit approval — this doc is that approval request.
- Position serialization is unchanged by any option below; no migration is needed.
- Ordering guarantees are per-source-partition (invariant 4) — the fix must not introduce
  cross-partition reordering of acks.
- Shutdown is graceful by default (invariant 7) — `kill -9` at any instant must remain recoverable
  without loss, and SIGTERM must still drain and checkpoint before exit.

## Decision

> **Implementation note — what actually shipped (PR #2680), pending DeVaris re-affirmation
> at Tier-1 sign-off.** Approach A shipped, but the bullets immediately below describe the
> **originally recommended** per-source-partition high-water-mark (HWM) design. The shipped
> implementation uses a simpler **connector-level FIFO `seq`** instead: `Source.Ack`
> (`pkg/connector/source.go`) unconditionally overwrites `Instance.State.Position` with the
> position of the _last_ record in each `Ack` call
> (`s.Instance.State = SourceState{Position: p[len(p)-1]}`, `source.go:394`) —
> `SourceState.Position` is always the connector's full **cumulative** position, never a
> per-partition offset map. Because of that, whichever flush lands durably necessarily
> **subsumes** every earlier-queued `seq`: there is no per-partition dimension for a later
> flush to fail to cover, so a purely engine-internal, monotonically increasing `seq` counter
> (queued and drained FIFO by `onPersistFlushed`, see `source.go`'s `pendingAcks`) is
> sufficient to preserve invariant 4 — no per-partition HWM state was needed, or built. The
> "Per-source-partition high-water-mark tracking" bullet below and the "Per-partition ordering"
> failure-mode entry further down are retained as-written for the historical record (they were
> the basis for the original sign-off request) but do **not** describe the shipped code — see
> `TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder`
> (`pkg/connector/source_test.go`) for the shipped ordering guarantee instead. This is a
> simplification relative to what was asked approval for, not an equivalent restatement of it,
> so it is flagged here explicitly rather than assumed to be silently covered by the original
> sign-off.

**Recommended: Approach A — ack-follows-durable-flush.**

Move the plugin-ack (`s.stream.Send` in `Source.Ack`) from _before_ `persister.Persist` to a
**post-flush callback**, so the plugin is only told "you may commit" once its position has actually
reached durable storage. Concretely:

- `Source.Ack` no longer sends `AckPositions` inline. Instead it enqueues `(position, ackFn)` where
  `ackFn` is deferred until the flush that durably persists that position completes.
- The persister already threads a `PersistCallback` through `flushNow` (`persister.go:261-264`,
  currently used only to propagate flush errors back via `s.errs`). Approach A extends that
  callback: on success, it invokes the plugin ack for every position that batch just made durable,
  in the order those positions were originally acked.
- **Per-source-partition high-water-mark tracking** is required to preserve invariant 4: the
  callback must ack the highest **contiguous** durably-flushed position per partition, not just
  "whatever flushed most recently," so a source with multiple partitions/keys never acks position
  N on partition X before position N-1 on partition X has flushed, even if flush batches interleave
  across partitions. This is new state the fix must add (it does not exist today because acking was
  never gated on persistence).
- The persister's debounce (1s / 10k items) is **unchanged** — this is the throughput mechanism the
  fix deliberately keeps. What changes is _when the plugin finds out_, not _when Conduit decides to
  write_.
- **Cost:** the plugin (and therefore the upstream, for pruning connectors) learns about a
  committable position up to one debounce interval later than today. For Postgres replication
  slots, this means WAL is held roughly one flush-interval longer before being eligible for
  pruning — bounded, tunable (via `delayThreshold`), and enormously preferable to occasional silent
  gaps. This is the entire trade the fix makes, stated plainly.
- **No protocol change, no connector change.** `AckPositions` still carries the same shape; only
  the engine-side timing of when it's sent changes. Every connector, standalone or built-in,
  benefits without a rebuild.
- **Graceful shutdown (invariant 7):** `lifecycle.Service.StopAndWait`
  (`pkg/lifecycle/service.go:475-490`) already forces a flush and blocks on it via
  `s.connectors.WaitPersisted()` (`persister.go:202-204`, `WaitPendingWrites`) before returning.
  Under Approach A, the drain path must additionally block on the deferred plugin-acks for that
  final flush completing — i.e. `StopAndWait` returns only after the plugin has actually been told
  about the last durable position, not merely after the write landed. This is a required, not
  incidental, part of the fix: without it, a graceful shutdown could still tear down the connector
  (and its stream) between "position flushed" and "plugin acked," silently dropping the final ack
  and leaving the plugin holding a commit it was never told to make — reintroducing invariant 1
  violation on the _graceful_ path even after fixing the crash path.
  **Shipped refinement:** the fix PR (#2680) bounds this wait at `Source.Teardown` itself
  (`DefaultTeardownFlushTimeout` = 10s, overridable via a test-only `teardownFlushTimeout`
  field) rather than blocking on it indefinitely — `Teardown` forces the flush and waits via
  `Persister.WaitPendingWritesContext`, and on timeout or ctx cancellation logs a warning and
  proceeds with teardown anyway. See the new "Graceful shutdown racing a stuck/slow flush"
  failure-mode entry below for why an unbounded wait here would itself have been a new
  hang-on-shutdown exposure this fix did not have before it started touching the persister on
  the shutdown path at all.
- **Crash between flush-complete and plugin-ack:** on restart, the position is already durably
  persisted (that's why the ack fired), so the resumed position is correct; the plugin simply never
  received that specific ack message. Consequence: the plugin (re)commits data it would have
  committed anyway on the next ack it does receive — a benign duplicate window, not a gap, because
  durability now strictly precedes the ack signal instead of following it.

### Alternatives considered

**B — synchronous persist-then-ack.** Make `Source.Ack` flush the batch durably and only then send
`AckPositions`, inline, removing (or drastically shortening) the debounce specifically on the code
path that leads to an ack. Correct: it trivially satisfies invariant 1 by construction, no
per-partition HWM tracking needed, simplest to reason about.

_Why it loses to A:_ it collapses the persister's entire batching benefit on the hot path. Every
ack (potentially every batch, at whatever cadence the pipeline processes batches) forces a
synchronous durable write + fsync before the plugin can proceed, serializing plugin throughput
behind storage latency instead of amortizing writes across a debounce window. `CLAUDE.md`'s
performance discipline requires a benchi comparison before claiming any throughput number, and
there is no need to run that comparison to know the qualitative direction: this converts a
batched-write system into a per-ack-write system on exactly the path most sensitive to write
latency. It also does not obviously help the _cross-connector blast radius_ problem (see Failure
modes) — if anything it worsens it, since a slow store write now blocks the ack path directly
instead of a background flush.

**C — connector-protocol "position durable" signal.** Add a new message to
`conduit-connector-protocol` that the engine sends only after a position is durably persisted;
require connectors to gate their own upstream commit on receiving it, replacing the implicit
"ack means durable" contract with an explicit one.

_Why it loses to A:_ `conduit-connector-protocol` is explicitly breaking-change territory
(`CLAUDE.md`: "Never change `conduit-connector-protocol` without an explicit versioning
discussion"). This approach requires **every** connector — standalone, WASM, and the
Kafka-Connect wrapper — to adopt the new signal and correctly gate its commit on it, or the bug
persists unfixed for any connector that hasn't been updated. It spreads the crash-safety burden
outward (every connector author must now get this right) instead of concentrating the fix once, in
the one place (the engine) that can guarantee it for everyone. It is also strictly more invasive
for equivalent safety: A achieves the same guarantee with zero protocol surface change.

## Failure-mode analysis

Walking `Source.Ack`'s sequence under Approach A (contrast with today's behavior in brackets):

1. **Crash before the ack is enqueued** (record read, batch not yet acked). No change from today:
   nothing happened anywhere; restart re-reads and reprocesses. Correct, benign duplicate.
2. **Crash mid-`stream.Send` of a _different_, already-durable ack** (the deferred-ack callback is
   mid-flight). The position is already on disk (that's why the callback fired); restart resumes
   from it correctly. The plugin may or may not have received the ack message; if not, it
   (re)commits on the next ack it does get. Benign duplicate — **[today: this exact window is where
   the sev-0 gap lives, because the ack already went out before the persist happened]**.
3. **Crash after the position is enqueued into the persister's batch, before that batch's flush
   completes.** No ack has been sent to the plugin yet (ack is deferred to post-flush). Restart
   resumes from the last durably-flushed position (behind the lost, unflushed one) and reprocesses
   the gap — a duplicate from the plugin's perspective, because the plugin was never told to commit
   past that point. **This is the exact scenario that produces a structural gap today; under A it
   cannot, because the plugin never got ahead of durability in the first place.**
4. **Crash during `flushNow`'s transaction commit** (torn-write risk, invariant 5). Unchanged from
   today and already verified: `badger`'s commit is atomic at the KV layer: a `kill -9` here cannot
   leave a half-written position, confirmed empirically by the chaos harness's restart path (any
   `store.Get` error other than "key not found" is a hard-fail `CORRUPT_POSITION`, which never
   fired across the DBZ-1 test's runs).
5. **Crash after flush completes, before the deferred per-partition callback fires.** Same as case
   2 — durable state is correct, plugin ack is merely delayed to the next opportunity, benign.

**Per-partition ordering under the new scheme (invariant 4).** The deferred-ack callback must ack
partition X's position N only once every position ≤ N on partition X has itself flushed — not
merely "some flush completed." Two flush batches can complete out of order relative to when their
constituent acks were originally issued (batch B might start after batch A but finish first, if A's
transaction stalls). Approach A's per-partition high-water-mark tracking exists specifically to
prevent acking N before N-1 on the same partition in that interleaving. This tracking is new;
nothing today needs it because today's ack fires synchronously in position order at enqueue time,
before any batching reordering could occur.

**Graceful (SIGTERM) vs. `kill -9` shutdown.** Covered under Decision above:
`lifecycle.Service.StopAndWait` must additionally wait for the final flush's deferred plugin-acks,
not just the flush itself, or graceful shutdown reintroduces the same class of bug it was
protecting against (dropping an ack the plugin was owed). This is a required code change alongside
the `Source.Ack`/`Persister` reordering, not a follow-up.

**Graceful shutdown racing a stuck/slow flush (new exposure this fix introduces, and explicitly
bounds — added post-implementation, PR #2680).** Before this fix, `Teardown` never touched the
persister at all, so waiting on the final flush's deferred ack during graceful shutdown is _new_
exposure, not a pre-existing one: an unbounded wait here would trade the sev-0 ack-before-persist
bug for a possible-hang-on-shutdown bug (a stalled disk or a `badger` compaction pause could hang
graceful shutdown indefinitely). The shipped fix bounds this wait
(`Persister.WaitPendingWritesContext`, `DefaultTeardownFlushTimeout` = 10s) — on timeout or ctx
cancellation, `Teardown` logs a warning and proceeds with teardown anyway. That fallback is safe,
not merely convenient: forcing teardown before the deferred ack is confirmed sent degrades to
exactly the same, already-proven-safe outcome as crash case 3 above — at worst a benign duplicate
on the next run (the position may not have reached disk yet, so a restart simply re-delivers it),
**never a gap**. Regression-tested by `TestSource_Teardown_BoundedWaitOnStuckFlush` (a stuck
flush: proves `Teardown` returns within the bounded timeout instead of hanging) and
`TestSource_Teardown_FastFlushCompletesWithinBoundedTimeout` (the fast path: proves the same short
timeout override does not truncate a normal flush, and the deferred ack still gets sent) — both in
`pkg/connector/source_test.go`.

**Process-wide blast radius (inherited, not introduced, by this fix — corrected here from
"cross-connector" as originally scoped; see `persister.go`'s `WaitPendingWrites` doc comment,
~lines 184-186).** `Persister.flushNow` (`persister.go:238-271`) flushes one shared `batch`
covering _every_ connector currently pending a write in that debounce cycle, in one transaction —
and, more than this doc originally stated, **`connector.Persister` is a single instance shared
across every pipeline in the process**, not scoped per-pipeline. Under Approach A, a slow or stuck
flush for one connector now delays not only that connector's own durable write (as today) but also
every other connector's deferred plugin-ack in the same cycle — across every pipeline currently
running in this process, not just other connectors within the same pipeline. This was already
true of the _write_ under the current design; A extends the same shared-batch coupling to the
_ack_, and widens the blast radius from "cross-connector" to **process-wide**. Worth naming
explicitly because it means one badly-behaved connector's slow store I/O can now delay every other
pipeline's upstream commits process-wide, not just its own local position durability. Not a
blocker for A (the alternative, B, has the same coupling and makes it synchronous instead of
async-deferred, which is worse), but a candidate follow-up if per-connector (or per-pipeline)
flush isolation is ever wanted.

**MySQL binlog / MongoDB change-stream connectors — must-verify, not yet classified.** DBZ-1's
finding is specific to Postgres-replication-slot-like upstreams (prune-on-commit). Whether MySQL's
binlog or MongoDB's change-stream/oplog connectors fall in the same structural-gap class depends
entirely on their own retention behavior relative to what "ack" means to them:

- MySQL binlog retention is typically time- or size-bounded (`expire_logs_days` /
  `binlog_expire_logs_seconds`), not driven by an explicit "confirmed position" acknowledgment the
  way a replication slot is — an unacked-but-committed offset does not, by itself, force binlog
  file deletion the way an advanced `confirmed_flush_lsn` frees WAL. This suggests MySQL is likely
  closer to the **retention-based / benign-duplicate** class, but this is an inference from how
  binlog retention generally works, not a verified fact about this codebase's MySQL connector or
  the wrapper's MySQL path (which DBZ-7 has not yet built — see the Debezium-compete roadmap,
  `docs/design-documents/20260722-debezium-compete-roadmap.md`).
- MongoDB's oplog/change-stream retention is a capped collection (oplog) with its own independent
  time/size window, not gated by Conduit's ack either. Likely also retention-based, but equally
  unverified here.

Both are flagged **must-verify** for the follow-up fix PR: run the same DBZ-1-style chaos harness
(`upstreamStore` with a `prune` toggle) against MySQL-binlog and MongoDB-change-stream semantics
specifically, rather than assuming the Postgres finding or the "probably retention-based" reasoning
above generalizes. Do not ship a "fixed for all CDC connectors" claim without that verification.

## Backward-compatibility and versioning plan

- **No position/state serialization format change.** The stored `SourceState{Position}` shape is
  untouched; only the in-memory sequencing of ack vs. persist changes. No migration, no upgrade
  test beyond the existing ones.
- **No connector-protocol change.** `pconnector.SourceRunRequest{AckPositions: p}` keeps its
  current shape; only when the engine calls `stream.Send` with it changes. Every existing
  connector — built-in, standalone Go, WASM, or the Kafka-Connect wrapper — is fixed by this change
  with no rebuild or protocol-version bump required.
- **Config surface:** `DefaultPersisterDelayThreshold` / `DefaultPersisterBundleCountThreshold`
  remain the only tunables; their semantics (batch window) are unchanged, only what happens at the
  end of that window (ack now included) changes.
- **Rollback:** the fix is a pure engine-code change with no format or protocol implications;
  rolling back to the current build reverts to today's ack-before-persist ordering (the known bug,
  not a new one) with no data-compat concerns in either direction.

## Regression gate

The DBZ-1 SIGKILL chaos test (PR #2677, `tests/chaos/`) already exercises exactly this window with
a `prune` toggle and currently _demonstrates_ the gap by design (that PR explicitly does not change
`Source.Ack`'s ordering). The follow-up fix PR must:

- Land the fix with the `prune=true` (Postgres-slot-like) scenario asserting **no gap**, in
  addition to the existing `prune=false` benign-duplicate assertion.
- Until the fix lands, the `prune=true` gap assertion in DBZ-1 should be marked `t.Skip`
  with a comment linking the tracking issue for this fix, so the chaos suite doesn't perpetually
  red the build on a known, tracked, not-yet-approved-to-fix issue — and so the skip itself is a
  visible, dated marker that this is not forgotten.
- The fix PR must additionally add: a graceful-shutdown (SIGTERM) variant proving
  `Teardown`/`StopAndWait` delivers the final deferred ack before returning (shipped as
  `TestSource_Teardown_SendsPendingDeferredAckBeforeReturning`), a bounded-wait variant proving a
  stuck/slow flush cannot hang graceful shutdown indefinitely (shipped as
  `TestSource_Teardown_BoundedWaitOnStuckFlush` and
  `TestSource_Teardown_FastFlushCompletesWithinBoundedTimeout`), and a test proving
  out-of-order flush-completion still delivers acks in order (shipped as
  `TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder` — exercising the shipped
  connector-level FIFO `seq`, not the per-partition HWM originally specified here; see the
  Decision section's implementation note).

## Observability

Named in DBZ-1's failure-mode analysis and inherited here, not solved by this fix: there is
currently **no metric or alert** for ack-vs-persist lag, replication-slot/WAL retention pressure, or
"how far behind is the durable position from the last ack we sent" anywhere in Conduit. A
production instance of today's bug is invisible until a downstream data-quality incident surfaces
it. The fix in this doc removes the _correctness_ problem; it does not add visibility into how
close to the debounce boundary the system is running. Recommend (as a follow-up, not blocking this
fix): a gauge for time-since-last-flush per connector and a counter for deferred-acks-pending, so
an operator (or the fix's own soak test) can see the mechanism working rather than only inferring
correctness from the absence of gaps. Tracked for DBZ-3/DBZ-4 (Phase 2) per the Debezium-compete
roadmap, consistent with the process-maturity table's current Phase 2 placement for
chaos-suite-as-gate maturity.

## Rollout

- **Throughput:** the fix must ship with a benchi run on a reference pipeline (per `CLAUDE.md`,
  no performance claim without a committed, reproducible benchi result) comparing record
  throughput and p99 batch latency before/after, specifically because Alternative B was rejected on
  a throughput argument that deserves empirical backing, and because Approach A's own claim ("hot
  path unchanged, only ack timing moves") should be verified rather than assumed. A >10% regression
  without justification fails the build per the existing benchmark-regression-gate discipline.
- **No feature flag / staged rollout needed:** this is a correctness fix to existing behavior, not
  a new capability; there is no meaningful "opt out of the fix" state to support, consistent with
  not introducing a bespoke toggle for a data-integrity invariant.
- **Sequencing:** this design doc → DeVaris Tier-1 sign-off on approach → fix PR (Tier 1: human
  sign-off, chaos test updated per the Regression gate section, failure-mode analysis restated in
  that PR) → un-skip the DBZ-1 `prune=true` assertion → benchi run attached to the fix PR.

## Risk tier & tests

**Tier 1** (data path: ack/position/checkpoint ordering, `pkg/connector/source.go`,
`pkg/connector/persister.go`). This doc requires DeVaris's explicit sign-off on the **approach**
(Approach A vs. the alternatives above) before the follow-up code PR is opened. Per the
process-maturity table in `CLAUDE.md`, the chaos suite (`tests/chaos`) is not yet a nightly gate
(Phase 2) — today it runs as a normal CI test, which is what the fix PR will rely on for its
regression proof; the nightly-cadence gate is not claimed here.

**Additionally pending re-affirmation:** PR #2680 shipped Approach A with a connector-level FIFO
`seq` instead of the per-source-partition HWM tracking this doc originally specified (see the
Decision section's implementation note) and added a bounded wait to `Teardown`'s flush-and-wait
step that this doc did not originally call for. Both are argued above to be safe simplifications
of what was approved, not departures from it, but neither has DeVaris's explicit sign-off as
stated — flagged here for that sign-off at Tier-1 review rather than treated as automatically
covered by the original approach approval.

## Related

- PR #2680 — `fix(engine): persist position before plugin ack — sev-0 Source.Ack ordering
  (Approach A)` — the shipped fix this doc now describes. See the Decision section's
  implementation note for where the shipped mechanism (connector-level FIFO `seq`, bounded
  `Teardown` wait) diverges from what was originally specified here, pending DeVaris
  re-affirmation at Tier-1 sign-off.
- PR #2677 — `test(chaos): DBZ-1 SIGKILL crash-safety + engine FIFO-ack (first tests/chaos)` —
  the chaos test that confirmed this bug and the source of the failure-mode analysis this doc
  builds on.
- `docs/design-documents/20260722-debezium-compete-roadmap.md` — DBZ-1/DBZ-2/DBZ-7 workstreams;
  this doc's must-verify follow-ups (MySQL/Mongo) feed DBZ-2's CDC acceptance-suite scope.
- `docs/design-documents/20260708-live-server-deploy-apply.md` — prior analysis of
  `Persister`'s async-flush-vs-drain race (`WaitPendingWrites`, "blocker 1"), the closest existing
  precedent for reasoning about this persister's durability-vs-timing behavior.
- `docs/postmortems/20260723-source-ack-persist-ordering.md` — the companion postmortem for this
  sev-0.
