# Default destination batching on the classic engine

## Summary

On the classic engine, the built-in Postgres and Kafka destinations write one record per round
trip unless the user sets `sdk.batch.size`. Conduit will set `sdk.batch.size=1000` and
`sdk.batch.delay=100ms` for those two plugins when the user set neither key. The injection is in
`pkg/connector` (`Destination.configure`), switched on by `pkg/lifecycle` for regular destinations
only. It never applies to a DLQ, to arch-v2, to sources, to other plugins, to standalone plugins
that share a built-in's bare name, or to Kafka mirror mode. Connector specs do not change.

Tier 1 (data path, default behavior change). Roadmap item: v0.21 Kafka Connect parity.

## Context and problem

ConduitIO/conduit#3228 (AWS c6i.2xlarge, 20k ops/s, classic engine) measured both destinations
against Kafka Connect at their shipped defaults:

| Destination | defaults | `sdk.batch.size=1000`, `sdk.batch.delay=100ms` |
| --- | --- | --- |
| Postgres | 0.302x | 0.999x |
| Kafka | 0.301x | 1.002x |

The cause is the SDK's `writeStrategySingle`: with `sdk.batch.size=0` each record is its own
`Write`, so each record is its own database round trip or produce request. The SDK default is 0
for every connector, and the built-in connectors inherit it.

A shipped default is what most users run. Parity with Kafka Connect is a v0.21 goal and users do
not tune what they do not know is slow.

## Constraints

