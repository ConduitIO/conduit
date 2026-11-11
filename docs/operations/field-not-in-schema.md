# A processor removed a field from one part of a schema-attached record (`ErrFieldNotInSchema`)

A record that carries a schema (Avro, for example Postgres CDC through a schema registry) is
re-encoded after a processor reshapes its payload. When a record has both a `Before` and an `After`
image, one schema describes both parts. If a processor removes a top-level field from `After` only,
Conduit cannot keep `Before` intact under that schema and cannot write the field into `After` as
NULL without inventing a value, so it fails the record. Before processor SDK v0.10.0 (Conduit
v0.22) the field was silently dropped from `Before` instead.

## Symptom

The failing record's error wraps `ErrFieldNotInSchema` ("field is not in the attached schema and
would be dropped by the encoder"). For the common case it reads:

```text
failed to encode before payload: field "x" is in payload.before but not in payload.after, and one
inferred schema describes both, so it would be dropped from payload.before; remove it from both
parts or from neither
```

It appears in the pipeline's `State.error` when the pipeline stops, in the Conduit log on the
processor node, and in DLQ records when the DLQ routes it. With the default DLQ settings (window 1,
threshold 0) the first such record stops the pipeline. No record is acknowledged, so the pipeline
resumes from the failed record once the cause is fixed.

## Diagnosis

1. The error names the field. Find the processor that removes it: `field.exclude` with a
   `.Payload.After.<field>` reference, a `custom.javascript` that deletes `After.<field>`, or a
   standalone processor built on the processor SDK.
2. Confirm the source emits a `Before` image on updates, for example Postgres with
   `REPLICA IDENTITY FULL`. Only updates that carry a `Before` image are affected: creates and
   snapshots have no `Before`, and deletes have no `After`.

## Remediation

Remove the field from both parts, in the same processor:

```yaml
processors:
  - id: strip-secret
    plugin: field.exclude
    settings:
      fields: ".Payload.Before.secret,.Payload.After.secret"
```

For `custom.javascript`, delete the field from `record.Payload.Before` and from `record.Payload.After`
directly. Guard each with a null check: creates and snapshots have no `Before`, and deletes have no
`After`.

```js
function process(record) {
  if (record.Payload.Before) { delete record.Payload.Before["secret"]; }
  if (record.Payload.After)  { delete record.Payload.After["secret"]; }
  return record;
}
```

Setting `sdk.schema.encode.payload.strategy` to `extend` does not fix the After-only shape: it
rejects it too. Once the field is excluded from both parts, `extend` is safe to use and keeps the
source column types (`int`, `decimal(14,2)`) instead of the widened types `infer` registers. There
is no setting that restores the old behavior, which lost the field from `Before` without any error.

Adding a field to `After` only on an update with a `Before` image (`field.set`) is a separate,
older limitation and fails with a "missing required field" error. It is not covered here.
