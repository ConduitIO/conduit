# S3 destination: parity with the Kafka Connect S3 sink

**Status: implemented** in `ConduitIO/conduit-connector-s3` v0.11.x: partitioners (s3#1054), the `bytearray`
format and gzip (s3#1053), unique object keys and `conditionalWrites` (s3#1066), SSE settings (s3#1051) and
ambient credentials under the operator policy (s3#1027, #3108). Conduit's built-in `s3` moves to v0.11.2 with
PR #3220 (`main` is on v0.11.1 until it merges). Where the implementation differs from the text below, the
difference is marked _Implemented:_.

## Summary

`conduit migrate kafka-connect` maps `io.confluent.connect.s3.S3SinkConnector` to `builtin:s3`. When this was
written (s3 v0.9.5), every such migration was blocking: the S3 destination wrote every batch to
`<prefix>/<UnixNano>.<ext>`, had no partitioners, wrote JSON as one OpenCDC-shaped object per line, and had no
raw-bytes format. Readers built for
the Kafka Connect layout (Athena, Hive, Spark and Trino tables over `topics/<topic>/year=.../`, partition
projection) break on the first object.

This document fixes the format and partitioning contract for `builtin:s3` in v0.21:

1. A **parity matrix** for every Kafka Connect S3 sink option: supported, mapped (a Conduit setting with the
   same effect, possibly with a documented difference) or unsupported, with the reason.
2. **Partitioners** that reproduce Kafka Connect's directory layout: `default`, `field`, `time`, `hourly`,
   `daily`. The time partitioner takes Kafka Connect's own `path.format` syntax, so a pattern is copied, not
   rewritten.
3. A **`bytearray` format** (the payload bytes, one per line), which is Kafka Connect's `ByteArrayFormat` and,
   with extension `.json`, the output of `JsonFormat` for JSON values. Plus gzip compression.
4. The existing layout stays the default. Every layout change is opt-in.

Roadmap item: v0.21: S3 sink parity (#3009). Risk tier of this document: Tier 3 (docs only). The
implementation PRs in `ConduitIO/conduit-connector-s3`: the format PR is Tier 2. The partitioner PR is Tier 1,
because splitting a batch into several objects changes how many records `Write` reports as written, and
that is ack logic (invariant 1).

## Context

### What exists (inventory)

- `ConduitIO/conduit-connector-s3` v0.9.5: destination formats `json` (OpenCDC-shaped lines, payload and key as
  JSON strings) and `parquet` (fixed string columns, GZIP). One `PutObject` per `Write`, key
  `<prefix>/<UnixNano>.<ext>`. `Write` returns after `PutObject` succeeds, so acks follow durable storage.
- No other Conduit S3 sink exists in `ConduitIO` or `conduitio-labs`. The generic `file` and `log` destinations
  do not apply. Nothing to reuse beyond extending this connector.
- In the same repo (`s3#N` below is `ConduitIO/conduit-connector-s3#N`), at the time of writing: SSE settings
  (s3#1047, PR s3#1051), the default credential chain (s3#1021, blocked on the ambient-credentials design #3041),
  typed Parquet (s3#1034), conditional writes (s3#621), deterministic keys and record encoding (s3#1030),
  partitioning (s3#1033, which this document supersedes in design). _Implemented:_ SSE (s3#1051) and the
  credential chain (s3#1027) are done; `conditionalWrites` exists (s3#1066). Still open: typed Parquet
  (s3#1034), deterministic names (s3#621) and s3#1030's remaining items.
- The mapping rows in `20261014-migrate-kafka-connect-mapping-and-harness.md` (_S3 sink_) say they follow this
  lane's matrix. This document is that matrix; the migrate lane updates its rule data from it.

### What Kafka Connect writes

Object key: `<topics.dir>/<topic>/<encoded partition>/<topic>+<kafka partition>+<start offset>.<ext>`, with
`topics.dir` defaulting to `topics`, `/` as directory delimiter and `+` as file delimiter. The encoded partition
is:

| Partitioner | Encoded partition |
| --- | --- |
| `DefaultPartitioner` | `partition=<kafka partition>` |
| `FieldPartitioner` | `<field>=<value>` per field in `partition.field.name`, joined by `/` |
| `TimeBasedPartitioner` | the record time, floored to `partition.duration.ms` in `timezone`, formatted with `path.format` (Joda `DateTimeFormat`) |
| `HourlyPartitioner`, `DailyPartitioner` | `TimeBasedPartitioner` with `'year'=YYYY/'month'=MM/'day'=dd/'hour'=HH` (1h) and `'year'=YYYY/'month'=MM/'day'=dd` (24h) |

Readers depend on the directory part: Hive-style `name=value` segments are what partition discovery and
projection read. They do not depend on the file name, which differs between Kafka Connect versions and formats.

## Decision

### Layout

A new destination setting `partitioner` selects the layout:

| `partitioner` | Object key |
| --- | --- |
| `none` (default) | `<prefix>/<UnixNano>.<ext>`: unchanged |
| `default` | `<prefix>/<collection>/partition=<kafka.partition>/<UnixNano>.<ext>` |
| `field` | `<prefix>/<collection>/<f1>=<v1>/.../<UnixNano>.<ext>` |
| `time`, `hourly`, `daily` | `<prefix>/<collection>/<formatted time>/<UnixNano>.<ext>` |

`<collection>` is the record's `opencdc.collection` metadata, which `builtin:kafka` sets to the topic. It takes
the place of `<topic>`. `prefix` takes the place of `topics.dir`; `migrate` emits `prefix: topics` when
`topics.dir` is absent, so the default Kafka Connect layout is reproduced.

The file name stays `<UnixNano>.<ext>` (plus the compression suffix). _Implemented:_ the name is
`<UnixNano>-<writer id>-<counter>.<ext>`, unique by construction (s3#1066). Kafka Connect's
`<topic>+<partition>+<offset>` names are deterministic and are the basis of its exactly-once. Conduit does not
have the Kafka offset in a generic form, and deterministic names without conditional writes would let a
replay overwrite an object that already holds acked records.

Conditional writes exist: `conditionalWrites` (default `true`, s3#1066) sends `If-None-Match: *` with every
`PutObject`, so a key collision fails with `s3.destination.key_exists` instead of overwriting acked records. A
store that rejects the header answers with `s3.destination.conditional_writes_unsupported`; set
`conditionalWrites: false` for it. What stays out of scope for v0.21 is deterministic, offset-based names, and
with them exactly-once (s3#621).

`default` needs the Kafka partition number. `builtin:kafka` does not put it in the record metadata today (only
the topic, the record timestamp and headers). The destination reads `kafka.partition`; the Kafka lane (#3008)
adds it to the source's metadata. Until it does, `partitioner: default` fails the record (see _Missing values_),
and `migrate` maps `DefaultPartitioner` as approximated. This is a cross-lane dependency, recorded in #3009.

### Settings

| Setting | Values | Default | Kafka Connect counterpart |
| --- | --- | --- | --- |
| `partitioner` | `none`, `default`, `field`, `time`, `hourly`, `daily` | `none` | `partitioner.class` |
| `partitioner.fields` | comma-separated field paths (dots for nesting) | none; required for `field` | `partition.field.name` |
| `partitioner.pathFormat` | Kafka Connect `path.format` syntax, see below | none; required for `time` | `path.format` |
| `partitioner.duration` | Go duration, at least 1s | none; required for `time` | `partition.duration.ms` |
| `partitioner.timezone` | IANA zone | `UTC` | `timezone` |
| `partitioner.timestampExtractor` | `record`, `recordField`, `wallclock` | `record` | `timestamp.extractor` |
| `partitioner.timestampField` | field path | none; required for `recordField` | `timestamp.field` |
| `format` | adds `bytearray` | (required) | `format.class` |
| `format.bytearray.separator` | string, escapes `\n`, `\r`, `\t` | `\n` | `format.bytearray.separator` |
| `format.bytearray.extension` | file extension | `.bin` | `format.bytearray.extension` |
| `compression` | `none`, `gzip`; for `parquet` also `snappy` | `none`; `gzip` for `parquet` | `s3.compression.type`, `parquet.codec` |

`hourly` and `daily` fix `pathFormat` and `duration`; setting either with them is a configuration error, not a
silent override.

`pathFormat` takes the Joda pattern letters `YYYY`, `yyyy`, `MM`, `dd`, `HH`, `mm` and `ss`, quoted literals
(`'year'`) and `''` for a quote. Every other character outside quotes that is not a letter is literal. Any other
letter (text months, week years, `S`, `Z`) is rejected when the pipeline is created, with the path and the
supported set in the error. Kafka Connect's `locale` only changes text fields, which are not supported, so it has
no effect and is dropped by `migrate` with a note. A Joda subset was chosen over Go layouts (`2006/01/02`)
because Go layouts treat digits as tokens: `'partition'=01` cannot be written unambiguously, and users already
have the Joda patterns.

Time bucketing follows Kafka Connect: the timestamp is floored to `duration` in `timezone` (offset applied before
flooring, removed after), then formatted in `timezone`.

`timestampExtractor`:

- `record`: the record's `opencdc.createdAt` metadata. `builtin:kafka` sets it to the Kafka record timestamp,
  which is what Kafka Connect's `Record` extractor uses.
- `recordField`: a field of the payload: an integer (epoch milliseconds, as Kafka Connect), an RFC 3339 string,
  or a time value in structured data.
- `wallclock`: the time of the `Write`. A replay can then land in a different directory: still at-least-once,
  but documented as non-deterministic.

Fields for `field` and `recordField` are read from the payload's `After`: structured data directly, raw data
parsed as a JSON object. `builtin:kafka` emits raw bytes, so a JSON-valued topic works without a decode
processor.

Partition values are escaped like Hive does for path names: `/`, `\`, `%`, `=`, `:`, `"`, `#`, `'`, `*`, `?`,
`[`, `]`, `^`, `{`, `}`, DEL and control characters become `%XX`. A value that Kafka Connect would write as an
extra directory level (a `/` inside) is therefore one segment here; that difference is documented. Kafka Connect
does not escape.

### Missing values

A record without the value its partitioner needs (no collection, no `kafka.partition`, no timestamp, a missing,
null or non-scalar field, an unparsable time) fails the `Write`: it is not written under an empty or zero
partition. That is the destination half of invariant 6 (never silently mangle) and matches Kafka Connect, which
fails the task with a `PartitionException`. With a DLQ configured the record goes there; without one the
pipeline stops with an error that names the record's position, the setting and the missing value. Error code:
`s3.destination.partition_value`.

### Writing a batch

`Write` groups the batch by object key prefix, in the order in which each prefix first appears, and writes one
object per group with one `PutObject` each, sequentially. It returns `(n, err)` where `n` is the index of the
first record of the first group that failed: every record before it belongs to a group whose first record comes
earlier, and groups are written in that order, so all of them were written. The SDK acks the first `n` records
and nacks the rest.

```go
// Invariant 1: report as written only records whose object PutObject confirmed.
```

Records of a failed group that were already written in an earlier group of the same batch do not exist (a record
is in one group). Records after `n` that were in groups written successfully are written again on retry: a
duplicate, never a loss.

### Formats

- `bytearray`: `Payload.After` bytes followed by the separator, per record. Structured data is its JSON encoding.
  A record without `After` (a delete or a Kafka tombstone) fails the `Write` with the error code
  `s3.destination.null_value`; this is Kafka Connect's default `behavior.on.null.values=fail`. `ignore` and
  `write` are not offered: dropping a record must be a visible pipeline decision (a `filter` processor), not a
  destination flag.
- `json` keeps its line shape. It now refuses a record whose key or payload is not valid UTF-8, with error code
  `s3.destination.invalid_utf8`, instead of writing U+FFFD in place of the bytes (`encoding/json` does that
  silently today: invariant 6). Binary data belongs in `bytearray` or `parquet`.
- `compression: gzip` gzips the object and appends `.gz` to the extension, as Kafka Connect does. For `parquet`
  the setting selects the column codec and the extension stays `.parquet`.

### Parity matrix

Status: **S** supported (same effect), **M** mapped (a Conduit setting or behavior with a documented
difference), **U** unsupported (reason given). Options that only tune Kafka Connect internals are grouped.

| Kafka Connect option | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `s3.bucket.name`, `s3.region` | S | `aws.bucket`, `aws.region` | |
| `store.url`, `s3.path.style.access.enabled` | S | `aws.endpoint`, `aws.pathStyle` | Defaults differ (KC `true`, Conduit `false`) |
| `aws.access.key.id`, `aws.secret.access.key` | S | `aws.accessKeyId`, `aws.secretAccessKey` | |
| `s3.credentials.provider.class` (default chain) | M | leave `aws.accessKeyId` and `aws.secretAccessKey` unset | The AWS SDK default chain, only when the operator enables `connectors.ambient-credentials` and lists the bucket, and only as `builtin:s3`; otherwise `connector.ambient_credentials_disabled` (s3#1027, #3108) |
| `sts.role.*` | U | none | Assume-role is not supported |
| `topics.dir` | S | `prefix` | `migrate` emits `topics` when absent |
| `partitioner.class` `DefaultPartitioner` | M | `partitioner: default` | Needs `kafka.partition` from `builtin:kafka` (#3008) |
| `partitioner.class` `FieldPartitioner`, `partition.field.name` | S | `partitioner: field`, `partitioner.fields` | Values are escaped |
| `partitioner.class` `TimeBasedPartitioner`, `path.format`, `partition.duration.ms`, `timezone` | S | `partitioner: time`, `partitioner.pathFormat`, `.duration`, `.timezone` | Joda subset: numeric fields only |
| `partitioner.class` `HourlyPartitioner`, `DailyPartitioner` | S | `partitioner: hourly`, `daily` | |
| `locale` | M | none | No effect on numeric patterns |
| `timestamp.extractor` `Record`, `RecordField`, `Wallclock`; `timestamp.field` | S | `partitioner.timestampExtractor`, `.timestampField` | `Record` reads `opencdc.createdAt` |
| custom partitioner or extractor classes | U | none | Java classes |
| `format.class` `ByteArrayFormat`, `format.bytearray.*` | S | `format: bytearray`, `format.bytearray.*` | |
| `format.class` `JsonFormat` | M | `format: bytearray`, extension `.json` | Same bytes for schemaless JSON values; KC re-serializes through its converter, so whitespace and key order can differ, and `schemas.enable=true` envelopes are not produced |
| `format.class` `ParquetFormat`, `parquet.codec` | M | `format: parquet`, `compression` | Fixed string columns until s3#1034 (typed Parquet) |
| `format.class` `AvroFormat`, `avro.codec`, `enhanced.avro.schema.support`, `connect.meta.data` | U | none | No Avro format in s3 v0.11.x. The library question is settled: the stack moved from the archived hamba/avro to `iskorotkov/avro` (#2817, #2823; s3#1031). An Avro format is a follow-up |
| `schema.compatibility`, `schema.generator.class` | U | none | No schema projection in the destination |
| `s3.compression.type` | S | `compression` | `none`, `gzip` |
| `s3.compression.level` | U | none | Default gzip level |
| `flush.size` | M | `sdk.batch.size` | Kafka Connect counts per partition; Conduit per batch, which is then split by partition. The destination defaults to `sdk.batch.size: 1000` and `sdk.batch.delay: 1s` (s3#1069) |
| `rotate.interval.ms` | M | `sdk.batch.delay` | Wall clock, not record time |
| `rotate.schedule.interval.ms` | U | none | |
| file naming: `<topic>+<partition>+<offset>`, `file.delim`, `directory.delim`, `filename.offset.zero.pad.width` | U | none | Names are `<UnixNano>-<writer id>-<counter>.<ext>`, unique by construction (s3#1066); offset-based names are s3#621 |
| `enable.conditional.writes` | M | `conditionalWrites` (default `true`) | `If-None-Match: *` on every `PutObject`; a collision fails with `s3.destination.key_exists`. It guards against overwrites; it does not make names deterministic |
| exactly-once (deterministic names) | U | none | At-least-once; a replay writes a new object (s3#621) |
| `s3.ssea.name`, `s3.sse.kms.key.id` | S | `aws.serverSideEncryption`, `aws.sseKmsKeyId` | Empty means the bucket default, as in KC (s3#1051) |
| `s3.sse.customer.key` | U | none | SSE-C |
| `s3.acl.canned` | U | none | Hardcoded `private`; follow-up |
| `s3.object.tagging*` | U | none | |
| `s3.part.size`, `s3.part.retries`, `s3.retry.backoff.ms`, `s3.elastic.buffer.*`, `s3.http.send.expect.continue`, `s3.wan.mode` | U | none | One `PutObject` per object (5 GiB limit); retries are the AWS SDK's |
| `s3.proxy.*` | U | none | Interacts with the credentials policy (#3041) |
| `behavior.on.null.values` | M | `fail` only | `bytearray` fails; `json`/`parquet` store the delete record |
| `store.kafka.keys`, `store.kafka.headers` and their formats | U | none | `json` and `parquet` already carry key and metadata |
| `hive.integration` and `hive.*` | U | none | |
| `storage.class` `S3Storage` | S | none | The only storage |

### Reading Kafka Connect output

Objects Kafka Connect already wrote are not touched. A pipeline migrated with the same `prefix`, `partitioner`
and pattern writes new objects into the same directories, so a table over the prefix keeps working and sees both.
The S3 source reads any object under a prefix, including Kafka Connect's.

## Alternatives considered

1. **Match Kafka Connect exactly**, including file names and offsets. Rejected: Kafka-specific (the offset is in
   `builtin:kafka`'s position, opaque to the destination), and deterministic names without conditional writes
   turn a replay into an overwrite of acked data. The directory layout, which readers use, is matched exactly.
2. **One templated key setting** (`keyTemplate: "{{.Collection}}/{{.Time \"2006\"}}/..."`) instead of named
   partitioners. More general, but users must rewrite every Kafka Connect configuration into a new syntax, Go
   time layouts are ambiguous with literal digits, and an arbitrary template makes the layout contract
   unreviewable. Named partitioners are a closed set that `migrate` maps one to one.
3. **Go time layouts for `pathFormat`**. Rejected for the ambiguity above; the Joda subset is small and
   unambiguous.
4. **A separate `jsonvalue` format** for `JsonFormat`. Rejected: `bytearray` with extension `.json` writes the
   same bytes for raw JSON values and the JSON encoding for structured payloads, so a second format would differ
   only in name.

## Consequences and failure modes

| Failure | Effect | Detection |
| --- | --- | --- |
| `PutObject` fails for group k of a batch | Records before group k's first record are acked; the rest are nacked and retried or sent to the DLQ. Groups after k that were not attempted are written on retry; records of successful later groups are written twice | Write error in the connector log with the object key |
| Crash between two `PutObject`s of a batch | Nothing of the batch was acked (acks follow `Write`'s return); the batch is redelivered; objects written before the crash exist twice afterwards | Duplicates in the destination; at-least-once is documented |
| Missing partition value | Record fails, DLQ or pipeline stop; never written under an empty partition | `s3.destination.partition_value` with the record position and setting |
| Kafka tombstone with `bytearray` | Record fails | `s3.destination.null_value` |
| Non-UTF-8 bytes with `json` | Record fails; before this change the bytes were replaced silently | `s3.destination.invalid_utf8` |
| High-cardinality field | One object per distinct value per batch: many small objects, more `PutObject` calls | Object count; documented with `sdk.batch.size` guidance |
| Host clock steps back | _Implemented:_ names carry a writer id and a counter after `<UnixNano>`, so they do not repeat (s3#1066); with `conditionalWrites` a collision fails instead of overwriting | `s3.destination.key_exists` |
| Store ignores `If-None-Match` | Writes are unconditional, with no warning; keys are still unique by construction | Not detected. Documented on the setting |
| `wallclock` extractor and replay | A redelivered record can land in a later directory | Documented as non-deterministic |
| `recordField` holds seconds instead of milliseconds | Records land in 1970 | Documented: epoch milliseconds, as Kafka Connect |

Ordering: within one object, records keep batch order. Across objects there is no order, as today.

Invariant 1 holds: `Write` reports as written only records whose `PutObject` returned success. Invariant 3: a
failed group is nacked, never dropped.

### Upgrade and rollback

The defaults (`partitioner: none`, existing formats) keep v0.9.5's layout, so an upgrade changes no directory
until a pipeline opts in. _Implemented:_ two defaults did change in v0.10/v0.11: object names gained a writer id
and counter (s3#1066), and the destination batches by default (s3#1069). `conditionalWrites` defaults to `true`;
a store that rejects `If-None-Match` fails the write with `s3.destination.conditional_writes_unsupported` until
the pipeline sets it to `false`. Rolling the connector back with a pipeline that uses the new settings fails at
configuration time (unknown parameter), not at write time. Objects already written stay valid for their readers.
The destination keeps no state, so there is nothing to migrate.

### Observability

Write errors carry the stable codes above and the object key. A debug log line per object written lists the
key and the record count. No new metrics in v0.21: the SDK's destination metrics count records and batches,
and the number of objects per batch is in the debug log. A per-object counter is a follow-up if operators ask
for it.

### Testing

- Unit tests per partitioner: path rendering against Kafka Connect's documented examples, time bucketing across a
  DST change and with non-hour durations, escaping, every missing-value case.
- `Write` with a batch spanning partitions and a `PutObject` failing on group k: the returned count is exactly the
  first index of group k; with the SDK, the acked positions are exactly that prefix.
- MinIO integration tests: keys and bodies for each partitioner and format, gzip round trip.
- Acceptance suite v2 runs with the default layout (`partitioner: none`, `format: json`); a second run with
  `partitioner: field` would prove nothing more about delivery, and the read-back would have to know the layout.
- Chaos: SIGKILL of a Conduit process mid-batch with `partitioner: time`, then verify every acked record is in
  the bucket (invariant 1) and every record is there at least once (invariant 3). Owned by the S3 chaos task of
  #3009; _Implemented:_ s3#1055.

## Related

- #3009 (lane), #3002 (v0.21 tracking), #3034 (certification gaps), #3071 (migration harness S3 corpus)
- `20261014-migrate-kafka-connect-mapping-and-harness.md` (_S3 sink_ rows follow this matrix)
- `ConduitIO/conduit-connector-s3`: s3#1033 (partitioning), s3#1030, s3#621, s3#1034, s3#1047 (PR s3#1051),
  s3#1021 (PR s3#1027)
- Implementation: s3#1053 (bytearray, gzip), s3#1054 (partitioners), s3#1055 (chaos), s3#1066 (unique keys,
  `conditionalWrites`), s3#1069 (default batching); Conduit #3220 (built-in s3 v0.11.2)
- #2817, #2823 (Avro library)
- #3041 (ambient credentials), #3008 (Kafka lane: `kafka.partition` metadata)
