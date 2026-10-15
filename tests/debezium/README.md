# Debezium golden captures

Output captured from real Debezium Postgres, byte for byte. These files are the
specification for Conduit's Debezium-compatible output mode
([design](../../docs/design-documents/20261012-debezium-compatible-output.md),
issue #3032): the encoder's tests rebuild every captured message and require the
same bytes, and every intentional difference has to be listed against a field here.

## What is captured

| Debezium | Image (pinned by digest in `capture.sh`) | Compat level |
| --- | --- | --- |
| 2.7.4.Final | `quay.io/debezium/connect@sha256:5a62d0fb…` | `2.6` (2.6.0.Beta1 to 3.0.4) |
| 3.7.0.Final | `quay.io/debezium/connect@sha256:65a33b21…` | `3.5` (3.5 and later) |

Each version is captured with five settings:

| Directory | Key and value converter |
| --- | --- |
| `json` | `JsonConverter`, `schemas.enable=true` |
| `json-numeric` | the same with `decimal.format=NUMERIC` |
| `json-txmeta` | `json` plus the connector's `provide.transaction.metadata=true` (fills the envelope's `transaction` block and writes the `dbz.transaction` topic) |
| `json-noschema` | `JsonConverter`, `schemas.enable=false` |
| `avro` | Confluent `AvroConverter` 7.9.1 (Apache-2.0, fetched at image build) with Schema Registry 7.9.1; `schemas.json` holds every subject version and every schema ID a key or value references |

Everything else is a Debezium default. The connector settings are in `capture.sh`.

The workload (`sql/`) runs against PostgreSQL 14 with `TimeZone=America/Los_Angeles`
and `DateStyle=SQL, DMY`, and the Connect JVM runs in `Asia/Kolkata`, so a value
that depends on a session or JVM default shows up. It covers: snapshot reads;
insert, update and delete with tombstones; primary-key changes on integer,
composite, numeric and uuid keys; a multi-statement, multi-table transaction; every type in the
design's mapping table that the stock image has (numeric constrained and
unconstrained, money, the time types at several precisions, interval, uuid, json,
jsonb, bytea, int, text and numeric arrays, enum, hstore, inet, char, xml, oid)
with edge values; unchanged TOASTed text, jsonb, text[] and bytea columns; the
four replica-identity cases; nullable columns with defaults; a keyless table;
transactional and non-transactional logical decoding messages; JSON string
escaping; and a truncate (which Debezium skips by default).

## Files

`events.jsonl` has one line per Kafka message, sorted by topic, partition and
offset:

```json
{"topic":"dbz.golden.customers","partition":1,"offset":3,"key":{"text":"..."},"value":null,"headers":[{"key":"...","value":{"text":"..."}}]}
```

Bytes that are valid UTF-8 are stored as `{"text": ...}`, which keeps them exact;
anything else, such as the Avro wire format, is stored as `{"base64": ...}`. A null
key or value is `null`, which is different from an empty one. `MANIFEST` records
the image and the connector status at the end of the capture.

## Regenerating

```sh
tests/debezium/capture.sh 3.7.0 json      # one version and converter
```

Each run starts its own compose project on ephemeral host ports, waits for the
snapshot, runs the stream workload, reads every topic as raw bytes
(`cmd/capture`) and tears the stack down. A full refresh is the ten
combinations of `2.7.4`/`3.7.0` and the five settings above.
Do not edit `capture.sh` while it runs; bash reads scripts incrementally.

`cmd/goldendiff <committed> <fresh>` compares two captures. JSON messages are
compared as bytes: the volatile values are masked in the original text with
regular expressions and nothing is decoded, so a change in key order, escaping,
number formatting or whitespace is a difference. Avro messages are compared by
their wire header (schema ID) byte for byte and by decoded value, and
`schemas.json` byte for byte. The volatile values are:

