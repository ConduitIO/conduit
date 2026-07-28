# Snapshot→CDC handoff deadlock: deferred acks a source blocks on must be delivered

## Summary

**Confirmed SEV-0-class, invariant-3 breach.** The Approach-A ack-persist fix (PR #2680,
[20260723-source-ack-persist-ordering-fix](20260723-source-ack-persist-ordering-fix.md)) made the
plugin-ack **deferred**: `Source.Ack` queues the position and `sendDeferredAck`
(`pkg/connector/source.go:488-503`) sends it to the plugin only after the position is durably
flushed. Its doc comment and #2680's failure-mode cases 2 & 5 assert that a **dropped** deferred ack
is "always benign … the plugin will learn about it on the next ack it does receive"
(`source.go:467-487`; #2680 doc lines 161-165, 205-222).

**That assumption is false for a source that gates its own progress on receiving that ack.** The
Postgres source's snapshot iterator blocks in `acks.Wait(ctx)`
(`conduit-connector-postgres@v0.14.0/source/snapshot/iterator.go:102`) until every snapshot record
has been acked back to the plugin, and only then returns `ErrIteratorDone` — the **sole** trigger
that starts the CDC subscriber (`.../source/logrepl/combined.go:122-134`). If the snapshot-boundary
deferred ack is dropped or indefinitely delayed, there is **no next ack** (the source emits no
further records until it receives that one), so the handoff never completes: CDC never starts, and
**every post-snapshot change (inserts, updates, deletes) is silently lost** — no error, no DLQ,
pipeline appears "running." At-least-once (invariant 3) is breached; for a delete this orphans
downstream rows (e.g. RAG vectors never removed).

This is the same failure **class** as the MySQL snapshot-handoff gap (mysql #180) and the Postgres
ack-persist SEV-0 (#2680): silent post-snapshot CDC loss at a handoff boundary.

**This PR is documentation only** — it records the bug, proposes a fix approach, and requests
DeVaris's Tier-1 sign-off on the _approach_ before any code, mirroring #2680's doc-first process. The
code fix + regression test are a separate, subsequent PR.

## How it was found

The engine-level RAG template e2e (`cmd/conduit/root/pipelines/template_gallery_rag_e2e_integration_test.go`,
tag `rag_template_e2e`) drives a real `Postgres CDC → chunk → embed → pgvector` pipeline through the
engine. Its **create path passes**; its **delete path fails** — a source-row `DELETE` never removes
the derived pgvector rows. Probing localized it precisely:

- Funnel `SourceTask.Do` → `source.Read` (`pkg/lifecycle-poc/funnel/source.go:88`) returns
  `op=snapshot` **exactly once** and never returns the CDC delete; the destination's `Write` only
  ever sees the snapshot record.
- Instrumenting `pkg/connector/source.go`, the **passing** trace is
  `Read(snapshot) → Ack → sendDeferredAck → Read(delete)`: the delete is read **only after** the
  snapshot's deferred ack is delivered.
- **Decisive experiment:** fault-injecting a drop of just the snapshot-boundary deferred ack in
  `sendDeferredAck` reproduces the exact symptom — `Read(snapshot)` once, delete never read, pipeline
  hangs, delete assertion fails.

### Honesty caveat: the natural trigger is timing/load-dependent and unconfirmed

Under a quiet machine the deferred ack is reliably delivered (~one debounce interval late) and the
bug does **not** reproduce — an independent investigation ran 13 natural runs, all passing. The
original failures occurred under heavy concurrent load (parallel builds + subagents + docker), where
scheduling around the persister's 1 s debounce flush (`DefaultPersisterDelayThreshold`,
`persister.go:28`) delays or drops the boundary ack. Candidate natural triggers, none confirmed: a
transient `stream.Send` failure in `sendDeferredAck` that is logged-and-dropped (`source.go:499-502`);
a persister callback coalescing/timing edge; a swallowed `preparePluginCall` failure. **What is
proven is the fragility, not a specific trigger:** a single dropped/indefinitely-delayed boundary ack
is catastrophic (permanent deadlock + silent CDC loss) rather than benign. The fix makes that class
of loss impossible regardless of which trigger fires; the regression test reproduces it
deterministically by fault injection.

## Context — why "benign to drop" was reasonable, and where it breaks

PR #2680 deliberately made `sendDeferredAck` non-escalating: during `Teardown`, the stream context is
cancelled before the final flush's deferred ack is sent, so a send there fails with
`context.Canceled` and escalating it via the unbuffered `errs` channel (with nothing reading `errs`
during teardown) would self-deadlock (`source.go:475-487`). The "benign" reasoning is sound for the
two cases #2680 considered:

1. **Retention-based upstreams** (Kafka): the ack is advisory pruning; a missed one is covered by the
   next ack or degrades to a benign duplicate. True.
2. **Teardown races** (position already durable): restart re-delivers → benign duplicate. True, and
   #2680 bounds the teardown wait (`DefaultTeardownFlushTimeout` = 10 s) to avoid a hang.

The unconsidered third case: **a source blocked on the ack for liveness.** Here the ack is not
advisory — it is the signal that unblocks the next stage of the source itself. "The plugin learns on
the next ack" presupposes a next ack exists; a snapshot-gating source produces nothing until this ack
arrives, so dropping it is a permanent liveness failure, not a benign pruning delay. The engine
cannot know which acks a plugin blocks on, so it must treat **every** deferred ack to a **running**
plugin as one that must eventually be delivered.

## Decision

**Recommended: Approach A — reliable deferred-ack delivery to a running plugin.** While the plugin is
running (not tearing down), a deferred ack that fails to send must be **retried until delivered**, not
logged-and-dropped. The teardown carve-out is preserved unchanged: once teardown has cancelled the
stream context / niled the plugin, dropping remains benign (position is durable; restart re-delivers —
exactly #2680's already-proven-safe case 2/5, bounded by `DefaultTeardownFlushTimeout`).

The delivery/running/teardown boundary already exists: `preparePluginCall` (`source.go`) returns
`ErrPluginNotRunning` once `Teardown` nils the plugin, and increments a wait group `Teardown` drains,
so "is the plugin still running" is a check the fix reuses rather than invents.

### Two viable implementation shapes (pick at sign-off)

**A1 — retry inside the persist callback, escalate-while-running.** In `sendDeferredAck`, on a send
failure: if `preparePluginCall` still succeeds and the stream context is not cancelled (plugin
running), retry with bounded backoff; if it keeps failing while running, escalate via `errs` (safe —
the node _is_ reading `errs` while running; the deadlock #2680 avoided is teardown-only). If the
plugin is tearing down, drop (benign, as today).

- _Pro:_ smallest diff, no new goroutine/lifecycle.
- _Con:_ `sendDeferredAck` runs in the persister's `callbackWg` goroutine, which flushes a
  **process-wide** shared batch (#2680 doc, "Process-wide blast radius", lines 256-271). A retry loop
  there delays every other connector's deferred ack in the same flush cycle across every pipeline.
  Retries must be tightly bounded to keep that blast radius small.

**A2 — dedicated per-source ack-delivery goroutine.** `onPersistFlushed` hands the durable positions
to a per-source buffered channel; a single goroutine (started at `Open`, drained+stopped at
`Teardown`) delivers them to the plugin in FIFO order, retrying transient failures while running and
aborting on teardown.

- _Pro:_ keeps the persister callback fast (enqueue only — no blast-radius widening); clean FIFO
  delivery; natural place to bound retries and abort on teardown.
- _Con:_ more surface — a new goroutine lifecycle to start/stop/drain, and `Teardown`/`StopAndWait`
  must drain it (the same "deliver final ack before returning, bounded" logic #2680 added, now on the
  delivery goroutine).

**Recommendation: A2.** It fixes the blast-radius coupling #2680 flagged as a follow-up instead of
worsening it, and isolates the retry/abort logic from the shared persister callback. A1 is acceptable
as a smaller interim if A2's goroutine lifecycle is judged too much surface for the urgency.

### Alternatives considered

- **B — decouple the handoff on the Postgres-source side** (don't gate `acks.Wait()` on the full
  downstream deferred-ack round-trip; use a source-local completion signal, preserving at-least-once
  via position durability). _Rejected as the primary:_ it lives in `conduit-connector-postgres` (a
  separate, versioned repo), fixes only Postgres (every other snapshot-gating source — MySQL, Mongo,
  future — stays exposed), and spreads the burden to every connector author. The engine-side fix
  concentrates the guarantee once, for all sources. Worth doing _additionally_ as connector hardening,
  not instead.
- **C — shorten/bypass the debounce on the ack path** (synchronous flush-then-ack, #2680's rejected
  Alternative B). Same rejection as in #2680: collapses the persister's batching benefit on the hot
  path. Does not address the delivery-reliability problem anyway (a synchronous send can still fail).
- **D — connector-protocol "you are blocked, here is your ack" signal.** Breaking protocol change
  (`CLAUDE.md`), spreads the fix to every connector. Rejected for the same reasons #2680 rejected its
  Alternative C.

## Failure-mode analysis (Approach A2, contrast with today in brackets)

1. **Snapshot boundary ack, transient send failure, plugin running.** A2 retries; the ack is
   delivered; `acks.Wait()` unblocks; CDC starts. **[today: dropped → permanent deadlock + silent
   post-snapshot CDC loss].**
2. **Boundary ack, plugin genuinely dying (stream broken, not teardown).** A2's bounded retry exhausts
   → escalate via `errs` while the node still reads it → connector/pipeline fails **loudly** with a
   clear error. **[today: silent deadlock].** Loud failure of an already-broken connector is correct;
   invariant 3 is not breached silently.
3. **Teardown race (position durable, stream cancelling).** Unchanged from #2680: drop is benign,
   bounded by `DefaultTeardownFlushTimeout`; restart re-delivers → benign duplicate, never a gap.
   A2's delivery goroutine aborts on the teardown signal exactly as `sendDeferredAck` does today.
4. **Crash before the boundary ack’s position is durable.** Unchanged: no ack was sent (deferred),
   restart resumes from the last durable position and re-runs the snapshot boundary → benign duplicate
   (invariant 1/3 upheld, as #2680 case 3).
5. **Crash after durable, before delivery goroutine sends.** Unchanged from #2680 case 5: durable
   state correct, ack re-delivered on restart → benign duplicate.
6. **Per-connector/per-partition ordering (invariant 4).** A2 delivers in FIFO enqueue order per
   source (same connector-level FIFO `seq` guarantee #2680 shipped and tests via
   `TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder`). No cross-partition
   reordering introduced.
7. **Graceful shutdown (invariant 7).** `Teardown`/`StopAndWait` must drain the delivery goroutine
   (deliver the final durable ack before returning), **bounded** by `DefaultTeardownFlushTimeout` —
   identical requirement and bound #2680 already established for the inline send; A2 moves the same
   bounded-drain from the persister callback to the delivery goroutine.
8. **Process-wide blast radius.** A2 _narrows_ it vs today: the persister callback only enqueues, so a
   slow/stuck delivery to one plugin no longer blocks other connectors' acks in the same flush cycle
   (the coupling #2680 flagged, lines 256-271). A1 would _not_ narrow it — a reason to prefer A2.

## Backward-compatibility & versioning

- **No position/state serialization change** — only the _reliability/timing_ of when the (unchanged)
  `SourceRunRequest{AckPositions}` reaches the plugin. No migration, no upgrade test beyond existing.
- **No connector-protocol change** — every connector benefits without a rebuild (as #2680).
- **Config surface** — `DefaultPersisterDelayThreshold` / `BundleCountThreshold` /
  `DefaultTeardownFlushTimeout` semantics unchanged. A2 may add a bounded retry policy (attempts /
  backoff cap) as internal constants (test-overridable), not user config.
- **Rollback** — pure engine-code change; reverting returns to today's drop-on-failure behavior (the
  bug), no data-compat concern either direction.

## Regression gate (required for the fix PR)

1. **The reproducing e2e:** with the fix, the RAG template e2e's **delete path passes** (the CDC delete
   removes the pgvector rows) — the end-to-end proof. Enable it in the `rag_template_e2e` job.
2. **Deterministic unit regression:** a `pkg/connector/source_test.go` test that fault-injects a
   dropped/failed boundary `sendDeferredAck` while the plugin is running and asserts the ack is
   **eventually delivered** (retried), not dropped — the deterministic analogue of the natural
   flaky trigger. Mirror the injection the investigation used.
3. **Preserve #2680's guarantees:** `TestSource_Teardown_BoundedWaitOnStuckFlush`,
   `TestSource_Teardown_FastFlushCompletesWithinBoundedTimeout`,
   `TestSource_Teardown_SendsPendingDeferredAckBeforeReturning`,
   `TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder` must all stay green
   (A2 moves the delivery site; these assert the properties it must keep).
4. **Chaos:** add a snapshot→CDC-handoff scenario to `tests/chaos` (SIGKILL mid-handoff, and a
   dropped-boundary-ack fault) asserting post-snapshot CDC is not lost — the standing regression for
   this class alongside the DBZ-1 prune scenario.

## Observability

Inherited from #2680 and still unsolved: no metric for deferred-acks-pending or ack-vs-persist lag.
This fix adds a natural signal — recommend a counter for **deferred-ack retry attempts** and
**delivery failures escalated**, and a gauge for **deferred-acks-pending per source**, so a stuck
handoff is visible before it manifests as missing data. Follow-up, not blocking (Phase-2
observability, per #2680's rollout).

## Rollout

- Ships with a benchi run on a reference pipeline (A2 must not regress throughput/p99 — the persister
  callback does _less_ work under A2, so the expectation is neutral-to-positive; verify, don't assume).
- No feature flag — correctness fix to existing behavior, not a new capability.
- **Sequencing:** this doc → DeVaris Tier-1 sign-off on approach (A2 vs A1) → fix PR (Tier 1: human
  sign-off, failure-mode analysis restated, the e2e delete assertion enabled, the deterministic unit
  regression + chaos scenario added, benchi attached) → the RAG template e2e delete path un-blocks.

## Risk tier & related

**Tier 1** (data path: ack delivery / at-least-once / snapshot→CDC handoff; `pkg/connector/source.go`,
`pkg/connector/persister.go`, and the Postgres source handoff it interacts with). Requires DeVaris's
explicit sign-off on the approach before code.

- [20260723-source-ack-persist-ordering-fix](20260723-source-ack-persist-ordering-fix.md) — the
  Approach-A fix whose "benign to drop" assumption this bug falsifies for snapshot-gating sources.
- `docs/postmortems/20260723-source-ack-persist-ordering.md` — the companion SEV-0 postmortem; this
  bug warrants an addendum (the assumption gap is a follow-on of the same fix).
- The MySQL snapshot-handoff gap (mysql #180) — same failure class at a handoff boundary.
- `conduit-connector-postgres@v0.14.0/source/{snapshot/iterator.go,logrepl/combined.go}` — the
  handoff gate this interacts with (Alternative B's home if connector-side hardening is also pursued).
- `cmd/conduit/root/pipelines/template_gallery_rag_e2e_integration_test.go` — the reproducing e2e
  (delete path), branch `test/rag-template-engine-e2e`.