- **arch-v2 batches in the funnel** (`pkg/lifecycle-poc/funnel`, #3201/#3203). It reads the
  user's `sdk.batch.*` settings from the connector config, strips them before `Configure`
  (`Destination.engineBatches`), and runs the plugin unbatched under a stop-and-wait loop. A
  default that reaches the plugin on arch-v2 puts SDK batching underneath that loop: every write
  waits for the SDK's size or delay before it is acked.
- **The arch-v2 guard reads user settings only.** `checkDLQBatching` and `PreflightPipelineConfig`
  look at the raw `DLQ.Settings` map; `config.SDKBatchSettings` in `lifecycle-poc/service.go` reads
  `instance.Config.Settings`. Neither sees spec defaults, so a spec default would not be refused
  and would not be stripped. It would silently apply.
- **The DLQ is also a destination object** (`buildDLQHandlerNode`, `ProvisionTypeDLQ`). A DLQ must
  not batch: its acks drive the nack window.
- **Standalone plugins keep their own defaults.** A bare `postgres` resolves to a standalone plugin
  first and falls back to the built-in (`PluginService.newDispenser`).
- Stored config and `LastActiveConfig` are compared to decide lifecycle events and are persisted.
  They must not change.
- Any data-path default change needs the invariants in CLAUDE.md to hold with the default on.

## Alternatives considered

### A. Connector spec defaults (rejected)

Set `sdk.batch.size` and `sdk.batch.delay` defaults in conduit-connector-postgres and
conduit-connector-kafka (specgen picks them up from the config struct; I built this and it works).

- **Silent slowdown under arch-v2.** The guard and the funnel see user settings, so they do not
  see a spec default. The plugin applies 1000/100ms inside the SDK under the funnel's stop-and-wait
  loop, and a postgres or Kafka DLQ gets it too. No error, only throughput and latency.
- **Overrides standalone defaults.** The same spec ships as a standalone plugin; users of the
  standalone binary get the new default with no engine involved.
- **Opt-out trap.** With SDK v0.17.2 and later, `sdk.batch.size=1` plus a default delay is
  delay-only batching capped at 1000, so `sdk.batch.size=1` alone would not opt out. The
  connector would have to rewrite the delay in `Validate`.
- It changes the published spec and needs a connector release per connector before Conduit sees
  it.

### B. Global injection for all destinations (rejected)

Apply the default to every classic destination plugin. Rejected: only Postgres and Kafka have a
benchi result and acknowledgement tests at this setting. Others (S3, file, generator, log,
third-party) have unmeasured memory per batch, object-store semantics (S3 writes one object per
batch) and partial-failure behavior. A default is a promise; ours covers two plugins.

### C. Documentation only (rejected)

Tell users to set `sdk.batch.size`. This is the status quo (the Kafka README already does). It
leaves the headline Kafka Connect comparison at 0.3x for everyone who did not read it.

### D. Chosen: engine-side injection, classic only, allowlisted built-ins

Described below.

## Decision

`Destination.configure` (`pkg/connector/destination.go`) works on the resolved copy
(`env.ResolveSettings` returns a new map). When `classicDefaultBatching` is set, it calls
`classicBatchDefaults(settings, builtinName)` (`pkg/connector/classic_batching.go`), which sets
`sdk.batch.size=1000` and `sdk.batch.delay=100ms` when all of these hold:

1. **Allowlist.** The dispenser is a built-in (`BuiltinDispenser.BuiltinPluginName`, implemented by
   `builtin.Dispenser` and forwarded by the cleanup wrapper) named `postgres` or `kafka`. A
   standalone plugin with the same bare name reports no built-in name and is skipped.
2. **User unset.** Neither `sdk.batch.size` nor `sdk.batch.delay` is set. The two keys are one
   unit: either one set, to any value, means the user chose their batching and nothing is
   injected. Set means the trimmed key has a non-empty trimmed value, the SDK's own `ApplyDefaults`
   rule. Writing `1000` out explicitly is therefore the same as leaving it unset.
3. **Not Kafka mirror mode.** The Kafka destination's `mirror` setting (`json:"mirror"`, a bool)
   is absent, empty or parses as false. Any other value, including a mistyped one, excludes the
   plugin so the connector's own validation reports it.

`pkg/lifecycle` `buildDestinationNodes` calls `SetClassicDefaultBatching()` on regular destinations.
`buildDLQHandlerNode` does not, and `pkg/lifecycle-poc` never does. `engineBatches` takes
precedence (an `if/else if`), so even a mistaken call cannot put the default back after arch-v2
strips the keys.

**Opt-out.** `sdk.batch.size: 1` (or `0`) is enough: the key is set, so nothing is injected and the
delay stays unset, which is a full opt-out on every SDK version. Conduit does not rewrite the
user's explicit values: `size=1` with an explicit delay is passed through as the user wrote it,
with whatever meaning the SDK gives it. That is why the delay is not dropped by Conduit.

**Sources are never touched.** The code is in `Destination.configure` only; the source adapter has
no flag and no injection.

Existing pipelines with no batch settings change behavior on upgrade (see Upgrade and rollback).

## Failure modes

Answered before writing code, per invariants 1, 3, 4 and 7.

- **Invariant 1 (no ack before durable).** The SDK acks a record only after the connector's
  `Write` returns for the batch that holds it (`writeStrategySingle.Write` acks `batch[:n]` and
  nacks the rest). Postgres writes a batch in one implicit transaction and returns 0 on any
  failure. Kafka returns the index of the first unconfirmed record and keeps `acks=all` and the
  idempotent producer. Nothing here acks earlier; the default only decides the batch size.
- **Invariant 3 (at-least-once), crash mid-batch.** A batch that is open or in `Write` when the
  process dies is not acked, so the source replays it. Up to 1000 records replay instead of 1.
  - **Postgres keyed tables:** upserts and deletes are idempotent, so replay is harmless.
  - **Postgres keyless tables:** keyless inserts append again. The duplicate window grows from at
    most the in-flight record to up to 1000 records. This is the main user-visible cost.
  - **Kafka:** the destination is not idempotent across a replay, so up to 1000 records can be
    duplicated per crash. The idempotent producer only removes duplicates from its own retries.
- **Partial batch failure.** Postgres: the whole batch is rolled back and nacked, nothing stored.
  Kafka: records before the first failure are acked, that record and all after it are nacked.
  A nack with no DLQ stops the pipeline; with a DLQ, each nacked record goes to the DLQ
  individually, and the DLQ itself is never batched.
- **Poison-record blast radius (DLQ).** A nack is per record. With one record per `Write`, one bad
  record is the only one nacked. With a batch, Postgres rolls the whole batch back, so one bad
  record nacks up to 1000 good ones, which then go to the DLQ (or count against its nack
  threshold, halting the pipeline sooner). Kafka nacks the failed record and everything after it
  in the batch. Nothing is lost (invariant 3 holds), but good records are diverted. This is the
  largest behavior change for pipelines with a DLQ. Mitigation, not in this change: have the
  destination retry a failed batch record by record to isolate the poison one. Users who rely on
  per-record DLQ isolation should set `sdk.batch.size: 1`. This needs DeVaris's decision before
  merge.
- **Invariant 4 (ordering).**
  - Postgres applies a batch in record order in one transaction, so create/update/delete of one
    key keep their order.
  - Kafka: records of one key go to one partition in the order enqueued. The existing documented
    exception widens: if the broker rejects a record after it was enqueued, later records of the
    batch may already be stored, and the replay stores the rejected record after them. Single-record
    writes could not hit this. With a batch of 1000 it is reachable in normal operation, whenever a
    record is rejected (for example `MESSAGE_TOO_LARGE`). The README already says per-key ordering is
    not claimed for the destination in the acceptance suite. A same-partition rejection within one
    produce usually fails the whole Kafka record batch, so this has not been reproduced.
  - **Kafka mirror mode is excluded.** Mirror mode promises per-partition order until the first
    produce error. A batch widens exactly that window, so mirror pipelines keep one record per
    `Write` unless the user sets `sdk.batch.*` themselves. `mirror` is matched on the trimmed key
    and a value that is not plain false.
- **Invariant 7 (graceful shutdown).** `Stop` flushes the pending batch (`writeStrategyBatch.Flush`,
  after waiting for the last position), covered by `ConduitIO/conduit#3092`'s fix. A `Teardown`
  without `Stop` (pipeline stopping on an error) leaves the batch unwritten and unacked, so it
  replays. The injected pair always includes a delay, so a quiet pipeline flushes within 100 ms
  without `Stop`.
- **Latency at low volume.** A record that arrives alone waits up to 100 ms before it is written
  (the delay is measured from the first record of the batch). A pipeline that needs lower latency
  sets `sdk.batch.size: 1` or its own pair.
- **Memory per batch.** At most 1000 records buffered in the SDK per destination, plus the Kafka
  producer's own buffers. A pipeline with large records can set a smaller size.
- **Throughput limit on Kafka (dependency).** The Kafka destination's batched path waits out
  franz-go's default 10 ms linger on every `Write`, which caps it at about batch/12 ms (about
  83k records/s at 1000). A separate lane is fixing that in conduit-connector-kafka with
  `ProduceSync`. The Kafka half of this change depends on that fix to reach the #3228 number at
  rates above that cap; at 20k ops/s the cap is not binding. The design does not need the fix for
  correctness.
