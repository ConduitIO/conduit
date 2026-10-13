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
the contract other connectors are held to.

### What v1 catches and what it lets through

The implementation PR carries a reference connector and a mutant for every case. The table shows the mutants that
have a v1 counterpart; the v1 column is measured: the v1 suite was run against each of them.

| Mutant (bug class) | Invariant | v1 | v2 case |
| --- | --- | --- | --- |
| Destination drops every 7th record but returns `n == len(recs)` | 3 | fails (record count) | DST-01 |
| Destination acks, buffers, flushes 400 ms later; a crash loses acked records | 1, 3 | fails, because the read-back races the flush | DST-04 |
| Destination claims idempotent writes, duplicates on replay | 3 | passes (v1 has no notion of the claim) | DST-05 |
| Destination reverses records within a batch | 4 | passes | DST-06 |
| Destination drops a new field silently | 6 | passes | DST-07 |
| Destination acks, buffers, and flushes only in `Teardown` | 1, 3 | fails, because the read-back runs before `Teardown` | DST-04 |
| Source resumes one record late after a restart | 2, 3 | fails | SRC-03 |
| Source ignores the position and starts over | 2, 3 | fails (the CDC resume test is exact) | SRC-03 |
| Source redelivers the last three acked records on resume, `ExactResume` claimed | 3 | fails | SRC-03 |
| Source resumes from a change capture position one record late (CDC resume loses one) | 2, 3 | fails | SRC-03 (CDC cycles) |
| Source resumes from a change capture position by starting the changes over | 2, 3 | fails | SRC-03 (CDC cycles) |
| Source resumes from a position it committed in `Teardown` only, and ignores the host's | 1, 2 | passes (v1 tears down before restarting) | SRC-03 (no `Teardown` on a kill) |
| Source reorders records within a key after a restart | 4 | fails (v1 compares exact order after resume) | SRC-04 |
| Destination buffers only batches over 500 until `Teardown` | 1, 3 | passes | DST-04 (large batch kill point) |
| Destination reorders batches over 100 | 4 | passes | DST-06 (batches of 300) |
| Destination reorders only after a restart | 4 | passes | DST-06 (across a restart) |
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
| `TestSource_Open_ResumeAtPositionSnapshot` (CORE-11) | SRC-03 snapshot cycles (no gap **and** bounded redelivery), SRC-07 |
| `TestSource_Open_ResumeAtPositionCDC` (CORE-12) | SRC-03 change capture cycles: records written after `Open`, a partial ack while capturing changes, a restart from that CDC position |
| `TestDestination_Write_Success` | DST-01, DST-03 |

The resume rows need a precise statement, because two drafts of this document got them wrong.

First draft: v1 is stricter than a bare "no gap" check. Its CDC test requires that after a restart from the last
acked position exactly the remaining records arrive, and its snapshot test accepts a restart from scratch only
because a snapshot may legitimately be taken again. A v2 that only checked "nothing is missing" would pass a source
that ignores the position and starts over. SRC-03 therefore bounds redelivery: after each restart the source may
deliver at most `MaxRedelivery` records that were already acked (default 10, `0` for a connector that claims
`ExactResume`).

Second draft: SRC-03 wrote all its data before the first `Open`, so every position it resumed from was a snapshot
position. v1's CDC test resumes from a change capture position, and two mutants that only misbehave there (CDC
resume loses one record, CDC resume starts over) survived all of v2 while v1 killed them. CORE-12 is only marked
replaced because SRC-03 now has change capture cycles of its own, and the two mutants are in the mutant set.

The bound is not a free parameter. A large `MaxRedelivery` would turn the check into a no-op (a source that starts
over redelivers fewer records than the bound), so SRC-03 sizes its data set from the bound: the records acked
before the last restart are at least five times `MaxRedelivery`, a source that starts over fails at any accepted
bound, and a bound above `MaxAcceptedRedelivery` (1000) fails the case. The report flags every bound above the
default (`limits.max_redelivery`, listed under `below_floor`), and Certified requires the default or a maintainer
waiver (section 4). The snapshot-restart allowance of v1 is not carried over. A connector that really has to
restart declares a bound, and everyone reading the report sees the number.

How the two connectors that motivated this are expected to qualify:

