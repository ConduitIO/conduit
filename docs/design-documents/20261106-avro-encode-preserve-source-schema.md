# avro.encode: preserve the schema the record carries

## Summary

`avro.encode` with `schema.strategy: autoRegister` ignores the Avro schema a record already carries and
infers a new one from the Go values the processor SDK decoded. Those values have lost the distinction
between Avro `int` and `long`, `date` and `timestamp`, and a decimal's precision and scale. A Postgres
table with `int4`, `numeric(14,2)` and `date` columns therefore reaches Kafka as `long`,
`decimal(31,10)` and `timestamp-micros`. The values are the same; the type signature is not, and a
Kafka Connect / Debezium consumer with a reader schema of `int` or `decimal(14,2)` cannot read it.

This document adds an opt-in strategy, `schema.strategy: fromRecord`. It takes the schema the record
carries (the subject and version in `opencdc.payload.schema.*` or `opencdc.key.schema.*`), adds a
nullable field for each top-level field the record has and the schema lacks (an added `__op`, for
example), registers the result under `schema.fromRecord.subject` and encodes with it. A value that
does not fit the schema exactly is an error, never a coerced or truncated value. Records that carry no
schema fall back to inference, with a warning and a metric.

Versioning: opt-in in v0.22, `autoRegister` warns in v0.23 when it infers while a record carries a
schema, and `autoRegister` uses the carried schema by default in v0.24.

Roadmap: v0.22 "Kafka→Kafka through `migrate`" and the Debezium-compatible output mode (Debezium
output parity). Evidence: the v0.21 CDC-parity smoke (smoke-20261105), wide Postgres table.

## Context

### Where the types are lost

The smoke pipeline is `builtin:postgres` → `field.set` (`.Payload.After.__op`) → `avro.encode`
(`field: .Payload.After`, `autoRegister`) → `builtin:kafka` (raw bytes, schema extraction off).

1. The Postgres source attaches a correct Avro schema: `int4` → `int`, `numeric(p,s)` →
   `bytes`/`decimal(p,s)`, `date` → `int`/`date` (`conduit-connector-postgres`, `source/schema/avro.go`).
   The connector SDK encodes the payload with it and puts the subject and version in the metadata.
2. Before each built-in processor, the processor SDK decode middleware
   (`conduit-processor-sdk/middleware.go`, `decodePayload`) unmarshals the payload into
   `opencdc.StructuredData`. The generic Avro decode returns Go `int` for Avro `int`, `time.Time` for
   `date`, and `*big.Rat` for a decimal. A `*big.Rat` has no precision or scale.
3. `avro.encode` with `autoRegister` calls `ExtractAndUploadSchemaStrategy`
   (`pkg/plugin/processor/builtin/impl/avro/internal/encoder.go`), which reflects over those Go values
   with `conduit-commons`' extractor: `int` → `long`, `time.Time` → `timestamp-micros`, `*big.Rat` →
   `decimal(31,10)` (a fixed estimate). The schema the record carried is never consulted, and
   `dropStaleSchemaMetadata` then removes its subject and version from the record.

### What the processor SDK encode middleware does between processors

The processor SDK also wraps every built-in processor in an encode middleware
(`ProcessorWithSchemaEncode`). When the processor returns data that still fits the incoming schema, it
re-encodes with that schema and keeps the subject and version. When it does not fit, for example
because `field.set` added `__op`, it extracts a schema from the values (the same lossy inference) and
registers it as a new version of the same subject. In the smoke pipeline that is what happens after
`field.set`: by the time `avro.encode` runs, the record's metadata names an inferred version, not the
source's.

So a strategy that reads the carried schema only helps if the processors before it leave the carried
schema in place. For a processor that changes the shape, that means
`sdk.schema.encode.payload.enabled: false` on it (the payload then stays structured and the metadata
keeps naming the source schema). This is a usage requirement of the new strategy, documented in the
processor description and in the docs; see _Open questions_.

### Prior decisions this builds on

- `20261012-mysql-type-semantics.md` records the inference rules, including `*big.Rat` →
  `decimal(31,10)` regardless of source precision.
- A separate, unreleased `conduit-commons` fix (`fix/avro-decimal-lossless`) makes the codec reject a
  `*big.Rat` that is not exact at the schema scale instead of truncating it. This design does not
  depend on it and must not regress it: `fromRecord` verifies every encode by decoding it back, which
  catches the same truncation on the released commons, and stays correct when the fix lands.

