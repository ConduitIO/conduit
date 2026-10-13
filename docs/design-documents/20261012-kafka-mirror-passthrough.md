# Kafka-to-Kafka mirroring: header, partition and timestamp passthrough

## Summary

`conduit-connector-kafka` cannot mirror a topic faithfully today. The source flattens headers into
strings and drops the partition; the destination writes no headers, no timestamp and lets the client
pick the partition. This document designs passthrough for the three, as opt-in options on both ends:
the source emits lossless metadata only when asked, the destination writes it only when asked. It
also records what a payload-only record format recipe does and does not preserve (it does not yet
preserve empty values; see Open questions) and sets aside topic include/exclude as a separate item.
Spike results against franz-go v1.20.7 on Apache Kafka 4.0.0 and Redpanda v25.1.5 are in _Spike
results_ and several decisions below depend on them. Offset translation, checkpoints, heartbeats and topic
config sync are out of scope. Tracked in ConduitIO/conduit-connector-kafka#309. Roadmap item:
v0.21 connector-certification. Risk tier: Tier 1 (changes the record written to Kafka, and
partition choice touches ordering, invariant 4).

## Context

What the code does today (conduit-connector-kafka, read 2026-10-12):

- Source `Read` (`source.go:88-112`) sets `opencdc.collection` to the topic, `opencdc.createdAt` to the
  Kafka record timestamp, and one `kafka.header.<key>` metadata entry per header with
  `string(h.Value)`. Key and value are both mapped with `opencdc.RawData(...)` (`source.go:109-110`),
  so a null key or value and an empty one start out as the same Go type.
  Metadata is a `map[string]string`, so a repeated header key overwrites the earlier value and binary
  values are not byte-preserved (invalid UTF-8 is passed through as a Go string, but is mangled by
  JSON-based record formats and some transports). The partition and offset exist only inside the
  opaque SDK position, not in metadata.
- Destination `prepareRecord` (`destination/franz.go:141-159`) sets key, value (`r.Bytes()`) and topic.
  No headers, no timestamp, no partition. The franz-go default partitioner is used: murmur2 key hash
  for keyed records, sticky/uniform-bytes for keyless. The client is created with
  `AllowAutoTopicCreation` (`franz.go:50`) and `RecordDeliveryTimeout(cfg.DeliveryTimeout)`
  (`franz.go:51`; the config field has no default, `destination/config.go:52`).
- The default `sdk.record.format` on a destination is the `opencdc/json` envelope, so the Kafka value
  is the whole OpenCDC record, including its metadata, not the original bytes. Any metadata the source
  adds therefore changes the bytes such a destination writes (and the bytes an S3 JSON destination
  writes). This is why new source metadata is opt-in.
