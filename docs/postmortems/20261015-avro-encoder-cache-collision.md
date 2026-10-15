# Postmortem: Avro encoding used another pipeline's named types

This postmortem is blameless. It covers what happened, why nothing caught it for two and a half
years, and what we are changing. Issues: `conduit-commons#305` (encoder cache) and
`conduit-commons#308` (parse cache).

## Summary

Conduit's Avro encoding went through the codec's package-level functions. In `conduit-commons`
`schema/avro`, `Serde.Marshal` called `avro.Marshal` and `Parse` called `avro.ParseBytes`. Both
codecs we have shipped, `github.com/hamba/avro/v2` and the `github.com/iskorotkov/avro/v2` fork,
back those functions with process-wide state:

- `avro.DefaultConfig` holds one encoder cache for the whole process. The cache key is a hash of the
  schema's `String()`. A reference to a named type (`record`, `enum`, `fixed`) stringifies as just
  its full name, so `{"type":"array","items":"R"}` produces the same key whatever `R` means in the
  schema that contains it.
- `avro.DefaultSchemaCache` registers every named type that any parse defines. A later schema can
  use that name without defining it, and the parse succeeds.

Two pipelines in one Conduit process can register schemas that define the same full name
differently. One pipeline encodes first, and from then on the other encodes those types with the
first pipeline's encoder. The output is valid Avro, the record is written and acknowledged, and
nothing is logged. A consumer that decodes it with the record's own schema gets wrong values: a
decimal at the wrong scale (1.5 read back as 15000), another enum symbol, or record fields swapped.

That violates invariant 6: schema handling never silently mangles data.

**Severity: sev-0 class** (silent data corruption, acked).
**Status:** fixed in `conduit-commons#309` (main) and `conduit-commons#310` (`release/v0.7.x`),
merged 2026-10-15. The v0.8.x backport is `conduit-commons#313`. The fix reaches Conduit when it
moves to `conduit-commons` v0.8.1 on `main` and v0.7.1 on `release/v0.20.x`. Data that was already
written is not repaired by upgrading.

## Impact

### Affected versions

- **Conduit:** v0.9.0, which shipped `avro.encode`, through v0.20.1.
  - Run end to end: v0.20.0 (the signed release binary) and `release/v0.20.x` 49258fe.
  - Earlier versions are inferred from the code: every one encodes through the codec's package-level
    `Marshal`. v0.9 and v0.10 do it in-tree. v0.11 and later do it through `conduit-commons` v0.3.0
    to v0.8.0.
- **`conduit-commons`:** v0.3.0 through v0.8.0.
- **`conduit-connector-sdk` and `conduit-processor-sdk`:** every release whose schema middleware
  encodes through `conduit-commons` `schema/avro`.

### What it takes to trigger

All three must hold:

1. At least two different Avro schemas encode in one process. Examples:
   - two pipelines with `avro.encode`;
   - two versions of one subject;
   - a connector that supplies several schemas.
2. Those schemas define a named type under the same full name with different contents: a different
   decimal scale, enum symbol order, or record field order or type.
3. The schema that encodes second refers to the type by name inside an array, map or union.

### Not affected

- Decoding. The decoder is built from the top-level schema, and decoding was already per Serde.
- A single self-contained schema.
- Schemas that Conduit and the SDKs infer from data, because they define every type inline.

### Which pipeline loses

Whichever one encodes second after the process starts.

- On v0.20.0 with two pipelines, all 20 records of one pipeline were wrong.
- On `release/v0.20.x`, both pipelines were partly wrong, because they built their encoders
  concurrently.
- In one run with one record each, both came out correct.

### Blast radius

Every record the losing pipeline encoded for the whole life of the process, delivered downstream
with acks. Built-in connectors and processors run inside the Conduit process and share the cache
with `avro.encode`. A standalone connector or processor shares it only within its own process. A
WASM processor shares it only within its own module.

**Visibility:** none. No error, no warning, no metric. The pipeline looked healthy.

**Known production occurrences:** none reported.

## How it was detected