## Constraints

- **Invariant 6.** A value that does not fit the carried schema is an error (the record becomes an
  error record: DLQ or a degraded pipeline, per the pipeline's DLQ settings). The strategy never
  widens, narrows, rounds or truncates a value and never fills a missing field with a default.
- **Opt-in in v0.22.** `autoRegister` and `preRegistered` behave exactly as before. The registered
  schemas and wire bytes of existing pipelines do not change until a user opts in.
- **No new dependencies**, no processor SDK or commons change required to ship.
- **Same registry.** Built-in processors and connectors share Conduit's schema registry (built-in or
  external), so the carried subject and version resolve through `schemaregistry.Registry`.
- **Errors are API.** Every new failure has a stable code, a config path where one applies, and a
  suggested fix.

## Alternatives considered

### A. New strategy that reuses the carried schema and extends it (chosen)

Described under _Decision_. It fixes `int`, `date` and `decimal` together because it never looks at
Go types for fields the schema already declares, and it keeps Avro features inference can never
recover: enums, named and fixed types, field order, docs and custom properties.

### B. Fix the inference to choose narrower types

Infer `int` for Go integers that fit 32 bits, `date` for midnight-UTC times, and a decimal scale from
the value. Rejected:

- Types would depend on values, so two records of the same table could register different schemas
  (`int` for 7, `long` for 2^40; `date` for midnight, `timestamp` for 00:00:01). That is worse than
  today's stable-but-wrong schema.
- It still cannot recover a declared `numeric(14,2)` from `32.3`: the Rat has no precision, and its
  scale depends on the value.
- It changes the default `autoRegister` output for every existing pipeline at once, with no opt-in.

### C. Carry the schema through the processor SDK decode

Make the SDK decode into Go types that keep Avro identity (`int32` for `int`, a date type, a decimal
type with precision and scale), so inference sees them. Rejected:

- It changes the Go types every processor sees (`custom.javascript`, `field.*`, standalone WASM
  processors), a much wider contract change in another repository with its own release.
- It needs new Go types for `date` and decimal-with-scale that every processor would have to handle.
- The inference would still lose enums, named types and properties.

### D. Use `preRegistered` with a hand-registered schema

Works today and is the benchmark workaround, but needs one schema per table registered out of band and
kept in sync with the source by hand. It does not scale to a CDC source with many tables or schema
evolution, and `migrate kafka-connect` cannot emit it without registry access at migration time.

## Decision

### Configuration

A third value for the existing `schema.strategy` parameter, modeled like the other two:

```yaml
- id: avro
  plugin: avro.encode
  settings:
    field: .Payload.After
    schema.strategy: fromRecord
    schema.fromRecord.subject: cdc.public.orders-value
```

- `schema.fromRecord.subject` (required): the subject the encoding schema is registered under. It is
  required for the same reason as `schema.autoRegister.subject`: the output subject (usually
  `<topic>-value` or `<topic>-key`) is not the source's subject.
- `field` must be the whole key or a whole payload part (`.Key`, `.Payload.After`,
  `.Payload.Before`, in any equivalent spelling). The carried schema describes the whole part, so a
  nested field is rejected at configure time (`common.invalid_argument`, config path `field`).
  `.Key` uses `opencdc.key.schema.*`; `.Payload.Before` and `.Payload.After` use
  `opencdc.payload.schema.*`.

### Per-record algorithm

1. Read the subject and version for the part from the record metadata. Both absent: fall back (step
   7). One present, or a version that is not a positive integer: error
   `processor.avro_encode.schema_unsupported`.
2. Fetch the schema by subject and version from the registry (the client caches it). A fetch error is
   `processor.avro_encode.registry_unavailable`. The record is not encoded with anything else.
3. The schema must be Avro with a top-level `record` and no schema references; anything else is
   `processor.avro_encode.schema_unsupported`.
4. **Missing fields.** Every field the carried schema declares must be present as a key in the data.
   A missing key (a processor removed the field) is `processor.avro_encode.schema_mismatch`: encoding
   it would write the field's default or null, which the data does not say.
5. **Added fields.** For each top-level key the schema does not declare, infer a type from its value
   with the existing extractor, make it nullable (`["null", T]`, or keep it as is when the inferred
   type is already a union with `null`) and give it `"default": null`. Added fields are appended in
   name order, so the same set of added fields always yields the same schema text and the registry
   returns the same ID. Nullable with a default keeps the extended schema backward compatible with
   the carried one under the registry's default `BACKWARD` rule, and matches what Debezium's
   `ExtractNewRecordState` does for `__op`. Fields are added at the top level only; a new key inside a
   nested record is caught by step 8.