- **Kafka** commits offsets in batches (about 1000 records), so after a crash it legitimately redelivers up to a
  batch. Either the test driver configures the connector so that the bound is tight (for example committing after
  every record, `commitOffsetsSize=1`; the driver is test configuration, and the production default is untouched),
  or it declares `MaxRedelivery` around 1000, runs the scaled data set, and carries the visible flag. Only the
  first qualifies for Certified without a waiver.
- **S3** restarts a snapshot from the beginning by design (`conduit-connector-s3#1041`). It cannot claim a tight
  bound; it fails SRC-03 until the snapshot position resumes, or declares a bound as large as its snapshot, which
  the suite rejects above 1000 and flags below it.

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
| DST-04 | `crash_replay_at_least_once` | destination, read-back | Kill at four points (inside a write, after the first ack, after half are acked, and after the first of two batches of 600, because connectors switch strategy on batch size); everything acked before the kill is stored, checked before `Teardown` runs; replay of the whole batch leaves nothing missing, duplicates allowed | 1, 3 |
| DST-05 | `crash_replay_idempotent` | claim `Idempotence=idempotent` | Same as DST-04, and replay leaves no duplicates. Fails (does not skip) if the driver cannot return duplicates | 3 |
| DST-06 | `ordering_per_key` | claim `OrdersByKey` | Records sharing a key are stored in write order in small batches, in batches of 300, and across a restart (the second part is written by a new instance after the first was stopped) | 4 |
| DST-07 | `schema_evolution` | claim `SchemaEvolution` | `evolve`: a record with a new field is stored with the field. `reject`: it is nacked with an error and earlier data is intact. Silent drop or coercion fails | 6 |
| DST-08 | `large_record` | destination | A record of 1 MiB (up to 3 MiB, below the 4 MiB default gRPC limit) is stored intact | 3, 6 |
| DST-09 | `graceful_stop_drains_in_flight` | destination | `Stop` while 50 batches are queued returns only after every record is acked and stored; nothing leaks | 7 |
| DST-10 | `context_cancellation` | destination | Cancelling mid-stream ends `Run` within 15 seconds; whatever was acked is stored, checked before `Teardown` | 1, 7 |
| DST-11 | `throughput_smoke` | destination | Records/s over N seconds (default 3), recorded, not asserted; all acks successful | none |
| SRC-01 | `pipelined_and_lagging_acks` | source | Acks arrive from another goroutine, lagging reads by 50 records, while reading continues; no deadlock or loss; restart from the last position is clean | 2, 3 |
| SRC-02 | `mixed_batch_sizes` | source | 1, 100 and 10000 records, snapshot then CDC, delivered once each. A duplicate fails the case, see the policy below | 3 |
| SRC-03 | `crash_resume_no_gap` | source | Five kills: three while reading the snapshot (50 of 100 records acked by the third), then, after the snapshot is completed and acked, two while capturing changes (changes written after `Open`, a partial ack, a restart from that CDC position, more changes written after the restart). The kill follows the connector's `Ack` return, not a sleep, and `Teardown` does not run. Every record is either acked in some run or delivered by the final run (records only delivered before a kill do not count), and no restart redelivers more than `MaxRedelivery` already acked records (`0` with `ExactResume`); redelivered records are not acked again, so the resume position never moves backwards. The data set scales with the bound | 2, 3 |
| SRC-04 | `ordering_per_key` | claim `OrdersByKey` | Records sharing a key arrive in write order, also across a restart: half are acked, the source is killed, the rest arrives after resuming | 4 |
| SRC-05 | `schema_evolution` | claim `evolve` | A field added while the source runs reaches the output intact | 6 |
| SRC-06 | `large_record` | source | A 1 MiB record arrives intact | 3, 6 |
| SRC-07 | `graceful_stop_with_unacked` | source | `Stop` with delivered-but-unacked records reports the last delivered position (the host waits for that record to arrive before comparing, see below); resume from the last acked position leaves no gap; nothing leaks | 2, 7 |
| SRC-08 | `context_cancellation` | source | An idle `Read` returns when its context is cancelled; an idle plugin stops and tears down | 7 |
| SRC-09 | `throughput_smoke` | source | Records/s reading 2000 records, recorded, not asserted | none |

"Read-back" means the driver can return what the destination stored. DST-04 and the other stored-data checks
need it; the cases that only need acks (DST-01 apart from the content check, DST-02, DST-03 apart from the
content check, DST-09 apart from the content check, DST-10, DST-11) run for write-only destinations such as `log`.

