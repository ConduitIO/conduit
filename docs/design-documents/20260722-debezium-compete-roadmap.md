# Debezium-compete: CDC parity roadmap

## Summary

Conduit is closer to Debezium on database _coverage_ than the public roadmap implies, but not at
parity on _mechanism_, _depth_, or _maturity_. This epic sequences the work to reach credible,
defensible CDC parity. It is organized around two tracks that run in parallel — **coverage via a
Kafka Connect wrapper** (fast credibility) and **native log-based depth** (the durable moat) — gated
by a **CDC correctness suite** that lets us earn the "Debezium-class" claim rather than assert it.

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
4. **Transaction metadata / boundaries.** Absent. Debezium emits it.
5. **Outbox pattern.** Debezium's `EventRouter` owns the microservices-CDC use case; no Conduit equivalent.
6. **Production maturity.** Most log-based labs connectors are v0.1–v0.2 with the edge-case hardening
   (failover, TOAST columns, replica identity, large transactions, connection recovery) still to come.

**Honest read.** Coverage is roughly at parity (9+ of Debezium's ~10 databases). Mechanism is at
parity on Postgres / MySQL / Mongo / Vitess only. Depth and production maturity are not at parity.
The credible external claim today is _"broad coverage, log-based like Debezium on Postgres/MySQL/Mongo"_ —
not _"at parity."_ Claiming parity to an Oracle or SQL Server shop would burn credibility, which is
the opposite of what this epic exists to rebuild.

## Goals / Non-goals

**Goals.**

- A credible, honest CDC parity story we can put in front of a Debezium user — including one who left.
- Log-based mechanism on the tier-1 databases; the hard ones covered by the KC wrapper until native lands.
- Universal CDC capabilities (schema evolution, incremental snapshot, heartbeats, transaction metadata)
  built once in the connector SDK so every native CDC connector inherits them.
- A CDC correctness suite that gates every connector and _earns_ the "Debezium-class" claim.

**Non-goals.**

- Rebuilding every Debezium connector natively before shipping any value (that is the years-long trap).
- Claiming a parity we do not have.
- A bespoke CDC/transformation DSL (transformations stay real-language code — a standing principle).
- Distributed-snapshot / event-time-watermark machinery (state-layer discipline; that is the Flink line).

## The two tracks

**Track A — Coverage via the Kafka Connect wrapper (fast credibility).** Debezium connectors _are_
Kafka Connect connectors. A wrapper that runs Debezium JARs inside Conduit gives real LogMiner Oracle,
native SQL Server CDC, and Db2 log-based CDC — instantly, and without the JVM Kafka Connect worker
cluster. This is the direct answer to a defected Debezium user (_"keep your Debezium connectors, drop
the cluster"_), and it doubles as the Kafka Connect migration story already in Phase 2. The JVM
footprint contradicts the "no JVM" pitch, so it is framed as a **migration bridge**, not the end state.

**Track B — Native log-based depth (the durable moat).** Deepen the connectors where we already own
the right mechanism (Postgres, MySQL, Mongo), invest the universal CDC capabilities into the SDK, then
replace the trigger-based connectors with native log-based over time. Native = no JVM, single binary,
agent-legible — the actual differentiation.

The tracks are complementary: A buys coverage and credibility now; B builds the moat that makes Conduit
worth choosing over Debezium in the long run.

## The epic — workstreams

Each workstream is sized to be independently shippable and independently reviewable. IDs are for
sequencing reference, not a numbering scheme in the repo.

| ID | Workstream | Track | Summary |
| --- | --- | --- | --- |
| DBZ-0 | CDC parity audit + honest `vs Debezium` comparison page | — | Publish the matrix above as an honest comparison doc — the messaging asset for the revival. Low effort, high credibility. Feeds the roadmap's Documentation track. |
| DBZ-1 | KC-wrapper PoC | A | Run one real Debezium connector (Postgres or MySQL) end-to-end inside Conduit, with a runnable demo. The single highest-leverage credibility item; proves coverage + migration at once. |
| DBZ-2 | CDC acceptance-test suite | — | Snapshot→stream handoff with no gaps/dupes, at-least-once under crash/failover, per-partition ordering. Defines "done" for every CDC connector and is the gate that earns the "Debezium-class" claim. |
| DBZ-3 | Harden Postgres to real parity | B | Incremental/resumable snapshot, heartbeats (WAL-bloat fix), DDL/schema evolution, transaction metadata, lock-light snapshot, failover hardening. The flagship — deepest first. |
| DBZ-4 | Universal CDC capabilities in the connector SDK | B | Schema/DDL-evolution framework, signal-based incremental-snapshot primitive, heartbeat support, transaction-boundary metadata — built once so every native CDC connector inherits them. |
| DBZ-5 | Harden MySQL + MongoDB on the DBZ-4 capabilities | B | They already have the right mechanism (binlog, change streams); bring them to flagship quality. Credible flagships #2 and #3. |
| DBZ-6 | Outbox-pattern processor | B | Reclaims the microservices-CDC use case Debezium's `EventRouter` owns. |
| DBZ-7 | Productionize the KC wrapper | A | PoC → supported path for running Debezium connectors (Oracle / SQL Server / Db2 log-based) in Conduit. The coverage backstop while native versions are years out. |
| DBZ-8 | Native log-based SQL Server | B | Replace the trigger/polling connector with SQL Server native CDC. |
| DBZ-9 | Native LogMiner Oracle | B | The crown jewel and the hardest. The KC wrapper (DBZ-7) covers Oracle until this lands. |
| DBZ-10 | Native Db2 / Spanner change-streams / Cassandra commitlog CDC | B | Round out native coverage for the remaining trigger-based / missing sources. |

## Placement in the roadmap

The epic slots into the existing phase structure. Dependencies are load-bearing: the correctness
suite (DBZ-2) and the SDK capabilities (DBZ-4) gate the connector work that follows.

| Workstream | Roadmap slot | Depends on | Why here |
| --- | --- | --- | --- |
| DBZ-0 | Phase 1 (Documentation track) | — | Credibility asset; ships immediately, near-zero engineering. |
| DBZ-1 | Phase 1 tail / Phase 2 (KC migration) | — | Revival-credibility lever; `STRATEGY.md` explicitly wants a scrappy KC-wrapper PoC surfaced early. |
| DBZ-2 | Phase 1 tail → Phase 2 | — | Defines "done" for all CDC; must exist before hardening claims correctness. |
| DBZ-3 | Phase 2 (CDC moat) | DBZ-2, DBZ-4 | Postgres is the flagship; re-scopes the roadmap's single "PostgreSQL (harden existing)" line. |
| DBZ-4 | Phase 2 (CDC moat) | DBZ-2 | Cross-cutting leverage — every later native connector inherits it. |
| DBZ-5 | Phase 2 (CDC moat) | DBZ-2, DBZ-4 | Right mechanism already; hardening is the work. |
| DBZ-6 | Phase 2 (Enterprise correctness) | — | Independent; pairs with the schema/DLQ features already in Phase 2. |
| DBZ-7 | Phase 2 (KC migration) | DBZ-1 | Productionizes the bridge; covers the hard databases now. |
| DBZ-8 | Phase 2 / 3 boundary | DBZ-2, DBZ-4 | Hard, long-lead native CDC; wrapper covers it meanwhile. |
| DBZ-9 | Phase 3 | DBZ-2, DBZ-4 | Hardest native connector; not on the critical path while DBZ-7 covers Oracle. |
| DBZ-10 | Phase 3 | DBZ-2, DBZ-4 | Completes native coverage. |

**Critical path to credible parity messaging (the near-term goal):** DBZ-0 → DBZ-1 → DBZ-2 → DBZ-3.
That sequence produces (1) an honest story, (2) a working "Debezium-in-Conduit" demo, (3) a correctness
bar, and (4) one flagship native connector at real parity — enough to credibly re-engage the developers
who left, without overclaiming.

## Proposed ROADMAP.md delta (Phase 2 — CDC "the moat")

The current Phase 2 CDC section understates the work with a five-line database checklist. Replace it
(when the roadmap-reconciliation PR settles, to avoid a conflicting edit) with a structure that
distinguishes mechanism, depth, and coverage:

```text
### CDC (the moat)

Production-grade change data capture — snapshot + streaming, schema evolution, heartbeats,
transaction metadata, tombstones — proven by the CDC acceptance suite:

- [ ] CDC acceptance-test suite (snapshot→stream no-gap/no-dup, at-least-once under crash, ordering)
- [ ] Universal CDC capabilities in the connector SDK (schema evolution, incremental snapshot,
      heartbeats, transaction metadata)
- [ ] PostgreSQL — harden to full parity (log-based already; add the capabilities above)
- [ ] MySQL — harden (binlog already; add the capabilities above)
- [ ] MongoDB — harden (change streams already; add the capabilities above)
- [ ] Kafka Connect wrapper — run Debezium connectors (Oracle/SQL Server/Db2 log-based) in Conduit
- [ ] Native log-based SQL Server (replace trigger-based)
- [ ] Native LogMiner Oracle (replace trigger-based)
- [ ] Native Db2 / Spanner change-streams / Cassandra commitlog CDC
- [ ] Outbox-pattern processor
```

## Failure modes / risks

- **KC-wrapper JVM footprint contradicts the "no JVM" pitch.** Mitigation: frame it explicitly as a
  migration bridge and coverage backstop, never the end state; native is the destination. Messaging
  discipline matters here — do not let the bridge become the story.
- **Shipping trigger-based CDC as "parity."** It is not; an experienced user will call it out. The
  matrix and DBZ-0 keep the claim honest; native/wrapper replaces trigger-based over time.
- **Correctness debt.** CDC is squarely on the data path (invariants 1–7). Without DBZ-2 gating, we
  ship the exact crash/failover bugs Debezium spent years fixing. DBZ-2 is sequenced before any
  hardening claims correctness for this reason.
- **Spreading thin across ten databases.** Mitigation: depth on the flagship (Postgres) and the two
  right-mechanism connectors (MySQL, Mongo) first; the wrapper carries breadth so native work stays focused.

## Related

- `ROADMAP.md` — Phase 2 (CDC moat, Kafka Connect migration), Documentation track (`vs Debezium` page)
- `STRATEGY.md` — the two bets (KC migration path + Debezium-class CDC); the KC-wrapper credibility note
- `CLAUDE.md` — data-integrity invariants 1–7 (the correctness bar CDC must meet)
- `docs/design-documents/20260704-phase-1-execution-plan.md` — phase-execution precedent
