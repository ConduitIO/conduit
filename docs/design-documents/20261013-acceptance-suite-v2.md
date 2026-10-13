# Acceptance suite v2: what "connector" means for certification

## Summary

The SDK acceptance suite is the compatibility contract for connectors, and CLAUDE.md calls expanding it
high-leverage. Today it has one version, 13 tests, and checks one thing about data: that a record written
through a destination can be read through a source, one call at a time, with no failure injected. It does not
exercise the stream Conduit actually uses, never kills anything, and does not look at acknowledgements. A
destination that acks before the write is durable can pass it.

This document proposes suite v2: a versioned entry point (`sdk.AcceptanceTestV2`) that keeps v1's specification
and configuration tests, replaces v1's data-path tests with pipeline-faithful ones, and adds 20 cases covering
pipelined and slow acks, mixed batch sizes, crash and replay, ordering, schema evolution, large records, graceful
stop, context cancellation and a throughput smoke check. Cases that depend on a
property of the connector (idempotent writes, ordering, a schema policy) run only if the connector claims it, and
a claim that is made is verified. A run produces a JSON report with the suite revision, the claims and every
case's outcome. That report is what the registry records for the "Conduit Certified" tier.

The suite lands in `conduit-connector-sdk` (`ConduitIO/conduit-connector-sdk#397`). This document lives here
because it also decides how the registry records a result and how non-Go connectors reach the same cases.

Roadmap: v0.21, "Acceptance suite v2, the basis for Conduit Certified".

## Context

### What v1 checks today

`sdk.AcceptanceTest(t, driver)` runs these tests against a `sdk.Connector`, in process, through the Go
`Source`/`Destination` interfaces:

| v1 test | What it proves | What it does not |
| --- | --- | --- |
| `TestSpecifier_Exists`, `_Specify_Success` | A specification exists and is well formed | |
| `TestSource_Parameters_Success`, `TestDestination_Parameters_Success` | Parameters are documented | |
| `TestSource_Configure_Success`, `TestDestination_Configure_Success` | The supplied config parses | |
| `TestSource_Configure_RequiredParams`, `TestDestination_Configure_RequiredParams` | Dropping a required parameter fails configure | |
| `TestSource_Read_Success` | 10 snapshot records, then 20 CDC records, come back with unique positions | Ordering, duplicates under failure, batches |
| `TestSource_Read_Timeout` | An idle source returns deadline exceeded or backoff-retry | That `Read` honours cancellation |
| `TestSource_Open_ResumeAtPositionSnapshot`, `_CDC` | After acking half and restarting from the last acked position, the rest is delivered | Anything beyond one clean restart |
| `TestDestination_Write_Success` | One batch of 20 records can be read back | Acks, durability, crash, batch size, concurrency |

Properties of the harness that matter for what follows:

- It calls `Destination.Write` synchronously, one batch at a time, and `Source.Read` one record at a time. Conduit
  does neither: it streams batches over `pconnector.DestinationPlugin.Run` and acknowledges asynchronously, so the
  SDK's batching, ack-sending and stop logic (`destinationPluginAdapter`, `sourcePluginAdapter`) is not exercised.
- It never injects a failure. A restart is a clean `Teardown` after every `Ack` the test intended to send.
- It never inspects acknowledgements, so it cannot see invariant 1 (never ack before the write is durable).
- It assumes every connector has a paired source and destination on the same system. Generator (source only),
  log (destination only) and S3 (the source returns one record per object, the destination writes many per
  object) cannot satisfy that without a custom driver.
- It calls `Source.Read` directly, which fails for sources that implement only `ReadN`.
- It reads many records before acknowledging any. A source that holds back the next batch until the previous one
  is acked cannot pass `TestSource_Read_Success` or the two resume tests as written. The Postgres source is one
  (`ReadN` fails with "failed to wait for acks: context deadline exceeded"); Conduit acks, so it works in a
  pipeline.
- It runs only in Go.

### Who runs it today

Of the five connectors the suite was run against for this work (file, generator, log, Postgres, S3), only
`conduit-connector-file` calls `sdk.AcceptanceTest`. The other four have no acceptance test at all. The Postgres
connector has a large chaos suite of its own, which is the right depth for that connector, but nothing ties it to
the contract other connectors are held to. SDK `main` itself is red on the suite since `twmb/go-cache` v1.4.0
(`conduit-connector-sdk#392`); another lane is fixing that, v2 does not depend on the fix and ignores the same
goroutine in its own reference test with a pointer to the issue.