**Policy for duplicates (SRC-02).** At-least-once allows a duplicate after a failure. Without a failure a
duplicate is a defect: v1's `Read_Success` asserted unique positions, and a source that delivers its newest
snapshot object twice has not lost data but has not delivered once what it was asked to deliver once. SRC-02
therefore fails on any duplicate, and no claim waives it. That is why `conduit-connector-s3#1040` (the newest
snapshot object is delivered again by CDC) fails SRC-02 although nothing is missing. Every other case tolerates a
duplicate and logs it; missing and unexpected records always fail.

**In-flight window (SRC-07).** `Stop` reports the last position the plugin sent into the stream. On a real stream
records can still be in flight between that moment and their arrival at the host, so the Go harness waits for the
record with that position and then requires that nothing followed it. A runner for gRPC (section 5) must do the
same; comparing against "the last record received so far" flakes for fast sources.

Skips have a kind, recorded in the report: `not_applicable` (the connector has no such side), `unclaimed` (a
capability that was not claimed), `declared` (`Synthetic` or `WriteOnly`), `replaced` (a v1 data-path test that
v2 replaces) and the two that count as unexplained, `in_case` (a case skipped itself; none do today) and `driver`
(skipped by the driver's `BeforeTest`, for example a `Skip` regular expression). A claimed capability that the
suite cannot verify is a failure, never a skip: idempotence with a driver that cannot return duplicates,
`WriteOnly` together with an idempotence or ordering claim, a schema evolution claim with a driver that
generates raw payloads.

**What the in-process crash can and cannot detect.** The harness cancels the context, closes the stream, waits for
`Run` to return, checks what is durable, and only then calls `Teardown` to release resources. A killed source is
not torn down at all until the end of the test (a source that commits its position only in `Teardown` would
otherwise be rescued by the harness; mutant `source_commits_in_teardown`). That detects the
common form of ack-before-durable (records buffered in memory and flushed on a timer or in `Teardown`; mutants
`dest_acks_before_durable` and `dest_flush_in_teardown`). It does not detect a connector that flushes in
response to context cancellation, because cancellation is how the harness ends the plugin, and it cannot
produce a torn write, a lost page cache or a dropped TCP connection. Only a process kill does. The standalone
harness (section 5) will kill the process, and the connector's own kill -9 test remains the proof for Tier 1
connectors. v2 raises the floor; it does not replace chaos testing. For Certified the kill -9 test is
**attested, not enforced**: maintainers list it in the registry entry after reviewing the connector's test, and
nothing in this suite or in index-CI runs it.

### 3. Capability declaration

Capabilities are declared in the test driver, next to the config that exercises them:

```go
sdk.ConfigurableAcceptanceTestDriverConfig{
    Connector: Connector,
    Capabilities: sdk.AcceptanceCapabilities{
        Destination: sdk.DestinationCapabilities{Idempotence: sdk.IdempotenceYes, SchemaEvolution: sdk.SchemaEvolutionReject},
        Source:      sdk.SourceCapabilities{OrdersByKey: true, ExactResume: true},
    },
}
```

The zero value claims nothing, so a connector that declares nothing runs the baseline cases only and the report
lists what was skipped and why. Fields:

- `Destination.Idempotence`: `idempotent`, `at_least_once` or unspecified. It is a recorded value, so "not
  idempotent" is a statement a registry can read and compare, not the absence of one. A first draft proposed
  "claim it or document why not"; documentation cannot be checked by a machine.
- `Source.ExactResume` and `Source.MaxRedelivery` (above), `OrdersByKey` on both sides, `SchemaEvolution` on both
  sides.
- `Source.Synthetic` (a generator cannot be seeded) and `Destination.WriteOnly` (no read-back). These are not
  capabilities, they are exceptions that remove checks: Synthetic removes every source case that compares
  written with read data, WriteOnly removes every durability check.
- `MaxBatchSize`, `MaxRecordBytes`, `ThroughputDuration`.

Drivers can optionally implement `ReadAllFromDestination` (duplicates are needed to tell duplicates from loss),
`GenerateRecordWithKey` and `GenerateLargeRecord`. `ReadAllFromDestination` is an explicit opt-in: the default
driver offers it only when `ReadAllFromSource` is set, and does not satisfy the interface through embedding, so a
driver that overrides `ReadFromDestination` is never silently read through the source behind its back.

A claim is a promise the suite tests, so claims cannot inflate a result: DST-05 fails an idempotence claim that
duplicates on replay, SRC-03 fails an `ExactResume` claim that redelivers.

**Dodge routes and how they are closed.** A suite whose cases can be switched off by the author proves little.
What the registry check (section 4) does about each:

| Route | Closed by |
| --- | --- |
| Declare `Synthetic` or `WriteOnly` | Allowed only with per-connector maintainer approval, recorded in the registry entry (`exceptions: ["write_only"]`) and shown next to the tier. A Certified destination needs read-back, or a **driver-provided** capturing sink: the driver supplies the context logger through `Driver.Context()` and returns what the destination wrote to it from `ReadAllFromDestination`. The `log` destination does exactly that and passes the durability cases without `WriteOnly` |
| Declare a large `MaxRedelivery` | SRC-03 scales its data set with the bound (acked records at least five times the bound) and fails above 1000; the report flags any value above the default (`limits.max_redelivery`, `below_floor`), and Certified requires the default or a maintainer waiver listed in the entry |
| Lower `MaxBatchSize` or `MaxRecordBytes` | The suite runs at the declared value, and the report states `certification_floor_met`. Floors are `CertificationMinBatchSize = 10000` and `CertificationMinRecordBytes = 1 MiB`, the defaults; a connector below them cannot be Certified without a maintainer waiver listed in the entry |
| Say nothing about idempotence | `Idempotence` must be `idempotent` or `at_least_once` for Certified; unspecified fails the check |
| Claim nothing, pass the baseline | The baseline is the Certified minimum; the claims a connector class must make are listed under Decisions |
| Skip cases with `-run`, a driver `Skip` list or a `t.Skip` in `BeforeTest` | The report states completeness against the expected case set and counts unexplained skips (section 4) |
| Weaken goroutine leak checks | The report lists the ignored functions (`GoleakIgnoreFunctions`). Options passed any other way are opaque (`goleak.Option` cannot be inspected): the report says so explicitly (`goleak.opaque_options`, `goleak.unreviewable`) and Certified requires none |

### 4. Versioning and what the registry records

The suite has a revision, `major.minor.patch`, written into every report (`suite_revision`):

- **major** is the bar. v2 is the current bar. A new major means new baseline cases or tighter semantics.
- **minor**: new cases that run only when a capability is claimed, or new claims. A connector that passed
  `2.0.x` still passes `2.1.x` without changes, because the new cases skip until it claims something.
- **patch**: harness fixes that do not change what a connector must do.

The revision is a constant in code, and a constant can go stale. The report therefore also carries the expected
case set (`expected_case_ids`, `expected_case_count`), `case_set_hash` (the SHA-256 of the revision and the sorted
IDs) and `case_content_hash` (the SHA-256 of every case's ID, name and logic version, without the revision). A
test in the SDK pins the content hash of the newest revision. What that enforces is limited, and worth stating:
adding, removing or renaming a case, or bumping a case's logic version (`v2Case.logic`, which a reviewer bumps for
any change that can change a verdict), fails the SDK's build until the pin is edited, so the edit is visible in
review. The test also fails if two revisions pin the same case set, so a new revision must change something. It
cannot see an edit inside a case that forgot the logic bump, and for an unreleased revision the pin may be edited in
place; once a revision is released its entry is final and a change needs a new revision. Core IDs are explicit
(`CORE-01` to `CORE-13`, one per v1 test method, in a table in the code); a v1 test without an ID fails the run
instead of getting a derived name.

**The report is evidence, not proof.** It is written by the process that ran the tests, so a connector author
controls it. Everything the registry does with it before a registry-side re-run exists is informational, and the
index must not present it as verified. What makes a result trustworthy is provenance, and that comes from a
re-run in CI that maintainers control: for project-maintained connectors that is the connector repo's own
release workflow, which maintainers also control; for others, a job in the registry repo that checks out the
tagged release, runs the suite with the maintainers' credentials for the target system and writes the report
itself. Until that job exists, `certified` rests on maintainer review of the report, as it does today for every
other item of the bar. This document does not claim the report authenticates anything.

The report is JSON (`CONDUIT_ACCEPTANCE_REPORT=path`), with a `report_schema_version`: suite version, revision,
case-set and case-content hashes, connector name and version, the claims and exceptions, the limits used
(including `max_redelivery`) and whether they meet the floors, goroutine-leak ignores (named ones, and an explicit
count of opaque ones with `unreviewable`), every case with outcome, skip kind, skip reason and duration, recorded
metrics, and the derived `complete`, `unexplained_skips` and `passed`. It is written when the run starts, with
`finished: false` and `passed: false`, and rewritten when the run ends, so a run that dies (a panic in the
connector kills the test binary before any cleanup) leaves an unfinished report and never the previous run's file.

`sdk_version` and `sdk_version_source` are informational. Binaries built by `go test` carry no dependency list in
their build information, so the suite reads the version from the path of its own source file: a module cache path
(`.../conduit-connector-sdk@v0.15.0/`, also with `-trimpath`) gives the version, `vendor/` gives `vendored`, and a
directory with a `go.mod` for the SDK's module path gives `local checkout`, whatever the directory is called (a
replace directive, a workspace, a fork); anything else is `unknown`. The source field says which route was used. No
linker flags, nothing from the driver. Compatibility is decided by revision and hashes, never by SDK version.

