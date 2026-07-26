# DBZ-2: CDC correctness / acceptance-test suite

**Status:** Accepted (DeVaris Tier-1-adjacent sign-off, 2026-07-26, PR #2692). v0.20 Workstream 6. All
four open questions are resolved — see [Resolved decisions](#resolved-decisions-devaris-sign-off-2026-07-26-pr-2692).

## Summary

DBZ-2 is the correctness gate that lets Conduit _earn_ the "Debezium-class" CDC claim instead of
asserting it. It generalizes the single-scenario chaos harness built for DBZ-1 (`tests/chaos`, the
repo's first `kill -9` crash-safety test) into a small, CDC-focused correctness suite that verifies
four properties every change-data-capture source must uphold: snapshot->stream handoff with no gaps
or duplicates; at-least-once delivery under SIGKILL at three distinct crash points (mid-snapshot,
mid-handoff, mid-position-write); per-partition ordering; and that schema drift is never silently
mangled (invariant 6). It explicitly covers the Kafka Connect wrapper's offset bridge and FIFO ack
queue on the Debezium-Postgres path, not just native connectors.

This document draws the one line that decides the whole shape of the work: **which properties are
engine guarantees testable synthetically in `conduit` core (like DBZ-1), and which fundamentally
need a real connector plus a real database and therefore belong in per-connector acceptance suites.**
The decision is a two-part deliverable: a **DBZ-2 core** engine-side suite in `tests/chaos` that
gates every connector at once by proving the shared engine guarantees they all inherit, plus a
**DBZ-2 contract** — a documented "CDC done" definition and a reusable acceptance profile that each
connector's own suite instantiates against its real database, seeded by DBZ-3 against Postgres.

DBZ-2 is v0.20 Workstream 6 and is on the critical path: per the v0.20 plan, chaos-CI and DBZ-2 must
land _before_ DBZ-3 (Postgres parity) or the AI-pipeline record-path work merges, because both put
new invariant-1..7 surfaces on the data path right after a sev-0.

## Context

### What exists: DBZ-1

`tests/chaos` (v0.19 Workstream 7, DBZ-1) is deliberately minimal — one scenario family: a SIGKILL
crash-safety test on the source connector's offset<->position bridge. Its `doc.go` states outright
that if a second scenario (DBZ-2) ever needs a more general harness, it should generalize _from_ this
package rather than build a speculative skeleton ahead of need. DBZ-2 is that second scenario.

The harness is worth understanding precisely, because DBZ-2 reuses its machinery almost verbatim for
Properties 1-3 (Property 4 is the exception — it needs a funnel + DLQ + destination the harness lacks;
see Q3 Property 4 and Rollout):

- **Re-exec-self child process.** `TestMain` (`sigkill_test.go`) intercepts `CONDUIT_CHAOS_CHILD=1`
  and runs `runChild` (`child.go`) instead of the package's Go tests. The parent spawns the child,
  watches its stdout marker lines, `kill -9`s it at a chosen point, respawns it against the _same_
  on-disk badger DB and upstream dir, and asserts on the restart. That cross-process, on-disk
  persistence is what makes "restart and check for a gap" mean something.
- **Real engine code, synthetic edges.** The child builds a real `pkg/connector.Source` backed by a
  real `connector.Persister` and a real on-disk badger DB (`child.go`), driven by an in-process
  `chaosPlugin` (`upstream.go`) standing in for a source plugin. Only the plugin and its upstream are
  synthetic; the ack/position/persist path under test is production code.
- **The `prune` flag is the whole insight.** `upstreamStore` (`upstream.go`) models the external
  system a source commits to. With `prune=false` it is a durable, replayable log (Kafka-like: can
  redeliver from an arbitrarily old offset). With `prune=true` it models a Postgres replication slot
  whose WAL is recycled once `confirmed_flush_lsn` advances — `chaosPlugin.Open` then hard-errors
  ("`GAP:`") if Conduit asks to resume behind the committed watermark, exactly as real Postgres
  surfaces "requested WAL segment ... has already been removed". Gap-vs-duplicate is decided by this
  one variable, against otherwise identical engine code.
- **Kill-timing is race-free.** The parent keys the kill off `READ` progress lines
  (`waitForReadCount`), never `ACK` lines. Under the Source.Ack sev-0 fix (Approach A), plugin acks
  are deferred behind the persister's debounce, so ack progress no longer tracks wall-clock; read
  progress, paced by `chaosPlugin.paceMS`, still guarantees a minimum elapsed time and never returns
  early. DBZ-2 must keep this discipline.

The engine code DBZ-1 exercises — and DBZ-2 extends coverage of — is the **FIFO ack queue** from the
Source.Ack sev-0 fix (PR #2680, `pkg/connector/source.go`): `Ack` updates in-memory state, appends a
`pendingAck{seq, positions}` to a FIFO queue under a monotonic `nextAckSeq`, and registers a
persister callback. The plugin ack is _not_ sent synchronously; `onPersistFlushed` sends it only
after the resulting position is durably flushed, draining `pendingAcks` from the head up to
`durableAckSeq` in strict seq order (`sendDeferredAck`). `nextAckSeq` is engine-internal and
deliberately _not_ derived from the opaque `opencdc.Position` bytes, which `Source` cannot parse. The
persister (`persister.go`) debounces on `DefaultPersisterDelayThreshold` (1s) or
`DefaultPersisterBundleCountThreshold` (10k), and `WaitPendingWrites` waits on both the store write
(`flushWg`) and the deferred-ack callbacks (`callbackWg`).

Note on DBZ-1's design record: `tests/chaos/doc.go` refers to a design doc "dbz1-chaos.md"; no file
by that name was ever created. DBZ-1's design rationale in fact lives in the package `doc.go` itself
and in `docs/design-documents/20260723-source-ack-persist-ordering-fix.md` plus
`docs/postmortems/20260723-source-ack-persist-ordering.md` (the sev-0 the chaos test caught). This
document treats those three as the DBZ-1 record and does not perpetuate the phantom filename.

### What is missing

DBZ-1 proves exactly one property (no gap under SIGKILL in the ack->persist window, conditional on
upstream prune behavior). It does not cover snapshot->stream handoff as a distinct boundary, the
mid-handoff and mid-position-write crash points, per-partition ordering, or schema drift. Nothing in
the repo defines "done" for a CDC connector, so "Debezium-class" is currently an assertion with a
single supporting test.

### The roadmap mandate

The Debezium-compete roadmap (`docs/design-documents/20260722-debezium-compete-roadmap.md`, the DBZ-2
row and the sequencing table) defines DBZ-2 as the CDC acceptance suite that verifies the four
properties above, covers the wrapper's offset bridge and FIFO ack queue on the Debezium-Postgres
path, "defines 'done' for every CDC connector", and "is the gate that earns the 'Debezium-class'
claim". The sequencing table makes DBZ-2 a hard dependency of DBZ-3, DBZ-5, DBZ-7, DBZ-8, DBZ-9, and
DBZ-10 — every connector-hardening workstream. The v0.20 plan scopes DBZ-2 as "engine-side (Conduit
core)"; reconciling that scoping with "defines done for every connector" is the central design
question this document resolves.

**Motivating evidence that the property is real.** A connector-specific instance of exactly this
suite's headline property — "snapshot->stream handoff, no gaps under SIGKILL mid-snapshot" — was just
built for the MySQL connector (`conduitio-labs/conduit-connector-mysql` #182). It caught a genuine
silent-data-loss bug: the CDC start position was not persisted across the snapshot, so a crash
mid-snapshot lost the streaming resume point. **That bug was connector-internal** — it lived in the
connector's own snapshot-position persistence, inside its opaque `opencdc.Position` handling, which
the engine cannot inspect. It is therefore caught only by the **DBZ-2 contract** (the per-connector
acceptance profile run against a real database), **never by DBZ-2 core** — a green core suite does
not and cannot gate the #182 bug class (consistent with the A1 rejection below). DBZ-2's value here
is to define this property _once_ as a named contract every CDC connector must instantiate, instead
of each connector rediscovering it after it has already lost someone's data.

## Goals / Non-goals

**Goals.**

- Generalize `tests/chaos` into a CDC correctness suite covering the four roadmap properties, reusing
  DBZ-1's re-exec-self harness, synthetic `upstreamStore`, and race-free kill-timing.
- Extend engine-side coverage of the FIFO ack queue and the offset-bridge contract the Kafka Connect
  wrapper depends on, without requiring the JVM wrapper or a real Postgres in `conduit` core.
- Publish a "CDC done" definition (the DBZ-2 contract) and a reusable acceptance profile that
  per-connector suites instantiate against a real database, seeded by DBZ-3.
- Keep the suite wired into the already-required `tests/chaos (race, x3)` CI check so a correctness
  regression fails the build for every connector at once.
- Every property ships verified-to-fail-without-the-fix, matching DBZ-1's discipline.

**Non-goals.**

- **This is not a generalized chaos framework.** It is scoped to CDC correctness (invariants 1-7 on
  the source ack/position/handoff/ordering path). Fault-injection breadth beyond that — arbitrary
  network partitions, byzantine plugins, a pluggable fault matrix — is explicitly out; if a future
  scenario needs it, it generalizes from DBZ-2 the way DBZ-2 generalizes from DBZ-1.
- **No distributed / Flink features.** No multi-instance partition claims, no distributed snapshot,
  no event-time watermarks, no cross-destination atomic commit. Per-partition ordering here is the
  single-node engine guarantee, not a distributed one (see the partition-claims RFC,
  `20260723-partition-claims-protocol.md`: the seam is inert until a scheduler exists).
- **DBZ-2 does not build a CDC-specific dead-letter store.** The queryable-DLQ-record gap is deferred
  Tier-1 work (`20260715-dlq-record-visibility.md`, #2640); the invariant-6 property asserts routing
  correctness, it does not add a store.
- **DBZ-2 does not test the JVM wrapper's Java code.** The Debezium-offset<->Conduit-position
  translation (`SimpleSourceTaskCtx`, `SourcePosition`) is covered by the wrapper repo's own
  `DebeziumPgSourceIT`. DBZ-2 owns the engine half of that contract.
- **DBZ-2 does not implement any connector CDC feature.** Snapshot, DDL reconstruction, heartbeats
  are DBZ-3 and belong in the connector repos. DBZ-2 defines the bar; it does not clear it for them.

## Constraints

- **No connector-protocol change.** DBZ-2 is a test suite; it must not touch
  `conduit-connector-protocol`. It tests the protocol contract as it stands (opaque `opencdc.Position`
  bytes; `AckPositions` delivered over the source run stream).
- **CI-friendly and deterministic.** The suite runs in the `tests/chaos` job with `-race` and 3x
  repetition (`tests/chaos (race, x3)`, a required check on `main` since #2686/#2690). No real
  database, no network, no JVM — every edge is synthetic and in-process, so it stays fast and
  hermetic. Kill-timing must remain race-free (READ-gated, never ACK-gated or fixed-sleep).
- **Position is opaque to the engine.** The suite may not assume `Source` can parse or compare
  `opencdc.Position`; engine-level ordering assertions key off the engine-internal `nextAckSeq`
  ordering, not off position contents.
- **Reuse, don't rebuild.** The harness, marker protocol, and `upstreamStore` are the substrate;
  DBZ-2 adds scenarios and assertions, not a parallel framework.

## Design questions resolved

### Q1. The synthetic-vs-real boundary

**Decision.** Draw the line at a single test: _does the property depend on interpreting
connector-specific payload/position semantics, or on a real external log's retention behavior?_ If
**no**, it is an engine guarantee and belongs in DBZ-2 core (synthetic, `tests/chaos`). If **yes**,
it needs a real connector plus a real database and belongs in the per-connector acceptance suite (the
DBZ-2 contract), instantiated first by DBZ-3 against Postgres.

Applying that test to each property:

| Property | Engine-side synthetic (DBZ-2 core)? | Why |
| --- | --- | --- |
| Snapshot->stream continuity (no-crash smoke) | Smoke only | The engine stores one opaque monotone position; snapshot and stream share one position space with no persisted boundary state, so a clean run is near-tautological. Real handoff atomicity (two position _encodings_ + mode switch) is a connector property, deferred to DBZ-3/contract. |
| At-least-once under SIGKILL x3 | Yes | The three cases are producer-timing windows over the same ack->persist path DBZ-1 already drives synthetically; the engine exposes two resume states (empty, valid-stale), not three. |
| Per-partition ordering | Partly | The engine guarantee (acks delivered to the plugin in strict Ack-call order; cumulative position monotone) is synthetic-testable. True per-partition offset independence lives in the connector's Position encoding and needs a real multi-partition source. |
| DDL-mid-stream / schema drift (invariant 6) | Partly | Real DDL emission + schema-history reconstruction + policy application need a real DB and the real connector. The transport-level half — a drift/poison-marked record is never _silently_ acked without being handled — is synthetic-testable. |

The two hard cases, resolved explicitly:

**DDL / schema drift cannot be tested end-to-end synthetically, and should not be faked.** Invariant 6
is about the configured drift policy (halt / DLQ / evolve). In Conduit's architecture that policy is
enforced by the connector, the schema subsystem, and processors — _not_ by `Source.Ack` or the
persister, which never inspect payload schema (see `source.go`: `sanitizeRecord` touches only
key/payload nil-ness and metadata, never schema). A genuine "real DDL arrives, the connector
reconstructs the new schema from `pgoutput`/catalog, the policy is applied" test therefore
fundamentally needs a real database emitting real DDL and the real connector's schema-change handling.
That is a per-connector acceptance property, exercised concretely by DBZ-3 (whose sign-off already
set Postgres `schemaDrift` default = `halt`). **What DBZ-2 core _can_ and does own synthetically is
the transport guarantee:** a record carrying a drift/poison marker is either delivered-and-acked, or
halted / routed to DLQ, but is **never** acked upstream without being handled — no silent coercion or
truncation at the record-transport layer. Faking a schema engine in the chaos harness would produce
false confidence that real drift is safe; the boundary is drawn precisely to avoid that.

**Per-partition ordering splits the same way.** The engine, single-node today, guarantees that acks
are delivered to the plugin in the exact order `Ack` was called (`onPersistFlushed` drains
`pendingAcks` by ascending `seq`), and that the persisted cumulative `Position` only advances. That
property — reorder-freedom of ack delivery and monotonicity of the checkpoint — is synthetic-testable
with a multi-key `chaosPlugin`. What is _not_ engine-testable is that partition A's offset never
regresses independently of partition B: that independence is encoded inside the connector's opaque
Position (a Kafka topic-partition map, a Postgres per-table LSN), which the engine cannot interpret.
So DBZ-2 core asserts the seq-ordering + monotonicity guarantee; the per-connector acceptance suite
asserts real per-partition offset monotonicity against a real multi-partition source.

**Asserted design statement (a decidable fact, not an open question).** This document _states_, rather
than asks: the engine guarantees exactly two ordering properties — (1) acks are delivered to the
plugin in `Source.Ack` **call** order (`onPersistFlushed` drains `pendingAcks` by ascending
`nextAckSeq`, `source.go:450-465`; `nextAckSeq` is assigned in call order and is explicitly _not_
position-derived, `source.go:78-90`), and (2) the persisted cumulative `SourceState.Position` only
advances. It does **not** guarantee, and cannot express, per-partition offset independence — that
lives in the connector's opaque Position and is a connector concern. This is readable directly from
the cited code and the single-node-engine commitment (`20260723-partition-claims-protocol.md`: the
partition-claims seam is inert until a scheduler exists, so invariant 4 holds today only because the
engine is single-node). Because it is decidable from the code, it is not routed to DeVaris as an open
question; it is a scope boundary this design fixes.

### Q2. "Gates every connector" — the actual mechanism

**Decision: both — an engine-side core plus a connector-facing contract, with a clean split of what
each owns.** This reconciles "engine-side (Conduit core)" with "defines done for every CDC connector".

**DBZ-2 core (the v0.20 Conduit-core deliverable).** An engine-side suite in `tests/chaos` that
verifies the guarantees _every_ out-of-process and native connector inherits for free from the shared
engine code: ack-after-durable-persist ordering (invariant 1), crash-safe monotonic position
(invariant 2), at-least-once floor (invariant 3), strict-FIFO ack delivery (invariant 4 at the engine
seam), and no-silent-drop transport (invariant 6, transport half). This is what "gates" in the CI
sense: it is a required status check, and a regression here breaks correctness for _all_ connectors
simultaneously, making it the single highest-leverage gate in the tree. It gates external connectors
not by running them, but by proving the engine contract they are built on cannot regress underneath
them.

**DBZ-2 contract (the connector-facing definition of done).** The engine-side suite cannot test a
real connector's snapshot->stream handoff against a real database — the connector lives in another
repo with a real DB dependency and (for the wrapper) a JVM. So the connector-facing half is:

1. A documented "CDC done" checklist in the connector authoring docs: the properties a CDC source
   must demonstrate (snapshot->stream continuity, resume-after-kill at the three points, per-partition
   offset monotonicity, drift-policy correctness) before it may be labeled log-based / Debezium-class.
2. A reusable acceptance profile extending the existing SDK acceptance suite
   (`conduit-connector-sdk`), providing shared assertion helpers so a native connector's own
   acceptance test instantiates these properties against its real database with minimal boilerplate.
   The SDK acceptance suite is already "the compatibility contract" per `CLAUDE.md`; DBZ-2 extends it
   with the CDC-specific properties and versions them so authors know the bar they passed.

The first _real_ instantiation of the contract is DBZ-3, against real Postgres. The JVM wrapper cannot
import a Go acceptance harness, so for the wrapper the contract is the prose checklist plus its own
`DebeziumPgSourceIT`, which must be shown to satisfy each checklist item.

So: **DBZ-2 core is what lands in v0.20 Conduit core and what CI enforces; the DBZ-2 contract is the
published bar plus an SDK-side helper that per-connector suites adopt on their own schedule.** "Gates
every connector" = (1) the engine guarantees are gated centrally and cannot silently regress, and
(2) no connector earns the Debezium-class label without instantiating the contract in its own suite.

### Q3. How each of the four correctness properties is tested

Each is specified as: what the synthetic upstream + harness must do, the assertion, and the engine
code it exercises. Property 1 lands in DBZ-2 core as a no-crash smoke test (not handoff-atomicity
coverage — that is a connector property); 2 lands in core (the SIGKILL cases carry the real
crash-safety weight); 3 lands as an engine-guarantee slice in core with the real-partition half
deferred to the contract; 4 lands as the transport half in core with the real-DDL half deferred to
DBZ-3.

**Property 1 — snapshot->stream continuity, no gaps/dupes (invariants 1, 2, 3). No-crash smoke test.**

- _What this is, honestly._ At the engine layer the snapshot->stream transition is **not** a distinct
  crash surface: the engine stores one opaque monotone `SourceState.Position` (`source.go:394`, always
  `p[len(p)-1]`), and models both phases in one position space where K and K+1 are adjacent — there is
  no persisted "boundary" state. The real handoff-atomicity risk (two distinct position _encodings_ —
  a snapshot cursor versus a streaming LSN — plus the mode switch inside the connector) is a
  **connector** property, and is correctly deferred to DBZ-3 and the DBZ-2 contract, not tested here.
  So the no-crash base variant is a **happy-path smoke test**, not handoff-continuity coverage: with a
  deterministic in-memory synthetic source, `durableAckSeq` dedup, and a monotone position, a
  spontaneous gap or duplicate is structurally unreachable _without_ a crash, making the clean run
  near-tautological. It is kept as a cheap regression tripwire and as the fixture the SIGKILL cases
  (Property 2) build on, not as evidence that handoff is safe.
- _Harness._ Extend `chaosPlugin` to produce a fast initial "snapshot" burst (positions 1..K, minimal
  pacing, mirroring Debezium dumping a table) followed by a paced "stream" phase (K+1..N), emitting a
  `HANDOFF <pos>` marker when the producer crosses K. The marker exists for kill-timing (Property 2),
  not to signal a persisted engine state.
- _Assertion._ On a clean run, every position 1..N is committed exactly once and in order; the
  persisted resume position advances monotonically. The deterministic-payload trick (record N's
  payload = `record-N`) makes "was N delivered" a pure function of position.
- _Engine code._ `Source.Read`/`Source.Ack`, `SourceState.Position` monotonicity, persister flush.

**Property 2 — at-least-once under SIGKILL at three points (invariants 1, 2, 3).**

- _The engine exposes exactly two post-crash resume states, not three._ `loadOrCreateInstance`
  (`child.go:121-139`) resumes from either `ErrKeyNotExist` -> fresh (**empty**) or the last durably
  flushed single `SourceState.Position` (**valid-stale**). There is no third "boundary" shape, so the
  honest discriminator each case asserts via `RESUME_POSITION` is two-state: **empty** vs
  **valid-stale**. The three scenarios differ in _where in producer time_ the kill lands, not in
  producing three distinct engine states.
- _Harness._ Scenarios keyed off READ progress (never ACK), each asserting via `RESUME_POSITION` that
  it crashed in the intended producer window (empty or valid-stale):
  - _Mid-snapshot._ Kill ~30 reads into a 1ms-paced burst, before the first debounce flush (~1s), so
    Conduit has persisted _no_ position yet. Restart must not skip the streaming start point — the
    engine-side reflection of the `conduit-connector-mysql` #182 bug class (the connector-internal
    variant of which only the contract catches). Assert `RESUME_POSITION` is **empty/fresh** at kill.
  - _Mid-handoff (producer-pacing variant, NOT a distinct engine state)._ Kill just after the
    `HANDOFF` marker, when the producer has crossed K but the debounce may not yet have flushed the
    boundary position. Stated plainly: because K and K+1 are adjacent in one monotone position space,
    this lands in the **same** persisted state as either the mid-snapshot (empty) or mid-position-write
    (valid-stale) case — it does **not** exercise a distinct engine crash window. It is retained as a
    producer-timing variant that stresses the ack->persist path at the moment production changes pace,
    not as coverage of handoff atomicity (which is a connector property, deferred to DBZ-3/contract).
  - _Mid-position-write._ Kill after a valid checkpoint exists but inside a later debounce window
    (~1.4s into a 15ms-paced stream, one flush done, a second armed but not fired), so the persisted
    position is valid-but-stale. Assert `RESUME_POSITION` is a **valid** earlier position.
- _Assertion (all cases, both prune classes)._ On restart: resume position `>=` upstream committed
  watermark at kill (no gap); no `OPEN_GAP_ERROR` even against `prune=true`; no `CORRUPT_POSITION`
  (invariant 2); `DONE` reached with `committed == total` (at-least-once — duplicates fine, gaps
  never). Run against both `prune=false` and `prune=true` so the same assertion covers Kafka-like and
  Postgres-slot-like upstreams, as DBZ-1 established.
- _Engine code._ `Source.Ack`'s deferred-ack ordering, `onPersistFlushed`, `pendingAcks`/`nextAckSeq`/
  `durableAckSeq`, persister debounce and crash-safe badger write.

**Property 3 — per-partition ordering (invariant 4).**

- _Harness._ A multi-key `chaosPlugin` producing interleaved records across several partition keys,
  each key's positions monotone within the key. The synthetic upstream records, per key, the exact
  sequence of positions the plugin was acked for (a real delivery ledger, not just a max-position
  counter — max-position alone cannot detect intra-batch reordering).
- _Assertion._ The plugin receives acks in exactly the order `Source.Ack` was called (ascending
  `nextAckSeq`), one Ack-call's positions at a time; within each partition key the acked sequence is
  strictly monotone; no reorder across a debounce flush or an out-of-order flush confirmation.
- _Engine code._ `onPersistFlushed`'s head-of-queue FIFO drain by `seq`, and its documented tolerance
  of out-of-order flush confirmations (`durableAckSeq` only advances; a covered seq is a safe no-op).
- _Boundary._ The real-multi-partition-offset-monotonicity half is deferred to the DBZ-2 contract /
  DBZ-3, per Q1.

**Property 4 — DDL-mid-stream / schema drift, never silently mangled (invariant 6).**

- _Harness (transport half, DBZ-2 core) — a substantial new integration, not harness reuse._ Today's
  child (`runReadAckLoop`, `child.go:159-183`) calls `src.Read`/`src.Ack` **directly**, deliberately
  bypassing the funnel, DLQ, and any destination — so there is no routing path to assert on. Property
  4 therefore requires standing up a real `funnel.Worker` (`pkg/lifecycle-poc/funnel`) plus a synthetic
  destination and a DLQ the harness has never had, and driving a drift/poison-marked record (an
  unparseable payload or an explicit schema-change flag) through it under a configured drift/error
  policy. This is the one property that is _not_ near-verbatim reuse of the DBZ-1 harness.
- _Assertion._ The drifted record is either delivered-and-acked or routed to DLQ / halts the pipeline
  per policy, but is **never** acked upstream without being handled — no silent coercion, truncation,
  or drop at the transport layer. Under `halt`, the source position must not advance past the
  unhandled record (so a restart re-delivers it, at-least-once preserved).
- _Engine code._ `funnel.Worker.Ack` (`pkg/lifecycle-poc/funnel/worker.go:491`, which calls
  `w.DLQ.Ack`) and `funnel.Worker.Nack` (`worker.go:507`, which routes via `w.DLQ.Nack` and acks only
  the successfully-DLQ'd prefix) feeding `Source.Ack`, and position non-advancement on halt.
- _Boundary._ Real DDL emission, schema-history reconstruction, and evolve-policy application are
  deferred to DBZ-3 against real Postgres, per Q1. The transport half here is the largest single lift
  in DBZ-2 (it needs the funnel + destination + DLQ integration above), but per DeVaris's sign-off
  (2026-07-26) it lands in **Phase 1**, not last, because the AI-pipeline record-path work depends on
  this engine-side no-silent-drop gate (see Rollout and the resolved decisions).

### Q4. KC-wrapper offset-bridge + FIFO-ack coverage

**Decision: cover it engine-side by testing the exact engine contract the wrapper depends on, via the
synthetic upstream — the same move DBZ-1 made, made explicit as an assertion.** The wrapper is a
separate JVM repo with a real Postgres dependency and is not in this module; it cannot be driven from
a Go test here. But the wrapper's behavior reduces to a contract on Conduit's engine:

- The wrapper's `DefaultSourceStream.onNext` calls Debezium's `commitRecord`/`commit` — which drives
  the Postgres replication slot's `confirmed_flush_lsn` forward, irreversibly freeing WAL — **only
  after** Conduit acks the record. That is precisely the `AckPositions` message `Source.sendDeferredAck`
  emits. So `chaosPlugin.ackLoop` (which commits to `upstreamStore` on each ack) _is_ the engine-side
  stand-in for the wrapper's ack consumer, and `upstreamStore(prune=true)` _is_ the replication slot
  the wrapper's offset bridge drives.
- The wrapper's ack queue is an unbounded `ConcurrentLinkedQueue` that dequeues the head and
  **hard-errors** if the acked position does not match the head — i.e. it assumes acks arrive strictly
  FIFO, in **read/produce** order, one at a time. The roadmap flags this as a residual risk: Conduit's
  engine-side `Ack` takes a position _slice_, so if a future batching change violated strict-FIFO
  one-at-a-time delivery, the wrapper breaks. **DBZ-2 pins the engine-seam (Ack-call-order) half of
  that contract** (it is the engine-side meaning of Property 3): `onPersistFlushed`'s FIFO drain by
  `nextAckSeq` guarantees acks reach the plugin exactly once, in ascending `nextAckSeq` order, one
  `Ack`-call's positions at a time. Be precise about what the seam does _not_ pin: `nextAckSeq` is
  assigned in `Source.Ack` **call** order (`source.go:403-405`), explicitly not derived from position
  (`source.go:78-90`). The wrapper assumes **read**-order; that read-order == Ack-call-order link is
  the **funnel's** contribution (the linear single-node pipeline acks batches in the order it read
  them), not something the engine seam itself pins — it is satisfied-by-construction in the direct-ack
  chaos harness (`child.go:159-183`) and is coincident for a linear single-node pipeline, but it is a
  funnel property, not an `onPersistFlushed` guarantee. DBZ-2 pins the seam half so a `Source.Ack`
  batching change cannot silently break the wrapper; the read-order coincidence is noted as a separate,
  funnel-owned assumption.
- The **offset bridge** proper (Debezium offset <-> `SourcePosition` translation, `SimpleSourceTaskCtx`)
  is Java and lives in the wrapper repo; DBZ-2 cannot test that code. What DBZ-2 _does_ cover is the
  Conduit half the bridge relies on: an opaque `opencdc.Position` round-trips crash-safely through
  `Open` -> `Read` -> `Ack` -> persist -> restart -> `Open`, and the resume position handed back to the
  plugin on `Open` is never behind what was durably persisted. The wrapper's `DebeziumPgSourceIT` owns
  the Java-side translation; DBZ-2 owns the engine-side round-trip and ordering guarantees. Stated
  plainly so no one reads a green DBZ-2 as having tested the JVM: it has not, and by construction
  cannot; it has tested the engine contract the JVM is coded against.

## Alternatives considered

**A1. Synthetic-only (everything in `tests/chaos`, no per-connector contract).** Model DDL, schema
history, and multi-partition offsets synthetically and call it complete engine-side. _Rejected:_ it
manufactures false confidence. A synthetic schema engine proves the harness's own fake is safe, not
that a real connector's `pgoutput` reconstruction or a real Postgres slot's WAL recycling is safe. The
`conduit-connector-mysql` #182 bug was in the _connector's_ position persistence; only a real-DB
acceptance test in the connector repo catches that class end-to-end. Synthetic-only would let us
claim "Debezium-class" on the strength of a test that never touched a database.

**A2. Real-DB-required (every property gated by dockerized real databases in Conduit core).** Stand up
real Postgres/MySQL/Mongo in the core CI job and test the real connectors. _Rejected:_ it violates the
constraints and duplicates the wrong layer. It drags real-DB (and, for the wrapper, JVM) dependencies
into `conduit` core, makes the required chaos check slow and flaky, and still cannot test connectors
that live in other repos and ship on their own cadence. The engine guarantees — the highest-leverage,
all-connectors-at-once part — need no database to test, and DBZ-1 already proves that a synthetic
`upstreamStore` with a `prune` flag isolates the one variable that decides correctness more cleanly
than a real DB would.

**A3. Hybrid — engine-side core + connector-facing contract (chosen).** Engine guarantees tested
synthetically and gated centrally (fast, hermetic, all-connectors-at-once); connector- and DB-specific
properties defined as a contract and instantiated per connector against real databases (real fidelity
where it is load-bearing). This is the only split that both satisfies "engine-side (Conduit core)" as
the v0.20 deliverable _and_ "defines done for every connector" as the durable bar, without faking a
database or dragging one into core.

**A4. SDK-acceptance-extension only (no engine-side suite).** Put all CDC properties in the
`conduit-connector-sdk` acceptance suite; every connector runs them against its real DB; no
`tests/chaos` generalization. _Rejected:_ it leaves the shared engine guarantees ungated. The
Source.Ack ordering, the FIFO ack queue, and the persister debounce are _one_ code path shared by
every connector; a regression there is a single sev-0 that an SDK-side per-connector suite would catch
only redundantly, slowly, and only for connectors that happen to run the suite in CI — after the fact.
The engine seam deserves a central, fast, required gate, which is exactly what DBZ-1 established and
DBZ-2 core extends. (This alternative is the connector-facing _half_ of the chosen design, not a
substitute for the core.)

## Failure modes — how DBZ-2 avoids being a test that gives false confidence

A correctness gate that is itself wrong is worse than no gate: it launders risk into a green check.
The specific ways DBZ-2 could be wrong, and the mitigations:

- **Timing flakiness landing the kill outside the intended window (false green) or CI-slowness false
  red.** A fixed-sleep or ACK-gated kill would land unpredictably under Approach A's deferred acks.
  _Mitigation:_ reuse DBZ-1's READ-count-gated timing (`waitForReadCount`), which guarantees a minimum
  elapsed wall-clock time and never returns early; slower CI can only add delay. Each SIGKILL scenario
  additionally _asserts_ (via `RESUME_POSITION`/`HANDOFF` markers) that it crashed in its intended
  window, so a mis-timed kill fails loudly instead of passing having tested nothing.
- **Over-claiming three distinct engine crash windows when the engine exposes only two resume
  states.** The engine persists one opaque monotone position, so post-crash resume is two-state
  (**empty** vs **valid-stale**) — the mid-handoff case cannot produce a third "boundary" shape and
  necessarily collapses into one of the two (see Property 2). Pretending otherwise would be exactly
  the false confidence this section guards against. _Mitigation:_ assert only the two states the engine
  actually exposes — mid-snapshot asserts `RESUME_POSITION` **empty**, mid-position-write asserts
  **valid**, and mid-handoff is documented as a producer-pacing variant that asserts whichever of the
  two it lands in, never a fictional boundary shape. A case observing the wrong two-state value fails.
- **Position-only gap detection missing payload corruption or intra-batch reordering.** The
  deterministic-payload trick detects gaps but not reordering _within_ a monotone position range.
  _Mitigation:_ the ordering property (3) records a real per-key delivery ledger, not just a max
  position, so reordering is detectable; the drift property (4) checks routing, not just position.
- **Synthetic determinism read as connector safety.** A green DBZ-2 core proves engine guarantees,
  not that connector X is crash-safe against its real DB. _Mitigation:_ the Q1/Q2 boundary is stated
  in the suite's own `doc.go` and in the "CDC done" checklist; the Debezium-class label requires the
  per-connector contract instantiation, not just a green core suite. This document forbids reading one
  as the other.
- **Faking schema/DDL synthetically.** Covered in Q1: DBZ-2 core tests only the transport-level
  no-silent-drop half; real drift-policy application is DBZ-3 against real Postgres. A synthetic schema
  engine is explicitly out of scope precisely because it would be false confidence.
- **A gate that cannot fail.** A regression test that passes even with the bug reintroduced is inert.
  _Mitigation:_ adopt DBZ-1's discipline — every property is verified to fail without its fix
  (DBZ-1's `TestSIGKILL_PruningUpstream_NoGap` is verified to fail if the ack ordering is reverted;
  see `sigkill_test.go`). Each new DBZ-2 property ships with a documented "how this was made to fail"
  note in the PR, or it is not done.
- **Out-of-order flush confirmations breaking a naive assertion.** `onPersistFlushed` tolerates
  out-of-order flush completion (`durableAckSeq` only advances). _Mitigation:_ assertions key off
  cumulative/monotone invariants (resume `>=` watermark; per-key monotonicity), never off a specific
  per-flush arrival order, so the test does not encode an assumption the engine deliberately does not
  make.
- **Cross-process reap/zombie leaks masking a hang as a pass.** _Mitigation:_ reuse the harness's
  `reapOnce`/`t.Cleanup` reaping and `waitExit` timeout; a child that hangs fails on the exit timeout
  with full stdout/stderr diagnostics rather than silently.
- **The wrapper contract asserted too weakly.** If DBZ-2 only asserts "no gap" and not "FIFO
  one-at-a-time," a future `Source.Ack` batching change could break the wrapper while DBZ-2 stays
  green. _Mitigation:_ Property 3 / Q4 make strict-FIFO-one-Ack-call-at-a-time an explicit assertion.

## How it gates connectors + CI integration

- **CI.** DBZ-2 core is additional scenarios in the existing `tests/chaos` package, which already runs
  as the required `tests/chaos (race, x3)` status check on `main` (#2686; restructured fail-closed in
  #2690 so any inability to diff the base runs the suite rather than skipping). No new gate wiring is
  needed — DBZ-2 adds cases to an already-required, race-enabled, 3x-repeated check. The `-race` and
  3x repetition are load-bearing for a concurrency-heavy suite: they surface the flaky-under-scheduling
  failures a single clean run would hide.
- **Merge gating within v0.20.** Per the plan, chaos-CI + DBZ-2 land _before_ DBZ-3 or the
  AI-pipeline record-path work merges. Per DeVaris's sign-off (2026-07-26), the definition-of-done that
  unblocks **DBZ-3** is Properties 1 + 2 + 3 green (smoke, SIGKILL x3, FIFO-ack + per-partition
  ordering); Property 4's real-DDL half rides inside DBZ-3's own real-DB acceptance test. Property 4's
  **transport** half is not deferred — it lands in Phase 1 and is the gate the **AI-pipeline**
  record-path work depends on. See the resolved decisions.
- **Gating external connectors.** The DBZ-2 contract (checklist + SDK acceptance profile) is the bar a
  connector meets in its _own_ repo's CI to claim log-based / Debezium-class status. DBZ-3 is the first
  connector to instantiate it; the checklist is what a new CDC connector's review checks against.

## Observability

The suite's observability surface is the child's stdout marker protocol (a test-only, out-of-band
channel — it never touches `conduit run`'s real config or output surface). DBZ-1 defines
`READ`/`ACK`/`RESUME_POSITION`/`OPEN_GAP_ERROR`/`DONE`/`CORRUPT_POSITION`; DBZ-2 adds:

- `HANDOFF <pos>` — the producer crossed the snapshot->stream boundary (Property 1/2 timing + the
  mid-handoff kill point).
- `ACK_ORDER <seq> <pos>` — the exact order and per-key positions the plugin was acked for (Property 3
  ledger; lets the parent assert FIFO/monotonicity without parsing opaque positions).
- `DRIFT_ROUTED <disposition>` — a drift-marked record's disposition: `delivered` / `dlq` / `halt`
  (Property 4 transport assertion).

Markers are the same machine-parseable, unbuffered `fmt.Printf` lines DBZ-1 uses, read by the parent
over the child's stdout pipe. On any failure, the harness already dumps full stdout + stderr
diagnostics (`childProcess.diagnostics`), which DBZ-2 inherits. No production observability change is
implied; production CDC-state observability (snapshot progress, replication lag, heartbeat staleness,
schema-drift decisions on the pipeline API) is DBZ-3's acceptance criterion, not DBZ-2's.

## Rollout / phasing

Revised per DeVaris's sign-off (2026-07-26, PR #2692): the invariant-6 transport half and one
SIGTERM/invariant-7 case are pulled **into Phase 1** rather than deferred, because the AI-pipeline
record-path work also leans on the engine-side no-silent-drop gate. Phase 1 is therefore larger than
originally scoped and is the DBZ-3 merge-gate.

1. **Phase 1 (the DBZ-3 gate — everything the engine-side suite needs).** Generalize `tests/chaos`
   from one scenario family to a scenario matrix:
   - The two-phase snapshot/stream `chaosPlugin` (Property 1, no-crash pacing/smoke).
   - The mid-handoff (producer-pacing variant) and mid-position-write SIGKILL cases (Property 2,
     extending the existing mid-snapshot/mid-stream table).
   - The strict-FIFO-one-Ack-call ack-ordering assertion plus the multi-key per-partition ordering
     scenario with a real delivery ledger (Property 3, engine half + the Q4 wrapper-seam contract).
   - **The invariant-6 transport half (Property 4 transport), now in Phase 1.** This is the biggest
     single lift: it requires standing up a real `funnel.Worker` (`pkg/lifecycle-poc/funnel`) plus a
     synthetic destination and DLQ — wiring the chaos harness has never had, since the current child
     bypasses all three by calling `src.Read`/`src.Ack` directly (`child.go:159-183`). It asserts a
     drift/poison-marked record is never silently acked without being handled (delivered / DLQ'd /
     halt), never silently coerced or dropped.
   - **One SIGTERM / invariant-7 graceful-shutdown case, now in Phase 1.** Drives `Source.Teardown`'s
     flush-and-wait-then-`stopStream` ordering (`source.go:249-326`) and asserts the final deferred
     ack is sent before the stream is torn down (no dropped final ack on the graceful path), the
     complement to the SIGKILL cases on the crash path.
   Property 1+2+3 green is the definition-of-done that unblocks DBZ-3 (see the resolved open
   questions); the Property 4 transport half lands in the same phase and gates the AI-pipeline
   record-path work.
2. **Parallel, owned with DBZ-3 — the DBZ-2 contract.** The connector-facing "CDC done" definition:
   the SDK acceptance-suite extension in `conduit-connector-sdk` for native connectors plus the prose
   checklist for the JVM KC-wrapper, with the first real instantiation (and Property 4's real-DDL
   half) landing inside DBZ-3's own real-Postgres acceptance test.

Each phase ships with its verified-to-fail-without-the-fix note and updates the suite `doc.go` to keep
the synthetic-vs-real boundary stated at the source.

## Resolved decisions (DeVaris sign-off, 2026-07-26, PR #2692)

All four open questions were answered at sign-off. Recorded here so the doc matches what we build.

1. **Property 4 (invariant-6 transport half) scope — RESOLVED 2026-07-26: build it in Phase 1, do not
   defer.** DeVaris pulled the transport-level no-silent-drop assertion into the initial
   implementation, explicitly because the AI-pipeline record-path work also depends on that engine-side
   gate. Phase 1 therefore stands up the `pkg/lifecycle-poc/funnel` Worker + DLQ + a synthetic
   destination in the harness — the substantial new integration, not harness reuse. (Only the
   _real-DDL_ half stays deferred to DBZ-3's real-DB acceptance test.)
2. **Where the DBZ-2 contract ships — RESOLVED 2026-07-26: the SDK acceptance suite for native
   connectors + a prose checklist for the JVM KC-wrapper.** The reusable "CDC done" profile is an
   extension of the `conduit-connector-sdk` acceptance suite (already the compatibility contract per
   `CLAUDE.md`); the wrapper, which cannot import a Go harness, satisfies a prose checklist verified
   against its own `DebeziumPgSourceIT`. Not a new CDC-specific acceptance package.
3. **Definition-of-done that unblocks DBZ-3 — RESOLVED 2026-07-26: Properties 1 + 2 + 3 green.**
   Crash-safety (SIGKILL x3), ack-FIFO, and per-partition ordering green is the DBZ-3 merge-gate.
   Property 4's real-DDL half rides inside DBZ-3's own real-Postgres acceptance test rather than
   blocking the gate; the Property 4 _transport_ half still lands in Phase 1 (decision 1) and gates the
   AI-pipeline record-path work.
4. **SIGTERM / invariant-7 coverage — RESOLVED 2026-07-26: add one graceful-shutdown case, in Phase
   1.** DBZ-2 covers `Source.Teardown`'s flush-and-wait-then-`stopStream` ordering
   (`source.go:249-326`) with a single SIGTERM/Teardown scenario asserting the final deferred ack is
   sent before teardown — the graceful-path complement to the SIGKILL crash-path cases, not left to
   `pkg/connector` unit tests alone.

## Related

- `docs/design-documents/20260722-debezium-compete-roadmap.md` — the DBZ epic; DBZ-2's authoritative
  scope and its position as the gate for DBZ-3/5/7/8/9/10.
- `tests/chaos` (`doc.go`, `harness.go`, `upstream.go`, `child.go`, `sigkill_test.go`) — DBZ-1, the
  harness DBZ-2 generalizes.
- `docs/design-documents/20260723-source-ack-persist-ordering-fix.md` — the Source.Ack sev-0 fix
  (Approach A) that introduced the FIFO ack queue DBZ-2 pins; the de facto DBZ-1 design record.
- `docs/postmortems/20260723-source-ack-persist-ordering.md` — the sev-0 the chaos test caught.
- `pkg/connector/source.go`, `pkg/connector/persister.go` — the engine data path DBZ-2 exercises.
- `docs/design-documents/20260723-partition-claims-protocol.md` — why per-partition ordering here is a
  single-node engine guarantee, not a distributed one.
- `docs/design-documents/20260715-dlq-record-visibility.md` — the deferred Tier-1 DLQ record-store gap
  DBZ-2's invariant-6 property inherits rather than fills (#2640).
- `conduitio-labs/conduit-connector-mysql` #182 — the real snapshot-mid-kill data-loss bug DBZ-2
  generalizes into an all-connectors property.
- `ConduitIO/conduit-kafka-connect-wrapper` (`DefaultSourceStream`, `SimpleSourceTaskCtx`,
  `SourcePosition`, `DebeziumPgSourceIT`) — the wrapper whose offset bridge + FIFO ack queue DBZ-2
  covers engine-side.