6. Register the schema text (the carried schema's own text when nothing was added) under
   `schema.fromRecord.subject`. A rejection with HTTP 409 (incompatible) is
   `processor.avro_encode.schema_incompatible`; any other failure is
   `processor.avro_encode.registry_unavailable`.
7. **Fallback.** A record with no schema for the part is encoded with the `autoRegister` inference
   under the same subject. Each processor instance logs a warning the first time, and every such
   record increments `conduit_processor_avro_encode_schema_fallback_total`.
8. **Lossless check.** Encode, decode the bytes with the same schema, and compare with the input by
   value (Go types may differ, data may not: numbers, strings, bytes, time instants, decimals, map
   keys, list lengths). Any difference, or a codec error, is `processor.avro_encode.schema_mismatch`
   naming the field path (never the value). The check also runs on fallback encodes, so an inferred
   `decimal(31,10)` that would truncate a scale-12 value is an error under this strategy.

`// Invariant 6` comments mark steps 4, 5 and 8 in the code.

### Error codes

| Code | gRPC | When |
| --- | --- | --- |
| `processor.avro_encode.schema_mismatch` | InvalidArgument | A value does not fit the carried (or extended) schema exactly: wrong type, out of range, null for a non-null field, finer precision than the logical type, a missing field, an unknown nested key. |
| `processor.avro_encode.schema_unsupported` | FailedPrecondition | The record's schema metadata is unreadable, or the carried schema is not an Avro record, or an extension cannot be built from it. |
| `processor.avro_encode.registry_unavailable` | Unavailable | The registry could not return the carried schema or register the encoding schema. |
| `processor.avro_encode.schema_incompatible` | FailedPrecondition | The registry refused the encoding schema under its compatibility rule for `schema.fromRecord.subject`. |

All of them make the record an error record. Nothing is acked upstream until the record is handled
downstream (DLQ write or pipeline stop), so invariants 1 and 3 hold on every path.

## Failure modes

| Case | Behavior | Why |
| --- | --- | --- |
| Record without a schema | Inference fallback, warning once per processor, metric per record; lossless check still applies | There is no declared type to preserve; erroring would break mixed pipelines that work today |
| A processor changed a field's type (e.g. `int` to `string`) | `schema_mismatch` | The codec rejects the Go type; never coerced |
| A processor produced a value finer than the type (`1.234` into `decimal(14,2)`, 01:00 into `date`) | `schema_mismatch` | The codec truncates silently on the released commons; the round trip catches it |
| A processor removed a field | `schema_mismatch` naming the field | Writing a default or null would invent data |
| A processor added a top-level field | Nullable field with default null appended | Decision by DeVaris; keeps backward compatibility |
| A processor added a key inside a nested record | `schema_mismatch` | Only top-level extension is defined; the codec would drop the key |
| Nullable changes: null into a non-null field | `schema_mismatch` | Codec error |
| Nullable changes: value into a `["null", T]` field | Encoded as `T` | Fits the schema |
| Unions with several branches | Encoded when the decoded value round-trips; otherwise `schema_mismatch` | Branch resolution is the codec's; the round trip proves the result |
| Registry unavailable | `registry_unavailable`, record not encoded | Never fall back to inference on a transient error: it would register a lossy version |
| Registry compatibility refuses the schema (e.g. the subject holds earlier `autoRegister` versions with `decimal(31,10)`) | `schema_incompatible`, suggestion: use a new subject or change the subject's compatibility | Conduit does not change compatibility on an existing subject |
| Mixed tables in one processor | Each record registers its own table's schema under the one subject; versions alternate and may be refused by compatibility | Same as `autoRegister` today; route each table to its own processor and subject (as the smoke does with `condition`) |
| Field `.Payload.Before` after `.Payload.After` was encoded | Fallback (the first encode removed the shared payload metadata) | Existing `dropStaleSchemaMetadata` behavior; documented |
| A preceding processor changed the shape with the SDK encode middleware on | Encodes with the middleware's inferred version (lossy types) | See _Context_; set `sdk.schema.encode.payload.enabled: false` on that processor |
| Two named types collide when an added field's inferred type is a record | `schema_unsupported` | The extended schema does not parse; fail closed |
| Encoded part is present but empty (zero-length raw data, as some delete records carry) with a carried schema | `schema_mismatch` (the first declared field is missing) | Encoding it would invent a record of nulls or defaults; skip deletes with a processor `condition`, as the smoke pipeline does |
| An added field is null in one record and set in the next | The null one is typed `["null","string"]`, the set one by its value, so the schema text differs and a new version is registered | Same rule as `autoRegister`; fields a pipeline adds should have a stable type |
| Carried schema uses schema references | `schema_unsupported` | Registering it under another subject would need the references resolved and copied; not needed for the sources this targets |
| Crash mid-record | Nothing new: the processor is stateless; the record is re-read and re-encoded | Registration is idempotent (same text, same ID) |

## Upgrade and rollback

- **Upgrade:** nothing changes until a pipeline sets `schema.strategy: fromRecord`. Opting in changes
  the schema registered under the output subject. A subject that already holds `autoRegister`
  versions may refuse it under `BACKWARD` (`decimal(31,10)` to `decimal(14,2)` is not a legal
  promotion), so the recommended move is a new subject (and topic), or setting that subject's
  compatibility deliberately. Consumers with a reader schema matching the source (Debezium-shaped)
  can then read the output; consumers that were reading the `long`/`decimal(31,10)` output keep
  working only where Avro promotion allows (`int` → `long` yes, `decimal(14,2)` → `decimal(31,10)` no),
  which is why this is opt-in and announced.
- **Rollback:** set `schema.strategy` back to `autoRegister`. New records are encoded with the inferred
  schema again; records already written keep their schema ID and stay readable. An older Conduit that
  does not know `fromRecord` fails pipeline validation with an inclusion error on `schema.strategy`, so
  a rollback of the binary needs the config change first.
- No serialized state, position or checkpoint format changes.

## Versioning and deprecation plan

- **v0.22:** `fromRecord` ships opt-in. Release notes and docs explain it and the
  `sdk.schema.encode.payload.enabled: false` requirement for shape-changing processors before it.
- **v0.23:** `autoRegister` logs a warning (once per processor) and increments a counter when it infers
  a schema for a record that carries one, naming `fromRecord`.
- **v0.24:** `autoRegister` uses the carried schema when a record has one (the `fromRecord` algorithm),
  and infers only for records without one. The pure-inference behavior for records that carry a schema
  is removed. This is the announce → warn → remove sequence over two minor versions.

## Observability

- `conduit_processor_avro_encode_schema_fallback_total{subject}`: records `fromRecord` encoded by
  inference because they carried no schema. Non-zero on a pipeline that is expected to preserve types
  means a source or a preceding processor is not attaching or keeping the schema.
- A warning log, once per processor instance, on the first fallback, naming the subject.
- Errors carry the codes above, so `inspect` and the DLQ show which records failed and why.

## Test plan

- Unit tests in `internal` against an in-memory registry, one per failure mode above.
- The smoke reproduction in `pkg/plugin/processor/builtin`: a record carrying an `int` /
  `decimal(14,2)` / `date` schema through the real middleware chain (`field.set` adding `__op`, then
  `avro.encode`); the schema behind the wire header equals the source schema plus a nullable `__op`
  with `fromRecord`, and is `long` / `decimal(31,10)` / `timestamp-micros` with `autoRegister`.
- A test pinning the middleware caveat: with `field.set`'s encode middleware left on, the carried
  schema is the inferred version.
- `tests/valuecorrectness`: a `fromRecord` pipeline among the overlapping-named-types pipelines,
  checking bytes and values with an isolated codec.
- Every test is checked to fail with the new code path reverted.

## Open questions

1. **The processor SDK encode middleware still infers between processors.** Without
   `sdk.schema.encode.payload.enabled: false` on a shape-changing processor before `avro.encode`,
   `fromRecord` preserves an already-inferred schema. Making the default v0.24 path work without that
   setting needs the same "extend, don't infer" rule in `conduit-processor-sdk`'s
   `ProcessorWithSchemaEncode`, which is a contract change in that repository and needs its own
   versioning discussion. Until then, the v0.24 default only helps pipelines whose processors before
   `avro.encode` keep the shape or disable that middleware.
2. **Removed fields** are an error. A "project the schema onto the data" option would let
   `field.exclude` before `avro.encode` work; it is not in the decision and is left out.