### What v1 catches and what it lets through

The implementation PR carries a reference connector and a mutant for each bug class below. The v1 column is
measured: the v1 suite was run against every mutant.

| Mutant (bug class) | Invariant | v1 | v2 case |
| --- | --- | --- | --- |
| Destination drops every 7th record but returns `n == len(recs)` | 3 | fails (record count) | DST-01 |
| Destination acks, buffers, flushes 400 ms later; a crash loses acked records | 1, 3 | fails, because the read-back races the flush | DST-04 |
| Destination claims idempotent writes, duplicates on replay | 3 | passes (v1 has no notion of the claim) | DST-05 |
| Destination reverses records within a batch | 4 | passes | DST-06 |
| Destination drops a new field silently | 6 | passes | DST-07 |
| Source resumes one record late after a restart | 2, 3 | fails | SRC-03 |
| Source `Read` ignores its context | 7 | passes | SRC-08 |

v1 already catches the blunt cases. What it cannot see is everything that needs a failure, a claim, an ack or a
context, and a connector that buffers its writes in a way that survives a read-back but not a kill.

## Constraints

- v1 keeps working unchanged. Connectors that call `AcceptanceTest` today must not need a change. (Behavior
  preserving fixes to shared v1 helpers are in the implementation PR and listed under upgrade and rollback.)
- A test that applies only to some connectors must say why it did not run. A silent skip is indistinguishable
  from a pass.
- No performance pass/fail threshold at first. CLAUDE.md: no performance claim without a committed benchmark. The
  smoke check records a number; benchi remains the tool for claims.
- No protocol change. Anything that would change `conduit-connector-protocol` (for example a `capabilities` field
  in the specification) needs its own versioning discussion.
- The suite must be runnable by a connector author with `go test`, with no engine and no registry.

## Decision

### 1. A versioned entry point

```go
sdk.AcceptanceTest(t, driver)    // v1, unchanged
sdk.AcceptanceTestV2(t, driver)  // subtest "Core" (v1 spec and config tests), then subtest "V2" (the cases below)
```

A connector has passed suite v2 only if both groups pass. Existing drivers work with v2 as they are. v1 is
frozen: bug fixes only, no new tests.

**What v2 keeps from v1 and what it replaces.** The 8 v1 specification, parameter and configuration tests do not
depend on how data is read or acked, and run unchanged as "Core". The 5 v1 data-path tests do not run under v2.
Each is covered by a v2 case that does the same through the plugin interface and acks like a pipeline:

| v1 test | v2 case |
| --- | --- |
| `TestSource_Read_Success` (snapshot then CDC, unique positions) | SRC-02 (also checks position uniqueness) |
| `TestSource_Read_Timeout` | SRC-08 (idle `Read` honours its deadline and its cancellation) |
| `TestSource_Open_ResumeAtPositionSnapshot`, `_CDC` | SRC-03, SRC-07 |
| `TestDestination_Write_Success` | DST-01, DST-03 |

Running them again would make v2 unpassable by exactly the connectors that need it (ack-gated sources such as
Postgres) and by generators and sinks that cannot be seeded or read back.

v2 drives the connector through `pconnector.SourcePlugin` / `pconnector.DestinationPlugin`, the interfaces Conduit
uses, with a buffered in-memory stream standing in for gRPC. The host side of the harness behaves like Conduit:
it sends batches without waiting for acks, collects acks on a separate goroutine, calls `Stop` with the last
position it sent, then closes the stream and calls `Teardown`. Records are copied across the boundary, because a
real host never shares memory with the plugin and the SDK middleware mutates what it receives. Cases that do not
withhold acks on purpose ack what they read, because a source may legitimately wait for acks before moving from
snapshot to change capture.

### 2. Cases

IDs are stable and never reused; reports, the registry and the language-neutral runner (below) refer to them.

