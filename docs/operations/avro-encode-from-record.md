# avro.encode with `schema.strategy: fromRecord`

`fromRecord` encodes a record with the Avro schema it carries (its `opencdc.payload.schema.*` or
`opencdc.key.schema.*` metadata), adds a nullable field for each top-level field the record adds, and registers the
result under `schema.fromRecord.subject`. It never changes a value to make it fit. This page covers the metric and
the error codes it produces. Design: `docs/design-documents/20261106-avro-encode-preserve-source-schema.md`.

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

## `processor.avro_encode.schema_mismatch`

### Symptom

Records go to the DLQ, or the pipeline degrades, with this code. The message names the field.

### Diagnosis

The value no longer fits the schema the record carries: a different type, a number out of range, null in a
non-null field, more decimal digits than the scale, a time of day in a `date`, a field the schema declares that
the record no longer has, or a new key inside a nested record.

### Remediation

Fix the processor that changed the value, or, if the change is intended, attach a schema that describes it and
use `preRegistered`. Removing a field before `fromRecord` is not supported.

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

## `processor.avro_encode.registry_unavailable` and `processor.avro_encode.schema_unsupported`

`registry_unavailable`: the registry did not return the carried schema or did not register the encoding schema.
Check that it is reachable. The record is not encoded with an inferred schema instead.

`schema_unsupported`: the record's schema metadata has a subject without a version (or the reverse) or a version
that is not a positive integer, or the schema is not an Avro record, uses schema references, or cannot take the
fields the record adds (for example a field name that is not a valid Avro name). Fix the metadata or the field
name, or use `preRegistered`.