- **Other interactions.** A source that already batches (`sdk.batch.size` on the source) still
  gets the destination default, so the destination sees batches of at least the source's size and
  batches them again; this is harmless. A user-set `sdk.rate.perSecond` splits a batch into
  chunks of `sdk.rate.burst`, so the rate limit still holds. Each pipeline restart (error
  recovery) logs the injection line again.
- **Wrong allowlist match.** If a connector's spec name changes, the default silently stops
  applying (fails safe: slow, not wrong). `builtin_name_test.go` pins the names.
- **Config surface invisible.** Validate, doctor and the persisted config do not show the
  injected values (see Observability).

## Upgrade and rollback

- **Behavior change on upgrade, for release notes.** Existing classic pipelines that use the
  built-in `postgres` or `kafka` destination and set neither `sdk.batch.size` nor `sdk.batch.delay`
  start batching on upgrade: up to 1000 records and 100 ms. Release-note line: "Built-in Postgres
  and Kafka destinations now batch writes by default on the classic engine (1000 records / 100 ms,
  about 3x throughput in our benchmark). A record can wait up to 100 ms, and a crash can replay up
  to 1000 records. Set `sdk.batch.size: 1` on the destination to keep the old behavior."
- **No stored-state change.** Settings, `LastActiveConfig` and positions are not modified, so
  downgrade needs nothing: the older Conduit just stops injecting.