| ID | Case | Applies | What is checked | Invariant |
| --- | --- | --- | --- | --- |
| DST-01 | `pipelined_writes` | destination | 20 batches sent before any ack is read; every position acked once, no nack; stored data equals sent data exactly | 1, 3 |
| DST-02 | `slow_acks` | destination | Host acknowledges slowly and the ack stream blocks the plugin; no deadlock, no loss | 1 |
| DST-03 | `mixed_batch_sizes` | destination | Batches of 1, 100 and 10000 (configurable); all acked, all stored once | 3 |
| DST-04 | `crash_replay_at_least_once` | destination, read-back | Kill mid-stream; everything acked before the kill is stored; replay of the whole batch leaves nothing missing, duplicates allowed | 1, 3 |
| DST-05 | `crash_replay_idempotent` | claim `IdempotentWrites` | Same as DST-04, and replay leaves no duplicates | 3 |
| DST-06 | `ordering_per_key` | claim `OrdersByKey` | Records sharing a key are stored in write order | 4 |
| DST-07 | `schema_evolution` | claim `SchemaEvolution` | `evolve`: a record with a new field is stored with the field. `reject`: it is nacked with an error and earlier data is intact. Silent drop or coercion fails | 6 |
| DST-08 | `large_record` | destination | A record of 1 MiB (up to 3 MiB, below the 4 MiB default gRPC limit) is stored intact | 3, 6 |
| DST-09 | `graceful_stop_drains_in_flight` | destination | `Stop` while 50 batches are queued returns only after every record is acked and stored; nothing leaks | 7 |
| DST-10 | `context_cancellation` | destination | Cancelling mid-stream ends `Run` promptly; whatever was acked is stored | 1, 7 |
| DST-11 | `throughput_smoke` | destination | Records/s over N seconds (default 3), recorded, not asserted; all acks successful | none |
| SRC-01 | `pipelined_and_lagging_acks` | source | Acks arrive from another goroutine, lagging reads by 50 records, while reading continues; no deadlock or loss; restart from the last position is clean | 2, 3 |
| SRC-02 | `mixed_batch_sizes` | source | 1, 100 and 10000 records, snapshot then CDC, delivered once each | 3 |
| SRC-03 | `crash_resume_no_gap` | source | Four kills at different points with partial acks, then a resume from the last acked position; every record is delivered at least once across all runs | 2, 3 |
| SRC-04 | `ordering_per_key` | claim `OrdersByKey` | Records sharing a key arrive in write order | 4 |
| SRC-05 | `schema_evolution` | claim `evolve` | A field added while the source runs reaches the output intact | 6 |
| SRC-06 | `large_record` | source | A 1 MiB record arrives intact | 3, 6 |
| SRC-07 | `graceful_stop_with_unacked` | source | `Stop` with delivered-but-unacked records reports the last delivered position; resume from the last acked position leaves no gap; nothing leaks | 2, 7 |
| SRC-08 | `context_cancellation` | source | An idle `Read` returns when its context is cancelled; an idle plugin stops and tears down | 7 |
| SRC-09 | `throughput_smoke` | source | Records/s reading 2000 records, recorded, not asserted | none |

"Read-back" means the driver can return what the destination stored. DST-04 and the other stored-data checks
need it; the cases that only need acks (DST-01 apart from the content check, DST-02, DST-03 apart from the
content check, DST-09 apart from the content check, DST-10, DST-11) run for write-only destinations such as `log`.

What the in-process harness cannot do: kill -9. A cancelled context and a missing `Stop` approximate a crash, but
`Teardown` still runs afterwards to release resources, so a connector that flushes only in `Teardown` would be
rescued by the harness. The standalone harness (section 5) kills the process, and the chaos suites in connector
repos remain the proof for Tier 1 connectors. v2 raises the floor; it does not replace chaos testing.

### 3. Capability declaration

Capabilities are declared in the test driver, next to the config that exercises them:

```go
sdk.ConfigurableAcceptanceTestDriverConfig{
    Connector: Connector,
    Capabilities: sdk.AcceptanceCapabilities{
        Destination: sdk.DestinationCapabilities{IdempotentWrites: true, SchemaEvolution: sdk.SchemaEvolutionReject},
        Source:      sdk.SourceCapabilities{OrdersByKey: true},
    },
}
```

The zero value claims nothing, so a connector that declares nothing runs the baseline cases only and the report
lists what was skipped and why. Other fields: `Source.Synthetic` (a generator cannot be seeded; the cases that
write expectations skip, the flow cases run), `Destination.WriteOnly` (no read-back), `MaxBatchSize`,
`MaxRecordBytes`, `ThroughputDuration`. Drivers can optionally implement `ReadAllFromDestination` (needed to
tell duplicates from loss), `GenerateRecordWithKey` and `GenerateLargeRecord`.

A claim is a promise the suite tests, so claims cannot inflate a result: DST-05 fails an idempotence claim that
duplicates on replay. Not claiming is also visible: the registry shows "ordering: not claimed", and the
Certified bar (below) names which claims are mandatory.