The registry records, per connector version, in the `conformance` object of
`20261008-sdk-parity-and-open-plugin-protocol.md` (#2948), not in a separate entry (decided, see below):

```json
{ "suite": "acceptance", "revision": "2.0.0", "caseSetHash": "…", "mode": "sdk-in-process",
  "claims": ["destination.idempotence=idempotent", "source.exact_resume"],
  "exceptions": [], "skipped": {"unclaimed": ["SRC-04"]},
  "provenance": "registry-ci", "reportSha256": "…" }
```

`tier` stays what #2963 defined: set by registry maintainers in the index, never by a publisher, never a trust
input. Passing v2 is a necessary condition for `certified`, not a sufficient one (certified also needs
integration tests against the real system in CI, a kill -9 chaos test attested by maintainers, a committed benchi
run and docs).

**The Certified check.** Defined over the report, not over a boolean. For a connector entry proposing `certified`,
index-CI requires all of:

1. `revision` and `case_set_hash` equal a pair the registry knows for a supported suite major; the registry holds
   the table, so a self-reported pair that does not match is rejected;
2. `complete` is true, and `expected_case_ids` equals the registry's list for that revision (a filtered `-run`
   can never satisfy this);
3. every expected case has an outcome, none is `failed`, and `unexplained_skips` is 0;
4. every skip is of kind `not_applicable` (and the connector really lacks that side), `replaced`, or `unclaimed`
   for a capability outside the mandatory set for the connector class; a `declared` skip requires the matching
   maintainer-approved exception in the entry; no skip is `in_case` or `driver`;
