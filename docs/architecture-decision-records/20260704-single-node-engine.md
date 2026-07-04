# Single-node engine; distribution lives in the scheduling layer

## Summary

The Conduit engine is single-node by design. It will never grow cluster-membership protocols,
leader election, gossip, or consensus (Raft/etcd). Distribution — running many pipelines across
many instances, or one hot pipeline across several — is a scheduling concern solved a layer above
the engine (Kubernetes operator / control plane), not inside it.

## Context

Data-integration systems that put distribution _inside_ the engine pay for it forever. Kafka
Connect's rebalance protocol is the canonical example: worker-cluster membership and task
rebalancing are a persistent source of operational pain, stop-the-world pauses, and subtle
correctness bugs. We want Conduit to be boring to operate (Principle 4), embeddable as a library
(a differentiator), and free of that class of failure.

At the same time, real deployments need scale-out and high availability. The question is not
_whether_ Conduit distributes, but _where the distribution logic lives_.

Two forces are in tension:

- Users will eventually run fleets of pipelines and expect rescheduling, autoscaling, and HA.
- Every distributed primitive added to the engine makes it heavier, harder to embed, and harder
  to reason about under failure.

## Decision

Keep the engine single-node and place all distribution in a scheduling layer above it.

- **No membership, no leader election, no gossip, no consensus in the engine.** Well-meaning
  clustering PRs are rejected and pointed at this ADR. If a design doc starts growing
  engine-level rebalancing, that is a stop-and-flag signal.
- **Level 1 scale-out = fleet sharding.** A scheduler bin-packs pipelines onto instances.
  "Who runs what" state lives in the control plane (Postgres- or Kubernetes-lease-backed), not in
  the engine.
- **Level 2 scale-out = hot-pipeline parallelism via partition claims.** Sources declare their
  partitionable units in the connector protocol; the scheduler assigns claims to instances. Kafka
  consumer-group sources get this natively. CDC is single-writer per replication slot and scales
  per-table, which is acceptable. The partition-claim protocol RFC is a Phase 1 deliverable even
  though the scheduler itself is Phase 3, so the protocol seam ships before it would need a
  breaking revision.
- **High availability = active/passive, resume-from-checkpoint.** An instance dies, the scheduler
  reassigns its pipelines, and each pipeline resumes from its last checkpoint. This is correct by
  construction given the data-integrity invariants (crash-safe positions, atomic checkpoints,
  at-least-once). No consensus and no warm standbys in v1.

The operator and the control plane share one scheduling brain; the operator is the control
plane's Kubernetes backend. Scheduling stays open source; org-scale governance and federation are
the commercial layer.

## Consequences

- The engine stays small, embeddable, and easy to reason about under failure.
- We inherit none of Kafka Connect's rebalance-protocol failure surface.
- HA correctness reduces to the data-integrity invariants rather than a bespoke consensus
  implementation we would have to prove correct.
- We take on a dependency on the scheduling layer for anything beyond a single instance; static
  pipeline-to-instance assignment (Helm, `conduit run --pipelines <dir>`) must cover users well
  before the operator exists.
- The connector protocol must carry a partition-claim concept earlier than the scheduler needs
  it, to avoid a later breaking change.
- We explicitly forgo in-engine dynamic rebalancing of a running pipeline across instances; that
  capability, if ever needed, is the scheduler's job, not the engine's.

## Related

- `ROADMAP.md` — Principle 8 (single-node engine, scale-out by scheduling); Phase 1 partition-
  claim RFC; Phase 3 operator, hot-pipeline parallelism, and active/passive HA
- [20220121-Conduit-plugin-architecture.md](20220121-conduit-plugin-architecture.md) — the
  built-in/standalone plugin model the engine already assumes
