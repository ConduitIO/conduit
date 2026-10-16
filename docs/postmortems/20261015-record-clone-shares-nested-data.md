# Postmortem: Record.Clone shared nested data between copies and changed nested map types

This is a blameless postmortem. It records what happened, why nothing caught it for almost three
years, and what changes. Issue: `conduit-commons#336`.

## Summary

`opencdc.StructuredData.Clone`, and with it `Record.Clone`, was meant to be a deep copy. It was
not. It copied a nested `map[string]any` (and turned it into `opencdc.StructuredData` on the way),
but kept every other nested value by reference: slices (`[]any`), arrays, typed maps such as
`map[string]string`, `[]byte`, and nested values that were already `StructuredData`.

The engine clones a record whenever it needs independent copies: arch-v1 fan-out to more than one
destination, the `clone` and `split` processors, arch-v2 fan-out branches that contain processors,
and the inspector. Each copy relied on being independent. A processor that changed a nested array
in place in one copy changed every copy and the original. That is invariant 6: data silently
changed in a record the processor never touched.

The same code had two loud side effects, both caused by the type change:

- After a clone, a processor that addresses a nested field (`field.set` on `.Payload.After.a.b`,
  `field.exclude`, `field.rename`) failed with "does not contain structured data", because the
  reference resolver only walks `map[string]any`. The record was nacked, so it went to the DLQ or
  stopped the pipeline.
- `avro.encode` infers a schema from the data. It encodes `map[string]any` as an Avro `map` and
  `StructuredData` as an Avro `record`, so a cloned record got a different schema from an uncloned
  one with the same data.

