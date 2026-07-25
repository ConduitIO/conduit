# Postmortem: Source.Ack acks upstream before the position is durably persisted

This is a blameless postmortem. The goal is to record what happened, why the automated gates that
exist today did not catch it sooner, and what changes as a result — not to assign fault. The bug
predates this repo having any chaos-testing infrastructure at all; it was found by the first test
ever written to look for it.

## Summary

`Source.Ack` (`pkg/connector/source.go:207-238`) sends the ack to the connector plugin
(`stream.Send`, line 218) before the resulting position write reaches durable storage
(`persister.Persist`, lines 223-235, which only enqueues into an in-memory batch flushed
asynchronously on a ~1s debounce or 10k-item threshold — `pkg/connector/persister.go:28-29,
137-170, 238-271`). For a plugin whose upstream **prunes** data once it is told to commit (Postgres
logical replication slots: acking advances `confirmed_flush_lsn`, which frees WAL for recycling),
a crash inside that window causes Conduit to resume, on restart, from a durably-persisted position
that is now _behind_ data the upstream has already discarded — a structural, unrecoverable gap.
This violates data-integrity invariants 1 (ack only after durable downstream handling), 2
(crash-safe, gap-free position resume), and 3 (at-least-once floor).

**Severity: sev-0** (confirmed data-loss class, per `CLAUDE.md`'s definition). **Status: latent,
confirmed, not yet fixed.** No production data loss is known to have occurred from this — it was
found by a chaos test in a PR that has not merged, before any release train shipped a
Kafka-Connect-wrapper-backed Postgres CDC pipeline in anger. This postmortem is issued at
discovery, per `CLAUDE.md`'s regression-response rule ("any regression that reaches a release
triggers a blameless postmortem... and produces at least one new automated check") — treated here
as if a release-blocking finding, because a sev-0 data-loss mechanism confirmed against real engine
code deserves the same rigor whether or not it happened to reach a tagged release first.

## Impact

- **Confirmed structural gap:** any source connector backed by an upstream that prunes/discards
  data once told it has been committed. The concrete case verified: Postgres logical replication
  slots (`confirmed_flush_lsn` advancement frees WAL), as used by the
  `conduit-kafka-connect-wrapper`'s Debezium-Postgres path (`DebeziumPgSourceIT`) — the one
  wrapper-backed CDC path this repo currently has test coverage for (DBZ-1, per
  `docs/design-documents/20260722-debezium-compete-roadmap.md`).
- **Confirmed benign (no gap):** the identical code path and crash window against a
  retention-based upstream (Kafka-like: replay from an older, still-valid offset simply
  redelivers) produces only a duplicate — consistent with the at-least-once floor, not a violation.
- **Verified retention-based, both benign (2026-07-24):** MySQL-binlog and
  MongoDB-change-stream/oplog-backed sources were classified by reading each connector's source, no
  longer inference. Both drive **zero** upstream state from the plugin-side `Ack` and are pruned
  only by server-side time/size, independent of any consumer position — so neither was ever in the
  prune-on-commit data-loss class this bug targets.
  - **MySQL** (`conduitio-labs/conduit-connector-mysql`): CDC is binlog streaming via go-mysql
    `canal` (`cdc_iterator.go`); `Ack` for the CDC path is a literal `return nil` no-op
    (`cdc_iterator.go:140-142`), and a full-tree search found no `PURGE`/`register…slave`/
    `binlog_expire` primitive. MySQL has no Postgres-slot equivalent: binlog is never pruned by a
    replica's _consumed_ position. Crash-in-window → benign duplicate (re-read from binlog); the
    only edge (binlog time/size-expired during downtime) is a **hard `RunFrom` error, never a
    silent skip**.
  - **MongoDB** (`conduitio-labs/conduit-connector-mongo`): CDC is Change Streams with a
    client-held `resumeAfter` token (`cdc.go:179-180`); `Ack` is a debug-log no-op
    (`source.go:135-139`). The dangerous case — a stale resume token whose oplog history has rolled
    away — **hard-errors** (`ChangeStreamHistoryLost` propagates out of `NewCombined`,
    `combined.go:61-65`; the sole swallowed error is the unrelated CosmosDB `$project` capability
    string). There is **no silent "start from now" fallback**. Crash-in-window → benign duplicate;
    oplog-rolled-past-token → hard resume error.

  A "fixed" claim now extends to both **for this bug class**. Residual risk from the persist-window
  class after the engine fix: none (the fix is redundant safety for connectors whose ack drives no
  upstream pruning). Regression-test lock-in for the per-connector property is tracked below
  (Follow-ups) — the determination is a structural property of each connector's no-op ack, and the
  engine-level ordering guarantee is already gated by the DBZ-1 chaos suite.

  **Scope caveat — a separate MySQL gap surfaced during this verification.** The adversarial pass
  (prompted to hunt for _any_ silent gap, not just the persist-window class) found an unrelated
  silent data-loss bug in `conduit-connector-mysql`: the CDC start position captured under the
  snapshot lock is held in-memory only and never persisted during the snapshot phase, so a crash
  during the initial snapshot resumes CDC from a fresh, later binlog position and silently drops
  `UPDATE`/`DELETE`s to already-copied rows (Invariant-3 violation, initial-sync window only). This
  is **not** the Source.Ack persist-window class — it is a snapshot→CDC handoff bug — so it does not
  change this postmortem's determination, but "retention-class, no-op ack" must not be read as
  "MySQL has no data-loss issues." Tracked as its own sev-0-class item with a Tier-1 design doc +
  position-format migration + SIGKILL-during-snapshot regression test (design doc in progress;
  DeVaris signed off the sequencing 2026-07-24). Two external assumptions the code analysis could
  not verify and that the tracked regression tests should pin: go-mysql `canal`'s internal replica
  registration (MySQL keeps no durable flush position for it) and mongo-driver's classification of
  `ChangeStreamHistoryLost` (286) as non-resumable.
- **Blast radius within a single crash:** bounded to whatever positions were acked-but-unflushed at
  the moment of the crash for the affected connector(s) — at most one debounce window (~1s) or
  10k records, whichever triggers first. `Persister.flushNow` batches across **every** connector
  sharing a debounce cycle in one transaction, so a slow write for one connector can extend the
  window during which other connectors' acks are also unflushed, though this postmortem's finding
  is about the ordering bug itself, not about that shared-batch coupling (named as inherited
  context in the companion design doc).
- **No customer/production impact is known.** This was caught by the repo's first chaos test,
  running against a synthetic upstream, before any tagged release combined the wrapper's
  Debezium-Postgres path with this exact crash-timing scenario in production use.

## How it was detected

`tests/chaos/` did not exist in this repo before PR #2677 (`test(chaos): DBZ-1 SIGKILL
crash-safety + engine FIFO-ack`). DBZ-1 (Debezium-compete roadmap workstream 1: "validate the
existing wrapper against Debezium-Postgres") called for a `kill -9` crash-safety test on the
offset-to-position bridge specifically because the wrapper's ack queue assumes strict FIFO
delivery and the offset↔position bridge is Tier-1 despite the wrapper already passing its
functional acceptance suite (`DebeziumPgSourceIT`). Building that test required a real engine
(`pkg/connector.Source`, `pkg/connector.Persister`, a real on-disk `badger` store — the same
backend production uses) driven against a synthetic `upstreamStore` with an explicit `prune`
toggle, because the one variable that decides gap-vs-duplicate is the upstream's own retention
behavior, which no existing integration test parameterized.

Two scenarios were run, both SIGKILL (not SIGTERM — per `CLAUDE.md`'s chaos-testing standard):

- **mid-snapshot**: killed ~30ms into a fast burst, before any automatic flush — Conduit has
  persisted nothing when the kill lands.
- **mid-stream**: killed ~1.4s in, after one automatic flush already landed a valid-but-stale
  checkpoint, with the kill inside the _next_ debounce window before its flush fires.

Result, exactly as designed to isolate the variable:

```text
SEV-0 FINDING confirmed for mid-snapshot: OPEN_GAP_ERROR: could not open source
connector plugin: GAP: chaos upstream already committed/pruned through position 30,
but Conduit asked to resume from position 0 — the 30 position(s) in between are no
longer available upstream

SEV-0 FINDING confirmed for mid-stream: OPEN_GAP_ERROR: could not open source
connector plugin: GAP: chaos upstream already committed/pruned through position 95,
but Conduit asked to resume from position 64 — the 31 position(s) in between are no
longer available upstream
```

`prune=false` (Kafka-like), same crash window, same engine code: restart resumes from the
stale-but-valid checkpoint, redelivers a handful of already-committed positions as harmless
duplicates, completes delivery. No gap, ever. `prune=true` (Postgres-slot-like): identical crash
window, identical engine code, restart asks to resume from behind an already-pruned watermark —
surfaced as a loud, structural `OPEN_GAP_ERROR` (modeling Postgres's real "requested WAL segment
has already been removed" behavior), not a silent skip.

## Timeline

All dates 2026-07-23 unless noted.

- **Unknown / pre-dates this analysis** — `Source.Ack`'s ack-before-persist ordering is introduced
  as part of the connector/persister architecture; no test at the time exercises a
  prune-on-commit upstream, so the ordering is never flagged.
- **2026-07-22** — Debezium-compete CDC parity roadmap merged
  (`docs/design-documents/20260722-debezium-compete-roadmap.md`), scoping DBZ-1: validate the
  existing Kafka-Connect wrapper against Debezium-Postgres, including a `kill -9` crash-safety
  test on the offset↔position bridge.
- **2026-07-23** — PR #2677 lands the repo's first `tests/chaos` harness, implementing DBZ-1's
  SIGKILL test. The `prune=true` scenario confirms a real, structural gap; the `prune=false`
  scenario confirms the identical window is benign against a durable upstream. The PR explicitly
  does not change `Source.Ack`'s ordering (out of scope, flagged as needing its own Tier-1 design
  doc) and escalates the finding for a fix decision.
- **2026-07-23** — This postmortem and the companion design doc
  (`docs/design-documents/20260723-source-ack-persist-ordering-fix.md`) are written, proposing
  Approach A (ack-follows-durable-flush) and requesting DeVaris's Tier-1 sign-off on the approach
  before any code changes.

## Root cause

`Source.Ack` was designed with two separate concerns interleaved in one function: (1) tell the
plugin its record(s) are safe to consider committed, and (2) durably remember the resulting
position for crash recovery. These were implemented in the order "tell the plugin, then queue the
remembering" rather than "remember durably, then tell the plugin" — the persister's async-batching
was added for write-throughput without re-examining whether the _ack_ should be allowed to precede
the _durability_ it implicitly promises. The two are only equivalent if the plugin's upstream
commit is itself reversible or idempotent from Conduit's perspective; nothing in `Source.Ack` or
`Persister` asserts or depends on that being true, and it is not true for replication-slot-based
upstreams.

## Remediation

- **Immediate (this PR):** document the bug (this file) and the recommended fix approach
  (companion design doc), and route both to DeVaris for Tier-1 sign-off on the _approach_ before
  any code change, per the standing Tier-1 rule that no data-path change merges on automated
  review alone.
- **Follow-up (separate PR, blocked on sign-off):** implement Approach A — defer the plugin ack to
  a post-flush callback, gated by per-source-partition high-water-mark tracking, with the
  graceful-shutdown drain path (`lifecycle.Service.StopAndWait`) updated to wait for the final
  deferred ack, not just the final flush. Un-skip the DBZ-1 `prune=true` gap assertion (marked
  `t.Skip` with a link to this fix's tracking issue until then) so it asserts **no gap** instead
  of demonstrating one.
- **New automated gate, per `CLAUDE.md`'s regression-response rule** ("any regression... produces
  at least one new automated check"): the DBZ-1 SIGKILL chaos test itself is that new check. It
  already exists (PR #2677) and already runs in CI as a normal test; the fix PR converts its
  `prune=true` scenario from a documented, skipped finding into an enforced regression gate. Note,
  per the process-maturity table: this runs as ordinary CI today, not yet as a scheduled nightly
  chaos job — that promotion is scoped for Phase 2, alongside the rest of the chaos-suite maturity
  work, and is not claimed as live now.

## Follow-ups

- [x] DeVaris Tier-1 sign-off on Approach A (or a decision to pursue an alternative), tracked in
      the companion design doc. — signed off; superseded the interim by PR #2680.
- [x] Fix PR: engine-side reorder + per-partition HWM tracking + `StopAndWait` drain update +
      un-skip the DBZ-1 gap assertion + benchi throughput comparison attached. — shipped as PR
      #2680 (connector-level FIFO seq; DeVaris blessed connector-level over per-partition HWM since
      `SourceState.Position` is always the full cumulative snapshot).
- [x] **Verify MySQL-binlog and MongoDB-change-stream/oplog connectors** (2026-07-24). Classified by
      reading each connector's source rather than the design doc's inference — see "Verified
      retention-based" under Impact above. Both no-op-ack / retention-class / fail-loud at the
      retention boundary; neither was ever in the data-loss class. Note: the DBZ-1 `prune`-toggle
      chaos harness (`tests/chaos`) uses an in-process synthetic upstream and cannot drive real
      MySQL/Mongo — the realistic per-connector regression gate is a SIGKILL-mid-stream +
      retention-boundary integration test in each `conduitio-labs` repo (neither harness models a
      real SIGKILL today; both use graceful `Teardown`). Tracked as its own item below.
- [ ] **Per-connector SIGKILL + retention-boundary regression tests** (MySQL, Mongo). Lock the
      verified determination: (a) kill mid-stream between emit and position-persist, restart, assert
      re-delivery (duplicate, not gap); (b) force binlog/oplog expiry past the persisted position,
      assert a **hard error, never a silent skip**. Both `conduitio-labs` harnesses need a real
      process-kill helper built (today's resume tests use graceful `Teardown`). Lower priority: the
      guarded property is structural (no-op ack) and the engine ordering is already chaos-gated —
      this defends against a future connector rewrite that adds a consumer-position-driven prune
      call, which neither upstream protocol even exposes.
- [ ] Longer-term observability follow-up (not blocking the fix): no metric exists today for
      ack-vs-persist lag or replication-slot retention pressure; a production instance of this bug
      class would be invisible until a downstream data-quality incident surfaced it. Tracked
      against DBZ-3/DBZ-4 (Phase 2), per the companion design doc's Observability section.

## Related

- PR #2677 — `test(chaos): DBZ-1 SIGKILL crash-safety + engine FIFO-ack (first tests/chaos)`.
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md` — the fix design doc this
  postmortem accompanies.
- `docs/design-documents/20260722-debezium-compete-roadmap.md` — DBZ-1 workstream that produced
  the test which found this.