- The destination batch path has an ack bug (#308, completion-order index); this design assumes it is
  fixed first and does not touch it.

MirrorMaker 2 (the reference for "faithful") keeps topic name (via a replication policy), key, value,
headers, partition (by default the same partition number) and timestamp, and additionally syncs
offsets, topic configs and ACLs.

## Constraints

- Ordering (invariant 4): Kafka guarantees order per partition. A mirror that keeps record order per
  source partition must write each source partition's records to one destination partition, in order.
- At-least-once (invariants 1 and 3): passthrough must not introduce any path that acks a record that
  was not produced, including when a header or partition cannot be honoured.
- Backward compatibility: existing pipelines read and write `kafka.header.<k>` strings. Existing
  destinations ignore unknown metadata. Defaults must not change what existing pipelines write.
- No SDK or protocol change. Everything fits in OpenCDC metadata (`map[string]string`) and connector
  config. `conduit-connector-protocol` is not touched. No `conduit-connector-sdk` change is required;
  see Open questions for the one place where an SDK feature would simplify the recipe.
- No speculative generality: only what the three passthrough items need.

## Decision

### Source: lossless metadata, off by default

New source option `emitMirrorMetadata` (bool, default `false`). When `false` the source emits exactly
what it emits today. When `true` it additionally emits the keys below. Default-off is required, not a
preference: metadata is carried to every destination, and destinations that serialize it (the Kafka
destination's default `opencdc/json` value, S3 JSON) would write different bytes for pipelines that
never asked for mirroring. Mirroring needs the option on at the source and the `preserve*` options on
the destination; the destination errors name the source option when the metadata is missing.

| Key | Value |
| --- | --- |
| `kafka.partition` | decimal partition number |
| `kafka.partitions` | decimal partition count of the source topic when the record was read (see below) |
| `kafka.offset` | decimal offset (informational; not used by the destination) |
| `kafka.timestamp` | milliseconds since epoch as decimal; the record timestamp as Kafka stores it |
| `kafka.timestamp.type` | `create` or `logappend` (from `Record.Attrs.TimestampType()`, which both brokers populate; see Spike results). Informational |
| `kafka.headers` | JSON array, always present when the option is on (`[]` for a record with no headers), format below |
| `kafka.key.null`, `kafka.value.null` | `true`, present only when the Kafka key or value is null. Absent means non-null (possibly empty) |

`kafka.partitions` is read at source `Open` for each configured topic (all topics are static literals
today) with a metadata request, cached, and refreshed when a record arrives whose partition is >= the
cached count. It exists so the destination can fail on the first record of a topic instead of at the
first record that happens to land on a missing partition.

`kafka.headers` format (version 1; the key name carries the version, a future incompatible format uses
a new key such as `kafka.headers.v2`):

```json
[{"k": "<base64 of key bytes>", "v": "<base64 of value bytes>"}, {"k": "...", "v": null}]
```

- Order is the original order; repeated keys are separate elements.
- `v` is JSON `null` for a null header value and `""` for an empty one. The two are different on the
  wire and both brokers preserve the difference (Spike results).
- Keys are base64 of the key bytes, not JSON strings. Both brokers accepted and returned an empty key
  and a key that is not valid UTF-8 byte for byte (Spike results), and a JSON string cannot carry the
  latter without mangling it. The earlier draft claim that Kafka rejects an empty header key was wrong.
- Base64 is the standard alphabet (RFC 4648 section 4) with `=` padding. The decoder is strict: URL
  alphabet, missing padding, whitespace or line breaks, unknown fields, duplicate fields, a missing `k`
  or `v`, and non-string `k`/`v` other than `v: null` all fail with `KAFKA_HEADERS_INVALID`.

`opencdc.createdAt` keeps its current meaning (record timestamp). `kafka.timestamp` exists because
`createdAt` is nanosecond-formatted and can be overwritten by processors, while a mirror needs the
exact original millisecond value; the destination must not depend on a field that other components
own.

Existing `kafka.header.<k>` entries stay, unchanged, as the human-readable view, and are emitted
regardless of `emitMirrorMetadata`. Where a key repeats, the last value wins, as today. The deprecation
policy applies (announce, warn, remove after two minor versions); removal is not proposed here.
`kafka.headers` is the lossless source of truth. When `kafka.headers` is present the destination reads
only it, and edits that a processor makes to `kafka.header.*` are ignored. A processor that needs to
change mirrored headers must rewrite `kafka.headers`. Likewise edits to `kafka.partition` and
`kafka.timestamp` change what the destination writes; they are not protected.

Size: base64 inflates headers by about a third and the metadata is carried with every record in the
pipeline, which is the other reason the option is off by default.

### Destination: opt-in preservation

Three booleans, default `false` (no change for anyone who does not set them), and one escape hatch:

- `preserveHeaders`: write headers from `kafka.headers`, in order, repeats intact, values decoded from
  base64, `v: null` written as a null header value. If `kafka.headers` is present but malformed, fail
  the record (`KAFKA_HEADERS_INVALID`); never skip silently. If `kafka.headers` is absent, fail the
  record with `KAFKA_HEADERS_MISSING` (suggested fix: set `emitMirrorMetadata: true` on the source).
  With the source option on, an empty header list is `[]`, not absent, so absence always means the
  source did not emit it. The only exception is the explicit `allowLossyHeaders` (bool, default
  `false`, only valid with `preserveHeaders`): then a record without `kafka.headers` is written with
  headers rebuilt from its `kafka.header.<k>` entries (string values, repeats collapsed, binary values
  as the source stringified them, order not defined), a warning is logged once per pipeline and a
  counter is incremented. Without `allowLossyHeaders` nothing lossy ever happens silently.
- `preserveTimestamp`: set the Kafka record timestamp from `kafka.timestamp`. Missing or unparsable
  `kafka.timestamp`: fail the record. Timestamp handling has more failure modes than the draft
  assumed; see _Timestamp preconditions_ below.
- `preservePartition`: write to the partition number in `kafka.partition`. Implemented by creating the
  franz-go client with `kgo.ManualPartitioner()`, which makes the client use `Record.Partition`. The
  draft proposed a wrapper around the default partitioner so that records without `kafka.partition`
  would still work. That is unnecessary: a record without a valid `kafka.partition` fails
  (`KAFKA_PARTITION_INVALID`), so every record on a preserving client carries a partition. The spike
  showed that both approaches work with the idempotent producer on both brokers, and that a wrapper
  is easy to get wrong (it must forward `PartitionByBackup` and `OnNewBatch` or the client panics),
  so the simpler client-wide option is chosen. A unit test asserts that a record with
  `Partition: 3` and a key lands on partition 3, and that the same record on a default client does not
  (Spike results 1: the default partitioner ignores `Record.Partition`).
- Validation: `preservePartition` requires `acks=all` (idempotent writes) and `preserveTimestamp`
  requires `deliveryTimeout` unset or zero (see below). Both fail config validation with an error that
  names the option.

Partition count checks, in order:

1. At `Open`, for a static `topic`, the destination fetches topic metadata. With `preservePartition`
   it does not auto-create the topic (`AllowAutoTopicCreation` is omitted from the client options when
   `preservePartition` is set; `franz.go:50` today always sets it), because a broker-default topic
   usually has the wrong partition count. A missing topic fails `Open` with
   `KAFKA_DESTINATION_TOPIC_MISSING`. For a templated `topic`, the same check runs on the first record
   for each resulting topic.
2. On the first record for each topic, if `kafka.partitions` is present and the destination has fewer
   partitions than the source, the record fails with `KAFKA_PARTITION_COUNT_MISMATCH` even when this
   particular record's partition would fit. This is the fail-fast the review asked for: the error
   shows at the first record, not at the first record that hits a missing partition.
3. On every record, if `kafka.partition >= destination partition count`, the destination refreshes
   before failing: it asks the broker for the current count (a fresh metadata request) and, if the
   broker has more partitions than the client knows, calls `Client.ForceMetadataRefresh` and retries
   the record for a bounded period (10 s) because the refresh is asynchronous. In the spike the client
   saw new partitions after one 35 ms retry on Kafka and after 5.0 s on Redpanda (the client's
   default `MetadataMinAge` is 5 s; I did not establish that this is the cause). If the count is still
   too small after that, the record fails with
   `KAFKA_PARTITION_OUT_OF_RANGE`. Without this a topic that is grown while the pipeline runs fails
   records until the client's own metadata refresh (Spike results 3).

The destination never falls back to key-hash partitioning, because that would silently change the
user's stated placement.

More partitions at the destination than at the source is allowed. The source-to-destination partition
mapping is preserved for the partitions that exist, the extra partitions stay empty, and key-hash
locality is not preserved: a consumer that assumes `hash(key) mod destinationPartitions` finds keys on
different partitions than the hash predicts. The destination logs this once per topic at warn level
when it sees `kafka.partitions` smaller than the destination count.

Interaction with topic rename: the `topic` template is evaluated as today; partition passthrough
applies to whatever topic results.

#### Timestamp preconditions

Spike results (below) found three ways `preserveTimestamp` can silently or noisily misbehave. On the
first record for each destination topic the destination reads the topic's configuration with
DescribeConfigs and applies these rules. Results are cached for the process lifetime and re-read once
after any `INVALID_TIMESTAMP` error from the broker.

| Condition | Behaviour |
| --- | --- |
| `message.timestamp.type=LogAppendTime` | Fail with `KAFKA_TIMESTAMP_TYPE_LOG_APPEND`. The broker would overwrite the preserved value, so preservation cannot be honoured. Suggested fix: use a `CreateTime` topic or turn off `preserveTimestamp` |
| `message.timestamp.before.max.ms` / `after.max.ms` (Kafka 3.6+) or `message.timestamp.difference.max.ms` (before 4.0) is finite and the record timestamp is outside the window | Fail the record before producing, `KAFKA_TIMESTAMP_OUT_OF_RANGE`, naming the config. Kafka rejects the whole produce batch with `INVALID_TIMESTAMP` if one record is out of range (spike), so catching it client-side keeps valid neighbours out of the failure |
| `retention.ms` is finite and the record timestamp is older than `now - retention.ms` | Fail the record, `KAFKA_TIMESTAMP_OLDER_THAN_RETENTION`. Kafka's time-based retention is evaluated on record timestamps: in the spike 400 records with a CreateTime 10 days old on a topic with `retention.ms=90000` were deleted within the first 10 s sample while the control topic kept its records for about 90 s. The broker acks the write and then deletes the data, which is a silent loss from the mirror's point of view. Suggested fix: raise `retention.ms` on the destination topic or turn off `preserveTimestamp` |
| DescribeConfigs fails with an authorization error | Log a warning once and skip the checks; the limitation is documented. Other errors fail the record |
| Redpanda | v25.1.5 does not return `message.timestamp.before.max.ms`/`after.max.ms`/`difference.max.ms` from DescribeConfigs and did not enforce them in the spike, so only the `message.timestamp.type` and `retention.ms` rules apply. Redpanda retention behaviour with old CreateTime was not observed (see What was not verified) |

`preserveTimestamp` also requires `deliveryTimeout` to be unset or zero. franz-go measures
`RecordDeliveryTimeout` from `Record.Timestamp` (`kgo/sink.go`, `isTimedOut`), so any record whose
preserved timestamp is older than the timeout fails immediately with "records have timed out before
they were able to be produced" (spike), and franz fails every buffered record in that partition with it.
With the timeout unset a produce to an unreachable broker waits instead of failing; the pipeline stalls
visibly and loses nothing, which is the better failure for a mirror. Config validation rejects the
combination with `KAFKA_DELIVERY_TIMEOUT_WITH_TIMESTAMP`.

If the source topic is `LogAppendTime`, `kafka.timestamp` is the broker append time and is written to
the destination as CreateTime. That is the closest available behaviour and is documented.

### Payload-only record format recipe

A faithful mirror needs the destination to write the source value bytes, not the OpenCDC envelope.
The recipe was run in a spike (pipeline-less; see Spike results for exactly what that covers). It is
not faithful today and is not claimed to be:

- Works: set `sdk.record.format` to `template` with options
  `{{ if .Payload.After }}{{ printf "%s" .Payload.After }}{{ end }}` (or without the `if`, which
  gave the same results). Non-empty values, including invalid UTF-8, trailing newlines, JSON and
  text that looks like template syntax, came out byte-identical, and a null value came out null.
- Broken: an empty non-null value comes out null. A Go template that prints nothing produces a nil
  byte slice, so an empty value becomes a Kafka tombstone. On a compacted topic that deletes the key.
  The template format cannot express the difference.
- Hazard: the obvious templates `{{ .Payload.After }}` and `{{ .Payload.After | toString }}` print the
  Go slice representation (`[104 101 ...]`), not the bytes, and turned a null value into a non-null
  one.
- Hazard: if the template fails to execute, `Record.Bytes()` silently falls back to the OpenCDC JSON
  envelope (`conduit-commons` `opencdc/record.go:63-71`), so a Kafka topic quietly fills with
  envelopes instead of payloads. This is the silent-mangling class that invariant 6 forbids, and the
  destination cannot currently detect it.

Until Open question 2 is decided, this design delivers header, partition and timestamp passthrough
and does not deliver faithful empty-value mirroring. Pipelines whose source topics can contain empty
non-null values must not use the template recipe. The README recipe, when written, states this limit.
The shipping example uses the template above with `preserveHeaders`, `preserveTimestamp` and
`preservePartition`, and a test that fails if any value written to the destination differs from the
source value (see Test plan).

### Topic include/exclude and discovery (separate item)

Source `topics` stays a literal list in this change. A regex include/exclude with new-topic
discovery is a separate design item, because discovery changes the consumer's subscription over time
(franz-go regex consumption plus metadata refresh) and interacts with rebalance handling
(`OnPartitionsRevoked` is a TODO today, `source/franz.go:61`). It gets its own design doc once this
one lands, and no config names are reserved here. Note that `kafka.partitions` assumes static topics;
discovery would need to revisit it.

### Ordering

The guarantee, stated as it holds: records of one source partition are written to one destination
partition in source order **as long as no produce error occurs after the records are enqueued**. It
is not an unconditional guarantee, and the spike shows why. A batch of ten records to one partition in
which record 5 is rejected by the broker (`MESSAGE_TOO_LARGE`) left records 0-4 and 6-9 in the
partition and 5 absent. The SDK acks `batch[:n]` and nacks `batch[n:]` with the error
(`conduit-connector-sdk` `destination.go:352-356`), so records 5-9 are all nacked although 6-9 were
written. On redelivery the partition ends up as `s0 s1 s2 s3 s4 s6 s7 s8 s9 s5 s6`: record 5 after
records 6-9, and 6 twice. Both brokers behaved identically. Idempotence does not prevent this; it
protects retries of the same batch, not a record the broker rejects.

What the design does about it:

- Every deterministic check (partition range and count, header decoding, timestamp window and
  retention, record size against `batchBytes`) runs in `prepareRecord`. The batch loop already stops
  at the first `prepareRecord` error and enqueues nothing after it (`franz.go:102-108`), which is
  "stop producing after the first error" for everything the destination can know ahead of time.
  Adding these checks to `prepareRecord` is what keeps the guarantee honest for them.
- What remains is errors only the broker or the network reports after enqueue. There the guarantee is
  weakened as described above. The pipeline halts (next section), so the damage is bounded to the
  affected destination partition, and the remedy is to re-seed that partition's mirror. The design
  does not try to prevent it by producing one record at a time per partition; that costs throughput
  and the cost is not measured here, so it is recorded as an alternative, not chosen.
- `preservePartition` requires `acks=all`. With `acks` below `all` the connector disables idempotence
  (`franz.go:58-61`) and a retry can reorder; config validation rejects the combination, naming
  `acks`. Idempotence was confirmed active in the spike (a producer ID was allocated on both brokers).
- Records from different source partitions may interleave arbitrarily; that is also true of MM2.

Records are not re-keyed. Key bytes are written as received, so consumers that re-hash see the same
key. Because partition is preserved explicitly, a destination with a different partition count still
preserves the source mapping for the partitions that exist; it does not preserve key-hash locality,
which is documented.

### Error policy: halt, do not dead-letter

The errors this design adds are not per-record data problems; they mean the destination topic or the
pipeline is configured wrongly (`KAFKA_PARTITION_OUT_OF_RANGE`, `KAFKA_PARTITION_COUNT_MISMATCH`,
`KAFKA_PARTITION_INVALID`, `KAFKA_HEADERS_INVALID`, `KAFKA_HEADERS_MISSING`, `KAFKA_TIMESTAMP_*`,
`KAFKA_DESTINATION_TOPIC_MISSING`). They must stop the pipeline, not be routed to a DLQ.

The reason is the SDK's nack granularity. When `Produce` returns index `n`, records `batch[n:]` are all
nacked with the same error (`destination.go:356`), including records after the bad one that were fine
and possibly already written. A DLQ that tolerates nacks diverts those records out of the mirror, which
leaves a hole in the destination partition and, for the ones already written, duplicates out of order
when the pipeline is later repaired.

Recommended pipeline config for a mirror, which is also the Conduit default (`pkg/pipeline/instance.go:84-93`)
and must not be loosened:

```yaml
dead-letter-queue:
  window-size: 1
  window-nack-threshold: 0
```

`window-size: 0` (never stop) or any `window-nack-threshold` above 0 turns a configuration error into
silent record loss from the mirror. The connector README and the mirror example state this. The
destination cannot enforce it: pipeline DLQ settings are not visible to a connector. A pipeline lint
for "mirror options plus tolerant DLQ" would be the enforcement and is out of scope here.

## Alternatives

**Pass headers and partition as ordinary `kafka.header.*` string metadata and derive everything from
that.** Smallest change. Lost: it cannot represent repeated keys or binary values, which is the
reported bug. Rejected.

**Emit the lossless source metadata by default.** Mirroring would need one option instead of two. Lost:
metadata is carried to every destination, so pipelines that never mirror would write different bytes
(the Kafka destination's default `opencdc/json` value, S3 JSON) and carry larger records. The first
draft defaulted it on; review rejected that. Default off, opt in at the source.

**Wrap the franz-go default partitioner so records without `kafka.partition` still work.** Proposed in
the first draft. Lost: with `preservePartition` a record without a valid partition fails anyway, so the
fallback path is dead code, and the wrapper must forward two extra franz interfaces
(`PartitionByBackup`, `OnNewBatch`) or the client panics (spike). `kgo.ManualPartitioner()` does the job.

**Make the destination preserve everything unconditionally.** Simplest config. Lost: it changes bytes
written by existing pipelines (headers appear, timestamps change, partitioning changes) and breaks
the backward-compatibility constraint; also `preservePartition` is unsafe by default with
auto-created topics. Rejected in favour of opt-in flags.

**Fall back to key-hash partitioning when the source partition does not exist at the destination.**
Never fails, so more convenient. Lost: it silently changes the user's declared placement and can
reorder records relative to prior writes for the same key (earlier records went to partition N,
later ones to hash(key) mod M). That is a silent ordering change. Rejected; fail loudly instead.

**Implement MM2 fully (a Connect-style `MirrorSourceConnector` equivalent with offset sync and
checkpoints).** Gives consumer failover and group offset translation. Lost: it is a different
product surface (cluster-level replication, a separate checkpoint state), large, and not needed for
the migrate-from-Kafka-Connect certification target. Deferred, not rejected forever.

## Failure modes

All rows with a code halt the pipeline by design (see _Error policy_). "Record fails" means `Produce`
returns that record's index: earlier records are acked, this one and the rest of the batch are nacked.

| Situation | Behaviour |
| --- | --- |
| `kafka.partition` >= destination partition count after a metadata refresh | Record fails, `KAFKA_PARTITION_OUT_OF_RANGE`, includes topic, partition, partition count and the config path `preservePartition`. Suggested fix: pre-create the topic with at least N partitions or disable `preservePartition`. Without our check franz-go itself fails only that record, immediately, with "invalid record partitioning choice of 9 from 4 available" (Spike results 3); it does not hang or fail neighbours |
| Destination has fewer partitions than `kafka.partitions` | First record for the topic fails, `KAFKA_PARTITION_COUNT_MISMATCH`, before any record is written to it |
| Destination has more partitions than the source | Allowed; warning once per topic; key-hash locality not preserved (documented) |
| Destination topic missing with `preservePartition` | `Open` fails (static topic) or the first record for it fails (templated), `KAFKA_DESTINATION_TOPIC_MISSING`; no auto-creation in this mode |
| Destination topic grown while running | Metadata refresh as described above; records are not failed until the bounded retry expires |
| `kafka.partition` missing or non-numeric with `preservePartition` | Record fails, `KAFKA_PARTITION_INVALID`; never defaults. Fix names `emitMirrorMetadata` on the source |
| `kafka.headers` malformed (bad JSON, bad base64, unknown or duplicate fields, wrong types) | Record fails, `KAFKA_HEADERS_INVALID`; never dropped |
| `kafka.headers` absent with `preserveHeaders` | Record fails, `KAFKA_HEADERS_MISSING`, unless `allowLossyHeaders` is set, in which case headers are rebuilt from `kafka.header.*`, one warning, counter incremented |
| Header null versus empty value | Both preserved: `v: null` and `v: ""` round trip as null and empty (spike) |
| Header with empty key or key that is not UTF-8 | Both brokers accept and return these (spike); carried as base64 key bytes, no failure |
| Header total size exceeds broker `message.max.bytes` / client `batchBytes` | The size check runs in `prepareRecord` (record size including headers against `batchBytes`) and fails that record, stopping the batch there; the broker error is the backstop. Headers count toward record size, so a record that fit before can now fail; documented |
| `message.timestamp.type=LogAppendTime` on the destination topic | Record fails, `KAFKA_TIMESTAMP_TYPE_LOG_APPEND` (see _Timestamp preconditions_) |
| Record timestamp outside `message.timestamp.before/after.max.ms` (or `difference.max.ms`) | Record fails client-side, `KAFKA_TIMESTAMP_OUT_OF_RANGE`. If the broker still returns `INVALID_TIMESTAMP` (config changed, or not described), the whole produce batch fails on Kafka, including valid records in it (spike); the destination re-reads the topic config once and reports the config values |
| Record timestamp older than `now - retention.ms` | Record fails, `KAFKA_TIMESTAMP_OLDER_THAN_RETENTION`. Kafka deletes such records within seconds of the segment closing, regardless of how recently they were written (spike) |
| `preserveTimestamp` with `deliveryTimeout` > 0 | Config validation fails, `KAFKA_DELIVERY_TIMEOUT_WITH_TIMESTAMP`; otherwise franz-go times out old-timestamp records immediately (spike) |
| DescribeConfigs not authorized | Warning once, timestamp checks skipped; documented limitation |
| Broker rejects a record after enqueue (for example `MESSAGE_TOO_LARGE`, auth, network) | Pipeline halts. Per-partition order is not guaranteed after this: later records of the batch may already be in the partition and are redelivered (Spike results 4, _Ordering_). Remedy: re-seed that destination partition |
| Crash (`kill -9`) mid-batch | Unacked records are re-read from the committed consumer-group offset and produced again: duplicates, no loss. Preserved fields are derived from the record, so a duplicate carries identical partition, timestamp and headers and lands in the same partition |
| Duplicate delivery with `preservePartition` | A re-produced record is appended again to the same partition (at-least-once). Consumers need idempotence, as with any at-least-once mirror |
| Batch produce returns results out of order | Depends on the #308 fix; this change must not merge before it |
| Source `emitMirrorMetadata` off or older source version | Destination `preserve*` options fail loudly with the codes above; nothing is written without the preserved field |
| Empty non-null value mirrored through the template recipe | Becomes a tombstone (spike). Not handled by this design; see Open questions |
| Template recipe fails to execute | `Record.Bytes()` falls back to the OpenCDC envelope silently (spike). Not detected by this design; see Open questions |

All new errors carry a stable code, the config path and a suggested fix, per the project error
convention.

## Compatibility, upgrade and rollback

- N+1 readers: new metadata keys are additive and, being opt-in at the source, absent unless the
  operator asks for them; old destinations ignore them. A new destination with all flags `false`
  behaves exactly as today, and a new source with `emitMirrorMetadata=false` emits exactly what it does
  today, so no existing pipeline's output bytes change.
- A new destination reading records from an old source, or from a new source with the option off, sees
  no `kafka.headers`, `kafka.partition` or `kafka.timestamp`: with any `preserve*` option on it fails
  the record loudly (`allowLossyHeaders` is the only exception, for headers). Operators upgrade the
  source side first and turn on `emitMirrorMetadata` before enabling the destination options. This
  ordering goes in the release notes.
- `kafka.headers` is versioned by key name (`kafka.headers`, later `kafka.headers.v2`); a destination
  that does not know the key reads none of it. This is a serialized format, so the strict decoder and a
  round-trip test are part of the change.
- No position format change. The SDK position is untouched, so no migration and no position upgrade
  test is needed.
- Rollback: downgrade the destination; new metadata keys are ignored. Data already written with
  preserved partitions or headers stays as it is. Turning `emitMirrorMetadata` off stops emission.

## Observability

- The destination logs once per pipeline, at info, which passthrough options are active, and the
  detected topic configuration it relies on (timestamp type, retention, timestamp bounds).
- Warnings, once each: lossy header fallback in use, destination has more partitions than the source,
  DescribeConfigs not authorized.
- Counters (via the SDK metrics where available, otherwise log fields): records produced with
  preserved partition, lossy header fallbacks used, failures by code. Metric names to be agreed with
  the connector metrics conventions; not specified here.
- Per-record failures include topic, partition and the record position so an operator can find the
  offending offset.
- Runbook entries under `docs/operations/` for each halt-class code: symptom, diagnosis, remediation
  (pre-create the topic with N partitions, raise `retention.ms`, set the source option).

## Test plan

- Unit: metadata round trip with repeated header keys, binary, empty and null header values, and empty
  and nil header lists; `kafka.headers` decoder rejecting each malformed class listed in _Source_;
  partition parsing; flag validation (`preservePartition` requires `acks=all`, `preserveTimestamp`
  rejects `deliveryTimeout` > 0, `allowLossyHeaders` requires `preserveHeaders`); a client built with
  `preservePartition` honours `Record.Partition` and one without ignores it.
- Property-based (rapid): encode headers to `kafka.headers` and back is the identity for arbitrary
  byte keys and values, including null versus empty and non-UTF-8 keys, and repeats; header order is
  preserved. Same for null versus empty key and value through `kafka.key.null`/`kafka.value.null`.
- Fuzz (native Go fuzzing) on the `kafka.headers` decoder, a parser of externally supplied data.
  Seeds: valid arrays; `null` and `""` values; URL-alphabet base64; missing padding; whitespace and
  newlines inside base64; trailing bytes after the array; duplicate and unknown fields; wrong types;
  nested arrays; very large element counts. Properties: never panics; accepts only canonical standard
  padded base64; `decode(encode(x)) == x`; anything accepted re-encodes to the same bytes.
- Integration on both Kafka and Redpanda (the repo already runs both): mirror from topic A (N
  partitions, keyed, headers with repeats, binary, null and empty values, empty and non-UTF-8 keys,
  explicit timestamps) to topic B and compare partition, key, value, headers and timestamp record by
  record; per-partition order equal to source. Negative cases: destination with fewer partitions (fails
  on the first record); destination with more partitions (allowed, warns); destination grown mid-run;
  missing destination topic; malformed or missing `kafka.headers`; `LogAppendTime` destination; a
  destination with `message.timestamp.before.max.ms` set (Kafka only, Redpanda does not enforce it);
  a destination with short `retention.ms` and old timestamps.
- Value fidelity test for the payload-only recipe: values that are null, empty, one byte, invalid
  UTF-8, with template syntax and trailing newlines. It currently fails for the empty value (Spike
  results) and stays red or skipped with a link to the decision on Open question 2; it is the gate for
  claiming a faithful mirror. A test that a failing template does not write envelopes also belongs here.
- Ordering test: record rejected by the broker in the middle of a same-partition batch; asserts the
  documented behaviour (tail nacked, later records may already be present) so a change in it is noticed.
- Fault injection and `kill -9`: SIGKILL the connector mid-batch (source and destination), restart,
  and verify at-least-once: every source record appears at least once in the destination, in the
  correct partition, with the original order per partition among first deliveries; duplicates are
  identical to the original. Runs on both brokers. This needs the repo's chaos harness for kafka,
  which does not exist yet and is already a certification item.
- SDK acceptance suite stays green with all flags off, and a golden test shows source output with
  `emitMirrorMetadata=false` is byte-identical to the previous release.
- No performance claims are made here. Any claim about header or partitioner overhead needs a benchi
  run with the configuration committed.

## Spike results

Throwaway code, not committed (kept in the session scratch area under `spikes/kafka-passthrough`, its
own Go module). franz-go v1.20.7 and kadm v1.17.2 (the versions in `conduit-connector-kafka/go.mod`),
Apache Kafka `apache/kafka-native:4.0.0` (the image in `test/compose-kafka.yaml`) and Redpanda v25.1.5
(the digest-pinned image in `test/compose-redpanda.yaml`), single broker each, in Docker with a unique
compose project and ephemeral host ports. Results are observations on those versions only.

1. Idempotent producer with a custom partitioner (4 partitions, 2000 records per run, default acks
   `all`, idempotence on: a producer ID was allocated). With `kgo.ManualPartitioner()`: 0 produce errors,
   every record in its requested partition, 0 per-partition order violations, timestamps, keys, values
   and headers identical, on both brokers. With a wrapper around the franz default partitioner that
   honours `Record.Partition` only for flagged records (1000 flagged, 1000 delegated): same results on
   both brokers. A wrapper that exposes only `Partition` panics the client ("unreachable") because the
   default partitioner is a `TopicBackupPartitioner`; the wrapper must forward `PartitionByBackup` and
   `OnNewBatch`. With a default (non-manual) client, `Record.Partition` is ignored and the key hash is
   used.
2. Null versus empty: header values `nil` and `[]byte{}`, and record values `nil` (tombstone) and
   `[]byte{}`, round trip distinct on both brokers. Header keys that are empty or not valid UTF-8 are
   accepted and returned byte for byte on both.
3. Out-of-range and grown partitions: producing to partition 9 of a 4-partition topic fails that record
   immediately with a client-side error and does not affect neighbours (both brokers). After growing a
   topic 4 to 8, a produce to partition 6 failed until `ForceMetadataRefresh`; with the refresh,
   a produce to a newly added partition succeeded after one 35 ms retry on Kafka and after 5.0 s on
   Redpanda. `ForceMetadataRefresh` is asynchronous.
4. Produce error inside a same-partition batch: described in _Ordering_ (both brokers).
5. Timestamps. Kafka 4.0.0 DescribeConfigs returns `message.timestamp.type`,
   `message.timestamp.before.max.ms` (default 9223372036854775807) and `message.timestamp.after.max.ms`
   (default 3600000, one hour); `message.timestamp.difference.max.ms` is not returned and creating a
   topic with it fails `INVALID_CONFIG`. With before/after set to 60000, a record 1 day old and a record
   1 day ahead failed `INVALID_TIMESTAMP`, and a valid "now" record in the same batch failed too;
   produced alone it succeeded. Redpanda v25.1.5 returned none of the three bounds and did not enforce
   them (accepted the config at topic creation without error, then accepted old and future records).
   `LogAppendTime` topics overwrite the produced timestamp on both brokers, and the consumer's
   `Record.Attrs.TimestampType()` reports 1 for them and 0 for `CreateTime` topics on both.
6. Delivery timeout: with `RecordDeliveryTimeout(30s)`, a record with CreateTime 2 days old fails
   immediately with "records have timed out before they were able to be produced" on both brokers
   (no produce request is sent); with no timeout it is produced.
7. Retention (Kafka only, observed): 400 records of 512 B with CreateTime 10 days old on a topic with
   `retention.ms=90000`, `segment.bytes=4096` and a 1 s retention check: all deleted by the first 10 s
   sample (log start offset equals log end offset). The control topic with current timestamps kept all
   records until the 90 to 100 s samples. On Redpanda (6000 records, 1 MiB segments) nothing was
   deleted from either topic over 130 s, so Redpanda's behaviour was not observed.
8. Payload-only recipe, pipeline-less. Records were read with franz-go and mapped as `source.go:109-110`
   does (`opencdc.RawData(key)`, `opencdc.RawData(value)`), serialized with the SDK v0.14.1
   `TemplateRecordSerializer` the way the `sdk.record.format=template` middleware does
   (`destination_middleware.go:403-407` sets it on each record), and `Record.Bytes()` was compared with
   the original value, as `franz.go:156` would write it. Per input, Kafka only (the transformation is
   broker-independent): the two `if`/`printf "%s"` templates were byte-identical for plain, JSON,
   invalid UTF-8, trailing newline, template-syntax and null values, and turned an empty value into
   null. `{{ .Payload.After }}` and `| toString` were wrong for every input. A template with a missing
   field fell back to the OpenCDC JSON envelope for every input.

### What was not verified

- Any behaviour inside a running Conduit pipeline or the connector's own source and destination
  code: the spike used franz-go and SDK serializer code directly. In particular whether a null key
  or value survives the gRPC/protobuf hop between connector and engine as distinct from empty was not
  tested, which is why `kafka.key.null`/`kafka.value.null` travel as metadata.
- Redpanda retention behaviour with old CreateTime, and Redpanda enforcement of timestamp bounds
  (observed only as "not returned, not enforced" on v25.1.5).
- Multi-broker clusters, replication factors above 1, `min.insync.replicas` interaction, and Kafka
  versions other than 4.0.0 (for example the `difference.max.ms` era before 3.6).
- Throughput or latency of any option.
- Whether `ForceMetadataRefresh` taking 5.0 s on Redpanda is the client `MetadataMinAge` or the broker.

## Open questions

1. Empty and null values and the payload-only format. The template recipe turns an empty non-null
   value into a tombstone and falls back to the envelope on template errors (Spike results 8). Options:
   (a) a destination option `preserveValue` that writes `Payload.After` bytes verbatim, null when
   `kafka.value.null=true` and empty otherwise, bypassing `sdk.record.format` and removing both
   hazards, connector-only, no SDK change; (b) a payload-only format in `conduit-connector-sdk`;
   (c) keep the template recipe and document the limit. Recommendation: (a), because it makes the
   fidelity test pass without depending on template behaviour and without an SDK release. Needs
   DeVaris sign-off since it adds a fourth destination option.
2. Whether the destination should also refuse to start when `sdk.record.format` is left at the default
   `opencdc/json` together with any `preserve*` option, since that writes envelopes and not a mirror.
   Leaning yes.
3. Whether unbounded produce waits (no `deliveryTimeout`, required by `preserveTimestamp`) are acceptable
   for a certified connector, or whether a separate timeout that does not key off `Record.Timestamp`
   must be built in the connector.

## Related

- ConduitIO/conduit-connector-kafka#309 (tracking issue), #308 (batch ack bug, prerequisite)
- `docs/catalog/v0.21-certification-gaps.md` (MirrorMaker 2 parity table, decision 9)