- **Rollback.** Per pipeline: `sdk.batch.size: 1` (or `0`). Global: revert the one commit; there
  is no flag. If a global switch is wanted it should be a runtime config, decided in review.
- **Connector versions.** Works with the connector versions already pinned (postgres v0.17.2,
  kafka v0.14.1, SDK v0.17.0) and with SDK v0.17.2+ (no delay is injected next to a user's
  `size=1`).
- **Postgres statement cache mode is unchanged** (`describe_exec` stays the default; this design
  does not touch it or the PgBouncer advisory GHSA-rcwq-pcpp-gx9v).

## Observability

One INFO line at configure when the default is injected, with the connector ID and plugin, naming
the values and the opt-out. A DEBUG line says why it was not applied (user set it, or mirror).
Effective batch size is not in validate, doctor or the stored config; the log is the record.
Follow-ups, not in this change: expose the effective settings on the connector's API object, and
a batch-size histogram.

## Arch-v2 removal plan

Arch-v2 already batches in the funnel from the same user keys. When arch-v2 becomes the default
and the classic engine is removed:

1. The injection and `classicDefaultBatching` are deleted with `pkg/lifecycle`.
2. The funnel's own default (3000 rows, #3203) is the only default left. Change it there if the
   two should agree.
3. The offline `validate` / `doctor` arch-v2 batching check, which still flags destinations that
   runtime no longer refuses, is removed or limited to the DLQ (tracked as a separate issue).

Until then the two paths are kept apart by `if/else if` in `configure` and by a regression test
that arch-v2 never receives the keys.

## Note: postgres source `snapshot.fetchSize` is dead config

`snapshot.fetchSize` in conduit-connector-postgres is not read. The snapshot FETCH size is
`sdk.batch.size`, or 50000 when that is 0. This is relevant here because the injection applies to
destinations only: a source's `sdk.batch.size` is never touched, so the snapshot fetch size of a
Postgres source is unaffected. The Kafka Connect migration mapping that maps `snapshot.fetch.size`
to `snapshot.fetchSize` (docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md,
docs/catalog/v0.21-certification-gaps.md) points at a setting that does nothing and should map to
`sdk.batch.size` on the source. That correction is out of scope here.

## Test plan

Unit (`pkg/connector`): the injection rules table; the resolved copy only; stored config
unchanged; standalone skipped; `engineBatches` beats the default; Kafka mirror variants.
Registry (`pkg/plugin/connector/builtin`): dispenser reports `postgres` and `kafka`, also through
the cleanup wrapper. Lifecycle (`pkg/lifecycle`): destination gets the keys, DLQ does not.
Lifecycle-poc: arch-v2 never gets them (fails if the call leaks in). Chaos (`tests/chaos`): SIGKILL
mid-batch and mid-stream over the real SDK batch strategy; opt-out and default write sizes.
Connector-level acknowledgement tests (ack after write, partial failure, flush on stop) are in
conduit-connector-postgres and conduit-connector-kafka.

## Related

- ConduitIO/conduit#3228 (evidence), #3201 / #3203 (funnel batching),
  `docs/design-documents/20261017-funnel-destination-batching.md`, #3092 (stop flush).
- ConduitIO/conduit-connector-kafka: franz-go linger / `ProduceSync` (dependency for the Kafka
  half).