### 4. Versioning and what the registry records

The suite has a revision, `major.minor.patch`, written into every report (`suite_revision`):

- **major** is the bar. v2 is the current bar. A new major means new baseline cases or tighter semantics.
- **minor**: new cases that run only when a capability is claimed, or new claims. A connector that passed
  `2.0.x` still passes `2.1.x` without changes, because the new cases skip until it claims something.
- **patch**: harness fixes that do not change what a connector must do.

The report is JSON (`CONDUIT_ACCEPTANCE_REPORT=path`): suite version and revision, SDK version, connector name
and version, the claims, every case with outcome, duration and skip reason, recorded metrics. The connector's
release workflow runs the suite against the release candidate and attaches the report as a release asset next to
the signed artifacts.

The registry records, per connector version, a `conformance` entry (the object proposed in
`20261008-sdk-parity-and-open-plugin-protocol.md`; if that document lands differently, the same fields go under
a sibling `acceptance` key):

```json
{ "suite": "acceptance", "revision": "2.0.0", "mode": "sdk-in-process",
  "claims": ["destination.idempotent_writes"], "skipped": ["SRC-04"],
  "reportSha256": "…" }
```

`tier` stays what #2963 defined: set by registry maintainers in the index, never by a publisher, never a trust
input. Passing v2 is a necessary condition for `certified`, not a sufficient one (certified also needs
integration tests against the real system in CI, a kill -9 chaos test, a committed benchi run and docs). The
index-CI check for a connector entry proposing `certified` verifies that the referenced report exists, matches
`reportSha256`, shows `passed: true` for suite major 2, and that no baseline case was skipped.

Deprecation of a major follows the project rule: announce, warn in index-CI, remove after at least two minor
releases. v1 results stay valid for `verified`.

### 5. Standalone and non-Go connectors

The v2 cases are written against `pconnector.SourcePlugin` / `DestinationPlugin`, not against SDK types. The
in-process Go entry point wraps a `sdk.Connector` with `NewSourcePlugin` / `NewDestinationPlugin`. Reaching a
connector in another process or language needs only a different way of obtaining the same two interfaces: the
`pconnector/client` package already launches a binary and returns them over gRPC. The case IDs and semantics are
the contract; each language runner implements them.

The runner for non-Go connectors is not built here. It belongs to `conduit plugins conformance` proposed in
`20261008-sdk-parity-and-open-plugin-protocol.md`, which already needs the connector-utilities service the SDK
requires of a standalone binary (`CONDUIT_CONNECTOR_UTILITIES_GRPC_TARGET`) and a fixture config. That runner
reuses this document's case table, IDs, claims and report schema, so a Python connector and a Go connector
produce comparable reports. It is also where kill -9 becomes testable. What this decision commits to: case IDs
are the shared vocabulary, and the report schema is language-neutral.

Fixture and seeding for non-Go connectors: where the connector has a paired source and destination on the same
system, the runner uses the round trip as `ConfigurableAcceptanceTestDriver` does. Otherwise the author supplies
seed and read-back hooks, which is the same split as `Synthetic` and `WriteOnly` in the Go driver.

## Alternatives

1. **Extend v1 in place.** Simplest. Rejected: every connector that passes today would start failing without a
   change on its side, a result could not say which bar was passed, and the new cases would have to be unconditional
   although half of them are only meaningful with a claim.
2. **Declare capabilities in the connector specification (`connector.yaml`).** The author states them once, the
   registry reads them without running anything, and non-Go connectors get them for free. Rejected for now:
   `Specification` is part of the connector protocol (`conduit-connector-protocol` is breaking-change territory
   and needs a versioning discussion), and a claim read from the spec is a claim nobody has exercised. A claim in
   the driver sits next to the config that proves it, and the report carries it out. Revisit once there are two
   language SDKs consuming claims; the report already has the shape a spec field would take.
3. **A separate conformance module or repository owning the suite.** Cleaner dependency story and release
   cadence. Rejected: the harness needs the SDK's plugin adapters and test driver, and splitting now costs a
   module boundary for no second consumer. The language-neutral runner in the engine (section 5) is the second
   consumer, and the case table is what moves, not Go code.
4. **Do the language-neutral engine runner first and skip the Go suite.** The right end state. Rejected as the
   first step: it needs the engine-side service, a fixture format and a kill harness, none of which exist, and
   connector authors need something runnable with `go test` this release.

