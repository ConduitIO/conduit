# MySQL source type semantics: NULL, DECIMAL and unsigned BIGINT

## Summary

The MySQL source (`ConduitIO/conduit-connector-mysql`, formerly `conduitio-labs`) silently changes data on
the way out: SQL `NULL` becomes the Go zero value of the column type, `DECIMAL` becomes a `float64`, and
`BIGINT UNSIGNED` above 2^63-1 is silently corrupted in the snapshot path and halts with an unstable encoder
error in the CDC path. These are invariant 6 violations (mysql#191, mysql#86). This document defines the
target semantics, how the type configuration is pinned in the position, and how existing pipelines migrate.
In short:

- Nullable columns become Avro unions `["null", T]` with default `null`, and `NULL` is emitted as nil.
- `DECIMAL(p,s)` is emitted losslessly, by default as a string, optionally as an Avro `decimal` logical type.
  The lossy `double` mapping stays available only as an explicit opt-in.
- `BIGINT UNSIGNED` stays `long`, but a value above 2^63-1 halts the pipeline with a stable error instead of
  being mangled. An opt-in maps the column to `decimal(20,0)` or string.
- The whole resolved type configuration (mode, decimal, bigintUnsigned) is stamped in the position. A restart
  with a different configuration is refused unless the operator names the stamp they are replacing.
- `legacy` keeps today's schema shapes so existing consumers keep working, but it no longer keeps the
  unsigned corruption: it halts on overflow too.
- Nothing infers a mode from "there is no position". A mode is either stamped in the position or set
  explicitly; a new pipeline that sets none gets a warning in N and is refused in N+1.
- Strict snapshot positions carry the key as exact text and resume with typed binds (integers) or an explicit
  `CAST(? AS DECIMAL(p,s))`. Spiked on MySQL 8.0.39 and 8.4.11, including one form that looks right and is not.

This is a payload contract change and Tier 1. It needs sign-off before code.

## Context

All line references are to `ConduitIO/conduit-connector-mysql` at `22f8da3` (`main`, 2026-10) unless a path
says otherwise. Behavior statements marked "observed" were produced by running that commit against MySQL
8.0.39 in a throwaway container (see Evidence).

How the source produces records today:

- Per table, `schemaMapper` builds an Avro record schema from the column list and registers it through the
  SDK schema service as `<table>_payload` and `<table>_key` (`schema.go:233-281`). Every field is a bare
  primitive (`schema.go:218`); there are no unions, no logical types, no field defaults and no custom
  properties. The text registered is `recordSchema.String()` (`schema.go:270`), which is hamba/avro's
  canonical form. The canonical form drops field defaults and custom properties (see Schema registration).
- Snapshot reads use `database/sql` column types (`sqlxRowsToAvroCol`, `schema.go:106`). CDC uses the
  `go-mysql` canal table schema (`mysqlSchemaToAvroCol`, `schema.go:170`). The two paths have separate type
  maps (`schema.go:52`, `schema.go:132`) that must agree, which is why `formatValue` has per-type branches
  that normalize driver differences. CDC builds a new schema mapper per row event (`cdc_iterator.go:395-396`)
  and re-registers the schema each time; the registry dedupes by schema text.
- `formatValue` runs for every value in both paths. Its first step is
  `if value == nil { return defaultValueForType(t.Type) }` (`schema.go:304-306`). The comment says this
  avoids modelling nullability in the schema. Observed: `NULL` columns come out as `0` and `""` in snapshot
  and in CDC.
- `DECIMAL` and `NUMERIC` map to Avro `double` (`schema.go:67-68`, `:136`); `formatValue` runs `ParseFloat` on
  the driver text (`schema.go:390-406`). Observed: `DECIMAL(30,10) 12345678901234567890.1234567891` comes
  out as `float64 1.2345678901234567e+19` in both paths. The text handed to `formatValue` is exact in both
  paths: snapshot gets `[]byte` (`1.50`, `-0.005`), CDC gets `string` (`1.50`, `0.00`, 65-digit values with
  scale 30 preserved), so a `string` mapping is lossless in both.
- `UNSIGNED BIGINT` maps to `long` (`schema.go:64`, `:161`). The two paths then diverge:
  - Snapshot: the driver returns a `[]byte` of ASCII digits for a `BIGINT UNSIGNED` above `MaxInt64`
    (`go-sql-driver/mysql@v1.10.1/packets.go:1347-1348`). `formatValue` treats every `[]uint8` in the `long`
    branch as a BIT value and folds the bytes as a big-endian integer (`schema.go:365-377`). The result is a
    valid but wrong `int64`. Observed: `18446744073709551615` is emitted as `3474867090068222261`, with no
    log line. This is silent corruption (it is the finding in mysql#86).
  - CDC: canal converts every unsigned `BIGINT` to `uint64` (`go-mysql@v1.14.0/canal/rows.go:46-86`).
    `formatValue` logs a warning and returns the raw `uint64` above `MaxInt64` (`schema.go:381-389`). The
    SDK's Avro encoder rejects `uint64`. Observed: `ReadN` returns `unable to encode record 0: ... ub: avro:
    uint64 is unsupported for Avro long`. The pipeline stops, with an error that has no stable code and no
    remedy. This is a halt, not corruption, but not a designed one.
  - Smaller unsigned types are correct in both paths.
- Integer range warnings in the `int` branch (`schema.go:330,341,351,357`) log and return the raw value. They
  are reachable only for types the maps already widen to `long`, so they are close to dead code today.
- The MySQL destination takes the structured payload and writes values with `ExecContext`; a nil value
  becomes SQL `NULL` (verified in the destination inventory below). Today it never sees one from this
  source, so the zero value is written to the target and the corruption propagates.
- The position envelope already has a `version` field (`common/position.go:38,53`). `ParseSDKPosition`
  refuses a position with a version higher than the build supports (`common/position.go:113-118`), so a
  downgrade fails loudly. Both position constructors hard-code `CurrentPositionVersion`
  (`common/position.go:72,170`). Positions are built at `snapshot_iterator.go:296` and `cdc_iterator.go:420`.
- Nothing in the connector checks `binlog_format` or `binlog_row_image` (grep of `*.go`). Observed: a writer
  session with `binlog_row_image=MINIMAL` makes today's source emit `id: 0` in the after image of an UPDATE
  (the primary key is not in a MINIMAL after image) and zero values for every column the UPDATE did not
  touch. `NOBLOB` empties unchanged `BLOB` columns. `go-mysql` leaves columns that are absent from the row
  image as nil (`replication/row_event.go:1221-1224`), which `formatValue` then coerces to zero values.
- Related, not changed here: temporal columns are strings, `BIT` is `fixed[8]` (the N in `BIT(N)` is not
  available from the driver).

Debezium MySQL, the migration reference, handles these as follows: `NULL` is a nullable (optional) field
and an actual null; `decimal.handling.mode` is `precise` (Connect `Decimal` logical type, bytes with scale)
by default, with `string` and `double` available; `bigint.unsigned.handling.mode` is `long` by default
(overflows for values above 2^63-1) with `precise` (a `Decimal` with scale 0) as the alternative.

### Evidence

Three levels are used in this document, and each claim is one of them.

- **Observed.** Run against the unmodified connector at `22f8da3` and MySQL 8.0.39 (Docker, loopback,
  ephemeral port), once with the raw `Source` as the connector's own tests use it, once through
  `sdk.SourceWithMiddleware` plus the SDK destination decoder. This covers NULL, DECIMAL, unsigned
  `BIGINT`, DECIMAL text exactness, row-image behavior, `information_schema` nullability and permissions,
  and snapshot resume with decimal and large-integer primary keys.
- **Spiked.** Run with the pinned libraries (connector SDK v0.14.2, conduit-commons v0.7.0, hamba/avro
  v2.31.0, in-memory SDK schema service, and the built-in Conduit schema registry
  `conduit-schema-registry` v0.2.6 in process): union and decimal round trips, custom schema properties,
  canonical versus full schema text, processor re-extraction (commons v0.7.0, the version the engine pins),
  and the strict-mode snapshot resume predicate against MySQL 8.0.39. Round 1 ran the schema spikes on SDK
  v0.14.1 and commons v0.6.0; round 2 re-ran them on the versions above and the output is identical (see
  Avro shapes). The resume-predicate spike is new in round 2.
- **Read from code, not run.** The engine's gRPC schema path, and the behavior of destinations and
  processors on a mid-stream type change (the consumer inventory below). The inventory says so per row.

mysql#191 and #86 carry the observed outputs from the certification probes, and the gap analysis
(PR #3034) cites them as "confirmed with probes". This revision re-ran them. One refinement: #3034 says
`BIGINT UNSIGNED` above 2^63 is "silently corrupted"; that is true for the snapshot path only. The CDC path
halts with the unstable encoder error described above.

The spike code (a Go module and a copy of the connector with a test-DSN change) is throwaway and is not
committed with this document; every result above is reproducible from the versions named.

MySQL 8.4.11 (round 3, Docker, ephemeral port): the resume-predicate matrix, the typed binds, the
`k+0` evidence and the binlog variable checks were re-run and the output is identical to 8.0.39, apart
from the version string. A reviewer ran the same binlog and typed-bind checks on 8.4.11 independently.
The connector itself, canal's behavior and the full schema spikes were not re-run on 8.4.

Not verified anywhere: MariaDB, MySQL 5.7, Aurora or RDS parameter-group behavior, a
Confluent-compatible external registry, primary-key types other than the four spiked (`VARCHAR`, temporal,
`BINARY`) on the resume path, and an end-to-end Conduit engine run with the new schemas.

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

Four new connector settings (names are proposals, to be fixed at implementation and added to
`connector.yaml`). Naming rule: settings are camelCase after the dot, as the existing `snapshot.fetchSize` and
`cdc.disableLogs` are (`connector.yaml:169,179`), so `types.bigintUnsigned`. Position JSON keys are
snake_case, as the existing `snapshot_position` and `last_read` are (`common/position.go:54,133`), so the
stamp key is `bigint_unsigned`. They name the same thing; this document uses each only in its own context:

| Setting | Values | Default |
| --- | --- | --- |
| `types.mode` | `strict`, `legacy` | the stamped mode if the position has a stamp. With no position, unset means `legacy` plus a warning in N and a refusal from N+1 (see Transition table) |
| `types.decimal` | `string`, `decimal`, `double` | the stamped value if stamped; otherwise `string` (strict) |
| `types.bigintUnsigned` | `long`, `decimal`, `string` | the stamped value if stamped; otherwise `long` |
| `types.acceptChangeFrom` | a stamp name, or `legacy` | unset |

`legacy` fixes `types.decimal=double` and `types.bigintUnsigned=long`; setting either to anything else with
`types.mode=legacy` is a configuration error at `Open`. `double` in `strict` is allowed but logs a warning
naming the columns and the precision loss.

### What `legacy` is

`legacy` keeps today's schema shapes so downstream destinations and consumers built on them keep working:
bare primitive fields, `NULL` as the zero value, `DECIMAL` as `double`. It exists for migration only.

Recommended lifecycle (recommended, pending DeVaris; not yet a decision):

- Existing positioned pipelines keep `legacy` for two minors, N and N+1. `legacy` is removed in N+2.
- Every start in `legacy` logs one warning per affected column (`table.column` and which loss applies:
  `NULL` becomes zero, `DECIMAL` becomes `double`), not one aggregate line, plus one line naming the removal
  release and its date. The date is taken from the release train when N is cut, written into the warning
  text and the N release notes, and not moved without a new ADR.
- In N, a new pipeline (no position) with no explicit `types.mode` runs as `legacy` and logs the same
  warnings plus "set `types.mode=strict`". In N+1 that pipeline is refused at `Open` with
  `MYSQL_TYPES_MODE_REQUIRED` and a one-line instruction ("set `types.mode=strict`"). From N+1, `types.mode`
  has no default for a no-position pipeline. This is stricter than flipping the default to `strict`, and it is
  the only form that does not guess for an idle old pipeline (see the Transition table notes).
- In N+2, `types.mode=legacy` is a configuration error. A `legacy`-stamped position in the `cdc` phase is
  refused with a message that points to the switch procedure (row 6, which stays available in N+2). A
  `legacy`-stamped position still in the `snapshot` phase has no switch (row 4) and is refused with
  `MYSQL_LEGACY_SNAPSHOT_REMOVED`, whose one explicit path is: create a new connector instance with
  `types.mode=strict` (re-snapshot; downstream sees duplicates, at-least-once holds). Operators are told
  before N+2 so they can avoid the re-snapshot: see the release-note callout (snapshot-phase pipelines)
  under Upgrade and rollback.

`legacy` does not keep the unsigned corruption. The two `BIGINT UNSIGNED` branches in `formatValue`
(snapshot `[]uint8` fold, CDC raw `uint64`) halt with `MYSQL_UNSIGNED_OVERFLOW` in both modes; the BIT fold is
removed from the `long` branch (BIT columns map to `fixed`, so the branch has no legitimate use). A
`legacy` pipeline that today emits wrong values for `BIGINT UNSIGNED` above 2^63-1 will stop after the
upgrade; that is the point and is stated in the release notes. This change ships as a bug fix in N and is
not gated on `types.mode`.

The halt must not strand the pipeline. `legacy` fixes `bigintUnsigned=long`, so the remedy "set
`types.bigintUnsigned`" means switching to `strict`, and a switch is refused in the snapshot phase (row 4).
The error code and remedy therefore depend on where the pipeline is. "Phase" in this table is the Transition
table's definition, applied to the position the restart will see: `snapshot` when a `snapshot_position` is
present, **including a snapshot that has finished but whose first CDC record has not been acked**; `cdc`
only once a `cdc_position` has been persisted. The remedy is applied by editing the configuration and
restarting, so the table must be keyed on what the next `Open` will read, not on which iterator is running.
Each remedy is valid in its row:

| State when the value is hit | Error | Remedy in the message |
| --- | --- | --- |
| `strict`, any phase | `MYSQL_UNSIGNED_OVERFLOW` | Set `types.bigintUnsigned` to `decimal` or `string`. In `cdc` phase that is a stamp change and needs the token (rows 10-11); the message names it. In `snapshot` phase it is refused (row 9), and the message says to create a new connector instance |
| `legacy`, phase `none` | `MYSQL_UNSIGNED_OVERFLOW` | Set `types.mode=strict` and `types.bigintUnsigned` (row 2) |
| `legacy`, phase `cdc` | `MYSQL_UNSIGNED_OVERFLOW` | `types.mode=strict`, `types.bigintUnsigned`, `types.acceptChangeFrom=legacy` (row 6) |
| `legacy`, phase `snapshot` | `MYSQL_UNSIGNED_OVERFLOW_LEGACY_SNAPSHOT` | No in-place switch exists. Create a new connector instance with `types.mode=strict` and `types.bigintUnsigned` (re-snapshot; downstream sees duplicates, at-least-once holds), or exclude the table, or fix the offending rows |

The connector cannot tell whether a record it emitted has been persisted as a position, so it keys on the
conservative definition: the phase of the position carried by the last record it emitted, or of the position
read at `Open` when it has emitted none. Consequences, each pinned by a test:

- A `legacy` source whose snapshot iterator is active and has emitted a record is in `snapshot`, and the
  message adds "if no record of this connector has been acked, row 2 applies".
- **Snapshot finished, no CDC ack yet.** The last emitted position is still a `snapshot_position`, so an
  overflow hit on the first CDC record (or any CDC record before one is acked) is `snapshot` phase, not
  `cdc`: `legacy` gets `MYSQL_UNSIGNED_OVERFLOW_LEGACY_SNAPSHOT`, and `strict` gets the "create a new
  connector instance" remedy. Naming the `cdc` remedy here would send the operator into row 6 or rows 10-11,
  which the restart then refuses (rows 4 and 9), so the pipeline would be stranded.
- A `strict` source in the same state is likewise `snapshot` for the `strict` row of the table.

Why the snapshot phase gets a distinct error and no in-place switch, rather than allowing a switch at a key
boundary: the `legacy` snapshot position stores the last key in the v1 form (`float64` or base64 after JSON;
see Keys and snapshot positions), which already resumes incorrectly for the key types where this overflow is
likeliest (`BIGINT UNSIGNED`, large `BIGINT`, `DECIMAL`; mysql#204). A boundary switch would have to
reinterpret that stored key, which means shipping a repair for the defective v1 form inside the switch
path. That is more state-format logic than the problem is worth, and it is the part that most needs the
upgrade test. The cost is a re-snapshot for a pipeline that hits an overflow during a `legacy` snapshot.
That pipeline was emitting corrupted values for that column before this change, so the re-snapshot output
is the correct one. Revisit if sign-off finds large `legacy` snapshots common.

What `legacy` still gets wrong, by design, because the shape forces it: `NULL` is a zero value, and `DECIMAL`
loses precision above about 15 significant digits. These are named in the startup warning, with the affected
tables and columns. Whether to treat those two as more corruption than a shape contract can excuse is an
open question (see Open questions).

The row-image check below applies to both modes.

### The type stamp in the position

Every position the connector writes in `strict` mode carries a `types` object:

```json
{"version": 2, "types": {"mode": "strict", "decimal": "string", "bigint_unsigned": "long"},
 "cdc_position": {...}}
```

The stamp is the whole resolved type configuration, not only the mode, because `decimal` and
`bigintUnsigned` change the Avro type of existing columns (and of keys) exactly as `mode` does. The stamp has
a name made of the three values, `<mode>/<decimal>/<bigintUnsigned>` (for example `strict/string/long`). `legacy` has the name
`legacy`.

- `legacy` writes the v1 envelope, unchanged. An unstamped position is a legacy position. This keeps
  rollback from N to N-1 possible for every pipeline that has not opted in, and it means nothing is ever
  stamped `legacy`.
- `strict` writes envelope version 2 on every snapshot and CDC position. `ParseSDKPosition` raises its
  supported maximum to 2 (`common/position.go:38,113`); N-1 refuses a v2 position with
  `ErrPositionVersionUnsupported`. That is the one-way door, and only pipelines that opted into `strict`
  cross it.
- Because the stamp rides on every position, the switch point between two configurations is exactly one
  record: the first one emitted under the new configuration.
- The position constructors (`common/position.go:71,169`) take the stamp as an argument; the two call
  sites (`snapshot_iterator.go:296`, `cdc_iterator.go:420`) pass it from the source.

### Transition table

At `Open`, before any iterator is built (`source.go:158-168` is where the position is parsed today), the
connector resolves the effective configuration and checks it against the position. Terms:

- **Stamp**: `legacy` (position has no `types`) or a strict stamp name.
- **Phase**: `none` (no position), `snapshot` (a `snapshot_position` is present, including a snapshot that
  has finished but whose first CDC record has not been acked), `cdc` (a `cdc_position` is present).
- **Explicit config**: settings the operator actually set. Unset settings are filled from the stamp when
  there is one, so a default that changes between releases never causes a mismatch by itself.

| # | Stamp | Phase | Explicit config | Result |
| --- | --- | --- | --- | --- |
| 1 | none | none | `types.mode` unset | N: `legacy`, with the per-column warnings and "set `types.mode=strict`". N+1 and later: refuse `MYSQL_TYPES_MODE_REQUIRED` (one-line instruction). Any other `types.*` setting without `types.mode` is a configuration error |
| 2 | none | none | `types.mode` set | Use it. Validate combinations. `strict` stamps its first position. `legacy` is a configuration error from N+2 |
| 3 | `legacy` | `snapshot` or `cdc` | unset, or `legacy` | N and N+1: `legacy`, positions stay v1. N+2 and later: `cdc` refused with the instruction to use row 6; `snapshot` refused with `MYSQL_LEGACY_SNAPSHOT_REMOVED` (new connector instance with `strict`, re-snapshot) |
| 4 | `legacy` | `snapshot` | `strict` | Refuse: `MYSQL_TYPES_SWITCH_IN_SNAPSHOT`. No override (reason under What `legacy` is). Message says to create a new connector instance |
| 5 | `legacy` | `cdc` | `strict`, no token | Refuse: `MYSQL_TYPES_STAMP_MISMATCH`, naming the stamp (`legacy`) and the setting to add |
| 6 | `legacy` | `cdc` | `strict` and `types.acceptChangeFrom=legacy` | Switch. First record emitted carries the strict stamp |
| 7 | strict `S` | `snapshot` or `cdc` | unset | Use `S` |
| 8 | strict `S` | any | explicit values all equal to `S` | Use `S` |
| 9 | strict `S` | `snapshot` | differs from `S` | Refuse: `MYSQL_TYPES_SWITCH_IN_SNAPSHOT`. No override |
| 10 | strict `S` | `cdc` | differs, no token | Refuse: `MYSQL_TYPES_STAMP_MISMATCH` |
| 11 | strict `S` | `cdc` | differs and `acceptChangeFrom` equals the name of `S` | Switch (including back to `legacy` until N+2, logged at error level). First record emitted carries the new stamp, or v1 for `legacy` |
| 12 | strict `S` | `cdc` | differs and `acceptChangeFrom` names another stamp | Refuse: `MYSQL_TYPES_STAMP_MISMATCH` (stale token) |

Notes on the table:

- **"No position" is not a signal.** A pipeline that has never acked a record has no position whether it is
  new or an old one that was idle, or crashed before its first ack after delivering records downstream with
  legacy shapes. The connector cannot tell these apart, so row 1 never picks `strict` for it: it runs `legacy` with a
  warning in N and is refused in N+1, so the operator decides while the old shape is still available. The alternatives considered were probing the schema
  registry for earlier versions of `<table>_payload` (rejected: `schema.Get` does not apply the schema
  context prefix that `schema.Create` does, `connector-sdk@v0.14.1/schema/schema.go:45-77`, so the probe
  would depend on an unexported naming rule,) and a connector-side
  state file (rejected: a second durable store with its own crash semantics for one bit). A new pipeline
  that wants `strict` sets it, and the templates and scaffolding set it. Idle old pipelines are the case to
  warn about: see the release-note callout under Upgrade and rollback.
- **The switch needs a token because it changes record shapes mid-stream.** `acceptChangeFrom` names the
  stamp being replaced; it is read from the refusal message. Once the first position with the new stamp is
  persisted, the token matches nothing, so a leftover token cannot approve the next change.
- **`snapshot` has no override.** Switching in the middle of a snapshot mixes two schemas inside one table
  scan. The operator finishes the snapshot (the phase becomes `cdc` at the first acked CDC record, so an idle
  source needs one change event) or recreates the connector to re-snapshot. The engine has no
  reset-position primitive (declined in `docs/design-documents/20260715-pipeline-repair-api-semantics.md`,
  Alternative A), so "reset" here means a new connector instance.
- **Crash at the switch.** Rows 6 and 11: if the process dies after emitting records under the new
  configuration and before they are acked, the last persisted position still has the old stamp, and the
  restart goes through row 5 or 10 again. The token must stay in the configuration until the log line
  `types stamp changed from <old> to <new>` has been followed by an acked record. If the operator removes it
  early the restart refuses; it never mixes silently. Covered by the kill -9 test.
- After `legacy` is removed (N+2), row 3 becomes a refusal. In `cdc` phase it points to row 6. In `snapshot`
  phase there is no in-place switch (the reason under What `legacy` is applies unchanged), so the path is a
  new connector instance with `strict`. Keeping a hidden `legacy` reader alive for snapshot-phase positions
  was rejected: it would keep the removed mode, and the v1 key defect (mysql#204), in the tree for the one
  case least able to use it. The cost is a re-snapshot, which is why the callout asks operators to get out of
  the snapshot phase before N+2.
- Stamps list every setting explicitly, so a later release that adds a setting can tell "unset because the
  stamp predates it" from an operator choice, and can define the old behavior as that setting's value.

### NULL

In `strict` mode a column whose `information_schema.columns.IS_NULLABLE` is `YES` becomes the union
`["null", T]` with default `null`; `NOT NULL` columns stay bare `T`. `formatValue` returns nil for nil and
`defaultValueForType` is deleted from the strict path.

Nullability is read once per table at schema creation from `information_schema.columns`, for both snapshot
and CDC, because canal's table schema does not expose it (`go-mysql@v1.14.0/schema/schema.go:44-59`,
`TableColumn` has no nullable field; confirmed). Both paths then build the schema from the same source of
truth, which also removes one class of snapshot/CDC drift. Observed: the query returns `IS_NULLABLE` per
column as expected.

- **A missing row is an error, not "NOT NULL".** `information_schema.columns` shows only columns the user
  has some privilege on, and it returns zero rows rather than an error for a table the user cannot read
  (observed with a user that has `SELECT` on a different table). So `Open` fails with
  `MYSQL_NULLABILITY_UNAVAILABLE` when any column canal reports for a captured table has no row in the
  result, and also when the query fails. There is no fallback to "assume NOT NULL", which would reintroduce
  coercion.
- **DDL.** Canal swaps its table schema after a DDL event. The nullability map is cached per table and keyed
  by the canal column list, and is re-read when the column list changes. Out of scope otherwise (see Failure
  modes).

#### Row images

A NULL that is really "this column was not in the row image" is the dangerous case for a design that emits
real nulls. `go-mysql` leaves columns that are outside the row image as nil (`replication/row_event.go:1221-1224`),
records them in `RowsEvent.SkippedColumns`, and `canal` drops that field when it builds its own
`RowsEvent` (`canal/sync.go:295`, `canal/rows.go:18-29`). The connector therefore cannot see which nils are
real.

- **Open check.** `Open` reads `@@GLOBAL.binlog_format`, `@@GLOBAL.binlog_row_image` and
  `@@GLOBAL.binlog_row_value_options` and fails with a stable error per variable, each naming the value and
  the fix (the `SET GLOBAL` statement, or the parameter group on managed services):
  - `binlog_row_image` other than `FULL`: `MYSQL_BINLOG_ROW_IMAGE`.
  - `binlog_format` other than `ROW`: `MYSQL_BINLOG_FORMAT`. The connector's contract is row events.
    Unverified: what canal emits under `STATEMENT` or `MIXED`; the check turns whatever it is into a named
    error.
  - `binlog_row_value_options` non-empty: `MYSQL_BINLOG_ROW_VALUE_OPTIONS`. `PARTIAL_JSON` makes the server
    log `JSON` updates as partial diffs, and a row event then no longer carries the full column value. It
    matters for `JSON` columns only, and the check applies to all tables because it is one variable. Observed
    on 8.0.39: the defaults are `ROW`, `FULL` and empty, and `SET GLOBAL binlog_row_value_options =
    'PARTIAL_JSON'` is accepted and read back by a user with only `SELECT` and the two replication
    privileges. Not observed: a binlog event logged with a partial JSON value, so the effect on canal is
    from the MySQL documentation, not run.
  - A variable may not exist (5.7 has no `binlog_row_value_options`; MariaDB differs). The check uses
    `SHOW GLOBAL VARIABLES LIKE '<name>'`, which returns zero rows for an unknown name without an error
    (observed on 8.0.39), and treats an absent variable as unset. Re-run on 8.4.11 in round 3 with the same results (defaults `ROW`, `FULL`, empty; `PARTIAL_JSON` and `MINIMAL` accepted and read back; absent name returns zero rows). 5.7 and MariaDB are unverified. `MINIMAL` and `NOBLOB` are both refused: observed, `MINIMAL` omits
  the primary key from the after image and every unchanged column, and `NOBLOB` omits unchanged `BLOB` and
  `TEXT` columns. A user with only `SELECT` and the two replication privileges can read the global variables
  (observed on 8.0.39). The check applies to `legacy` as well as `strict`, because the corruption exists in both
  today; this halts pipelines that currently run on a non-`FULL` server, which is deliberate and flagged
  under Open questions.
- **Runtime net.** In `strict`, a nil for a `NOT NULL` column halts with `MYSQL_NULL_IN_NOT_NULL` (table,
  column, config path `binlog_row_image`, fix text), instead of being passed through. That catches the
  primary key and every other `NOT NULL` column when a writer session overrides the image.
- **Residual risk, stated.** `binlog_row_image` is also a session variable, so a writer session that sets
  `MINIMAL` while the global stays `FULL` passes the Open check (observed). For a nullable column that the
  statement did not touch, the nil is indistinguishable from `NULL` at the connector, and `strict` would
  emit `NULL` for a value that exists. This cannot be closed in the connector with the current `canal`
  API. The proposal is to ship with the Open check and the runtime net and document the residual risk, and
  to open an upstream change to expose `SkippedColumns` on `canal.RowsEvent`, after which any skipped
  column halts. A decision for sign-off.

Keys: key columns are `NOT NULL` for primary keys and stay bare `T` in the key schema. A configured
`sortingColumn` that is nullable also stays bare `T`; a NULL in a key column halts with
`MYSQL_NULL_IN_NOT_NULL`.

### Avro shapes and what consumers see

Spiked with the real SDK middleware (`SourceWithEncoding` encode, `DestinationWithSchemaExtraction`
decode), connector SDK v0.14.2, conduit-commons v0.7.0, hamba/avro v2.31.0. Round 1 ran this on SDK v0.14.1
and commons v0.6.0. Round 2 re-ran the same test file with only those two versions bumped, and the output is
identical line for line: the schemas, the input types, the decoded types, the encoder errors for `uint64`,
`string` into `long` and nil in a bare field, and the property and registry round trips. No decode shape
changed. The shapes below hold on both:

- The connector hands the SDK plain Go values: `nil` for a null, `T` for a present nullable value. The
  encoder accepts both for `["null", T]` fields, including `*big.Rat` for a decimal inside a union and
  `[]byte`, `int32`, `int64`, `float64`, `string`. No wrapper `map[string]any{"string": v}` is needed on the
  way in (the `BeforeMarshal` hook in `conduit-commons/schema/avro/union.go` only rewrites unions nested in
  maps and arrays).
- On decode the null-union hook (`union.go`, `afterUnmarshalNullUnionSubstitutions`) removes the wrapper.
  Observed decoded values, field by field: null -> `nil`; present nullable string -> `string`; nullable
  long -> `int64`; nullable int -> `int`; nullable double -> `float64`; nullable bytes -> `[]uint8`;
  nullable `decimal(10,2)` -> `*big.Rat`; bare `decimal(20,0)` -> `*big.Rat`. The union form and the bare
  form decode to the same Go types. So the open question in the previous revision (wrapper versus plain
  value) is answered for every destination and processor that decodes through the SDK or `commons`
  `Schema.Unmarshal`: plain value, no unwrap step in the connector.
- Two things the previous revision did not say and a consumer should know: Avro `int` decodes to Go `int`,
  not `int32`, and a `decimal` decodes to `*big.Rat`. `*big.Rat` is not JSON-friendly (see the consumer
  inventory), which is the reason `string` is the default.
- A `nil` in a bare (non-union) field fails encoding in the SDK
  (`avro: *avro.null is unsupported for Avro long`). With the nullability map built correctly this cannot
  happen for a nullable column; for a `NOT NULL` column it is the `MYSQL_NULL_IN_NOT_NULL` case above, and
  the connector raises its own stable error before the encoder does.
- Non-SDK consumers that decode with `hamba/avro` directly get hamba's own union representation
  (`map[string]any{"string": v}`); that is hamba behavior and out of the connector's control.

### Schema registration: properties and defaults

The connector registers `recordSchema.String()` (`schema.go:270`). Spiked: `String()` is hamba's canonical
form and drops both field defaults and custom properties, so a `"mysql.type": "decimal(30,10)"` property
would be lost before it reaches the registry, and `"default": null` on the union fields would be lost too.
`json.Marshal(recordSchema)` (which calls `MarshalJSON`) keeps both and is deterministic (property keys are
sorted).

- **The connector registers `json.Marshal(recordSchema)`, not `String()`.** That is a change to
  `createSchema`, and it changes the registered text for every table including `legacy` ones (no
  properties in `legacy`, but the decimal scale form and field order stay the same). To avoid a gratuitous
  new schema version for `legacy` pipelines on upgrade, `legacy` keeps `String()`.
- Spiked with the property on the field: the SDK in-memory service returned the registered bytes unchanged;
  the built-in registry (`conduit-schema-registry` v0.2.6, in process) returned the identical text; parsing
  the returned bytes with hamba gives `field.Prop("mysql.type") == "decimal(30,10)"` for a plain string
  field, a nullable-union field and a decimal-logical-type field. A property on a primitive type schema also
  survives, but the property goes on the field, which is the form tested on all three shapes. Encoding and
  decoding with properties present is unaffected.
- The engine's own schema service passes the text through verbatim
  (`pkg/plugin/connector/connutils/schema.go:59-70` hands `req.Bytes` to the registry unchanged), so no engine
  step is expected to rewrite it. That path was read, not run end to end.
- The property survives storage only. Any processor that re-encodes the record re-derives the schema from the
  Go values and drops the property and the defaults (see the consumer inventory), and an external
  Confluent-compatible registry might normalize the text (not tested). So `mysql.type` is advisory metadata
  for schema readers, not a contract, and no feature may depend on it.

### DECIMAL and NUMERIC

- `string` (default): Avro `string`, the exact decimal text from the server (`12345678901234567890.1234567891`),
  no exponent, scale preserved (`1.50` stays `1.50`; `0.00` stays `0.00`). Readable in JSON and safe through
  any destination. The field carries the Avro property `"mysql.type": "decimal(30,10)"` where the registry
  keeps it (see above), so the precision and scale are recoverable from the schema.
- `decimal`: Avro `bytes` with `logicalType: decimal`, `precision` and `scale` from the column (MySQL's
  maximum is 65). The Go value is a `*big.Rat`, which decodes through the SDK (spiked) but is rejected or
  mangled by several consumers (see the consumer inventory). This mode is for Avro-native consumers. Matches
  Debezium `precise`.
- `double`: today's mapping, explicit and warned.

Why `string` is the default and not `decimal` as in Debezium: records in Conduit routinely pass through
JSON-oriented processors and destinations, where a `big.Rat` or a base64 `bytes` value is a worse surprise
than a string. Parity with Debezium's default is a documented difference, and `types.decimal=decimal` gives
it. This is a judgment call for sign-off.

### BIGINT UNSIGNED

- `long` (default): schema unchanged, so keys and consumers are not disturbed. A value above 2^63-1 stops
  the source with error `MYSQL_UNSIGNED_OVERFLOW` (name proposed), carrying the table, column, and the config
  path `types.bigintUnsigned`, and the fix "set `types.bigintUnsigned` to `decimal` or `string` for this
  connector". The source does not skip, truncate or wrap the value. In both modes, in both paths. Snapshot
  parses the driver's digit string with `ParseUint`; CDC compares the `uint64`.
- `decimal`: `decimal(20,0)` logical type on `bytes`.
- `string`: Avro `string`.

This setting is connector-wide, not per column, to keep the schema story simple. Per-column overrides are
not proposed (no speculative generality); a mixed case is handled by the operator choosing the wider mode.

Smaller unsigned types are unchanged and already correct. `strict` mode adds an assertion that every
converted integer is in range and halts with the same error class otherwise, replacing today's
log-and-return-unconverted branches.

### Keys and snapshot positions

This section exists because the test-plan item "decimal primary key" found a data-loss defect that the type
change must not inherit and that interacts with it.

The snapshot position stores the last key read as the raw driver value, before `formatValue`
(`fetch_worker.go:175-180`, stored at `:274`). That value is JSON-encoded into the position, and decoded on
restart into an `any` (so numbers become `float64`, byte slices become base64 strings), then used as the bind
parameter of `WHERE key > ?` (`fetch_worker.go:208-222`). Observed on the unmodified connector, with
`snapshot.fetchSize=2`, acking part of the snapshot and restarting from the last acked position:

| Primary key | Stored `last_read` | What the restart emitted | Effect |
| --- | --- | --- | --- |
| `DECIMAL(10,2)`, positive keys | `"MTAwLjAw"` style base64 of the digits | all keys again, from the beginning | duplicates (at-least-once holds) |
| `DECIMAL(10,2)`, negative keys, acked `-300`, `-200` | base64 | only `5`; `-100`, `-50`, `-1` never emitted | **loss** |
| `BIGINT`, keys around 2^53, acked up to `9007199254740995` | JSON number | nothing; `9007199254740996` never emitted | **loss** |
| `BIGINT UNSIGNED`, acked up to `9223372036854775807` | JSON number | `3474867090068222260`, `3474867090068222261`; `9223372036854775808` never emitted | **loss and corruption** |

Cause: the key value round-trips through JSON as a `float64` or a base64 string, so the resume predicate is
wrong for decimals and for integers above 2^53. This is an invariant 2 and 3 defect independent of this
design. It is present in `legacy` today and is filed as ConduitIO/conduit-connector-mysql#204 ("Snapshot
resume loses rows for DECIMAL, large BIGINT and BIGINT UNSIGNED keys"), a separate sev-0 candidate. Within
this design `strict` does not inherit it:

- `strict` serializes `last_read` and `snapshot_end` as exact text: the decimal text for `DECIMAL` keys and a
  decimal string (not a JSON number) for every integer key. It reads them back with the column's type, taken
  from `information_schema` (`DATA_TYPE`, `COLUMN_TYPE` for signedness, `NUMERIC_PRECISION`,
  `NUMERIC_SCALE`), not into `any`. This is a position-format change and therefore rides on the v2 envelope
  and its upgrade test.
- **Resume predicate, as specified by the spike below.** The stored text is never bound as an untyped string:
  - `BIGINT`: `ParseInt`, bind an `int64`.
  - `BIGINT UNSIGNED`: `ParseUint`, bind a `uint64`. **Never** `CAST(? AS UNSIGNED)` over a prepared string
    parameter.
  - `DECIMAL(p,s)`: bind the exact text inside `CAST(? AS DECIMAL(p,s))`, with `p` and `s` from the column.
    `database/sql` has no typed decimal bind, so the cast carries the type.
  - The same rule applies to `snapshot_end` (`k <= ?`).
- A fix for `legacy` positions is not part of this document; it needs its own design because `legacy`
  positions are v1 and must stay readable by N-1. Tracked as ConduitIO/conduit-connector-mysql#204.
- Tests required before sign-off of the implementation are in the Test plan.

#### Strict-path resume spike

The concern: the strict position holds a string, and a string compared with a numeric column may be compared
as a `double`, which would reproduce the loss with a different cause. MySQL documents that a string against a
number is compared as floating point; the question is what happens when the other side is an indexed column.

Spiked on MySQL 8.0.39 (round 2) and re-run on 8.4.11 (round 3, identical results) with
`go-sql-driver/mysql` v1.10.1, with server-side prepared statements and with
`interpolateParams=true`, each also with the primary-key index excluded (`IGNORE INDEX (PRIMARY)`). Every key
in the table in turn was taken as "last acked", and `WHERE k > <last> AND k <= <max> ORDER BY k` was compared
with the expected remaining set. Keys: `DECIMAL(10,2)` `-300.00 -200.00 -100.00 -50.00 -1.00 5.00`;
`DECIMAL(30,10)` with `12345678901234567890.1234567891`, `...892`, `...893`, `0.0000000001` and a negative
20-digit value; `BIGINT` `-9007199254740993`, `-300`, `9007199254740991` through `9007199254740996` and
`2^63-1`; `BIGINT UNSIGNED` `1`, `9007199254740993`, `2^63-1`, `2^63`, `2^63+1`, `2^64-2`, `2^64-1`.

| Bind form | `DECIMAL(10,2)` | `DECIMAL(30,10)` | `BIGINT` | `BIGINT UNSIGNED` |
| --- | --- | --- | --- | --- |
| A. untyped string, `k > ?` | pass | pass | pass | pass |
| B. string in `CAST(? AS DECIMAL(p,s))`, `CAST(? AS SIGNED)`, `CAST(? AS UNSIGNED)` | pass | pass | pass | **fail with prepared statements**, count depends on predicate shape (below); pass with `interpolateParams=true` |
| C. typed `int64` / `uint64` | n/a | n/a | pass | pass |

Results are identical with and without the index hint. For the `BIGINT UNSIGNED` table (7 keys, so 6
resume points) the number of wrong resume points depends on the predicate shape (round 3 matrix, prepared
statements; every shape passes with `interpolateParams=true`):

| Predicate shape | Wrong resume points |
| --- | --- |
| `k > CAST(? AS UNSIGNED)` (lower bound only) | 3 of 6 |
| `k <= CAST(? AS UNSIGNED)` (upper bound only) | 3 of 6 |
| `k > CAST(? AS UNSIGNED) AND k <= CAST(? AS UNSIGNED)` | 6 of 6 |
| `k > CAST(? AS UNSIGNED) AND k <= ?` (string upper) | 3 of 6 |
| `k > ? AND k <= CAST(? AS UNSIGNED)` (string lower) | 6 of 6 |
| `k > ? AND k <= ?` (untyped strings, form A), or typed `uint64` | 0 of 6 |

The 6-of-6 figure in earlier revisions is the `CAST`/`CAST` shape, which is the one the resume query uses.
`SIGNED` and `DECIMAL` casts, and `BIGINT` and `DECIMAL` keys, passed in every shape. What they mean:

- **Form A passes, and is not safe to specify.** In these queries the left side is a bare column. The same
  server compares as a double when it is not: `SELECT 9007199254740993 > '9007199254740992'` returns 0, and
  `SELECT CAST(9007199254740993 AS DECIMAL(20,0)) > '9007199254740992'` returns 0, while
  `... > CAST('9007199254740992' AS DECIMAL(20,0))` returns 1. On a table holding the `BIGINT` key
  `9007199254740993`, the correct answer to "is the key greater than `9007199254740992`" is 1. Counting rows
  with `k > '9007199254740992'` returned 1 on both driver paths. With the key wrapped in an expression,
  `k+0 > ?` returned 1 on the prepared path and 0 with `interpolateParams=true`, and `k+0 >
  '9007199254740992'` as a literal returned 0. Only the prepared-path answer is correct, and which driver
  setting is on changes the answer, so the pass depends on the key being a bare column in the predicate and
  is not a property to rely on. Observed on 8.0.39 and 8.4.11; 5.7 and MariaDB are unverified. The design
  binds by type and does not rely on it.
- **`CAST(? AS UNSIGNED)` over a prepared string parameter is wrong.** With a prepared statement (driver
  and plain SQL `PREPARE`/`EXECUTE`), the cast of `'18446744073709551615'` returns `9223372036854775807`;
  the same cast over a string literal, or with `interpolateParams=true`, returns `18446744073709551615`. In
  the `CAST`/`CAST` shape every resume point returned rows only up to `2^63-1`, so a resume would lose every
  `BIGINT UNSIGNED` row above `2^63-1`; the other shapes in the table lose some of them.
  It is the form a reviewer would reach for first, so the test plan pins it.
- **The cast and typed forms keep the range plan.** `EXPLAIN` over 5000 rows, `BIGINT` and `DECIMAL(10,2)`:
  `type=range key=PRIMARY` for the bare form and the `CAST` form.

Conclusion: on 8.0.39 and 8.4.11 the strict path does not lose the rows that mysql#204 describes, provided it binds as
specified. The spike code and raw output are throwaway and not committed; the key sets above reproduce them.

### Unchanged on purpose

Temporal-as-string, `BIT` as `fixed[8]`, `FLOAT`/`DOUBLE`, `ENUM`/`SET`/`JSON` as string. They are not
silent-corruption cases in the findings, and changing them would widen the compatibility surface of this
change. They get their own item if needed.

### Invariant comment

The enforcement site (`formatValue` and the schema builder) gets `// Invariant 6: a value that cannot be
represented in the schema type halts with MYSQL_UNSIGNED_OVERFLOW; it is never coerced`, and a package doc
paragraph stating the mapping table.

## Consumers of a mid-stream type change

Registry compatibility is `NONE`: the Conduit schema registry client sets `CompatNone` on a subject it
creates (`pkg/schemaregistry/client.go:102`, in the `ConduitIO/conduit` repo), and the built-in registry
has no compatibility logic. A new schema under the same subject is therefore always accepted, and any
consumer can see `<table>_payload` change type under it. Each record carries its own subject and version, so
a consumer that resolves the schema per record decodes correctly; nothing in the engine compares versions or
rejects a record because the version changed (read from code). What matters is what each consumer does
with the new values. Everything below was read from code, not run, unless it says "observed" or "spiked".

### Destinations

None of the five destinations reads the payload schema subject or version itself; the only schema consumer is
the SDK `DestinationWithSchemaExtraction` middleware (default on), which decodes each record with the schema
named in that record's metadata (`connector-sdk@v0.14.1/destination_middleware.go:497-557`), so a version
change decodes correctly per record. None of them creates or alters the target schema from the Avro schema
(no `CREATE TABLE`, `ALTER TABLE`, index mapping or file schema is derived from it). What differs is what the
target does with the new Go values. Scenarios: (a) a column changes from `double` to `string` (strict
decimal); (b) a bare `long` becomes a nullable union and nil values appear (also covers a nil in a column
that never had one); (c) `double` becomes a decimal logical type and the value is a `*big.Rat`.

| Destination (commit) | (a) string where a double was | (b) nil where there was always a value | (c) `*big.Rat` |
| --- | --- | --- | --- |
| Postgres (`conduit-connector-postgres@42627f6`, `destination.go`) | Sent as a text-format parameter; the server parses it. Works against `numeric` or `double precision` with valid text; an `int` column or bad text fails the batch (`:118-126`). Column types are not determinable from code | NULL parameter. A nullable column stores NULL; a `NOT NULL` column is rejected by the server and the batch rolls back. Column nullability not determinable from code | Handled: `formatValue` turns it into a string at the column's scale (`:347-374`), only for `numeric`/`decimal` columns; the scale is cached for the connector's life and never refreshed (`internal/db_info.go:28-68`). A `double precision` column errors ("not a numeric type", `db_info.go:95-97`) and the batch stops |
| MySQL destination (`conduit-connector-mysql@22f8da3`, `destination.go`) | Bound as a string parameter; the target server coerces it, and a non-numeric string errors depending on `sql_mode` (not determinable from code) | `nil` becomes SQL `NULL`. `NOT NULL` column: error in strict `sql_mode`, an implicit default otherwise (server documentation, not shown in the repo) | Hard failure: `database/sql` rejects `*big.Rat` (`go-sql-driver/mysql@v1.10.1/statement.go:161-211` converter, reached from `connection.go:772-775`); `ExecContext` fails (`destination.go:175`) and the transaction rolls back |
| S3 (`conduit-connector-s3@e8f04f2`, `destination/format/json.go:40`, `parquet.go:48`) | Schema-blind: the payload is written as one JSON string (`Payload.After.Bytes()`); the number becomes a JSON string. No failure | `null` appears in the JSON. No failure | `*big.Rat` marshals through `MarshalText`, so the JSON string is `"3/2"` (or `"3"` for an integer), not a decimal |
| Kafka (`conduit-connector-kafka@fad0819`, `destination/franz.go:156`) | Pass-through of the serialized record (`opencdc/json` by default). No registry or Avro. Type drift reaches consumers unmediated | `null` in the JSON | Same as S3: `"3/2"` |
| Elasticsearch (`conduit-connector-elasticsearch@58cb18c`, `internal/elasticsearch/v8/client.go:153-163`; SDK v0.12.0) | JSON string sent to a dynamically mapped index (no explicit mapping in the code). Whether the service coerces it depends on the field's first-seen type: service behavior, not determinable from code | `null` sent; the service is expected to ignore it, not confirmed here | Sent as the string `"3/2"`; against a numeric field mapping the service is expected to reject it per item. Not confirmed here |

Consequences for the design:

- `string` decimals reach every destination in a form it can store; the destinations that parse them into a
  numeric column do it on the server. `decimal` (`*big.Rat`) is safe only against Postgres `numeric`; the
  MySQL destination fails on it and the three JSON destinations write a fraction string. This is why
  `string` is the default, and why the release notes list `decimal` as "Avro-native consumers only".
- The MySQL destination builds its column list from the first record of each batch
  (`destination.go:133-142`), so a batch that spans a version adding a column silently drops that column from
  the later records. That is pre-existing and not caused by this change, but a mid-stream schema change
  makes it reachable; the destination round-trip tests cover a version change inside one batch.
- Elasticsearch fixes a field's type at first sight. Under `strict`, a nullable column whose first
  indexed value is `null` leaves no mapping, and the next value decides it.
- Nothing here halts on the new shapes except the two hard failures (MySQL destination on `*big.Rat`,
  Postgres `double` column on `*big.Rat`) and server-side type rejection. That is acceptable only because
  the switch is an explicit, announced step (rows 6 and 11 of the Transition table).

### Processors and the engine

Processors are wrapped by the processor SDK middleware, which decodes each record with its own subject and
version before the processor runs (default on) and re-encodes after it (default on).

- **Re-encoding rewrites the schema.** If a processor changes a record whose schema is attached, the encode
  middleware re-infers an Avro schema from the Go values and registers it under the same subject
  (`conduit-processor-sdk@v0.6.0/middleware.go`, encode path; builtin processors use the default
  middleware, `pkg/plugin/processor/builtin/registry.go:234`). Spiked with the commons version the engine
  pins (v0.7.0): a column whose value is nil in that record becomes `["null","string"]` regardless of the
  source type; a `*big.Rat` becomes `["null", decimal(31,10)]` regardless of the source precision and scale;
  `uint64` is unsupported; `mysql.type` and field defaults are dropped. The output subject then gets
  versions that depend on which values happened to be nil. This applies to every `strict` shape, because
  `strict` introduces nil and `*big.Rat`. Pipelines that must keep the source schema through a processor
  set `sdk.schema.encode.payload.enabled=false` on it.
- `avro.encode` with `autoRegister` re-infers the same way; with `preRegistered` it marshals every record
  with one fixed schema, so a changed value type, a nil in a non-union field, or a `*big.Rat` in a double
  field fails the batch.
- `avro.decode` reads the Confluent wire-format ID from the bytes (SDK-encoded payloads have no such
  header), uses `Schema.Unmarshal` and so produces the same unwrapped shapes as the SDK, and only accepts
  `RawData`, so on records already decoded by the default middleware it fails unless
  `sdk.schema.decode.payload.enabled=false` (read from code).
- `field.convert` stringifies with `%v` and parses: nil becomes the text `<nil>` (an error for numeric
  targets, silently the literal `<nil>` for a `string` target), and `*big.Rat` becomes `3/2`.
  `field.set` renders templates, so a nil renders as `<no value>` and a `*big.Rat` as a fraction.
  `field.rename` and `field.exclude` are value-agnostic. All of them error on `RawData` payloads.
- `json.encode` skips nil; `json.decode` turns numbers into `float64` (above 2^53 precision is lost);
  `base64.*` error on `*big.Rat`; `unwrap.opencdc` errors on a nil field and replaces the metadata,
  including the schema keys, with the inner record's.
- The JavaScript processor passes `int64` through goja, which turns values outside the safe range into
  `float64`. Standalone (WASM) processors receive records as protobuf `Struct`: numbers become `float64`
  (above 2^53 precision is lost), and `*big.Rat` is unsupported and errors.
- The engine proper (lifecycle, DLQ, connector layer) does not reference schema versions.

Consequences for this design: `string` decimals and `long` unsigned (with halt) are the shapes that survive
the JSON, JavaScript and protobuf boundaries without loss; `decimal` and nil are the shapes that need care.
Release notes carry a table of the above, and the connector README documents the processor setting.

## Alternatives

**Keep zero-value coercion, document it.** Cheapest. Rejected: `NULL` and `0` are different data, and the
MySQL destination writes the coerced value to the target. It is a standing invariant 6 violation.

**Make every column nullable.** Avoids the `information_schema` lookup and makes snapshot and CDC schemas
trivially equal. Rejected: it destroys the `NOT NULL` information, forces every consumer to handle null for
primary keys, and still changes every schema.

**DECIMAL always as Avro `decimal` (Debezium parity).** Best typed form. Rejected as the default, not as an
option: it is the worst experience for the JSON-centric paths most Conduit pipelines use, it makes a decimal
primary key an opaque bytes key, and several in-tree consumers reject or mangle `*big.Rat`. Kept as
`types.decimal=decimal`.

**BIGINT UNSIGNED as `fixed[8]` (the suggestion in mysql#86).** Lossless and fixed width. Rejected: the
bytes carry no signedness, so a reader that treats them as a signed 64-bit value silently misreads values
above 2^63-1, which is the corruption we are removing. A union of `fixed[8]` and `long` (also from #86)
makes the type value-dependent, which breaks the rule that a column has one type per schema version.

**BIGINT UNSIGNED as `decimal(20,0)` or string by default.** Safe for all values. Rejected as default
because it changes the key type of every `BIGINT UNSIGNED AUTO_INCREMENT` table for the sake of values that
almost never occur. Halt-on-overflow keeps the common case unchanged and the rare case loud; the opt-in is
there for tables that need the full range.

**Route out-of-range values to a DLQ instead of halting.** A source cannot skip a record without breaking its
ack and position contract, and the engine DLQ handles records that failed in the pipeline, not source read
errors. Rejected; halting is the available policy.

**Stamp only the mode.** The first revision did this. Rejected: `decimal` and `bigintUnsigned` change column
and key types exactly as `mode` does, so a restart with `types.decimal` changed would emit a second schema
silently. The stamp is the whole resolved configuration.

**Infer `strict` for "no position".** Rejected, see the Transition table notes.

**Wait for a registry or SDK change to carry the stamp (for example a compatibility level per subject).**
Out of scope: no SDK or protocol surface changes in this design, and the position is the only durable place
the connector already owns.

## Failure modes

| Failure | Behavior |
| --- | --- |
| `BIGINT UNSIGNED` value above 2^63-1 under `long` | Source returns `MYSQL_UNSIGNED_OVERFLOW`, no record emitted, position not advanced. Pipeline stops until the operator changes the setting. Both modes, both paths. In `legacy` during the snapshot phase (as the Transition table defines it, including a finished snapshot with no CDC ack yet) the code is `MYSQL_UNSIGNED_OVERFLOW_LEGACY_SNAPSHOT` with a remedy that is valid there (table under What `legacy` is) |
| Any integer outside its Avro type under `strict` | Same error class; replaces warn-and-return-raw |
| Nullability lookup fails, or a canal column has no `information_schema` row (permissions, table dropped) | `Open` fails with `MYSQL_NULLABILITY_UNAVAILABLE` naming the table and column; no fallback to "assume NOT NULL" |
| `binlog_row_image` is not `FULL`, `binlog_format` is not `ROW`, or `binlog_row_value_options` is non-empty at `Open` | `Open` fails with `MYSQL_BINLOG_ROW_IMAGE`, `MYSQL_BINLOG_FORMAT` or `MYSQL_BINLOG_ROW_VALUE_OPTIONS` |
| New pipeline with no `types.mode` | N: runs `legacy` with warnings. N+1 and later: `Open` fails with `MYSQL_TYPES_MODE_REQUIRED` |
| Writer session overrides the row image to `MINIMAL` or `NOBLOB` | A nil in a `NOT NULL` column halts with `MYSQL_NULL_IN_NOT_NULL`. A skipped nullable column is emitted as `NULL`: documented residual risk, closable only with an upstream `canal` change |
| NULL in a key column | Halts with `MYSQL_NULL_IN_NOT_NULL` |
| N+2 start with a `legacy`-stamped position in the `snapshot` phase | `Open` fails with `MYSQL_LEGACY_SNAPSHOT_REMOVED`; nothing is read. Path: new connector instance with `strict` (re-snapshot). Avoidable before N+2 by the release-note callout |
| Restart with a configuration that differs from the stamp | `MYSQL_TYPES_STAMP_MISMATCH` or `MYSQL_TYPES_SWITCH_IN_SNAPSHOT` per the Transition table; nothing is read |
| kill -9 between the first record under a new configuration and its ack | Restart sees the old stamp; refused unless the token is still set; records are re-emitted under the new configuration (at-least-once) |
| DDL mid-stream changes nullability or type | Out of scope (DDL is untested today). A column whose schema-derived type no longer matches the registered schema must halt, not coerce. A test pins current behavior so the gap is visible |
| Decimal text that does not parse (`types.decimal=double`) | Halt with the error class above; never fall back to a raw value (today's `ParseFloat` failure returns the raw string, `schema.go:393-396`) |
| Snapshot and CDC disagree on a column type | Caught by a parity test that runs every supported type through both paths and compares the Avro-decoded value |
| Downgrade after upgrade, `strict` pipeline | New position version is refused by the older build with `ErrPositionVersionUnsupported` (`common/position.go:113-118`), instead of mis-reading. A `legacy` pipeline never wrote v2 and downgrades cleanly |
| A processor in the path re-encodes the record | Schema re-inferred from values; see Consumers. Not detectable by the connector |
| Destination given a string for a numeric column, or a nil for a `NOT NULL` column | See the destination inventory; verified per destination by a round-trip test, not assumed |
| Snapshot resume with a decimal key or an integer key above 2^53 | Loses or duplicates rows in `legacy` today (mysql#204). `strict` resumes with typed binds or an explicit decimal cast; see Keys and snapshot positions |
| A resume predicate that binds `CAST(? AS UNSIGNED)` over a string | Loses `BIGINT UNSIGNED` rows above 2^63-1 (observed). Pinned by a test |

## Upgrade and rollback

Schema subject names do not change. A different schema registered under `<table>_payload` becomes a new
version of the same subject (registry behavior: identical text returns the existing version, a different
text appends a new one; read from `conduit-schema-registry@v0.2.6/registry.go`, and the SDK in-memory
service does not dedupe), and each record carries its subject and version in metadata, so a consumer that
resolves the schema per record reads old and new records correctly. A consumer that pins a version or
generates code from version 1 will break; that is the compatibility cost and the reason for the staged
rollout. Versions can go backwards (re-registering an older text returns its old version).

1. **Release N (v0.21 target):** ship the settings. Nothing changes for an existing pipeline except the
   unsigned halt (a bug fix, both modes), the Open checks on the binlog variables (both modes) and the
   per-column startup warnings in `legacy`. A pipeline opts into `strict` by setting it: from no position
   directly (row 2), or through the token procedure (rows 5-6). `strict` pipelines write v2 positions.
   Unstamped positions are `legacy`. A new pipeline with no `types.mode` runs `legacy` and warns (row 1).
2. **Switching an existing pipeline** (row 6): stop; destinations and consumers get ready for the new schema
   version; set `types.mode=strict` and `types.acceptChangeFrom=legacy`; start. Refused in the snapshot
   phase. Leave the token until the log line reports an acked record under the new stamp.
3. **Release N+1:** a pipeline with no position and no `types.mode` is refused (`MYSQL_TYPES_MODE_REQUIRED`).
   Existing `legacy` positions still run, with the same warnings. **Release N+2:** `legacy` is removed; row 3
   becomes a refusal: `cdc` phase points to row 6, `snapshot` phase gets `MYSQL_LEGACY_SNAPSHOT_REMOVED` and a
   re-snapshot on a new connector instance. Recommended, pending DeVaris (see What `legacy` is).
4. **Rollback:** from N to N-1 is refused by the position gate once N has written a v2 position, so only for
   `strict` pipelines. To roll back, the operator recreates the connector (re-snapshot) or keeps running N
   (and may go back to `legacy` with the token, which writes v1 positions again). The envelope bump is the
   one-way door and is why it needs the upgrade test.

Because the envelope bump is a serialized-format change, it ships with an upgrade test (see Test plan).

**Release-note callout (idle pipelines).** A pipeline that has never acked a record has no position, so on
upgrade it looks like a new pipeline: it runs `legacy` with warnings in N and is refused at `Open` in N+1.
That includes pipelines on empty tables, pipelines with `snapshot.enabled=false` that have seen no change
since the last restart, and pipelines that crashed before their first ack. The N release notes say so in a
separate callout: "if a pipeline has been idle or has no position, set `types.mode` explicitly before
upgrading to N+1; if it already delivered records downstream with `legacy` shapes, choose `legacy` (valid
until N+2) or follow the switch procedure after it has acked a record". The N+1 refusal repeats the
instruction in one line.

**Release-note callout (snapshot-phase pipelines).** Repeated in the N and N+1 notes, and in the N+1 startup
warning for any `legacy` pipeline whose position is in the snapshot phase: "a `legacy` pipeline that is
still in its initial snapshot, or whose snapshot has finished but has not acked a change event, cannot be
switched to `strict` in place, and in N+2 it will be refused and must be re-snapshotted on a new connector
instance. To avoid that, let the snapshot finish and let one change event be acked, then follow the switch
procedure (row 6), before upgrading to N+2." Detection, by reading the position the engine stores for the
source connector (read from code, not run): the API returns it as base64 JSON in
`state.sourceState.position` of `GET /v1/connectors/{id}` (`proto/api/v1/api.proto`, `Connector.SourceState`).
The `conduit` CLI has no command that prints it (the `connectors` subcommands manage plugins, not connector
instances), so the command is the HTTP API:

```sh
curl -s localhost:8080/v1/connectors/<connector-id> \
  | jq -r '(.state.sourceState // .state.source_state).position' | base64 -d \
  | jq '{snapshot: has("snapshot_position"), cdc: has("cdc_position")}'
```

`snapshot: true` with `cdc: false` is the snapshot phase and needs action before N+2. `cdc: true` is safe
for row 6. An empty position means no position (row 1). The connector also logs the phase at `Open`
(Observability), so the same answer is in the log without an API call. The exact JSON field casing and the
base64 encoding follow the gateway's protojson defaults and have not been run against a live engine.

## Observability

- Log at `Open`: the resolved `types.mode`, `types.decimal`, `types.bigintUnsigned`, where each came from
  (position stamp, explicit, default), the position stamp and phase, and in `legacy` or `double` mode one
  `warn` line per affected column, plus the `legacy` removal release and date.
- Log on each new schema version registered: table, subject, version, mode.
- Log `types stamp changed from <old> to <new>` at the first record emitted under a new stamp, and again when
  the first position with the new stamp is acked.
- There is no metrics surface in the connector today. The halts are the error itself; every error is
  machine-readable with code, table, column, config path and fix text.
- Documentation: a type mapping table in the connector README and the `llms.txt`/docs source in step with
  the change; the migration note for steps 1 to 4 and the processor setting from Consumers in the release
  notes.

## Test plan

Written before the implementation (review the tests first):

- Table-driven NULL tests: every supported type, nullable and not, in snapshot and CDC; assert real nil and
  that `NOT NULL` columns stay bare.
- Nullability lookup: a user without `SELECT` on a captured table fails `Open` with
  `MYSQL_NULLABILITY_UNAVAILABLE` (not zero columns, not NOT NULL).
- Row image: `Open` refuses `MINIMAL` and `NOBLOB`; a session-level `MINIMAL` UPDATE halts on the `NOT NULL`
  primary key in `strict`; the known nullable-column residual case is pinned by a test that documents it.
- Round-trip DECIMAL tests with precision 65, scale 30, negative values, trailing zeros, `0.00`, and the
  exact reproduction row from #191, in all three decimal modes, snapshot and CDC.
- `BIGINT UNSIGNED` at 0, 2^63-1, 2^63, 2^64-1 under each mode and both paths; halting under `long` in
  `strict` and `legacy`; the snapshot path no longer returns `3474867090068222261`.
- Decimal primary key (new): snapshot a table with a `DECIMAL(10,2)` key including negative values, with a
  small `fetchSize`, kill and restart at every chunk boundary; assert exactly the remaining keys are emitted,
  in `strict` and for the `legacy` defect pinned as a failing-but-documented case (mysql#204). The same for
  `BIGINT` keys above 2^53, `BIGINT UNSIGNED` keys above 2^63 and `DECIMAL(30,10)` keys that differ only
  below double precision. The key sets of the resume spike are the minimum. Run with prepared statements and
  with `interpolateParams=true`.
- Resume predicate pin: a test that a `BIGINT UNSIGNED` resume at every point from 1 to `2^64-2` returns the
  rows above `2^63-1` (it fails against `CAST(? AS UNSIGNED)` over a prepared string), and a test that no
  resume path binds an untyped string to a numeric key. Other key types (`VARCHAR`, temporal, `BINARY`) get
  their own resume cases; they were not spiked.
- Snapshot/CDC parity: insert, snapshot it, update it through CDC, compare decoded types per column.
- Schema registration: the registered text contains the field properties and `"default": null`; the same
  table registers one schema version across repeated CDC events; `legacy` still registers the canonical
  text.
- Stamp: a table-driven test over every row of the Transition table (12 rows, with row 1 in its N and N+1
  forms, row 2 in its N+2 form for `legacy`, and row 3 in its N+2 forms: `cdc` refused pointing to row 6, `snapshot` refused with `MYSQL_LEGACY_SNAPSHOT_REMOVED`), asserting the outcome and the error code. Position round trip: v1 positions parse
  as `legacy`; v2 positions carry all three fields; a build with an unknown extra setting reads the stamp.
- Unstamped never-emitted pipeline (new): a pipeline that has run and acked nothing (empty tables with
  `snapshot.enabled=false`, and a crash before the first ack) restarted on N with no `types.mode`: starts in
  `legacy` with warnings in N, and is refused with `MYSQL_TYPES_MODE_REQUIRED` in N+1, and in both cases no
  stamped position exists to disagree.
- Overflow remedies: `BIGINT UNSIGNED` above 2^63-1 hit in each state of the overflow table (`strict`;
  `legacy` in phases `none`, `cdc`, `snapshot`); assert the code and that the remedy named is accepted by the
  transition table (following it does not hit a refusal). Includes the state **snapshot finished, no CDC ack
  yet** (position still a `snapshot_position`, overflow value is the first CDC record): in `legacy` assert
  `MYSQL_UNSIGNED_OVERFLOW_LEGACY_SNAPSHOT`, in `strict` assert the new-instance remedy, in both assert that
  the `cdc` remedy is not offered, and that the restart with the `cdc` remedy applied is refused by rows 4
  and 9 (this is the stranded-pipeline regression). Then ack one CDC record, hit the overflow again, and
  assert the code and remedy move to the `cdc` forms.
- Snapshot-phase legacy pipeline (new): from a v1 position with a `snapshot_position`, N+1 logs the warning
  from the callout; N+2 refuses with `MYSQL_LEGACY_SNAPSHOT_REMOVED`; the same position after one acked CDC
  record is refused in N+2 with the row 6 instruction, and row 6 then succeeds.
- Open checks: `binlog_format=STATEMENT`/`MIXED`, `binlog_row_value_options=PARTIAL_JSON` and `MINIMAL`/`NOBLOB`
  each fail `Open` with their code; an absent variable is treated as unset.
- kill -9 at the mode switch (new): SIGKILL (a) between the first record under the new stamp and its ack,
  and (b) after the ack; restart each time with the token present and absent; assert refusal, or resume
  without loss, and that the next emitted record carries the new stamp. Covers rows 6, 11 and the crash note.
- Property-based test (`rapid`, which is not in the tree yet; adopting it is its own dependency
  justification) over decimal text and uint64 values: format then parse is the identity or a stable halt.
- Fuzz the decimal text parser and the stamp parser.
- Destination round trip: MySQL source to MySQL destination with NULLs, large decimals and unsigned values;
  assert the target rows equal the source rows. A Postgres destination case for NULL and string decimals.
  The mid-stream change cases from the destination inventory, per destination.
- Processor path: a schema-attached record through a builtin processor with default middleware and with
  `sdk.schema.encode.payload.enabled=false`; assert what the registry holds.
- Upgrade test: write a v1 position with N-1 in the snapshot phase and again in the CDC phase, start N,
  verify resume without duplicates beyond at-least-once and with `legacy` types and v1 positions; then follow
  row 6 and verify the next records carry the new schema version and a v2 stamp. And a downgrade test that
  expects `ErrPositionVersionUnsupported` for a `strict` position.
- kill -9 during a snapshot and during CDC after the switch to `strict`, verifying no loss and consistent
  types across the restart. The connector's existing crash test (`crash_integration_test.go`) is the base.
- Acceptance suite stays green in both modes.

## Open questions for sign-off

1. Default for `types.decimal`: `string` (proposed) or `decimal` for Debezium parity.
2. Is halting on `BIGINT UNSIGNED` overflow acceptable as the default, or should `types.bigintUnsigned`
   default to `decimal`/`string` and accept the key-type change?
3. Staging (recommended, pending DeVaris): positioned pipelines keep `legacy` for N and N+1 with per-column
   warnings and a committed removal in N+2; a no-position pipeline with no `types.mode` warns in N and is
   refused in N+1. The alternative is flipping the default to `strict` for no-position pipelines at some
   release, which guesses wrong for an idle old pipeline. Or flip everything at once, given that `legacy` is a
   known corruption path.
4. Row image: refuse non-`FULL` at `Open` in both modes (proposed; it stops pipelines that run today on a
   server with `MINIMAL`, which are emitting zero values and a zero primary key), or refuse only in `strict`
   and warn in `legacy`.
5. `legacy` keeps `NULL` as zero and `DECIMAL` as `double` because the shape requires it. Acceptable for two
   minor versions, or should `legacy` also refuse tables with nullable or decimal columns unless the operator
   acknowledges the loss?
6. The session-level row-image residual risk: ship documented, and open the upstream `canal` change.
7. The snapshot-resume defect for decimal and large-integer keys (mysql#204): track as its own sev-0 item
   and fix on `legacy` positions separately. The fix for `strict` is in this design, and the `legacy`
   snapshot-phase overflow depends on it staying unfixed in place (see What `legacy` is).
8. Where the code lives at implementation time: the connector has moved from `conduitio-labs` to `ConduitIO`
   (certification decision 7), so the implementation PR lands in `ConduitIO/conduit-connector-mysql`.

## Related

- ConduitIO/conduit-connector-mysql#191 (NULL and DECIMAL), #86 (unsigned BIGINT), #158, #187, #188
- ConduitIO/conduit-connector-mysql#204 (snapshot resume loses rows for DECIMAL, large BIGINT and
  BIGINT UNSIGNED keys; see Keys and snapshot positions and What `legacy` is)
- `docs/catalog/v0.21-certification-gaps.md` (PR #3034), section 1
- `docs/design-documents/20260724-snapshot-cdc-position-handoff.md` in the MySQL connector repo (position
  envelope versioning)
- `docs/design-documents/20260715-pipeline-repair-api-semantics.md` (no reset-position primitive)
- Invariants 2, 3 and 6 in `CLAUDE.md`