5. `certification_floor_met` is true or the entry carries a maintainer waiver naming the limit. The floors are
   `max_batch_size` and `max_record_bytes` at their defaults or above, and `max_redelivery` at the default (10) or
   `ExactResume`: a larger bound is listed under `below_floor` and needs a waiver;
   `goleak.unreviewable` is false; `finished` is true;
6. `Idempotence` is `idempotent` or `at_least_once` for any entry with a destination;
7. `provenance` is `registry-ci`. Until the registry-side re-run exists, this condition cannot be met, and the
   check runs in warning mode: it reports what it would decide and does not gate the tier.

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

## Non-goals

Suite v2 deliberately does not cover, and the Certified bar is silent on:

- **Updates, deletes and tombstones.** Only `create` and `snapshot` records are generated. Tracked in
  `ConduitIO/conduit-connector-sdk#398` (revision 2.1).
- **Type changes, narrowing and drift.** Only an added field is exercised. Tracked in
  `ConduitIO/conduit-connector-sdk#399` (revision 2.1).
- Authentication and TLS failures, network partitions and slow third-party systems (the chaos suites), throughput
  thresholds (benchi), multi-instance or concurrent use of the same source, and the engine's behavior (pipeline
  tests).

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

- **Suite passes a broken connector.** Mitigation: a mutant test per case in the SDK repo (a reference connector
  with 28 fault-injecting variants (30 runs) plus five configurations the suite cannot verify, each run in a
  subprocess restricted to the case that must fail it, and a test that pins the effective redelivery bound at
  exactly the declared one: a source redelivering N passes, N+1 fails).
  Two cases have no mutant, with reasons: DST-02, because acks are sent by the SDK's destination adapter and a
  connector cannot block them (the case guards the adapter's ack path), and the Core group, which is v1's own.
  A new case lands with a mutant that it kills.
- **Suite fails a correct connector (flake).** Timeouts guard against loss, not slowness (10x the driver
  timeout, at least 10 seconds), and reads end on a quiet period. Cases that need the source to continue from a
  snapshot into change capture ack what they read, like a pipeline; a source may legitimately wait for acks.
  Flake rate is tracked once the suite runs in CI on the reference connector; the target is the CLAUDE.md
  Certified bar.
- **In-process crash is not a crash.** Stated above, with what it does and does not detect. The kill -9 test is
  attested, not enforced.
- **Shared-memory artifacts.** The SDK middleware mutates records in place. The harness copies records on both
  sides of the stream; the race detector run on the reference connector covers it.
- **Claims used to dodge cases.** A connector can claim nothing and pass the baseline. Mitigation: the report
  and the registry entry show what was skipped, and the Certified check names the claims it requires. Which
  claims are mandatory is decided (see Decisions).
- **External state pollution.** Cases write real data into real systems. The driver's `BeforeTest` and
  `AfterTest` own isolation and cleanup, as for v1.
- **Time.** On the file connector the full suite (Core plus v2) runs in about 20 seconds. 10000-record cases
  dominate for slow systems; `MaxBatchSize` lowers it with a documented reason.
- **Test data generation.** `ConfigurableAcceptanceTestDriver` re-seeded its random generator on every call,
  4 ms per record, so 10000 records took 41 seconds to generate. Fixed in the implementation PR (shared locked
  source).

## Upgrade and rollback

Additive: `AcceptanceTestV2` is a new symbol, `Capabilities`, `ReadAllFromSource` and `GoleakIgnoreFunctions` are
new optional fields on the config struct, and the driver gains optional interfaces. Nothing is required of
existing drivers. The default test logger is now info level (a trace-level connector log had grown to 1 GB in one
run). Changes to code v1 shares, all behavior preserving for connectors that pass today: a wildcard required parameter (`collections.*.operations`)
is no longer treated as a key the config must contain; `Source.Read` falls back to `ReadN(1)` for sources that
implement only `ReadN`; the default driver's random generator is shared instead of re-seeded per call. The first
two only turn a test-setup failure into a run. Rollback is to stop calling `AcceptanceTestV2`; the registry ignores a missing
report.

The suite ships with the SDK, so a connector tests with the revision its pinned SDK contains. The report records
the revision, the case set hash and, informationally, the SDK version; the registry check compares the first two.

## Observability

- JSON report per run with revision, claims, per-case outcome, duration, skip reason, metrics.
- Throughput numbers (`destination_throughput`, `source_throughput`) with their conditions in the `detail` field,
  to build a history; they are never compared to a threshold here.
- Failure messages name the invariant at stake and the evidence (counts, first missing record), for example
  "N of M records are not stored; an acked record must be durably written".
- A CI summary step in the connector template prints the report as a table.

## Decisions

Decided by DeVaris:

1. **What Certified requires.**
   - The core delivery claims: `Idempotence` stated (either value), bounded resume (`ExactResume`, or a declared
     `MaxRedelivery` at the default), `OrdersByKey` for sources and destinations that read or write an ordered log,
     and a `SchemaEvolution` policy for schema-aware connectors.
   - `Synthetic` and `WriteOnly` only with maintainer approval, per connector, listed in the registry entry.
   - For destinations, read-back or a driver-provided capturing sink.
   - The batch and record floors (`max_batch_size`, `max_record_bytes`).
   - A bounded `max_redelivery`: the default, or `ExactResume`, or a maintainer waiver listed in the entry.
2. **Where capabilities are declared.** In the test driver now, in the connector specification later (a protocol
   discussion of its own; the report already has the shape).
3. **Registry shape.** Results go in the `conformance` object of #2948, not in a separate `acceptance` entry.
4. **Adoption.** Connector repos adopt suite v2 before the Certified cuts.

Still open:

- **Registry-side re-run.** Who builds the job that re-runs the suite with credentials for the target system, and
  when. Until it exists the report is informational and the Certified check is in warning mode.
- **Who approves an exception or a waiver**, and where the approval is recorded, so that the registry check can
  read it. The registry entry carries the result; the review process is not defined here.

## Related

- `ConduitIO/conduit-connector-sdk#397`: the implementation, with the per-connector results (file, generator, log,
  Postgres, S3) and the issues they produced.
- `20261008-registry-license-and-tier.md` (#2963): the tier field this feeds.
- `20261008-sdk-parity-and-open-plugin-protocol.md` (#2948): the language-neutral conformance runner and the
  `conformance` registry object.
- `docs/code_guidelines.md`, CLAUDE.md data integrity invariants 1 to 7.