By a property test. On 2026-10-15, the Elasticsearch certification lane (#3007) was writing property
tests for decimal handling (`conduit-connector-elasticsearch#171`). A test that encoded two schemas
in one process, each with a decimal `fixed` named `fx` at a different scale, read back 15000 for
1.5. The lane reported it to the Avro owner as `conduit-commons#305` and did not fix it, because the fix was outside
its lane.

## Timeline

All times are UTC.

- **2024-03-07:** v0.9.0 ships the built-in `avro.encode` processor ("Better processors", #1378). It
  encodes with hamba's package-level `avro.Marshal` and parses with `avro.Parse`
  (`pkg/plugin/processor/builtin/impl/avro/schemaregistry/avro/schema.go`). Every later release
  carries the same call, first in-tree and from v0.11.0 through `conduit-commons` (`schema/avro`,
  added 2024-07-09 in `conduit-commons#73`).
- **2026-01-18:** upstream `hamba/avro` is archived.
- **2026-08-23/24:** the codec design doc (#2823,
  `docs/design-documents/20260823-avro-codec-archived-decoder-advisories.md`) chooses to replace
  hamba with the maintained `iskorotkov/avro` fork. `conduit-commons#279` makes the switch. The fork
  has the same package-level API and the same cache, so the behaviour carries over unchanged.
- **2026-10-15 04:21:** `conduit-connector-elasticsearch#171` is filed by the Elasticsearch lane.
  This is the decimal work that led to the finding.
- **2026-10-15 04:48:** `conduit-commons#305` is filed, with a 10-line in-process repro. The likely
  cause is named, but not confirmed.
- **2026-10-15 ~05:20:** reproduced end to end, with two pipelines in one Conduit process on v0.20.0
  and on `release/v0.20.x`. The root cause is confirmed in the encoder cache key.
- **2026-10-15 05:28:** `conduit-commons#308` is filed for the parse cache. It was found while
  reviewing `conduit-connector-elasticsearch#172` with `conduit-commons#305` in mind.
- **2026-10-15 05:29:** fixes opened as `conduit-commons#309` (main) and `conduit-commons#310`
  (`release/v0.7.x`, for Conduit v0.20.2).
- **2026-10-15 05:47:** the root cause is reported upstream as iskorotkov/avro#52. Our fix does not
  depend on it.
- **2026-10-15 05:48:** `conduit-commons#309` and `#310` are merged. The v0.8.x backport,
  `conduit-commons#313`, is opened to carry the fix to v0.8.1 without the unreleased changes on commons `main`.
- **2026-10-15:** prevention work is opened:
  - the lint ban (`conduit-commons#321`, `conduit-connector-sdk#410`, `conduit-processor-sdk#173`,
    `conduit#3090`);
  - the nightly value-correctness test (`conduit#3091`);
  - a process-global state audit (results below).

## Root cause

The direct cause is a codec property. The encoder cache key does not expand references, so two
different types share a key whenever they share a name. That is a reasonable cache for a program
that uses one schema family. Conduit runs many unrelated schemas in one process, and for us the
cache is wrong.

Our side of it: `conduit-commons` used the package-level API, which carries that shared state,
instead of a config owned by each schema. Nothing in our code, lint rules or reviews made that
choice visible. By v0.8.0, `Serde` already decoded with a config of its own, built for the decode
limits, while it still encoded through the shared one. Nobody asked why the two paths differed.

## Why it wasn't caught

- **No test ran two overlapping schemas in one process and checked what was written.**
  - Every Avro test either used one schema family per test, or used schemas whose named types had
    distinct names.
  - Tests asserted that encoding succeeded and that a round trip through the same Serde worked. That
    round trip hides the bug: the decoder is right, so the wrong bytes decode to wrong values that
    no one compared to the input.
  - No end-to-end test ran pipelines with overlapping Avro schemas in one process.
- **No property-testing framework.** CLAUDE.md requires property-based tests (rapid or gopter) for
  serialization. No such framework is in any ConduitIO module. The Process-maturity table marks that gate as only partly
  live, with fuzz seeds and no property framework. A generator of random schema pairs with shared
  names is the natural way to find this bug, and that is how it was found. It just came from a
  connector lane's hand-rolled test, not from the shared suite.
- **Silent, valid-looking output.** The wrong encoding is valid Avro for the schema it was written
  under, so nothing downstream rejects it. Decimals keep their byte width, swapped fields of the
  same type stay in range, and so do most enum indexes. There was no error, no log line and no
  metric.
- **Intermittent by design.** Which pipeline is affected depends on which builds its encoders first
  after the process starts. A restart can move the corruption to the other pipeline or hide it. That
  makes a field report unlikely, and a lucky test run likely.
- **Dependency semantics were never audited for global state.** The codec design doc chose between
  forking, replacing and accepting an archived library. It reviewed maintenance, advisories and wire
  compatibility, which was its scope. Nothing in our design-doc guidance asks how a dependency
  behaves when one process holds many pipelines, so that question was never asked. Swapping hamba
  for the fork carried the global cache over unchanged.

## What went well

- A property test in a certification lane found it before any user report. The lane filed it with a
  minimal repro and a hypothesis, and handed it to the right owner instead of patching outside its
  lane.
- The repro was confirmed end to end against a signed release binary within the hour. That turned
  "likely" into a measured statement of which versions are affected.
- The fix is small, two lines in `schema/avro/serde.go`, and needs no codec change. It produces
  byte-identical output for schemas that do not collide, checked against the existing hamba
  wire-compatibility fixtures. Backports were opened at the same time for both supported commons
  lines.
- A review in the Elasticsearch lane found the related parse-cache leak (`conduit-commons#308`),
  and it was fixed in the same change instead of being left for later.
- The fix PR states which versions were run and which were inferred, and that existing data is not
  repaired. It does not claim more than was verified.

## Fix

`conduit-commons#309` and `conduit-commons#310`, in `schema/avro/serde.go`:

- Each `Serde` encodes with its own `avro.Config{}.Freeze()`. It has the same settings as
  `DefaultConfig`, so it writes the same bytes, but its cache is private. Names are unique within
  one schema, so a cache that only ever sees one schema cannot mix types up.
- `Parse` uses a fresh `avro.SchemaCache` per call.
- An invariant-6 comment sits at both enforcement sites.

The behaviour change: a schema that used a name defined only by another schema in the process now
fails to parse. It never parsed in a fresh process, so it was order-dependent before.

Regression tests in `conduit-commons`:

- `TestSerde_Marshal_NamedTypeReuseAcrossSchemas`;
- a 500-pair property test, `TestSerde_Marshal_PropertyNamedTypeIsolation` (20,000 pairs with
  `CONDUIT_PROPERTY_LONG=1`);
- `FuzzSerde_Marshal_NamedTypeIsolation`;
- two parse tests for `conduit-commons#308`.

Each one failed with `serde.go` from main, v0.7.0 and v0.6.0, and passes with the fix.

## New automated checks

- **Lint ban:** `conduit-commons#321`, `conduit-connector-sdk#410`, `conduit-processor-sdk#173` and
  `conduit#3090`.
  - A forbidigo rule, matched by import path, bans the codec's package-level API for both hamba and
    iskorotkov:
    - encode and decode: `Marshal`, `Unmarshal`, `NewEncoder*`, `NewDecoder*`;
    - global configuration: `Register*`, `DefaultConfig`, `DefaultSchemaCache`;
    - parsing: all the `Parse*` functions.
  - The only sanctioned call sites, which parse with a fresh per-schema cache, carry a `//nolint`
    with the reason.
- **Nightly value-correctness test:** `conduit#3091`, `tests/valuecorrectness`.
  - It runs five pipelines in one runtime, on the built-in registry, with schemas that reuse the
    names of a record, an enum and a decimal fixed type. Two pipelines encode with two versions of
    one subject, and two go through `avro.decode` → `avro.encode` round trips with decimal scales 6
    and 2.
  - The pipelines start one at a time, so a leak shows up on every run.
  - It compares every record's schema ID, Avro bytes and decoded value against a fresh codec.
  - It runs nightly and on data-path PRs, with chaos.yml's always-report and fail-closed detector.
  - On `conduit-commons` v0.7.0 and v0.8.0, three of the five pipelines write 20 of 20 records
    wrong, including 1.5 written as 15000. With the fix, all pass.

## Process-global state audit

We checked the four core repos and the Postgres, MySQL, Kafka, S3 and Elasticsearch connectors for
process-global mutable caches and registries whose keys are not unique across pipelines, schemas or
runtimes.

- No package-level codec call is left in non-test code in any of them.
- One new problem was found and filed as #3088. Built-in plugins reach the schema registry through
  package-level variables in the SDKs, and every new runtime overwrites them. With two runtimes in
  one process, which is possible through the embed API, the first runtime's built-in processors
  silently resolve schemas against the second runtime's registry.

The CLI runs one runtime per process and is not affected. The other candidates were either
per-instance or keyed exactly. A few were routed to their owners.

## Action items

| Item | Owner | Tracking |
| --- | --- | --- |
| Release `conduit-commons` v0.8.1 (backport `conduit-commons#313`) and v0.7.1 (`conduit-commons#310`, merged) | DeVaris (Tier 1 sign-off), Avro lane | `conduit-commons#313` |
| Bump Conduit `main` to `conduit-commons` v0.8.1, and `release/v0.20.x` to v0.7.1 for v0.20.2 | release lane | #3012, v0.20.2 milestone |
| Rebuild standalone connectors and processors against a fixed `conduit-commons` | Avro fleet lane | #3012 |
| Publish the user advisory: conditions, affected versions, workarounds, how to detect affected data | DeVaris | `conduit-commons#305` |
| Lint ban on the codec's package-level API, in four repos | Avro lane | `conduit-commons#321`, `conduit-connector-sdk#410`, `conduit-processor-sdk#173`, `conduit#3090` |
| Nightly multi-pipeline value-correctness test; make it a required check after a green nightly history | Avro lane, then DeVaris | `conduit#3091` |
| Fix the runtime-global schema services that a second runtime overwrites (Tier 1) | engine lane, DeVaris sign-off | #3088 |
| Adopt a property-based testing framework, and share the Avro generators from `conduit-commons#309` | DeVaris (decision), test lane | #3093 |
| Add a process-global state review to design docs for data-path dependencies | DeVaris | #3094 |
| Upstream fix to the codec's encoder cache key (ours does not depend on it) | upstream | iskorotkov/avro#52 |
