# avro.encode with `schema.strategy: fromRecord`

`fromRecord` encodes a record with the Avro schema it carries (its `opencdc.payload.schema.*` or
`opencdc.key.schema.*` metadata), leaves out each top-level field a processor removed from the record, adds a
nullable field for each top-level field the record adds, and registers the result under `schema.fromRecord.subject`.
It never changes a value to make it fit. An empty key or payload part (nil, or raw data with no bytes, as a delete
record's `.Payload.After` often is) is not encoded and passes through unchanged. This page covers the metric and the
error codes it produces. Design: `docs/design-documents/20261106-avro-encode-preserve-source-schema.md`.

## The output has inferred types (`long`, `timestamp-micros`, `decimal(31,10)`)

### Symptom

`conduit_processor_avro_encode_schema_fallback_total{subject="..."}` increases, or the schema registered under
`schema.fromRecord.subject` has `long` where the source declared `int`.

### Diagnosis

- The counter increases: the records reach `avro.encode` without schema metadata for the encoded field. The
  source does not attach a schema (for Postgres, check `logrepl.withAvroSchema`), or a processor before it
  replaced the field. A second `avro.encode` on `.Payload.Before` after one on `.Payload.After` also counts here:
  the first one removes the payload schema metadata both parts share.
- The counter is flat but the types are inferred: a processor before `avro.encode` changed the shape of the
  record (for example `field.set` added a field) with its schema-encode middleware on. That middleware registers a
  schema inferred from the values as a new version of the source subject, and the record carries that version.

### Remediation

Set `sdk.schema.encode.payload.enabled: "false"` (or `sdk.schema.encode.key.enabled` for `.Key`) on each processor
before `avro.encode` that adds or removes fields. The record then keeps the source schema metadata.

A `conduit-processor-sdk` change is planned that lets that middleware extend the carried schema instead of inferring
one (opt-in in v0.22, warning in v0.23, default in v0.24). Once a release ships with it, this setting is no longer
needed.

## `processor.avro_encode.schema_mismatch`

### Symptom

Records go to the DLQ, or the pipeline degrades, with this code. The message names the field.

### Diagnosis

The value no longer fits the schema the record carries: a different type, a number out of range, null in a
non-null field, more decimal digits than the scale, a time of day in a `date`, or a key added or removed inside a
nested record.

A top-level field that a processor removed (for example with `field.exclude`) is not a mismatch: it is left out of
the registered schema. A field that is still there keeps its carried type, so a field set to null (rather than
removed) in a non-null column is a mismatch.

Two cases that look harmless are also mismatches. A nil `[]byte` or nil list in a non-null `bytes` or `array`
field counts as null, not as an empty value (the codec writes an empty value, and the check compares what was
written); set it to an empty value, or make the field nullable. A `time.Time` in a `local-timestamp-millis` or
`-micros` field fails when its zone differs from the process's local zone at that instant: the codec writes the wall
clock of the local zone, so the wall clock a reader sees would differ from the one the value has. Convert the time to
UTC and run with `TZ=UTC`, or to the local zone, before this processor.

### Remediation

Fix the processor that changed the value, or, if the change is intended, attach a schema that describes it and
use `preRegistered`. To leave a top-level field out of the output, remove it (`field.exclude`) instead of setting it
to null.

## `processor.avro_encode.schema_incompatible`

### Symptom

Every record fails with this code right after switching a pipeline to `fromRecord`.

### Diagnosis

The registry refuses the schema under the compatibility rule of `schema.fromRecord.subject`. This is expected when
the subject holds versions written by `autoRegister`: `decimal(31,10)` to `decimal(14,2)` is not a legal Avro
promotion.

### Remediation

Use a new subject (and topic) for the switch, or change the compatibility of that subject deliberately after
checking which consumers read it.

The same refusal can follow a removed field. Each set of fields the records have is its own version under the
subject: records with a field removed register a version without it. A subject Conduit creates has compatibility
`NONE`, so the versions only alternate. On a subject that already existed with `BACKWARD` compatibility, a version
that has a non-null field after one that does not is refused. Remove the field from every record (not some of
them), or use a subject Conduit creates.

## `processor.avro_encode.registry_unavailable` and `processor.avro_encode.schema_unsupported`

`registry_unavailable`: the registry did not return the carried schema or did not register the encoding schema.
Check that it is reachable. The record is not encoded with an inferred schema instead.

`schema_unsupported`: the record's schema metadata has a subject without a version (or the reverse) or a version
that is not a positive integer, or the schema is not an Avro record, uses schema references, cannot take the fields
the record adds (for example a field name that is not a valid Avro name), or cannot lose a field the record removed
(the removed field defines a named record, enum or fixed type that another field uses by name). Fix the metadata or
the field name, keep the field that defines the type, or use `preRegistered`.

## A delete record's empty payload is not encoded

### Symptom

Output records whose `.Payload.After` (or the encoded part) is empty, with no schema ID header.

### Diagnosis

Expected. `fromRecord` does not encode an empty part (nil, or raw data with no bytes): there is no value, and
encoding one would write a record of nulls or defaults. The record passes through unchanged, including its schema
metadata, which the connector and processor SDK middlewares also skip for an empty part. `autoRegister` behaves
differently: it encodes zero-length raw data as a record with no fields and fails on a nil part.

### Remediation

None needed. To drop deletes, or route them elsewhere, use a `condition` on the processor or a filter.
