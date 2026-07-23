# Partition-claims protocol RFC (seam for hot-pipeline parallelism)

**Status:** Accepted (DeVaris Tier-1 sign-off, 2026-07-23; v0.19, Workstream 5). This is a protocol
**proposal**, not an implemented change: no `.proto` file in `conduit-connector-protocol` is touched by
this document — it defines the agreed seam shape for a future additive change. The three questions the
draft left open are now resolved; see [Resolved at
sign-off](#resolved-at-sign-off-devaris-2026-07-23).

## Summary

`docs/architecture-decision-records/20260704-single-node-engine.md` (accepted) commits Conduit to a
single-node engine with all distribution pushed to a scheduling layer above it, and names two levels
of scale-out. Level 1 is fleet sharding (whole pipelines bin-packed onto instances, no engine
change). **Level 2 is "hot-pipeline parallelism via partition claims"** — running one pipeline's
source across multiple instances, each owning a subset of the source's partitionable units — and the
ADR is explicit that the protocol seam for this must exist before the scheduler itself does, "to
avoid a later breaking change" (ADR, Consequences).

This RFC is that seam. It defines, and only defines:

1. What a **partitionable unit** is, per connector kind already in the built-in set (Kafka
   topic-partition, Postgres CDC table, file/object shard, non-partitionable sources like
   `generator`).
2. How a source **declares** the units it can offer, additively, over
   `conduit-connector-protocol`.
3. How a **future** scheduler (not designed here — that is Phase 3, its own design doc) would
   consume that declaration to compute and hand out claims.
4. The failure modes this seam must survive with **zero scheduler code running**, because the wire
   fields exist starting the release this lands, and must be safe to leave dormant for however many
   releases pass before a scheduler consumes them.

It does not build a scheduler, a claim store, a lease/liveness mechanism, or any clustering
primitive — see [Out of scope](#out-of-scope). Accepting this RFC is a decision to reserve wire
vocabulary now; it is explicitly **not** a decision that a per-unit checkpoint store or a fencing
implementation are safe, funded, or scoped — those are deferred forward dependencies this RFC
creates (see [Deferred forward dependencies this RFC creates](#deferred-forward-dependencies-this-rfc-creates)),
and invariant 4 (ordering, per `CLAUDE.md`) holds today for one reason only: the seam is inert until
a consumer exists, not because this RFC has proven a multi-instance implementation correct.

## Context

### The problem

Data-integration systems that put distribution logic inside the ingestion engine pay for it
forever (Kafka Connect's rebalance protocol is the standing example CLAUDE.md and the single-node
ADR both point at). Conduit's answer is to keep the engine single-node and put distribution in a
scheduling layer — but that only works for "hot" pipelines (one source too large for one instance)
if the scheduler has something to hand out claims _over_. Today it has nothing: the connector
protocol has no concept of a source being internally partitioned at all.

### What exists today (verified against the pinned dependency versions this repo builds against)

The v2 source protocol (`conduit-connector-protocol@v0.9.5/proto/connector/v2/source.proto:12-66`)
has five lifecycle RPCs — `Configure`, `Open`, `Run`, `Stop`, `Teardown` — plus three
lifecycle-event hooks. Position is a single opaque `bytes` value end to end:

- `Source.Open.Request.position` (`source.proto:77-85`, package `v0.9.5`) — "the position of the
  last record that was successfully processed... the Source should start producing records after
  this position."
- `Source.Run.Request.ack_positions` / `Source.Run.Response.records` (`source.proto:87-98`).
- `Source.Stop.Response.last_position` (`source.proto:100-110`).

Nowhere in this file, or in `Specification` (`specifier.proto:16-37`, ending at field 7,
`source_params`), does the protocol have any concept of a source having more than one internally
partitioned stream. The Go mirror, `pconnector/source.go:26-36` (`SourcePlugin` interface) and
`:66-83` (`SourceOpenRequest{Position opencdc.Position}`, `SourceRunRequest{AckPositions
[]opencdc.Position}`), confirms the same: one position stream per source, full stop. These
citations were spot-checked directly against the `conduit-connector-protocol@v0.9.5` module this
repo's `go.mod` pins — they are not asserted from memory.

Partitioning exists today, but entirely inside individual connectors, invisible to the protocol:

- **Kafka** (`conduit-connector-kafka@v0.12.3/source/position.go:24-29`, confirmed): `type Position
  struct { GroupID string; Topic string; Partition int32; Offset int64 }`. The connector doesn't use
  Conduit's position mechanism to resume multi-partition state — `source.go:57` (`Open`) hands off
  to franz-go's own Kafka consumer-group protocol, which already does partition assignment and
  offset commit **server-side**, external to Conduit entirely. This is why the single-node ADR
  calls Kafka's Level 2 scale-out "native."
- **Postgres CDC** (`conduit-connector-postgres@v0.14.0/source/logrepl/`): single replication slot,
  single writer, one WAL stream. The unit is naturally a table, but today one connector instance
  processes every configured table through one slot; there is no protocol-level way to say
  "instance A owns tables `{orders, customers}`, instance B owns `{shipments}`."
- **File/S3-style connectors:** the unit would naturally be a file or object-prefix shard; nothing
  declares this today either.
- **`generator`:** no natural partitionable unit. This connector kind must land on the explicit
  single-instance fallback ([Failure modes](#failure-modes)), not be forced to invent one.

### Constraints this design is bound by (from `CLAUDE.md`)

- **Invariant 4** ("ordering guarantees are per-source-partition and documented... changes that
  could reorder records within a partition key require a design doc and explicit sign-off") is the
  central constraint this RFC is reviewed against.
- **Protocol-versioning rule**: never change `conduit-connector-protocol` without an explicit
  versioning discussion; changes must be additive/backward-compatible.
- **Absolute no-clustering rule**: never add membership, leader election, gossip, or consensus
  primitives to the engine.
- **Tier-1 gate**: human (DeVaris) sign-off is required before this RFC is accepted, independent of
  the fact that no code merges against it in v0.19 — this is a Tier-1 _decision_, not a Tier-1
  _merge_.

## Decision

### Partitionable unit, defined per connector kind

| Connector kind | Partitionable unit | Who assigns it today | Mode this RFC proposes |
| --- | --- | --- | --- |
| Kafka (consumer-group source) | topic-partition | Kafka broker's own consumer-group protocol (external, already solved) | `MODE_EXTERNALLY_MANAGED` — declares "I am partitionable, but don't manage my claims through this protocol; Kafka already does" |
| Postgres CDC (logical replication) | table (or table-group) | Nobody — one slot owns everything configured | `MODE_STATIC` — units fixed at `Configure` time from the table list |
| File/S3 | file or object-prefix shard | Nobody | `MODE_DYNAMIC` — units can appear after `Configure` (new files land at runtime) |
| `generator` and other non-partitionable sources | none | N/A | `MODE_UNSPECIFIED` — explicit fallback, see [Failure modes](#failure-modes) |

### How a source declares claims over the protocol

All changes below are new fields (fresh field numbers) on existing messages, one new optional RPC,
and new top-level messages. **Nothing existing is renamed, renumbered, or removed** — see
[Backward-compatibility and versioning plan](#backward-compatibility-and-versioning-plan) for why
that matters mechanically, not just as a promise.

**`proto/connector/v2/specifier.proto`** — add a capability declaration to `Specification`
(currently ends at field 7, `source_params`):

```proto
message Specification {
  // ... existing fields 1-7 unchanged ...

  // NEW, field 8. Zero value (unset) = MODE_UNSPECIFIED, which is exactly today's behavior.
  PartitioningCapability partitioning = 8;
}

message PartitioningCapability {
  enum Mode {
    MODE_UNSPECIFIED = 0;         // fallback: today's single-instance model, unchanged
    MODE_STATIC = 1;              // units fixed at Configure time
    MODE_DYNAMIC = 2;             // units can change at runtime; engine must re-poll
    MODE_EXTERNALLY_MANAGED = 3;  // units exist, but an external system (e.g. Kafka) already
                                  // assigns them -- the engine must NOT attempt its own claim
                                  // assignment for this source (see Failure modes, double-claiming)
  }
  Mode mode = 1;
  string unit_label = 2; // human-readable, e.g. "kafka topic-partition", "postgres table"
}
```

**`proto/connector/v2/source.proto`** — one new optional RPC, called by the engine only if
`Specification.partitioning.mode` is `MODE_STATIC` or `MODE_DYNAMIC` (never for `MODE_UNSPECIFIED`
or `MODE_EXTERNALLY_MANAGED`):

```proto
service SourcePlugin {
  // ... existing RPCs unchanged ...

  // DeclareClaimableUnits enumerates the partitionable units this source can currently offer.
  // Optional: connectors built against an SDK predating this RFC do not implement it, and the
  // engine must never call it unless Specify() already reported a non-UNSPECIFIED,
  // non-EXTERNALLY_MANAGED mode.
  rpc DeclareClaimableUnits(Source.DeclareClaimableUnits.Request)
      returns (Source.DeclareClaimableUnits.Response);
}

message Source {
  // ... existing nested messages unchanged ...

  message DeclareClaimableUnits {
    message Request {}
    message Response {
      repeated PartitionUnit units = 1;
    }
  }

  message Open {
    message Request {
      bytes position = 1; // unchanged -- aggregate/legacy position, still required

      // NEW, field 2. The units this instance is claimed to own for this run, with a
      // per-unit resume position. Empty = "claim everything" -- the fallback that preserves
      // today's single-instance behavior for every MODE_UNSPECIFIED connector and for any
      // engine build that predates this RFC.
      repeated PartitionClaim claimed_units = 2;
    }
    // Response unchanged.
  }
}

message PartitionUnit {
  string id = 1;             // stable, connector-assigned identifier, e.g. "orders" or "topic-p3"
  bytes opaque_metadata = 2; // connector-defined (e.g. serialized table name); round-tripped
                             // back to the connector inside PartitionClaim, opaque to the engine
}

message PartitionClaim {
  string unit_id = 1;  // matches PartitionUnit.id
  bytes position = 2;  // per-unit resume position, connector-defined encoding (opaque, same
                       // contract as today's Open.Request.position, just scoped to one unit)
  uint64 epoch = 3;    // fencing token: a monotonically increasing generation number a future
                       // scheduler would assign each time a unit is (re)claimed. See
                       // "Open question 1" -- committing to this shape now, rather than deferring
                       // it to the Phase 3 scheduler doc, is NOT yet decided.
}
```

**Go SDK shape (`conduit-connector-sdk`)** — an optional interface, following the same duck-typed
capability-negotiation idiom Go already uses (`io.ReaderFrom`, `sql.Scanner`):

```go
// PartitionAwareSource is an optional interface a Source may implement to declare
// partitionable units. A Source that does not implement it is treated as
// MODE_UNSPECIFIED automatically -- zero behavior change, zero required migration.
type PartitionAwareSource interface {
    Source
    DeclareClaimableUnits(context.Context) ([]PartitionUnit, error)
}
```

`NewSourcePlugin` type-asserts `impl.(PartitionAwareSource)`; existing `Source` implementations need
no changes at all, in perpetuity, unless they opt in.

### How a future scheduler would consume this (not designed here)

This RFC does not design the Phase 3 scheduler. What it commits to is the _shape_ a scheduler would
read and write, so that when that design doc is written it has a stable input:

1. At connector-instantiation time, the scheduler (or, pre-scheduler, a single instance acting as
   its own trivial scheduler) reads `Specification.partitioning.mode`. `MODE_UNSPECIFIED` or
   `MODE_EXTERNALLY_MANAGED` means the scheduler does nothing further for this source.
2. For `MODE_STATIC` or `MODE_DYNAMIC`, the scheduler calls `DeclareClaimableUnits` to get the
   current `PartitionUnit` list, decides a bin-packing/assignment (its own design, out of scope
   here), and constructs one `PartitionClaim` per unit assigned to a given instance, each carrying
   that unit's last-known position and the epoch the scheduler is minting for this assignment.
3. The scheduler passes the resulting `claimed_units` into that instance's `Source.Open.Request`.
   An instance with an empty `claimed_units` list claims everything (today's behavior).
4. For `MODE_DYNAMIC` sources, the scheduler is expected to re-poll `DeclareClaimableUnits`
   periodically or on a connector-emitted signal (signal mechanism unspecified — Phase 3) to notice
   new units (e.g. new files) and issue new claims.

Nothing above requires the engine to gain membership, leader election, or consensus: the scheduler
is external to the engine per the single-node ADR, exactly as fleet sharding (Level 1) already is.

### Out of scope

Explicitly, per the ADR and the absolute rule in `CLAUDE.md`:

- The scheduler itself: how claims are computed, bin-packed, rebalanced, or leased. Phase 3, a
  separate design doc.
- **Any membership, leader-election, gossip, or consensus mechanism in the engine.** The
  single-node engine ADR forbids this outright. If anything in this document reads like it's
  growing toward that, it is a bug in this RFC, not an accepted design.
- Changes to Kafka's own consumer-group rebalance protocol, which already solves this problem for
  Kafka sources externally.
- Any change to today's runtime behavior. Zero connectors implement the new optional interface in
  v0.19; single-instance behavior is unchanged for every existing pipeline.
- The state/checkpoint storage format rework that per-unit position tracking would eventually need.
  Flagged as a forward dependency, not designed here — see
  [Deferred forward dependencies this RFC creates](#deferred-forward-dependencies-this-rfc-creates).

## Alternatives considered

1. **Out-of-band claim registry** (an external coordination service, or side-channel config mapping
   units to instances, connector protocol untouched). **Rejected:** reintroduces exactly the
   external-coordination dependency the single-node engine ADR walls the engine off from, and fails
   the ADR's own mandate that the _protocol_ carry the concept before the scheduler needs it — it
   would leave the wire format silent on units, guaranteeing a breaking change later when the
   scheduler ships.
2. **Static config-declared partitioning** (operator manually declares "pipeline has N shards,
   instance i owns shard i" in pipeline YAML; connector infers its shard from config, no protocol
   change). **Rejected:** fails `MODE_DYNAMIC` connectors outright (new Kafka partitions or new
   files appearing after deploy can't be picked up without a redeploy); puts correctness in
   human-maintained YAML instead of the party that actually knows the units — the connector;
   gives a future scheduler no machine-readable capability signal to bin-pack against.
3. **In-protocol, connector-declared claims (chosen).** The source is the only party that actually
   knows what's partitionable; it declares mode + units via `Specify`/`DeclareClaimableUnits` and
   receives an assignment via `Open`. This mirrors Kafka's own consumer-group split (broker asks,
   consumer reports partitions and commits offsets) — a pattern already proven at the scale this
   RFC targets, reusing a shape instead of inventing one.
4. **Bump to a new `connector/v3` proto package** instead of additive fields on `v2`. **Rejected:**
   the prior `v1`→`v2` bump was justified by a genuinely breaking semantic change (`Start`→`Open`
   rename, single-`Record` `Run` → batched-`Records` `Run`). Nothing proposed here is semantically
   breaking; paying for a parallel package and a dual-version compatibility window is unjustified
   when additive fields on the existing message set are sufficient, and buf's breaking-change gate
   (see [Backward-compatibility and versioning plan](#backward-compatibility-and-versioning-plan))
   proves it stays additive.

## Failure modes

| Case | Failure/risk | Mitigation | Where handled |
| --- | --- | --- | --- |
| **Double-claiming** — two instances both believe they own the same unit | Two writers producing records for the same partition key concurrently — directly violates invariant 4 (ordering) and risks duplicate delivery beyond at-least-once | The _scheduler_ (out of scope) is the only party allowed to hand out claims; every claim carries an `epoch` so staleness is detectable without the engine needing consensus to prevent the double-assignment from happening (that prevention is the scheduler's job) | Proto shape, above; no double-claim is _possible_ in v0.19 because nothing computes or hands out non-default claims yet |
| **Kafka-specific double-claiming**: a future Conduit scheduler and Kafka's own consumer-group rebalance both try to assign the same topic-partitions | Two independent assignment authorities racing/conflicting | `MODE_EXTERNALLY_MANAGED` is the explicit opt-out: a Kafka-kind source reports this mode and the engine/scheduler must never call `DeclareClaimableUnits` or attempt its own assignment for it — Kafka's broker-side protocol remains sole authority | "Partitionable unit" table, above (subject to Open question 2 below — whether this belongs in the wire protocol at all) |
| **Non-partitionable connector** (e.g. `generator`) | Forcing a unit concept onto a source with none produces a fabricated, meaningless partitioning | `MODE_UNSPECIFIED` (zero value) is the default and requires no code change from existing connectors; the engine's fallback is exactly today's single-instance model | Proto shape (`Mode` enum, value 0); interoperability matrix, below |
| **Orphaned claim** — an instance holds a claim past the point a future scheduler reassigned it (GC pause, network partition, slow shutdown) | The "zombie writer" problem: a dead-but-not-yet-detected instance keeps producing records for a unit now owned elsewhere, causing duplicate/out-of-order writes for that partition | The protocol carries a fencing token (`epoch`) rather than requiring the engine to solve liveness detection itself; whichever component holds authoritative state for a unit downstream (destination dedup, or a future claim store) would reject any record/ack tagged with a lower epoch than the highest it has seen for that unit. **This rejection mechanism does not exist yet** — liveness detection, reassignment timing, and epoch enforcement are scheduler-and-checkpoint-store concerns, out of scope here; the seam only makes staleness _detectable_ in the wire format, it does not make it _handled_ | `epoch` field, above; see [Deferred forward dependencies](#deferred-forward-dependencies-this-rfc-creates) |
| **Claim overlap during a live rebalance** (unit handed from instance A to B while A is still draining) | Same failure class as orphaned claim, at a shorter timescale | Same fencing-token mechanism; additionally, because per-unit `position` (not just aggregate) is carried in `PartitionClaim`, instance B could resume the unit from its own last-acked position rather than the aggregate stream's, without waiting for A's shutdown to fully complete — once a scheduler and a per-unit checkpoint store exist to make this real | `PartitionClaim.position`, scoped per unit, distinct from `Open.Request.position` |
| **Rebalance interaction with invariant 4** | A naive rebalance could reorder records across a partition if position tracking stays aggregate instead of per-unit | Per-unit `position` makes per-source-partition ordering (invariant 4's own stated granularity) the unit of resumption, not an afterthought bolted onto a single aggregate position — but this is a property the _wire format_ enables, not one this RFC proves an implementation upholds | [Why invariant 4 holds today](#why-invariant-4-holds-today-and-what-it-will-take-to-keep-holding-it-later) |

## Why invariant 4 holds today (and what it will take to keep holding it later)

**Claim: invariant 4 is preserved by this RFC because the RFC does not change ordering semantics —
it names the granularity invariant 4 already assumes.**

Invariant 4 states ordering is guaranteed _per-source-partition_, not globally. Today, "partition" is
an implicit property baked into each connector's own position encoding (e.g. Kafka's
`Topic`/`Partition` fields inside its opaque position blob) — the engine has no visibility into it
at all. This RFC's `PartitionUnit`/`PartitionClaim` types make that same granularity a named,
protocol-level concept. Nothing about _how_ records within a unit are ordered changes; what changes
is that the engine can, in the future, be told "instance X currently owns unit Y" — a prerequisite
for ever safely running one source across multiple instances without violating invariant 4 by
accident.

**Why this is safe with zero scheduler code, concretely:**

- In v0.19, no connector implements `PartitionAwareSource`, so every source reports
  `MODE_UNSPECIFIED`. The engine never calls `DeclareClaimableUnits`, `Open.Request.claimed_units`
  is always empty, and runtime behavior is byte-for-byte identical to today. There is no code path
  by which this RFC's fields can be reached in v0.19.
- Once a connector eventually implements the optional interface (future work, not this release),
  the engine still has no scheduler to compute non-trivial claims — a single-instance runtime
  handing itself one implicit claim (all units) is the only caller until Phase 3, again identical
  to today's behavior, just expressed through the new vocabulary instead of implicitly.

### Deferred forward dependencies this RFC creates

This is the section that must not be read as "solved." Accepting this RFC does **not** mean the
following exist or have been proven safe — it means this RFC is aware it is creating the need for
them, and defers them explicitly rather than silently:

1. **Epoch enforcement.** Nothing in this RFC, or anywhere in the codebase, actually rejects a
   stale-epoch claim. The `epoch` field is reserved wire space; the component that would read and
   enforce it does not exist. Until it does, `epoch` is inert data, not a safety mechanism.
2. **Per-unit checkpoint storage.** Today's checkpoint format stores one opaque position per source
   (invariants 2 and 5). Multi-instance claim consumption requires storing one position **per
   unit**, which is a real forward dependency on whatever the state layer looks like when Phase 3
   arrives. This RFC does not change the checkpoint format, and does not authorize changing it —
   see Open question 3, below. When that change happens, it needs its own versioned migration path
   per `CLAUDE.md`'s backward-compatibility rule, exactly like any other checkpoint format change.
3. **Scheduler-side double-claim prevention.** The scheduler never handing out an overlapping claim
   without incrementing `epoch` is a scheduler design obligation, not something this seam enforces
   or can enforce by itself.

**In short: invariant 4 holds today only because the seam is inert until a consumer exists.** The
moment a real scheduler starts handing out genuinely partial claims (Phase 3), invariant 4's
preservation becomes a property the _scheduler design_ (plus whatever checkpoint-store rework
accompanies it) must independently prove, with its own failure-mode analysis and its own Tier-1
sign-off. This document does not, and cannot, prove that in advance — it only ensures the wire
format doesn't force an unsafe shortcut when that time comes.

## Backward-compatibility and versioning plan

**The change is additive at the proto3 wire level:**

- New fields use fresh field numbers on existing messages (`Specification.partitioning = 8`,
  `Source.Open.Request.claimed_units = 2`); proto3 unset fields default to zero-value and unknown
  fields are ignored by parsers that predate them.
- One new RPC (`DeclareClaimableUnits`) is added to `SourcePlugin`; it is only ever invoked by
  engine code that has already read a non-default `PartitioningCapability.mode` from `Specify()` —
  an engine that doesn't know about it simply never calls it, and a connector that doesn't
  implement it is never asked.
- Three new top-level messages (`PartitioningCapability`, `PartitionUnit`, `PartitionClaim`) are
  purely additive; nothing references them unless a connector opts in.

**Interoperability matrix:**

| Engine | Connector | Behavior |
| --- | --- | --- |
| Pre-RFC | Pre-RFC | Unchanged (today's behavior). |
| Post-RFC | Pre-RFC | `Specify()` response has no `partitioning` field set, so it decodes as the zero value, `MODE_UNSPECIFIED`. Engine takes today's single-instance path, never calls `DeclareClaimableUnits`; `Open.Request.claimed_units` stays empty. **Zero behavior change.** |
| Pre-RFC | Post-RFC (implements `PartitionAwareSource`) | Old engine doesn't read the new `Specification` field or call the new RPC; new connector's `Open` still receives empty `claimed_units` and must treat that identically to "claim everything" — the SDK adapter enforces this fallback so a partition-aware connector still runs correctly, single-instance, against an old engine. |
| Post-RFC | Post-RFC | Full negotiation as specified above. |

**Mechanical enforcement, not just a promise:** `conduit-connector-protocol`'s `proto/buf.yaml` sets
`breaking: use: FILE` (confirmed directly in the pinned `v0.9.5` module this repo builds against).
That repo's CI runs buf's breaking-change detector on every PR touching `proto/**`, diffed against
`main`. Any change that is not actually additive (a renumbered field, a removed method, a changed
type) fails that check automatically — the versioning discipline this RFC relies on is backed by
existing tooling in the protocol repo, not reviewer vigilance alone. (This RFC does not open a PR
against `conduit-connector-protocol`, so the gate has not actually run against this exact proposal
yet — it will, on the first PR that adds these fields for real, which is Tier 2 per
[Relationship to future implementation work](#relationship-to-future-implementation-work).)

**Why no new proto package (`v3`) is needed:** see Alternative 4, above — the prior `v1`→`v2` bump
was used for a genuinely breaking rename/reshape; nothing here rises to that bar.

**Deprecation policy:** nothing is deprecated by this change. If a future scheduler design (Phase 3)
ever needs a genuinely breaking revision of this shape (e.g. a different fencing mechanism), that
follows `CLAUDE.md`'s standard policy: announce → warn → remove, minimum two minor versions, with a
migration note.

### Relationship to future implementation work

No implementation PR follows this RFC in v0.19. A future PR that adds the proto fields themselves
(still no runtime behavior change, since nothing consumes them) would be Tier 2 at most, given this
RFC has already cleared the Tier-1 bar for the design. A PR that has any connector or the engine
actually _act_ on non-default claims is a separate, later Tier-1 change requiring its own sign-off,
because that is where invariant 4 risk becomes live rather than inert.

## Observability

Not implemented in v0.19 (no code ships), but the seam is designed so a future implementation can
expose the following without redesigning the wire format:

- **Per source, at `Specify` time:** the reported `PartitioningCapability.mode` — an operator or
  agent should be able to ask "is this connector partitionable, and how" via the existing
  `conduit connectors describe` surface (Tier 2, no protocol change needed beyond what's here).
- **Per running instance (once a scheduler exists, Phase 3):** which units it currently holds
  claims for, at what epoch, and the per-unit position — this is exactly the state the
  `PartitionClaim` fields carry, so no new field is needed to _surface_ it later, only a consumer
  that reads what's already on the wire.
- **Failure signal for double-claim/orphaned-claim detection (future):** an epoch-mismatch
  rejection should be a named, coded error (`CLAUDE.md`'s "errors are API" rule) once a scheduler
  exists to trigger it — flagged here so the future implementation doesn't invent an uncoded error
  path.

## Risk, confidence, and open questions

**Risk: low-medium.** Design-only, well-scoped, no code ships. The main risk is under-scoping
failure modes because "it's just a seam" — this RFC tries to counter that by tracing each edge case
above to a concrete mechanism (epoch, per-unit position, explicit mode enum) rather than hand-waving
"the scheduler will handle it."

**Confidence: medium.** The proto shape and versioning mechanics are verified directly against the
pinned module versions this repo depends on (cited by file:line, spot-checked while writing this
doc, not carried over from memory) and against the existing `v1`→`v2` precedent. The softer parts —
whether `epoch`/fencing is the right level of detail for a protocol seam with no consumer yet, and
whether `MODE_EXTERNALLY_MANAGED` belongs in the wire protocol at all versus being a documentation
convention — were resolved at sign-off, below.

### Resolved at sign-off (DeVaris, 2026-07-23)

The three questions the draft left open were decided at Tier-1 sign-off as follows. They are
recorded here as the accepted disposition of this RFC.

**Resolved (1). Fencing-token scope — reserve the field, defer the mechanism.** `PartitionClaim.epoch`
stays in the wire shape as **reserved space for forward-compatibility** (claiming the field number now
so no breaking proto change is needed later), but this RFC does **not** commit to the enforcement
semantics — the fencing _mechanism_ (what mints epochs, how staleness is rejected, liveness/reassignment
timing) is deferred in full to the Phase 3 scheduler design doc. Until that doc lands, `epoch` is inert
reserved wire space, not a safety mechanism, exactly as [Deferred forward
dependencies](#deferred-forward-dependencies-this-rfc-creates) states.

**Resolved (2). `MODE_EXTERNALLY_MANAGED` — wire-level enum, not docs-only.** It stays a
protocol-level enum value. The engine/scheduler must be able to _read_ a source's declared mode to know
it must never attempt its own claim assignment for a Kafka-kind source; documentation guidance alone
cannot be enforced at the seam. The wire vocabulary carries the opt-out.

**Resolved (3). No pre-authorization of state-layer scope.** Accepting this RFC does **not**
pre-authorize extending `CLAUDE.md`'s locked state-layer scope (dedup, lookup tables, simple windows).
The per-unit checkpoint storage this RFC creates a forward dependency on is **deferred and separately
gated** — it requires its own explicit maintainer sign-off when Phase 3 arrives. The ratchet stays
tight; this seam does not widen it.

## Acceptance criteria

Verifiable by reading this document (design-only):

1. "Partitionable unit, defined per connector kind" defines the term concretely for every connector
   kind currently in the built-in set (Kafka, Postgres CDC, file/S3-style, non-partitionable), each
   grounded in cited, version-pinned code — not a hypothetical.
2. "How a source declares claims over the protocol" states the exact additive proto shape (new
   fields, one new RPC, new messages) and the Go SDK optional-interface shape a source uses to
   declare claims.
3. "Backward-compatibility and versioning plan" states not just "this is additive" as an assertion,
   but _why_ it's additive (proto3 semantics), _what enforces it mechanically_ (buf's
   breaking-change gate, cited by file), and _what happens_ for old-connector/new-engine and
   new-connector/old-engine pairings.
4. "Failure modes" enumerates double-claiming (including the Kafka-specific case), a connector that
   can't express partitionability (with its explicit fallback), an orphaned claim, and a rebalance
   interaction — each with a stated mitigation that doesn't require scheduler code to exist yet.
5. "Out of scope" states the boundary explicitly: no clustering primitives, no scheduler design.
6. "Why invariant 4 holds today" demonstrates invariant 4 is preserved by the seam as specified
   _today_, while "Deferred forward dependencies this RFC creates" is explicit about what is **not**
   yet proven for any future consumer, because none exists yet.
7. This document is not accepted until DeVaris signs off — tracked in the PR that adopts it, not
   asserted here.

## Consequences

- The connector protocol gains reserved vocabulary for partition claims before any scheduler needs
  it, avoiding a breaking protocol revision later — the single-node ADR's stated goal for this
  workstream.
- Zero runtime behavior changes for any existing pipeline or connector in v0.19; this is a pure
  wire-format reservation.
- A real risk is created and explicitly not resolved by this document: the epoch-fencing mechanism
  and per-unit checkpoint storage that would make multi-instance claims _safe_ under invariant 4 do
  not exist yet. Any future work that starts handing out non-default claims must treat that as its
  own Tier-1 design-and-sign-off exercise, not something this RFC already covered.
- If either open question 1 or 2 is resolved by narrowing scope at sign-off (deferring `epoch`'s
  shape, or dropping `MODE_EXTERNALLY_MANAGED` from the wire protocol), the proto shape above changes
  before any implementation PR is opened — this document's proposal is not final until sign-off.

## Related

- [`docs/architecture-decision-records/20260704-single-node-engine.md`](../architecture-decision-records/20260704-single-node-engine.md)
  — the accepted ADR this RFC executes; source of the Level 1/Level 2 scale-out framing and the
  "protocol seam before scheduler" mandate.
- `conduit-connector-protocol/proto/connector/v2/source.proto`,
  `conduit-connector-protocol/proto/connector/v2/specifier.proto` (pinned at `v0.9.5` in this repo's
  `go.mod`) — current wire shape this RFC proposes extending.
- `conduit-connector-protocol/pconnector/source.go` — current Go-facing mirror of the wire protocol.
- `conduit-connector-protocol/proto/buf.yaml` — existing mechanical breaking-change enforcement
  (`breaking: use: FILE`) this RFC's versioning plan relies on.
- `conduit-connector-kafka/source/position.go`, `conduit-connector-kafka/source.go` (pinned at
  `v0.12.3`) — the Kafka precedent for "partitioning solved externally"
  (`MODE_EXTERNALLY_MANAGED`).
- `conduit-connector-postgres/source/logrepl/` (pinned at `v0.14.0`) — the single-replication-slot
  precedent for per-table `MODE_STATIC` partitioning.
- `CLAUDE.md` — invariant 4, the protocol-versioning rule, and the absolute no-clustering rule this
  RFC is bound by.