## Failure modes

For a data-integrity test suite the failure that matters is a false pass.

- **Suite passes a broken connector.** Mitigation: a mutant test per bug class in the SDK repo (reference
  connector plus seven mutants run in a subprocess; the suite must fail each on the named case). A new case lands
  with a mutant that it kills.
- **Suite fails a correct connector (flake).** Timeouts guard against loss, not slowness (10x the driver
  timeout, at least 10 seconds), and reads end on a quiet period. Cases that need the source to continue from a
  snapshot into change capture ack what they read, like a pipeline; a source may legitimately wait for acks.
  Flake rate is tracked once the suite runs in CI on the reference connector; the target is the CLAUDE.md
  Certified bar.
- **In-process crash is not a crash.** Stated above. Certified requires the connector's own kill -9 test.
- **Shared-memory artifacts.** The SDK middleware mutates records in place. The harness copies records on both
  sides of the stream; the race detector run on the reference connector covers it.
- **Claims used to dodge cases.** A connector can claim nothing and pass the baseline. Mitigation: the report
  and the registry entry show what was skipped, and the Certified check names the claims it requires. Which
  claims are mandatory per connector class is an open decision (below).
- **External state pollution.** Cases write real data into real systems. The driver's `BeforeTest` and
  `AfterTest` own isolation and cleanup, as for v1.
- **Time.** On the file connector the full suite (Core plus v2) runs in about 20 seconds. 10000-record cases
  dominate for slow systems; `MaxBatchSize` lowers it with a documented reason.
- **Test data generation.** `ConfigurableAcceptanceTestDriver` re-seeded its random generator on every call,
  4 ms per record, so 10000 records took 41 seconds to generate. Fixed in the implementation PR (shared locked
  source).

## Upgrade and rollback

Additive: `AcceptanceTestV2` is a new symbol, `Capabilities` a new optional field on the config struct, and the
driver gains optional interfaces. Nothing is required of existing drivers. Changes to code v1 shares, all
behavior preserving for connectors that pass today: a wildcard required parameter (`collections.*.operations`)
is no longer treated as a key the config must contain; `Source.Read` falls back to `ReadN(1)` for sources that
implement only `ReadN`; the default driver's random generator is shared instead of re-seeded per call. The first
two only turn a test-setup failure into a run. Rollback is to stop calling `AcceptanceTestV2`; the registry ignores a missing
report.

The suite ships with the SDK, so a connector tests with the revision its pinned SDK contains. The report records
both, and the registry check compares the revision, not the SDK version.

## Observability

- JSON report per run with revision, claims, per-case outcome, duration, skip reason, metrics.
- Throughput numbers (`destination_throughput`, `source_throughput`) with their conditions in the `detail` field,
  to build a history; they are never compared to a threshold here.
- Failure messages name the invariant at stake and the evidence (counts, first missing record), for example
  "N of M records are not stored; an acked record must be durably written".
- A CI summary step in the connector template prints the report as a table.

## Open decisions for DeVaris

1. **Mandatory claims for Certified.** Proposal: destinations must either claim `IdempotentWrites` or document
   why not; sources that read an ordered log (Kafka, Postgres CDC) must claim `OrdersByKey`; every schema-aware
   connector must declare a `SchemaEvolution` policy. Alternative: leave claims optional and rely on the
   skipped list. This changes what Certified means and needs your call before the registry check is written.
2. **Where capabilities live long term.** Driver now (this document), spec later. Confirm that deferring the
   protocol discussion is right.
3. **Registry shape.** Share the `conformance` object with the SDK-parity document, or keep a separate
   `acceptance` entry. Depends on which of the two documents lands first.
4. **Release.** The suite ships in the next SDK minor. Say if connector repos should adopt it before the
   Certified connectors are cut.

## Related

- `ConduitIO/conduit-connector-sdk#397`: the implementation, with the per-connector results (file, generator, log,
  Postgres, S3) and the issues they produced.
- `20261008-registry-license-and-tier.md` (#2963): the tier field this feeds.
- `20261008-sdk-parity-and-open-plugin-protocol.md` (#2948): the language-neutral conformance runner and the
  `conformance` registry object.
- `conduit-connector-sdk#392`: SDK `main` red on the v1 suite (`twmb/go-cache` goroutine); another lane.
- `docs/code_guidelines.md`, CLAUDE.md data integrity invariants 1 to 7.
