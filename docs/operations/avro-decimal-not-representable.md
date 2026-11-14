# Avro decimal does not fit its schema (`processor.avro_encode.decimal_not_representable`)

Since v0.22 (`conduit-commons` v0.13.1), Conduit refuses to Avro-encode a decimal that does not fit the schema's
`decimal(precision, scale)` exactly. Before, the value was written truncated, with no error: `1.123456789012`
against `decimal(31,10)` became `1.1234567890`, and `-1.5` against `decimal(5,0)` became `-2`. Now the record
fails. Nothing is rounded, truncated or acknowledged without being written.

A value does not fit when it has more fractional digits than the scale, no finite decimal expansion (such as 1/3),
or more digits than the precision.

## Symptom

Records go to the DLQ, or the pipeline degrades or keeps recovering, according to its dead-letter settings. The
error message starts with `a decimal value does not fit its Avro schema exactly` and names the field and the
precision and scale the value needs, for example:

```text
field "amount": the value needs precision 13 and scale 12, the schema is decimal(31,10)
```

The code depends on where the record was encoded:

- `avro.encode` with `schema.strategy: autoRegister` or `preRegistered`: `processor.avro_encode.decimal_not_representable`.
- `avro.encode` with `schema.strategy: fromRecord`: `processor.avro_encode.schema_mismatch`, as for any other value
  that does not fit the carried schema (see [the fromRecord runbook](avro-encode-from-record.md)).
- The schema handling of a built-in connector or processor (a source that attaches a schema, or a processor that
  re-encodes a schema-attached record): no Conduit code; the message contains
  `avro decimal value is not representable in its schema`. A source that fails this way stops the pipeline, since a
  source error has no DLQ; nothing is acknowledged, and it resumes from the last position once fixed. The built-in
  Postgres source can hit this with `logrepl.withAvroSchema: false` and `sdk.schema.extract.payload.enabled: true`,
  where the SDK infers `decimal(31,10)` for a `numeric`.

The message never contains the value.

## Diagnosis

1. Read the precision and scale the message says the value needs and compare them with the schema's.
2. With `autoRegister`, the schema is always `decimal(31,10)`, whatever the source column is. A value with more than
   10 fractional digits fails, as does a value that a processor computed by division (a rate, an average).
3. With `preRegistered` or a source-attached schema, check whether the registered schema matches the source column,
   or whether a processor before the encode changed the value (division, currency conversion).

## Remediation

- **Keep the source's decimal type:** use `schema.strategy: fromRecord` on `avro.encode`, so the value is encoded
  with the schema the source attached (for example Postgres `numeric(20,12)` stays `decimal(20,12)`). See
  [the fromRecord runbook](avro-encode-from-record.md) for the switch: use a new subject, because a subject holding
  `autoRegister` versions can refuse the new schema.
- **Register a wider schema:** with `preRegistered`, register a schema whose decimal has at least the scale and the
  integer digits the message asks for. The message's fix names one, for example `decimal(33,12)`.
- **Round on purpose:** if fewer digits are acceptable, round the value in a processor before `avro.encode`, so the
  rounding is explicit and visible in the pipeline config.
- **Route and continue:** to keep the pipeline running while you fix it, enable the DLQ with a threshold above 0
  so failing records go to the DLQ. The table in [the Avro element-limit runbook](avro-limit-exceeded.md) shows what
  each engine does with a failing processor record under each DLQ setting.

To restore the old behavior there is no setting; rolling back means running a Conduit build older than v0.22, which
writes the truncated value.