**Severity: sev-0 class** (silent cross-record corruption, invariant 6). **Status:** fixed on
`conduit-commons` main by #338 (type-preserving deep copy, ships in v0.9.0 / Conduit v0.21). The
patch lines get a deep copy that keeps the old types (#339 for `release/v0.7.x` → v0.7.2 →
Conduit v0.20.3; #340 for `release/v0.8.x` → v0.8.3). No production occurrence is known.

## Impact

### Affected versions

- **Conduit:** v0.8.0 through v0.20.2, and `main` until it takes commons v0.9.0.
  - The current `Clone` came in with #1247 (2023-10-24), which replaced a reflection-based deep
    copy (`jinzhu/copier` with `DeepCopy: true`) by a hand-written one for speed (#885). v0.8.0
    (2023-11-09) is the first release with it. From v0.11.0, Conduit uses `conduit-commons`'
    `opencdc.Record`, which carries the same code (`conduit-commons#1`, 2023-12-05). #1622
    (2024-07-01) removed Conduit's own `pkg/record`.
  - Reproduced on `main` at `5679267` and on `release/v0.20.x` (commons v0.7.1) with package-level
    repro tests that call the engine's `stream.Message.Clone` and run real built-in processors on
    the copies. No full pipeline was run. Earlier versions are inferred from the code, which is
    unchanged since #1247.
- **`conduit-commons`:** every release, v0.0.x through v0.8.2.
- **SDKs and plugins:** anything that calls `Record.Clone` or `StructuredData.Clone` and then
  changes one of the copies.
  - Built-in connectors and processors are compiled into Conduit, so they use Conduit's
    `conduit-commons`, whatever their own `go.mod` pins. They are fixed by Conduit's bump.
  - Standalone plugins use the commons they were built with. `conduit-processor-sdk` v0.8.1 and
    `conduit-connector-sdk` v0.16.1 both pin commons v0.8.0. In the connector SDK, `Clone` is
    called only by the v2 acceptance harness (`acceptance_v2_harness.go`, which clones what it sends
    to the destination and keeps the originals to compare against) and by the internal reference
    connector. Neither runs in a production connector. A shallow copy there could let the SDK's
    destination middleware change the harness's kept originals, which would weaken what the
    acceptance suite checks; we found no middleware that changes nested values in place. Rebuilding
    on commons v0.8.3 removes the question.

### Paths that clone

| Path | Clones when | Exposure |
| --- | --- | --- |
| arch v1 fan-out (`pkg/lifecycle/stream/fanout.go`) | **always**, once per destination, whenever a pipeline has more than one destination (one destination takes a shortcut and doesn't clone) | Each destination branch gets a clone, and destination-level processors run on it. The main exposure |
| `clone` processor (v0.14.0+) | every record, `count` times | Copies share nested data with each other and the original |
| `split` processor (v0.14.0+) | every element | Split records share the rest of the record |
| arch v2 fan-out (`pkg/lifecycle-poc/funnel`) | branches that contain a processor get `Batch.clone()`; destination-only branches share the records on purpose (#2946, 2026-10-12) | A processor in one branch that changes a nested array in place also changes the record a sibling destination-only branch is writing at the same moment. That is corrupted output and a data race. #2946's safety argument (`branchMutatesRecords`) also leans on `Clone`: it counts a built-in destination as read-only because the in-memory plugin stream clones each request (`conduit-connector-protocol` `pconnector/clone.go`), and with a shallow `Clone` that copy shared nested data with the records the siblings read |
| inspector (`pkg/inspector`) | once per record, sent to every session | The inspector's copy shares nested values with the record the pipeline keeps processing |

### What changes data silently

A copy is only corrupted if something changes, **in place**, a nested array, an element inside one
(including a map), or a nested `StructuredData`. Built-in processors
mostly replace values through references (`Set`), and the reference resolver cannot walk into
arrays, so they don't do that. The one in-tree mutator we found is the **JavaScript processor**
(`custom.javascript`). goja wraps the record's Go maps and slices live, so a script that does
`record.Payload.After.tags[0] = "x"` writes into the shared slice, and
`record.Payload.After.items[0].x = 1` writes into a map inside it.

Standalone (WASM) processors cannot reach the engine's records: the engine serializes every batch
to protobuf for each call (`pkg/plugin/processor/standalone/processor.go`, `Process`, and the host
module's `commandRequest` / `commandResponse` in `host_module.go`), so the module works on its own
copy. A standalone processor that calls `rec.Clone()` **inside the module** is a different case.
It uses the `conduit-commons` it was built with (`conduit-processor-sdk` v0.8.1 pins commons
v0.8.0), so its copies share nested data with each other until it is rebuilt against commons
v0.8.3 or later (or v0.9.0).

### Inspector risk

The inspector clones a record once and hands the copy to session goroutines, which marshal it for
the API while the pipeline keeps processing the original. The old `Clone` shared every `[]any`
whole, together with everything inside it, so the maps in an array of objects
(`items: [{...}, {...}]`, the common shape for line items, tags with attributes, or any JSON
array) were shared too, as was any nested `StructuredData`.

A JavaScript processor downstream of the inspected component that runs `items[0].x = 1` while a
session marshals the same record is a concurrent map read and write. Go treats that as a fatal
error, not a panic, and the whole process exits, taking every pipeline with it. A write into a
shared slice element that is not a map is a data race instead, which shows up as a torn view in
the inspector only. We have not observed either. The window is a session marshalling at the same
moment as the write, so it needs an open inspector session, an array of objects, and an in-place
JavaScript change downstream, but nothing else.

### Blast radius

- **Silent:** any pipeline that clones (more than one destination on arch v1, `clone`, `split`,
  arch v2 branches with processors) and has a JavaScript processor that changes, in place after
  the clone, a nested array, an element inside one (including a map), or a nested
  `StructuredData`. The sibling copies and the original carry the change to their destinations,
  with acks. Nothing logs it.
- **Standalone processors built against commons ≤ v0.8.2** that clone records inside the module
  and then change one copy in place.
- **Loud:** nested-path processors after a clone nack the record (DLQ or stopped pipeline).
- **Shape:** `avro.encode` after a clone registers a schema with `record` where the uncloned data
  gives `map`. The published example output for `avro.encode` encodes this wrong shape, because the
  example harness clones its input.

**Known production occurrences:** none reported.

## How it was detected

By property tests. On 2026-10-15 the lane writing processor property tests for #3093 (adopt a
property-based testing framework) built a type-preserving `recordgen.Clone` for its generators
and compared it with `Record.Clone`. The two disagreed on nested arrays and nested map types. The
lane filed `conduit-commons#336` with a five-line repro and did not fix it in place, because the
fix was outside its lane.

## Timeline

All times are 2026-10-15 UTC unless stated otherwise.

- **2023-10-24:** #1247 replaces the reflection deep copy in `record.Clone` with a hand-written
  one. It recurses into `map[string]any` and copies everything else by reference.
- **2023-11-09:** v0.8.0 ships it.
- **2023-12-05:** `conduit-commons#1` starts the shared module with the same `Clone`.
- **2024-07-01:** #1622 moves Conduit to `conduit-commons`' `opencdc.Record` (v0.11.0) and removes
  `pkg/record`.
- **2025-06:** the `split` (#2292) and `clone` (#2332) processors land (v0.14.0). Both clone every
  record they emit.
- **2026-10-12:** #2946 lets arch-v2 destination-only branches share records instead of cloning.
- **2026-10-15:** `conduit-commons#336` is filed from the #3093 property-test work.
- **2026-10-15:** reproduced in Conduit with the engine's own `stream.Message.Clone`
  (`repro/clone-sharing`): JavaScript mutation leaking across fan-out branches, and `field.set`
  failing on a cloned record. While testing the fix, the `avro.encode` example's change of output
  showed the schema-shape effect.
- **2026-10-15:** `conduit-commons#338` (type-preserving deep copy, main) is opened, reviewed, and
  signed off for merge after #334.
- **2026-10-15:** DeVaris decides the patch lines get a deep copy that keeps the old types, so
  Avro schemas stay stable within a patch release (variant (b)). `conduit-commons#339`
  (`release/v0.7.x`) and `#340` (`release/v0.8.x`) are opened. The tags (v0.7.2, v0.8.3) wait for
  these and for the union-unwrap backports `#324` / `#325`.
- **2026-10-15:** found during review: a JavaScript processor can return a record that contains
  itself, which makes any `Clone` overflow the stack and crash the process. Filed as #3148.

## Root cause

`Clone` was written as a fast path for the common shape of record data, and the fast path was
taken for the whole job. The recursion handled the one container type it named,
`map[string]any`, and assumed everything else was a value. Converting the copy to
`StructuredData` was most likely meant to keep the result a `Data`. It applied to nested maps
too, and that changed their type.

## Why no test caught it

- `TestRecord_Clone` (now `opencdc/record_test.go`) checked one thing: the clone is equal to the
  original. It never changed the clone and looked at the original, which is the property that
  matters for a copy. Its fixture held `[]string` and `map[string]string` (both shared, both
  invisible to an equality check) and no nested `map[string]any`, so the type change didn't show
  either. `cmp.Diff` would have caught the retyping had the fixture contained one.
- There was no property-based testing on record paths. The process-maturity table in `CLAUDE.md`
  lists rapid/gopter as absent from the tree. A generator over nested shapes finds this in its
  first few cases, and that is how it was found.
- The silent symptom needs three things at once: a path that clones, a processor that mutates a
  nested container in place, and someone checking a sibling copy. Only the JavaScript processor
  mutates in place, and the effect looks like a script bug.
- The loud symptom (nested-path processors failing after fan-out) looks like a configuration
  error, and the record goes to the DLQ, where it was presumably read as bad input.
- The `avro.encode` example's golden bytes were produced through the example harness, which clones
  its input. The test pinned the buggy shape.

## Fix

- **main (`conduit-commons#338`, v0.9.0):** `Clone` copies maps, slices and arrays element by
  element at every depth and keeps every value's Go type. The result is `reflect.DeepEqual` to the
  input. Fast paths cover the common record shapes; other container types go through reflection.
  Pointers stay shared and structs are copied by value. This is documented. Review NITs (godoc for
  the top-level nil case and cycles, nested `RawData` test) are in `conduit-commons#341`.
- **patch lines (`conduit-commons#339`, `#340`):** the same deep copy, but a `map[string]any` held
  directly in a `StructuredData` is still retyped to `StructuredData`, exactly as before. The
  sharing (the corruption) is fixed; types are unchanged, so nested-path processors still fail
  after a clone on v0.20.x (known issue, release-noted, workaround: put them before the fan-out).

## New automated checks

- `conduit-commons` `opencdc/data_clone_test.go` (main, #338): a property test over 2000 random
  nested values (maps, `StructuredData`, slices, arrays, typed maps, bytes, times, scalars, depth 4)
  asserts that the clone is `DeepEqual` to the original with every Go type kept, that changing the
  clone at every depth leaves the original equal to an independently generated twin, and the other
  way round. It fails on the old `Clone`.
- The same file on `release/v0.7.x` and `release/v0.8.x` (#339, #340): the independence properties,
  plus a check that the result equals the released `Clone`'s result for every generated value, and
  a test that pins the retained retyping type by type.
- Conduit (lands with the commons v0.9.0 bump, from `repro/clone-sharing`): arch-v1 fan-out with a
  JavaScript processor changing a nested array on one branch must leave the sibling and the
  original unchanged; `field.set` on a nested path must work on a cloned record.

## Follow-ups

- **Conduit commons v0.9.0 bump (with #3140):** take #338, add the two Conduit regression tests,
  and update the `avro.encode` example. Its `myMap` has two keys, so its encoded bytes depend on Go
  map iteration order. Switch it to a one-key map or compare decoded values, then regenerate the
  processor docs.
- **Release notes:**
  - v0.20.3: records cloned by the engine no longer share nested data; known issue for
    nested-path processors after fan-out, `clone` or `split`.
  - v0.21: Avro schemas that `avro.encode` auto-registers for nested maps in cloned or fanned-out
    records change from `record` to `map`, matching uncloned records. A subject registered under
    the old shape gets a new version.
- **JavaScript cyclic output (#3148):** reject a record that contains itself with an error record
  instead of letting the next `Clone` crash the process.
- **#3093 processor property harness:** for every built-in processor, assert that processing a
  clone never changes the original, and that processing works the same on a cloned record. Those
  two properties would have caught both symptoms.
- **Inspector:** add a `-race` test that inspects a pipeline whose downstream JavaScript processor
  changes nested data in place. With the deep copy it should be clean; without it, it shows the
  race described above.
