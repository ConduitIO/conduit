# Postmortem: snapshot→CDC handoff deadlocks when a deferred ack a source blocks on is dropped

## Summary

A follow-on of the [Source.Ack persist-ordering SEV-0](20260723-source-ack-persist-ordering.md)
(#2680). That fix made the plugin-ack **deferred** — sent only after the position is durably flushed
— and documented that a _dropped_ deferred ack is "always benign: the plugin learns about it on the
next ack it does receive." That assumption is **false for a source that gates its own progress on
receiving that ack.** The Postgres source's snapshot iterator blocks until every snapshot record has
been acked back to the plugin, and only then hands off to the CDC (logical-replication) subscriber.
If the snapshot-boundary deferred ack is dropped or indefinitely delayed, there is no "next ack" —
the source emits no further records until it receives that one — so the handoff never completes:
**CDC never starts, and every post-snapshot change (inserts, updates, deletes) is silently lost**,
with no error and no DLQ. At-least-once (invariant 3) is breached. Same failure class as the MySQL
snapshot-handoff gap (mysql #180): silent post-snapshot CDC loss at a handoff boundary.

Found **pre-release** by the engine-level RAG template e2e; no known production occurrence. Latent in
every build since #2680 shipped.

## Impact

- **Blast radius:** any snapshot-gating CDC source (Postgres logical replication today; the same
  class covers MySQL binlog / MongoDB change-stream if their connectors gate a handoff on the ack)
  running an initial snapshot followed by CDC. A source with no snapshot phase, or an upstream that
  does not gate on the ack (Kafka-style retention), is unaffected.
- **Severity: SEV-0-class** when triggered — silent loss of all post-snapshot CDC (a delete that
  never propagates orphans downstream data; e.g. RAG vectors are never removed). Silent: the pipeline
  reports "running," no error, no DLQ entry.
- **Trigger is load-dependent and was not deterministically reproduced.** On a quiet machine the
  deferred ack is reliably delivered (~one persister debounce interval late) and the bug does not
  manifest — an independent investigation ran 13 natural runs, all passing. The original failures
  occurred under heavy concurrent load (parallel builds + subagents + docker), where scheduling
  around the persister's ~1 s debounce flush delayed or dropped the boundary ack. What was proven is
  the **fragility**: a single dropped/indefinitely-delayed boundary ack is catastrophic, not benign.
  So the production likelihood is "intermittent under load," not "always" — but the consequence when
  it fires is total post-snapshot data loss.

## How it was detected

The engine-level RAG template e2e (`cmd/conduit/root/pipelines/template_gallery_rag_e2e_integration_test.go`,
tag `rag_template_e2e`) drives a real `Postgres CDC → chunk → embed → pgvector` pipeline through the
real engine. Its **create path passed** but its **delete path hung** — a source-row `DELETE` never
removed the derived pgvector rows. Probing localized it precisely:

- The funnel source read loop returned `op=snapshot` exactly once and never the CDC delete; the
  destination only ever saw the snapshot record.
- Instrumenting `pkg/connector/source.go`, the passing trace was
  `Read(snapshot) → Ack → sendDeferredAck → Read(delete)`: the delete is read **only after** the
  snapshot's deferred ack is delivered.
- Fault-injecting a drop of the snapshot-boundary deferred ack reproduced the exact symptom.

The component-level `rag_e2e` / `embedding_bundle_e2e` tests could not have caught this: they inject
the egress policy directly and drive `Process`/`Write` by hand, bypassing the engine's source →
processor → destination → ack loop entirely. The engine-level e2e was the first test to exercise the
real deferred-ack delivery against a real snapshot-gating source.

## Timeline

- **2026-07-23** — #2680 ships Approach A (persist-before-ack): the plugin-ack becomes deferred, and
  `sendDeferredAck` logs-and-drops a failed send as "benign." The latent handoff dependency is
  introduced here; the doc's failure-mode cases 2 & 5 record the (soon-to-be-falsified) assumption.
- **2026-07-27/28** — the engine-level RAG template e2e is built; its delete path fails under load.
- **2026-07-28** — root-caused to the deferred-ack / snapshot-handoff interaction; design doc
  ([20260728-snapshot-handoff-deferred-ack-deadlock](../design-documents/20260728-snapshot-handoff-deferred-ack-deadlock.md))
  written; fix (Approach A2) implemented, reviewed, and merged (#2707). Delete path green 4/4 under
  load (~15 s, previously a 71 s deadlock-timeout).

## Root cause

The engine cannot know which acks a plugin blocks on. #2680's "dropped deferred ack is benign"
reasoning holds for two cases it considered — retention-based upstreams (the ack is advisory pruning)
and teardown races (position already durable, restart re-delivers). It missed a third: **a source
blocked on the ack for liveness.** For such a source the ack is not advisory — it is the signal that
unblocks the source's own next stage. "The plugin learns on the next ack" presupposes a next ack
exists; a snapshot-gating source produces nothing until this ack arrives, so dropping it is a
permanent liveness failure and silent data loss, not a benign pruning delay.

Contributing: the drop was silent (a `Debug`-level log), and there is no metric for deferred-acks
pending or ack-vs-persist lag, so the condition is invisible until a downstream data-quality incident
surfaces it.

## Remediation

**Fix (#2707, Approach A2):** the deferred ack is delivered by a dedicated per-source goroutine that,
**while the plugin is running**, retries a transient send failure until it succeeds rather than
dropping it; if retries exhaust while genuinely running, it escalates loudly via `errs`. The
teardown carve-out is preserved unchanged (drop remains benign there, bounded by
`DefaultTeardownFlushTimeout`; escalation is suppressed so nothing deadlocks on the unread `errs`
channel). A2 also moves the send off the shared persister callback goroutine, _narrowing_ the
process-wide blast radius #2680 flagged.

**Regression coverage shipped with the fix:**

- Deterministic unit test (`TestSource_DeferredAck_TransientSendFailure_EventuallyDelivered`) —
  fault-injects transient send failures, asserts eventual delivery; **verified to fail without the
  fix**.
- `TestSource_DeferredAck_PersistentSendFailure_EscalatesViaErrs` — a never-recovering send fails
  loudly, not a silent hang.
- The RAG template e2e delete path (#2708) — the end-to-end regression against a real Postgres
  snapshot-gating source.
- All #2680 tests preserved.

## Follow-ups

- **`tests/chaos` snapshot→CDC-handoff scenario.** The current chaos harness's synthetic plugin
  deliberately does not model the connector-internal ack-gating (handoff atomicity is scoped to
  DBZ-3 per `handoff_test.go`). A chaos scenario that models a snapshot-gating source and asserts
  post-snapshot CDC survives a dropped/delayed boundary ack + SIGKILL is the standing regression for
  this class — **tracked to DBZ-3**, not built now (the unit + e2e regressions above already cover
  the fix).
- **Connector-side hardening (defense-in-depth).** Don't gate the Postgres snapshot→CDC handoff on
  the full downstream deferred-ack round-trip (design doc Alternative B); use a source-local
  completion signal, preserving at-least-once via position durability. Lives in
  `conduit-connector-postgres`.
- **Observability.** A counter for deferred-ack retry attempts / escalations and a gauge for
  deferred-acks-pending per source, so a stuck handoff is visible before it becomes missing data
  (the ack-vs-persist-lag observability gap #2680 already named).
- **MySQL / MongoDB must-verify.** Confirm whether their connectors gate a handoff on the ack the
  same way (inheriting #2680's must-verify list).

## Related

- [20260723-source-ack-persist-ordering](20260723-source-ack-persist-ordering.md) — the SEV-0 whose
  fix introduced the deferred-ack machinery and the assumption this bug falsifies.
- [20260728-snapshot-handoff-deferred-ack-deadlock](../design-documents/20260728-snapshot-handoff-deferred-ack-deadlock.md)
  — the design doc for the fix.
- PR #2707 — the fix (Approach A2). PR #2708 — the engine e2e that found it and now regresses it.
- mysql #180 — the MySQL snapshot-handoff gap (same failure class).
