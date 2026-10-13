# MySQL source type semantics: NULL, DECIMAL and unsigned BIGINT

## Summary

The MySQL source (`conduitio-labs/conduit-connector-mysql`, moving to `ConduitIO`) silently changes data on
the way out: SQL `NULL` becomes the Go zero value of the column type, `DECIMAL` becomes a `float64`, and
`BIGINT UNSIGNED` above 2^63-1 is not representable and is emitted as an invalid value after a log warning.
These are invariant 6 violations (labs-mysql#191, labs-mysql#86). This document defines the target semantics
and how existing pipelines migrate. In short:

- Nullable columns become Avro unions `["null", T]` and `NULL` is emitted as nil.
- `DECIMAL(p,s)` is emitted losslessly, by default as a string, optionally as an Avro `decimal` logical type.
  The lossy `double` mapping stays available only as an explicit opt-in.
- `BIGINT UNSIGNED` stays `long`, but a value above 2^63-1 halts the pipeline with a stable error instead of
  being mangled. An opt-in maps the column to `decimal(20,0)` or string.
- The mode is stamped in the position, so it cannot change silently across a restart or a downgrade.

This is a payload contract change and Tier 1. It needs sign-off before code.

## Context

How the source produces records today (read from `schema.go`, `fetch_worker.go`, `cdc_iterator.go` at labs
`main` plus the open fix branches; nothing here was changed):

- Per table, `schemaMapper` builds an Avro record schema from the column list and registers it through the
  SDK schema service as `<table>_payload` and `<table>_key`. Every field is a bare primitive; there are no
  unions, no logical types and no field defaults.
- Snapshot reads use `database/sql` column types (`sqlxRowsToAvroCol`). CDC uses the `go-mysql` canal
  table schema (`mysqlSchemaToAvroCol`). The two paths have separate type maps that must agree, which is why
  `formatValue` has per-type branches that normalize driver differences.
- `formatValue` is called for every value in both paths. Its first step is
  `if value == nil { return defaultValueForType(t.Type) }`. The code comment says this avoids modelling
  nullability in the schema.
- `DECIMAL` and `NUMERIC` map to Avro `double`; `formatValue` parses the driver's text with `ParseFloat`.
- `UNSIGNED TINYINT/SMALLINT/MEDIUMINT` map to `int`, `UNSIGNED INT` and `UNSIGNED BIGINT` to `long`. For
  `BIGINT UNSIGNED`, a `uint64` above `math.MaxInt64` hits a warning branch and the unconverted `uint64` is
  returned. What the Avro encoder then does with it was not tested here; the earlier probe in the
  certification findings observed corruption.
- The MySQL destination takes the structured payload and writes values with `ExecContext`; a nil value
  becomes SQL `NULL`. Today it never sees one from this source, so the zero value is written to the target
  and the corruption propagates.
- The position envelope already has a `version` field. `ParseSDKPosition` refuses a position with a version
  higher than the build supports, so a downgrade fails loudly.
- Related, not changed here: temporal columns are strings, `BIT` is `fixed[8]` (the N in `BIT(N)` is not
  available from the driver).

Debezium MySQL, the migration reference, handles these as follows: `NULL` is a nullable (optional) field
and an actual null; `decimal.handling.mode` is `precise` (Connect `Decimal` logical type, bytes with scale)
by default, with `string` and `double` available; `bigint.unsigned.handling.mode` is `long` by default
(overflows for values above 2^63-1) with `precise` (a `Decimal` with scale 0) as the alternative.

### Constraints

- Invariant 6: no silent coercion or truncation. A value that cannot be represented must follow a configured
  policy (halt, DLQ, evolve), never be altered.
- Snapshot and CDC must emit identical types for the same column, or a row updated after its snapshot changes
  type in the middle of a stream.
- The key schema follows the same rules. A `BIGINT UNSIGNED AUTO_INCREMENT` primary key is common, so the
  default for that type cannot change the key type for every such table.
- Backward compatibility is a design input: serialized state and payload schemas must be readable by N+1,
  with announce, warn, remove across at least two minor versions.
- No new SDK or protocol surface. Everything below is connector-side.

## Decision

### Settings

Three new connector settings (names are proposals, to be fixed at implementation and added to
`connector.yaml`):

| Setting | Values | Default |
| --- | --- | --- |
| `types.mode` | `strict`, `legacy` | `strict` for a pipeline with no position; the mode stamped in the position otherwise (see Upgrade) |
| `types.decimal` | `string`, `decimal`, `double` | `string` |
| `types.bigintUnsigned` | `long`, `decimal`, `string` | `long` |

`types.mode=legacy` reproduces today's behavior exactly, including the zero-value coercion. It exists for
migration only, logs a warning at every start, and is removed after two minor versions.
`types.decimal=double` and `types.bigintUnsigned` other than `long` are only honored in `strict` mode;
`double` is allowed but logs a warning naming the columns and states the precision loss.

### NULL

In `strict` mode a column whose `information_schema.columns.IS_NULLABLE` is `YES` becomes the union
`["null", T]` with default `null`; `NOT NULL` columns stay bare `T`. `formatValue` returns nil for nil and
`defaultValueForType` is deleted from the strict path.

Nullability must be read once per table at schema creation from `information_schema.columns`, for both
snapshot and CDC, because canal's table schema does not expose it (to verify at implementation). Both paths
then build the schema from the same source of truth, which also removes one class of snapshot/CDC drift.

Structured payloads carry a real nil. How an Avro union decodes through the SDK schema middleware to
`opencdc.StructuredData` (plain value or a `map[string]any{"string": v}` wrapper) must be pinned by a test
before the change ships, because every downstream processor and destination sees that shape. If the
middleware yields the wrapper form, the schema encoding step must unwrap it; this is a blocker to resolve
in implementation, not a design choice.

### DECIMAL and NUMERIC

- `string` (default): Avro `string`, the exact decimal text from the server (`12345678901234567890.1234567891`),
  no exponent, scale preserved (`1.50` stays `1.50`). Readable in JSON and safe through any destination. The
  field carries the Avro property `"mysql.type": "decimal(30,10)"` so the precision and scale are recoverable
  from the schema.
- `decimal`: Avro `bytes` with `logicalType: decimal`, `precision` and `scale` from the column. Precision
  above Avro's practical limits is not a concern for `bytes` (MySQL maximum is 65). The Go value is a
  `*big.Rat`; consumers that treat records as JSON see whatever the middleware renders, so this mode is for
  Avro-native consumers. Matches Debezium `precise`.
- `double`: today's mapping, explicit and warned.

Why `string` is the default and not `decimal` as in Debezium: records in Conduit routinely pass through
JSON-oriented processors and destinations (the `opencdc/json` format, the MySQL and Postgres destinations),
where a `big.Rat` or a base64 `bytes` value is a worse surprise than a string. Parity with Debezium's default
is a documented difference, and `types.decimal=decimal` gives it. This is a judgment call for sign-off.

### BIGINT UNSIGNED

- `long` (default): schema unchanged, so keys and consumers are not disturbed. A value above 2^63-1 stops
  the source with error `MYSQL_UNSIGNED_OVERFLOW` (name proposed), carrying the table, column, and the config
  path `types.bigintUnsigned`, and the fix "set `types.bigintUnsigned` to `decimal` or `string` for this
  connector". The source does not skip, truncate or wrap the value. In snapshot this is raised before the
  record is emitted; in CDC the same.
- `decimal`: `decimal(20,0)` logical type on `bytes`.
- `string`: Avro `string`.

This setting is connector-wide, not per column, to keep the schema story simple. Per-column overrides are
not proposed (no speculative generality); a mixed case is handled by the operator choosing the wider mode.

Smaller unsigned types are unchanged and already correct: `TINYINT/SMALLINT/MEDIUMINT UNSIGNED` fit `int`
and `INT UNSIGNED` is `long`. `strict` mode adds an assertion that the converted value is in range and
halts with the same error class otherwise, replacing today's log-and-return-unconverted branches (`int`
overflow paths in `formatValue` currently log a warning and return the raw value).

