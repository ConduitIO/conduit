# Debezium-compete: CDC parity roadmap

## Summary

Conduit is closer to Debezium on database _coverage_ than the public roadmap implies, but not at
parity on _mechanism_, _depth_, or _maturity_. This epic sequences the work to reach credible,
defensible CDC parity. It is organized around two tracks that run in parallel — **coverage via the
existing Kafka Connect wrapper** (fast credibility) and **native log-based depth** (the durable
moat) — gated by a **CDC correctness suite** that lets us earn the "Debezium-class" claim rather
than assert it.

Track A is not a build-from-scratch item: `ConduitIO/conduit-kafka-connect-wrapper` already exists,
already runs a real Debezium connector end-to-end under CI, and is already linked from the main
README. This revision replaces the earlier "build a wrapper" framing with what the wrapper actually
does today, what it proves, and — critically — what it does not yet prove for the databases the
pitch depends on most. See [The two tracks](#the-two-tracks).

The epic does not stand apart from the roadmap: it threads through Phase 1 (revival credibility) and
Phase 2 (the CDC moat + Kafka Connect migration), and it re-scopes the thin Phase 2 CDC checklist
into the real task list. See the sequencing table in [Placement in the roadmap](#placement-in-the-roadmap).

## Context — where we actually stand (verified 2026-07-22)

Assessment was done by reading each connector's actual CDC mechanism, not its description. The
distinction that matters is **log-based CDC** (binlog / oplog / logical replication / LogMiner —
what Debezium does) versus **trigger/polling-based CDC** (a tracking table populated by triggers,
then polled). Trigger-based works, but it adds write amplification on the source database, cannot
capture history from before the trigger was installed, and requires manual tracking-table schema
sync — an experienced Debezium user will reject it on sight.

| Database | Conduit connector | Mechanism | Log-based (Debezium-class)? | Maturity |
| --- | --- | --- | --- | --- |
| PostgreSQL | `ConduitIO/conduit-connector-postgres` v0.14 | Logical replication (slot + publication) | Yes | Most mature |
| MySQL | `conduitio-labs/conduit-connector-mysql` v0.2 | Row-based binlog | Yes | Early |
| MongoDB | `conduitio-labs/conduit-connector-mongo` v0.2 | Change streams | Yes | Early |
| Vitess | `conduitio-labs/conduit-connector-vitess` v0.1 | VStream | Yes | Alpha |
| SQL Server | `conduitio-labs/conduit-connector-sql-server` v0.1 | Triggers + tracking table + polling | No | Early |
| Oracle | `conduitio-labs/conduit-connector-oracle` | Triggers + tracking table + polling | No | Very early |
| Db2 | `conduitio-labs/conduit-connector-db2` | Triggers + tracking table + polling | No | Pre-release |
| Spanner | `conduitio-labs/conduit-connector-spanner` | CDC "planned" — snapshot only | No | Pre-release |
| Cassandra | `conduitio-labs/conduit-connector-cassandra` | Sink-only, no CDC source | No | v0.1 |

**Cross-cutting gaps versus Debezium, independent of database:**

1. **Schema / DDL evolution.** No Conduit CDC connector auto-handles DDL changes; several require
   stopping the pipeline and hand-editing a tracking table. Debezium has schema history + DDL parsing.
   This is the single largest universal gap.
2. **Incremental (signal-based) snapshots.** Some connectors have resumable snapshots (MySQL, Mongo);
   none has Debezium's ad-hoc incremental re-snapshot (re-snapshot a table without stopping the stream).
3. **Heartbeats.** Not surfaced. On low-traffic Postgres this bloats the WAL / replication slot — a
   real production footgun Debezium solved years ago.
4. **Transaction metadata / boundaries.** Absent. Debezium emits it — and even Debezium's own
   transaction metadata is boundary information (begin/end + affected collections), not a
   cross-destination atomic commit. Conduit's DBZ-3 scope carries the same limitation, stated
   plainly rather than implied to be more than it is.
5. **Outbox pattern.** Debezium's `EventRouter` owns the microservices-CDC use case; no Conduit equivalent.
6. **Production maturity.** Most log-based labs connectors are v0.1–v0.2 with the edge-case hardening
   (failover, TOAST columns, replica identity, large transactions, connection recovery) still to come.

**Honest read.** Coverage is roughly at parity (9+ of Debezium's ~10 databases). Mechanism is at
parity on Postgres / MySQL / Mongo / Vitess only. Depth and production maturity are not at parity.
The credible external claim today is _"broad coverage, log-based like Debezium on Postgres/MySQL/Mongo"_ —
not _"at parity."_ Claiming parity to an Oracle or SQL Server shop would burn credibility, which is
the opposite of what this epic exists to rebuild.

**Positioning note (locked claim, not an open question).** One asset the matrix above doesn't
capture: Conduit already covers the exact standalone-process use case **Debezium Server** and
**Debezium Engine** exist to serve — running CDC without a Kafka Connect cluster, either as a
lightweight standalone binary or embedded in your own process. This is a settled `vs Debezium`
comparison-page claim, not a question to resolve later: `conduit run` ships that standalone binary
today, and the embedding path is `libconduit` (the Go embedding surface works today; the stable,
semver-committed API is the embed-v1 productization of that same already-working path). State it
plainly on the DBZ-0 page — it names both Debezium products directly and it requires no new
engineering to claim, because the capability already ships.

**Fix-in-passing.** The `conduitio-labs/conduit-connector-sql-server` README has a copy-paste bug —
it labels itself "DB2" in at least one section. If DBZ-0's comparison page cites connector docs as
evidence, fix this first; citing a mislabeled README undercuts the "we verified this by reading the
actual code" credibility the whole audit rests on.

## Goals / Non-goals

**Goals.**

- A credible, honest CDC parity story we can put in front of a Debezium user — including one who left.
- Log-based mechanism on the tier-1 databases; the hard ones covered by the KC wrapper until native lands.
- Universal CDC capabilities (schema evolution, incremental snapshot, heartbeats, transaction metadata)
  built once in the connector SDK so every native CDC connector inherits them.
- A CDC correctness suite that gates every connector and _earns_ the "Debezium-class" claim.
- Operator visibility into CDC state (snapshot progress, replication lag, heartbeat staleness,
  schema-drift decisions) as a first-class API surface, not an afterthought bolted on after the
  connectors ship. See [Operator UX and observability](#operator-ux-and-observability).

**Non-goals.**

- Rebuilding every Debezium connector natively before shipping any value (that is the years-long trap).
- Claiming a parity we do not have.
- A bespoke CDC/transformation DSL (transformations stay real-language code — a standing principle).
- Distributed-snapshot / event-time-watermark machinery (state-layer discipline; that is the Flink line).
- Treating the KC wrapper as a permanent substitute for native depth on the databases where it
  matters most (see the Track-A checkpoint in [Failure modes / risks](#failure-modes--risks)).

## The two tracks

**Track A — Validate and harden the existing Kafka Connect wrapper (fast credibility).**
`ConduitIO/conduit-kafka-connect-wrapper` already exists: a Java, out-of-process Conduit plugin that
speaks gRPC to the engine and loads Kafka Connect connector JARs from a `libs/` directory at
runtime. It is **not** a JVM embedded in the core binary and **not** a Kafka Connect worker cluster —
it's a single sidecar process per connector, which is the architecture the "drop the cluster" pitch
needs and already has.

What's actually proven today, from reading the wrapper's source and tests:

- **Debezium-Postgres works and is CI-tested.** `DebeziumPgSourceIT` runs Debezium's real Postgres
  connector against a dockerized Postgres instance on every PR, through a `DebeziumToOpenCDC`
  envelope translator. This is not a PoC claim — it is a passing, gated integration test today.
- **Offset↔position is correctly designed.** The wrapper implements the bare Kafka Connect
  `SourceTaskContext` SPI itself (`SimpleSourceTaskCtx`), backing offsets with Conduit's own
  `SourcePosition` rather than a Kafka Connect `OffsetBackingStore`. Ack ordering is correct:
  `DefaultSourceStream.onNext` only calls Debezium's `commitRecord()` / `commit()` after Conduit
  acks the record — the invariant-1 ordering (never ack upstream before durable downstream
  handling) is upheld, and on Postgres this is what drives replication-slot LSN confirmation. An
  earlier version of this analysis described an "async `OffsetBackingStore` trap" here; that
  framing does not apply to this wrapper and is retracted.
- **The strategic inversion — the most important finding.** The wrapper has, to date, only ever
  run Debezium **Postgres** in its test suite — the one database where we need it _least_, because
  a native log-based Postgres connector already exists. Debezium's **MySQL, Oracle, SQL Server, and
  Db2** connectors — the ones Track A's "instant coverage" pitch actually depends on — all require a
  durable **schema-history store** (Debezium's internal record of DDL over time, used to interpret
  historical binlog/redo entries). That store is **not wired up or tested** anywhere in the wrapper
  today. So "instant Oracle/SQL Server/Db2 CDC via the wrapper" is **unproven engineering**, not an
  instant win — it is real, scoped work (DBZ-7), not a checkbox.
- **Residual risks, named explicitly:**
  1. The ack path assumes acks arrive strictly FIFO, in read order, over an unbounded
     `ConcurrentLinkedQueue`: `onNext` dequeues the head and hard-errors if the acked position
     doesn't match it. Conduit's engine-side `Ack()` takes a position _slice_
     (`pkg/connector/source.go`), so DBZ-1 must verify the engine actually delivers acks
     one-at-a-time, in strict read order, to standalone connectors — if a future batching change
     violates that, the wrapper breaks silently on error, not silently on correctness, but breaks.
  2. **SMT gap.** There is no Single Message Transform pipeline anywhere in the wrapper's source
     tree. Any non-unwrap SMT a Kafka Connect user relies on today (routing, masking, custom
     transforms) silently does not run when their connector moves to the wrapper. This is a
     migration-fidelity hole, not a cosmetic one — a KC migration path that silently drops
     transform behavior is exactly the kind of thing `conduit migrate kafka-connect`'s
     compatibility-report requirement (see `CLAUDE.md`) exists to prevent, and the wrapper needs
     the same discipline.
  3. **Packaging.** JDK 20, Unix-only, and a manually populated `libs/` directory with no packaged
     or containerized artifact. "Drop the cluster" is, today, actually "swap the cluster for a
     manual JVM build you assemble yourself." That's a real improvement over running a Kafka
     Connect worker, but it isn't yet the polished distribution a "supported" claim implies.

Track A's job, restated: prove the ack/crash-safety story on the one connector that already works
(DBZ-1), then do the real, scoped engineering to extend that proof to the databases that actually
need a wrapper (DBZ-7) — never claim the second thing is already true because the first thing is.

**Track B — Native log-based depth (the durable moat).** Deepen the connectors where we already own
the right mechanism (Postgres, MySQL, Mongo), invest the universal CDC capabilities into the
connector SDK, then replace the trigger-based connectors with native log-based over time. Native =
no JVM, single binary, agent-legible — the actual differentiation. The SDK capabilities built here
(DBZ-4) are native-Go-only; they do not flow to the Java wrapper, whose schema-history story is a
separate, JVM-side problem (see DBZ-7).

The tracks are complementary: A buys coverage and credibility now, bounded by what's actually
proven; B builds the moat that makes Conduit worth choosing over Debezium in the long run.

## The epic — workstreams

Each workstream is sized to be independently shippable and independently reviewable. IDs are for
sequencing reference, not a numbering scheme in the repo.

| ID | Workstream | Track | Summary |
| --- | --- | --- | --- |
| DBZ-0 | CDC parity audit + honest `vs Debezium` comparison page | — | Publish the matrix above as an honest comparison doc — the messaging asset for the revival, including the embeddability positioning note. Fix the SQL Server README mislabel before citing it as evidence. Low effort, high credibility. Feeds the roadmap's Documentation track. |
| DBZ-1 | Validate the existing wrapper against Debezium-Postgres | A | The `conduit-kafka-connect-wrapper` already passes `DebeziumPgSourceIT`. Add a `kill -9` crash-safety test on top of it, and verify the engine delivers acks strictly FIFO/in-order to standalone connectors (the wrapper's ack queue assumes this and hard-errors otherwise). Small — days, not weeks. The offset↔position bridge is Tier-1 (invariant 2) and gets the full Tier-1 review bar despite the wrapper already existing. |
| DBZ-2 | CDC acceptance-test suite | — | Snapshot→stream handoff with no gaps/dupes, at-least-once under SIGKILL (`kill -9`, not graceful shutdown) mid-snapshot / mid-stream-handoff / mid-position-write, per-partition ordering, and a DDL-mid-stream test proving schema drift is never silently mangled (invariant 6). Covers the wrapper's offset bridge and FIFO ack queue on the Debezium-Postgres path, not just native connectors. Defines "done" for every CDC connector and is the gate that earns the "Debezium-class" claim. |
| DBZ-3 | Harden Postgres to real parity | B | Incremental/resumable snapshot, heartbeats (WAL-bloat fix), DDL/schema evolution, transaction-boundary metadata (boundary markers, not cross-destination atomic commit), lock-light snapshot, failover hardening. Ships snapshot progress, replication lag, heartbeat staleness, and schema-drift decisions on the pipeline API (see Operator UX). The flagship — deepest first, and the first concrete implementation. |
| DBZ-4 | Shared CDC orchestration primitives in the connector SDK | B | **Not** a universal DDL/schema-evolution framework — DDL is inherently per-dialect (MySQL parses binlog SQL text; Oracle LogMiner reads redo; Postgres `pgoutput` carries no DDL at all and reconstructs from catalog snapshots). Scoped to: shared orchestration plumbing (heartbeat scheduling, transaction-boundary envelope, incremental-snapshot state machine) plus a per-connector `SchemaChangeAdapter` interface with shared history-storage/versioning plumbing behind it. Extracted _after_ two concrete connectors exist (DBZ-3, DBZ-5), not designed speculatively ahead of them. Native-Go-only; does not apply to the Java wrapper. May ship as independently reviewable sub-items: 4a heartbeat plumbing, 4b transaction-boundary metadata, 4c incremental-snapshot state machine, 4d `SchemaChangeAdapter` interface. |
| DBZ-5 | Harden MySQL + MongoDB as the second concrete implementation | B | They already have the right mechanism (binlog, change streams). Hardens them to flagship quality and, in doing so, is the second real data point DBZ-4 needs to know which Postgres plumbing generalizes and which is dialect-specific — sequenced before DBZ-4, not after. Credible flagships #2 and #3. |
| DBZ-6 | Outbox-pattern processor | B | Reclaims the microservices-CDC use case Debezium's `EventRouter` owns. |
| DBZ-7 | Extend the wrapper to schema-history-store connectors + close the migration-fidelity gaps | A | Extends the wrapper to the Debezium connectors that actually need it and don't yet work: MySQL/Oracle/SQL Server/Db2, all of which require wiring up and testing a durable schema-history store that today doesn't exist in the wrapper. Also resolves the SMT gap (no transform chain runs today) and ships a packaged/containerized distribution (closes wrapper issues #7 "single binary for each platform", #19 "save schema info to record metadata", #52 "automate release"). This is real engineering, not a polish pass on DBZ-1 — see the confidence-tier split below. Must not get a lower correctness bar than the native connectors it's meant to backstop. |
| DBZ-8 | Native log-based SQL Server | B | Replace the trigger/polling connector with SQL Server native CDC. |
| DBZ-9 | Native LogMiner Oracle | B | The crown jewel and the hardest. The wrapper (DBZ-7) covers Oracle until this lands — reassess the investment case at the Phase 3 checkpoint (see Failure modes). |
| DBZ-10 | Native Db2 / Spanner change-streams / Cassandra commitlog CDC | B | Round out native coverage for the remaining trigger-based / missing sources. Same Phase 3 checkpoint applies. |

**Wrapper coverage confidence tiers.** DBZ-1 and DBZ-7 cover very different risk profiles and should
never be described with the same confidence level:

| Tier | Databases | Status | What's actually verified |
| --- | --- | --- | --- |
| Proven, low incremental value | Debezium-Postgres via wrapper | CI-tested today (`DebeziumPgSourceIT`) | Envelope translation, ack ordering, offset↔position bridge — on the one database where native already covers us. |
| Unproven, real engineering | Debezium-{MySQL, Oracle, SQL Server, Db2} via wrapper | Not wired, not tested | Schema-history store integration, SMT support, packaging — the actual "instant coverage" pitch. Scoped as DBZ-7, not assumed. |

## Placement in the roadmap

The epic slots into the existing phase structure. Dependencies are load-bearing: the correctness
suite (DBZ-2) gates the connector work that follows, and the DBZ-3 → DBZ-5 → DBZ-4 order reflects
that shared plumbing is extracted from two real implementations, not designed ahead of them.

| Workstream | Roadmap slot | Depends on | Why here |
| --- | --- | --- | --- |
| DBZ-0 | Phase 1 (Documentation track) | — | Credibility asset; ships immediately, near-zero engineering. |
| DBZ-1 | Phase 1 tail / Phase 2 (KC migration) | — | Revival-credibility lever; `STRATEGY.md` explicitly wants a scrappy KC-wrapper validation surfaced early. |
| DBZ-2 | Phase 1 tail → Phase 2 | — | Defines "done" for all CDC; must exist before hardening claims correctness. |
| DBZ-3 | Phase 2 (CDC moat) | DBZ-2 | Postgres is the flagship and the first concrete implementation; re-scopes the roadmap's single "PostgreSQL (harden existing)" line. Does **not** wait on DBZ-4 — there is no shared-SDK dependency yet because nothing has been extracted. |
| DBZ-4 | Phase 2 (CDC moat) | DBZ-2, DBZ-3, DBZ-5 | Cross-cutting leverage extracted once two connectors (Postgres, then MySQL/Mongo) prove what's actually shared versus dialect-specific. |
| DBZ-5 | Phase 2 (CDC moat) | DBZ-2, DBZ-3 | Right mechanism already; hardening is the work, and it's the second implementation DBZ-4 needs before generalizing. |
| DBZ-6 | Phase 2 (Enterprise correctness) | — | Independent; pairs with the schema/DLQ features already in Phase 2. |
| DBZ-7 | Phase 2 (KC migration) | DBZ-1, DBZ-2 | Extends the wrapper to the hard databases; must clear the same correctness bar as native connectors, not a lower one, because it's covering the databases with the least native depth. |
| DBZ-8 | Phase 2 / 3 boundary | DBZ-2, DBZ-4 | Hard, long-lead native CDC; wrapper covers it meanwhile. |
| DBZ-9 | Phase 3 | DBZ-2, DBZ-4 | Hardest native connector; not on the critical path while DBZ-7 covers Oracle. Checkpoint: revisit at Phase 3 kickoff (see Failure modes). |
| DBZ-10 | Phase 3 | DBZ-2, DBZ-4 | Completes native coverage. Same checkpoint applies. |

**Critical path to credible parity messaging (the near-term goal):** DBZ-0 → DBZ-1 → DBZ-2 → DBZ-3.
That sequence produces (1) an honest story, (2) a validated "Debezium-in-Conduit" demo on the one
path that already works, (3) a correctness bar, and (4) one flagship native connector at real
parity — enough to credibly re-engage the developers who left, without overclaiming. This is now
consistent with the dependency table above: DBZ-3 depends only on DBZ-2, so it is not blocked on
DBZ-4/DBZ-5 work that comes later in Phase 2.

## Operator UX and observability

Debezium users expect to _see_ CDC state, not just receive records: snapshot progress, replication
lag, heartbeat staleness, and schema-drift decisions are all things a Debezium operator checks
routinely. This epic was originally silent on that gap; it isn't anymore.

- **API surface.** DBZ-3 and DBZ-4 ship these as explicit acceptance criteria, not follow-on work:
  snapshot progress, replication lag, heartbeat staleness, and schema-drift decisions are exposed
  through the pipeline API. Per the "errors are API" and "no divergent CLI/UI/MCP logic" rules in
  `CLAUDE.md`, the same API surface is consumed by `conduit pipeline inspect` on the CLI and is
  queued for the built-in UI's Phase-2 schema-drift/replay slice — one code path, multiple
  front-ends.
- **Incremental-snapshot operator UX.** A CLI verb / API to trigger an ad-hoc re-snapshot (the
  Debezium incremental-snapshot capability from the Context gaps list) ships with `--json` output
  and stable error codes, matching every other new CLI surface.
- **DLQ Tier-1 gap inheritance.** `docs/design-documents/20260715-dlq-record-visibility.md`
  (issue #2640 filed alongside it) already decided that the engine keeps no queryable store of
  dead-lettered record _content_ — v0.18 ships a config-only DLQ view, and a real bounded,
  crash-safe, queryable DLQ record store is deferred as Tier-1 data-path work requiring its own
  design doc, ADR, and sign-off. CDC dead-letters (malformed DDL, schema-drift rejects, poison
  records from a corrupt binlog entry) inherit that exact same gap — this epic does not get to
  quietly build a CDC-specific dead-letter store as a side effect of DBZ-3/DBZ-4. See
  [Failure modes / risks](#failure-modes--risks).
- **Runbook stub.** A `docs/operations/` runbook entry is owed for two CDC-specific alertable
  conditions this epic introduces: replication-slot bloat (symptom: growing WAL / slot lag on
  Postgres) and heartbeat staleness (symptom: no heartbeat observed within N intervals). Both need
  a symptom → diagnosis → remediation entry before DBZ-3 ships, per the existing runbook standard.

## Proposed ROADMAP.md delta (Phase 2 — CDC "the moat")

The current Phase 2 CDC section understates the work with a five-line database checklist. Replace it
(when the roadmap-reconciliation PR settles, to avoid a conflicting edit) with a structure that
distinguishes mechanism, depth, and coverage:

```text
### CDC (the moat)

Production-grade change data capture — snapshot + streaming, schema evolution, heartbeats,
transaction metadata, tombstones, operator-facing observability — proven by the CDC acceptance
suite:

- [ ] CDC acceptance-test suite (snapshot->stream no-gap/no-dup, at-least-once under kill -9,
      ordering, DDL-mid-stream schema-mangling check)
- [ ] PostgreSQL -- harden to full parity (log-based already; incremental snapshot, heartbeats,
      DDL evolution, tx metadata, operator API surface)
- [ ] MySQL -- harden (binlog already; second implementation feeding shared SDK plumbing)
- [ ] MongoDB -- harden (change streams already; second implementation feeding shared SDK plumbing)
- [ ] Shared CDC orchestration primitives in the connector SDK (extracted from Postgres + MySQL/Mongo)
- [ ] Validate + harden the existing Kafka Connect wrapper against Debezium-Postgres (kill -9,
      ack ordering)
- [ ] Extend the wrapper to Debezium connectors needing a schema-history store
      (MySQL/Oracle/SQL Server/Db2), close the SMT gap, ship a packaged distribution
- [ ] Native log-based SQL Server (replace trigger-based)
- [ ] Native LogMiner Oracle (replace trigger-based)
- [ ] Native Db2 / Spanner change-streams / Cassandra commitlog CDC
- [ ] Outbox-pattern processor
```

## Failure modes / risks

- **The JVM shows up in packaging, not architecture.** The wrapper is already an out-of-process,
  opt-in sidecar plugin over gRPC — that architectural decision is already made and is sound; there
  is no embedded-vs-sidecar question left to litigate with a fresh ADR. The real gap is packaging:
  JDK 20, Unix-only, a hand-populated `libs/` directory, no container image. Mitigation: DBZ-7 ships
  a packaged/containerized distribution before any "supported" claim is made for the wrapper —
  that's the actual fix, not messaging discipline about a JVM that was never going to be in the
  core binary.
- **Shipping trigger-based CDC as "parity."** It is not; an experienced user will call it out. The
  matrix and DBZ-0 keep the claim honest; native/wrapper replaces trigger-based over time.
- **Correctness debt.** CDC is squarely on the data path (invariants 1–7). Without DBZ-2 gating, we
  ship the exact crash/failover bugs Debezium spent years fixing. DBZ-2 is sequenced before any
  hardening claims correctness for this reason, and now explicitly covers the wrapper's ack/offset
  path, not just native connectors.
- **Spreading thin across ten databases.** Mitigation: depth on the flagship (Postgres) and the two
  right-mechanism connectors (MySQL, Mongo) first; the wrapper carries breadth so native work stays focused.
- **The wrapper quietly becoming the permanent answer.** If DBZ-7 lands and works well enough,
  there's a real risk that native Oracle/SQL Server/Db2 (DBZ-9/DBZ-10) simply never gets funded,
  and the durable native moat this epic is supposed to build never materializes for the hardest
  databases. Mitigation: DBZ-9/DBZ-10 investment is explicitly revisited at Phase 3 kickoff, as a
  deliberate decision rather than a default — not assumed to proceed just because it's on the
  table, and not assumed to be dropped just because the wrapper is covering the gap adequately in
  the meantime.
- **CDC dead-letters have nowhere better to go than the rest of the engine.** See
  [Operator UX and observability](#operator-ux-and-observability) — this epic inherits the
  deferred Tier-1 DLQ record-store gap rather than building a parallel one.

## Related

- `ROADMAP.md` — Phase 2 (CDC moat, Kafka Connect migration), Documentation track (`vs Debezium` page)
- `STRATEGY.md` — the two bets (KC migration path + Debezium-class CDC); the KC-wrapper credibility note
- `CLAUDE.md` — data-integrity invariants 1–7 (the correctness bar CDC must meet)
- `docs/design-documents/20260704-phase-1-execution-plan.md` — phase-execution precedent
- `docs/design-documents/20260715-dlq-record-visibility.md` — deferred Tier-1 DLQ record-store
  decision that CDC dead-letters inherit (issue #2640)
- `ConduitIO/conduit-kafka-connect-wrapper` — the existing wrapper this epic validates and extends
  (`DebeziumPgSourceIT`, `SimpleSourceTaskCtx`, `SourcePosition`, `DefaultSourceStream`)