- the wall clock: envelope and source `ts_ms`, `ts_us`, `ts_ns`;
- absolute WAL positions and transaction IDs (`source.lsn`, `source.sequence`,
  `source.txId`, the transaction block's `id`), which depend on what initdb wrote
  before the workload and differ across hosts (two fresh stacks on one host
  agree on them);
- the per-run `__debezium.context.runId` header;
- the partition of a null-key message (sticky partitioner);
- the partition and offset of a message whose key holds one of these values: the
  `dbz.transaction` topic is keyed by `<txId>:<lsn>`, so its messages hash to
  host-dependent partitions. That topic is compared as a multiset of masked
  messages;
- the Java identity hash Debezium prints as the `data_collection` of a logical
  decoding message in a transaction `END` event
  (`...LogicalDecodingMessageId@1d709497`), which changes every run.

Offsets, keys, headers, schema IDs and every other byte must match. The
`debezium goldens` workflow re-captures weekly and on changes here, fails when
the committed files no longer reproduce, attaches the fresh capture, and opens
(or comments on) an issue when the weekly run fails.

## What the goldens established

Facts the captures settle, including the ones where the design text was wrong or
left an item to the harness (**(V)** in the design):

1. **`timestamptz` always has six fractional digits** in 2.7.4 and 3.7.0
   (`2024-01-01T00:00:00.000000Z`, `2024-03-15T08:11:12.500000Z`). The design's
   "minimal fractional digits" is wrong for these versions. `timetz` does use
   minimal digits (`07:44:15.5Z`, `00:00:00Z`).
2. **Unconstrained `numeric`** strips trailing zeros (`1.10` is scale 1, value 11;
   `1.000` is scale 0, value 1) but never goes below scale 0 (`100` is scale 0,
   value 100, not scale -2).
3. **`replace.null.with.default` is on.** An explicit NULL in a nullable column
   with a literal default is written as the default (`d_text` NULL becomes
   `"dflt"`, `d_int` NULL becomes `42`). A NULL in a column whose default is
   `now()` is written as `1970-01-01T00:00:00.000000Z`. Consumers of Debezium JSON
   never see these NULLs. This is Kafka Connect converter behaviour, not
   Debezium's. Conduit mirrors it by default (design D20, decided 2026-10-15) and
   exposes it as an explicit setting.
4. **Deletes under `REPLICA IDENTITY DEFAULT`** fill required non-key columns in
   `before`: `label` becomes `""`, and `qty`, which has `DEFAULT 7`, becomes 7. A
   primary-key change does the same in its delete event. This is the design's D15,
   and the fill uses the column default where one exists.
5. **Primary-key change**: delete (header `__debezium.newkey`), tombstone (same
   header), create (header `__debezium.oldkey`). The header values are the key
   struct as Kafka's `Values.convertToString` writes it: `{"seq":2,"region":"us"}`,
   and a numeric key as `BigDecimal.toString`, `{"id":1.50}`, not base64.
6. **3.7.0 adds `__debezium.context.*` headers to every message**, tombstones
   included: `connectorLogicalName`, `taskId`, `connectorName`, `runId`, after the
   key headers. 2.7.4 writes none.
7. **Source block**: 3.7.0 adds `origin` and `origin_lsn` and `.version(1)`; the
   `snapshot` enum's `allowed` list is `true,last,false,incremental` in 2.7.4 and
   `true,first,first_in_data_collection,last_in_data_collection,last,false,incremental`
   in 3.7.0. Snapshot events have `sequence` `[null,"<lsn>"]`.
8. **Logical decoding messages** go to `dbz.message` with key
   `io.debezium.connector.postgresql.MessageKey {prefix}`. The value
   (`MessageValue`) has fields `op, ts_ms, source, message`, and from 3.7.0
   `op, ts_ms, source, transaction, message`; it has no `ts_us`/`ts_ns`. Both
   transactional and non-transactional messages are emitted. For messages,
   `source.schema` and `source.table` are empty strings.
9. **JSON text**: the JsonConverter escapes characters outside the Basic
   Multilingual Plane as surrogate pairs (`😀`), writes U+2028 and DEL
   raw, and writes floats with Java's `Double.toString`/`Float.toString` (JDK 19+
   shortest form, so `4.9E-324` and `1.1754944E-38`). Both images run JDK 21 or
   later.
10. **Values decided at the source** that the encoder must receive already
    decoded: `real` `-0` becomes `0.0`; `interval '-178000000 years'` saturates to
    `Long.MIN_VALUE`; `bpchar` keeps its padding; `jsonb` text is Postgres's
    normalized form; the unchanged-TOAST placeholder is a string for text and
    jsonb, a one-element list for `text[]`, and the placeholder's bytes for
    `bytea`.
11. **Truncate produces nothing** with the default `skipped.operations=t`.
12. **Transaction metadata** (`json-txmeta`): the envelope's `transaction` block is
    `{"id":"<txId>:<lsn>","total_order":n,"data_collection_order":n}` where the LSN
    is **each event's own** LSN, not the transaction's: the four events of one
    transaction carry four different ids (`769:25304640`, `769:25304808`, ...),
    and the `BEGIN` record on `dbz.transaction` uses the first event's LSN, `END`
    the commit's. `total_order` is the event's 1-based position in the transaction
    and `data_collection_order` its position among that table's events. Logical
    decoding messages get a transaction block too (3.7.0).