### Unchanged on purpose

Temporal-as-string, `BIT` as `fixed[8]`, `FLOAT`/`DOUBLE`, `ENUM`/`SET`/`JSON` as string. They are not
silent-corruption cases in the findings, and changing them would widen the compatibility surface of this
change. They get their own item if needed.

### Invariant comment

The enforcement site (`formatValue` and the schema builder) gets `// Invariant 6: a value that cannot be
represented in the schema type halts with MYSQL_UNSIGNED_OVERFLOW; it is never coerced`, and a package doc
paragraph stating the mapping table.

## Alternatives

**Keep zero-value coercion, document it.** Cheapest. Rejected: `NULL` and `0` are different data, and the
MySQL destination writes the coerced value to the target. It is a standing invariant 6 violation.

**Make every column nullable.** Avoids the `information_schema` lookup and makes snapshot and CDC schemas
trivially equal. Rejected: it destroys the `NOT NULL` information, forces every consumer to handle null for
primary keys, and still changes every schema.

**DECIMAL always as Avro `decimal` (Debezium parity).** Best typed form. Rejected as the default, not as an
option: it is the worst experience for the JSON-centric paths most Conduit pipelines use, and it makes a
decimal primary key an opaque bytes key. Kept as `types.decimal=decimal`.

**BIGINT UNSIGNED as `fixed[8]` (the suggestion in labs-mysql#86).** Lossless and fixed width. Rejected: the
bytes carry no signedness, so a reader that treats them as a signed 64-bit value silently misreads values
above 2^63-1, which is the corruption we are removing. A union of `fixed[8]` and `long` (also from #86)
makes the type value-dependent, which breaks the rule that a column has one type per schema version.

**BIGINT UNSIGNED as `decimal(20,0)` or string by default.** Safe for all values. Rejected as default
because it changes the key type of every `BIGINT UNSIGNED AUTO_INCREMENT` table for the sake of values that
almost never occur. Halt-on-overflow keeps the common case unchanged and the rare case loud; the opt-in is
there for tables that need the full range.

**Route out-of-range values to a DLQ instead of halting.** The connector has no DLQ of its own; the engine
DLQ handles errors returned from `Read`, so a returned error already follows the pipeline's configured
`on-error` policy. No separate mechanism is needed.

## Failure modes

| Failure | Behavior |
| --- | --- |
| `BIGINT UNSIGNED` value above 2^63-1 under `long` | Source returns `MYSQL_UNSIGNED_OVERFLOW`, no record emitted, position not advanced. Pipeline follows its `on-error` policy |
| Any integer outside its Avro type under `strict` | Same error class; replaces warn-and-return-raw |
| Nullability lookup fails (permissions on `information_schema`, table dropped) | `Open` fails with a stable error naming the table; no fallback to "assume NOT NULL", which would reintroduce coercion |
| DDL mid-stream changes nullability or type | Out of scope of this doc (DDL is untested today). A column whose schema-derived type no longer matches the registered schema must halt, not coerce. A test pins current behavior so the gap is visible |
| Decimal text that does not parse | Halt with the error class above; never fall back to a raw value (today's `ParseFloat` failure returns the raw string) |
| Snapshot and CDC disagree on a column type | Caught by a parity test that runs every supported type through both paths and compares the Avro-decoded value |
| Crash mid-snapshot or mid-CDC after upgrade | Position carries the stamped mode, so the restart uses the same types as the records already emitted. Covered by the kill -9 test below |
| Downgrade after upgrade | New position version is refused by the older build with `ErrPositionVersionUnsupported` (already the contract), instead of mis-reading |
| Union decoding yields a wrapper type in some processor/destination | Blocking test (see NULL) before release |
| Destination (MySQL, Postgres) given a string for a numeric column | The database coerces text to numeric on insert. Verified per destination by the destination round-trip test, not assumed |

## Upgrade and rollback

Schema subject names do not change. A different schema registered under `<table>_payload` becomes a new
version of the same subject (SDK schema service behavior, to confirm), and each record carries its subject
and version in metadata, so a consumer that resolves the schema per record reads old and new records
correctly. A consumer that pins a version or generates code from version 1 will break; this is the
compatibility cost and the reason for the staged rollout.

1. **Release N (v0.21 target):** ship `types.mode`. Fresh pipelines (no position) get `strict`. Existing
   pipelines resume in `legacy`, because their downstream destinations and consumers were built on the old
   shapes, and log a warning each start that names the open data-loss risks and the setting to change.
   The mode is written into the position (envelope version bumped to 2, new optional field `types_mode`).
   Positions without it are read as `legacy`.
2. **Switching an existing pipeline** is explicit: stop, set `types.mode=strict`, start. The connector
   refuses to change mode on a non-empty position unless the position is cleared or the mode was never
   stamped, because a changed schema on a table with an in-flight snapshot would mix types. Documented
   procedure: for a pipeline in CDC phase the switch is allowed at a restart (records after the restart use
   the new schema version); for a pipeline mid-snapshot, finish or restart the snapshot.
3. **Release N+2 minor:** default flips to `strict` for all pipelines that have not pinned `legacy`; the
   warning becomes louder. **Release N+3:** `legacy` removed.
4. **Rollback:** from N to N-1 is refused by the position gate once N has written a v2 position. To roll
   back, the operator clears the position (re-snapshot) or keeps running N in `legacy`. This is stated in the
   release notes. The envelope bump is the one-way door and is why it needs the upgrade test.

Because the envelope bump is a serialized-format change, it ships with an upgrade test: write a v1 position
with N-1 in the snapshot phase and again in the CDC phase, start N, and verify resume without duplicates
beyond at-least-once and with `legacy` types; then switch to `strict` and verify the next records carry the
new schema version.

## Observability

- Log at `Open`: the effective `types.mode`, `types.decimal`, `types.bigintUnsigned`, and in `legacy` or
  `double` mode the list of tables and columns affected by lossy mapping, at `warn`.
- Log on each new schema version registered: table, subject, version, mode.
- A counter-style log line (no metrics surface exists in the connector today) for overflow halts is the
  error itself; the error is machine-readable with code, table, column, config path and fix text.
- Documentation: a type mapping table in the connector README and the `llms.txt`/docs source in step with
  the change; the migration note for step 1 to 3 in the release notes.

## Test plan

Written before the implementation (review the tests first):

- Table-driven NULL tests: every supported type, nullable and not, in snapshot and CDC; assert real nil and
  that `NOT NULL` columns stay bare.
- Round-trip DECIMAL tests with precision 65, scale 30, negative values, trailing zeros, and the exact
  reproduction row from #191, in all three decimal modes.
- `BIGINT UNSIGNED` at 0, 2^63-1, 2^63, 2^64-1 under each mode; halting behavior under `long`.
- Snapshot/CDC parity: insert, snapshot it, update it through CDC, compare decoded types per column.
- Property-based test (`rapid`, which is not in the tree yet; adopting it is its own dependency
  justification) over decimal text and uint64 values: format then parse is the identity or a stable halt.
- Fuzz the decimal text parser.
- Destination round trip: MySQL source to MySQL destination with NULLs, large decimals and unsigned values;
  assert the target rows equal the source rows. A Postgres destination case for NULL and string decimals.
- Upgrade test as described above, and a downgrade test that expects `ErrPositionVersionUnsupported`.
- kill -9 (SIGKILL) test during a snapshot and during CDC after the switch to `strict`, verifying no loss and
  consistent types across the restart. The labs connector's existing crash test is the base.
- Acceptance suite stays green in both modes.

## Open questions for sign-off

1. Default for `types.decimal`: `string` (proposed) or `decimal` for Debezium parity.
2. Is halting on `BIGINT UNSIGNED` overflow acceptable as the default, or should `types.bigintUnsigned`
   default to `decimal`/`string` and accept the key-type change?
3. Staging: fresh pipelines `strict`, existing `legacy` for two minors (proposed), or flip everything at once
   given that `legacy` is a known corruption path.
4. Where the code lives at implementation time: the connector is moving from `conduitio-labs` to `ConduitIO`
   (certification decision 7), so the implementation PR should land after the transfer or on whichever repo
   owns `main` then.

## Related

- conduitio-labs/conduit-connector-mysql#191 (NULL and DECIMAL), #86 (unsigned BIGINT), #158, #187, #188
- `docs/catalog/v0.21-certification-gaps.md` (PR #3034), section 1
- `docs/design-documents/20260724-snapshot-cdc-position-handoff.md` in the MySQL connector repo (position
  envelope versioning)
- Invariants 2 and 6 in `CLAUDE.md`
