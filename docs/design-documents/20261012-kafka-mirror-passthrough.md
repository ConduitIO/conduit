# Kafka-to-Kafka mirroring: header, partition and timestamp passthrough

## Summary

`conduit-connector-kafka` cannot mirror a topic faithfully today. The source flattens headers into
strings and drops the partition; the destination writes no headers, no timestamp and lets the client
pick the partition. This document designs passthrough for the three, as opt-in destination options
backed by lossless source metadata, plus a payload-only record format recipe and a separate
discovery item for topic include/exclude. Offset translation, checkpoints, heartbeats and topic
config sync are out of scope. Tracked in ConduitIO/conduit-connector-kafka#309. Roadmap item:
v0.21 connector-certification. Risk tier: Tier 1 (changes the record written to Kafka, and
partition choice touches ordering, invariant 4).

## Context

What the code does today (conduit-connector-kafka, read 2026-10-12):

- Source `Read` (`source.go`) sets `opencdc.collection` to the topic, `opencdc.createdAt` to the Kafka
  record timestamp, and one `kafka.header.<key>` metadata entry per header with `string(h.Value)`.
  Metadata is a `map[string]string`, so a repeated header key overwrites the earlier value and binary
  values are not byte-preserved (invalid UTF-8 is passed through as a Go string, but is mangled by
  JSON-based record formats and some transports). The partition and offset exist only inside the
  opaque SDK position, not in metadata.
- Destination `prepareRecord` (`destination/franz.go`) sets key, value and topic. No headers, no
  timestamp, no partition. The franz-go default partitioner is used: murmur2 key hash for keyed
  records, sticky for keyless.
- The default `sdk.record.format` on a destination is the `opencdc/json` envelope, so the Kafka value
  is the whole OpenCDC record, not the original bytes.
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

### Source: lossless metadata

Metadata keys added by the source (all always emitted once the feature ships; they are additive):

| Key | Value |
| --- | --- |
| `kafka.partition` | decimal partition number |
| `kafka.offset` | decimal offset (informational; not used by the destination) |
| `kafka.timestamp` | milliseconds since epoch as decimal; the record timestamp as Kafka stores it |
| `kafka.timestamp.type` | `create` or `logappend`, when the client exposes it (informational) |
| `kafka.headers` | JSON array of `{"k": "<key>", "v": "<base64 of value bytes>"}` in original order, repeats preserved. Absent when the record has no headers |

`opencdc.createdAt` keeps its current meaning (record timestamp). `kafka.timestamp` exists because
`createdAt` is nanosecond-formatted and can be overwritten by processors, while a mirror needs the
exact original millisecond value; the destination must not depend on a field that other components
own.

Existing `kafka.header.<k>` entries stay, unchanged, as the human-readable view. Where a key repeats,
the last value wins, as today. The deprecation policy applies (announce, warn, remove after two minor
versions); removal is not proposed here. `kafka.headers` is the lossless source of truth, and the
destination reads only that.

Size: base64 inflates headers by about a third and metadata is carried with every record in the
pipeline. Headers are normally small, but this is a cost for every Kafka source record. A source
option `emitHeaders` (default `true`, so the lossless view exists unless disabled) lets operators
turn it off. Open question below on the default.

### Destination: opt-in preservation

Three booleans, default `false` (no change for anyone who does not set them):

- `preserveHeaders`: write headers from `kafka.headers`, in order, repeats intact, values decoded
  from base64. If `kafka.headers` is absent, write nothing. If it is present but malformed (bad JSON,
  bad base64), fail the record (see Failure modes); never skip silently. As a courtesy to records
  from older sources, if `kafka.headers` is absent and `kafka.header.<k>` entries exist, write those
  as headers (best-effort, lossy) only when `preserveHeaders` is set, and log once at warn level.
- `preserveTimestamp`: set the Kafka record timestamp from `kafka.timestamp`. Requires the broker
  topic to use `message.timestamp.type=CreateTime` (default); with `LogAppendTime` the broker
  overwrites it. The destination cannot detect this per topic cheaply, so it is documented, not
  checked. Missing or unparsable `kafka.timestamp`: fail the record.
- `preservePartition`: write to the partition number in `kafka.partition`. Implemented with a custom
  franz-go partitioner that returns `Record.Partition` when set by the connector and otherwise
  delegates to the default (franz's `ManualPartitioner` is client-wide, so a wrapper is needed so that
  records without `kafka.partition` still work). Before producing to a topic the destination checks
  the topic's partition count from client metadata; if `kafka.partition >= count`, the record fails
  with an actionable error (see below). It never falls back to key hash silently, because that would
  reorder a partition relative to the source mapping and violate the user's stated intent.

Interaction with topic rename: the `topic` template is evaluated as today; partition passthrough
applies to whatever topic results. Partition numbers are meaningful only if source and destination
topics have at least as many partitions. Topic auto-creation (`AllowAutoTopicCreation`) creates
topics with broker defaults, which may have fewer partitions than the source. With
`preservePartition` this surfaces as the per-record error above; it is not worked around by the
connector (topic config sync is out of scope).

### Payload-only record format recipe

A faithful mirror needs the destination to write the source value bytes, not the OpenCDC envelope.
Documented recipe (to be verified against the SDK version in use before it is published; see Open
questions): set `sdk.record.format` on the destination to the `template` format with a template that
prints the raw `Payload.After` bytes, and key as today (the key is already written raw). A
pipeline-level example ships in the connector README and the conduit docs with `preserveHeaders`,
`preserveTimestamp` and `preservePartition` enabled. Tombstones (nil value) must come out as nil
values, not empty or `null`; the recipe needs a test for that.

### Topic include/exclude and discovery (separate item)

Source `topics` stays a literal list in this change. A regex include/exclude with new-topic
discovery is a separate design item, because discovery changes the consumer's subscription over time
(franz-go regex consumption plus metadata refresh) and interacts with rebalance handling
(`OnPartitionsRevoked` is a TODO today). It gets its own short design doc once this one lands. This
document only reserves the config names `topicsRegex` and `topicsExclude`.

### Ordering

Preserving partition number plus the existing single-producer write path keeps per-partition order
only if the destination produces one source partition's records in sequence and a produce failure
does not let a later record land before an earlier one. With idempotent writes (`acks=all`, the
default) franz-go retries within a partition preserve order. With `acks` other than `all`,
idempotence is disabled by the connector and a retry can reorder. Decision: `preservePartition`
requires `acks=all`; config validation rejects the combination otherwise, with an error that names
`acks`. Records from different source partitions may interleave arbitrarily; that is also true of MM2.

Records are not re-keyed. Key bytes are written as received, so consumers that re-hash see the same
key. Because partition is preserved explicitly, a destination with a different partition count still
preserves the source mapping for the partitions that exist; it does not preserve key-hash
locality, which is documented.

## Alternatives

**Pass headers and partition as ordinary `kafka.header.*` string metadata and derive everything from
that.** Smallest change. Lost: it cannot represent repeated keys or binary values, which is the
reported bug. Rejected.

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

| Situation | Behaviour |
| --- | --- |
| `kafka.partition` >= destination partition count | Record fails; `Produce` returns the failing index, so earlier records are acked and this one is not. Error code `KAFKA_PARTITION_OUT_OF_RANGE`, includes topic, partition, partition count and the config path `preservePartition`. Fix suggested: pre-create the topic with at least N partitions or disable `preservePartition` |
| Destination topic does not exist yet and gets auto-created with fewer partitions | Same as above, at the first affected record. Documented: pre-create topics for partition-preserving mirrors |
| `kafka.partition` missing or non-numeric with `preservePartition` | Record fails with `KAFKA_PARTITION_INVALID`; never defaults |
| `kafka.headers` malformed | Record fails with `KAFKA_HEADERS_INVALID`; never dropped |
| Header total size exceeds broker `message.max.bytes` / client `batchBytes` | Produce error from the broker or client is returned for that record; not retried indefinitely. Headers count toward record size, so a record that fit before can now fail; documented |
| Header with empty key | Kafka rejects; record fails with the broker error |
| Timestamp skew: `preserveTimestamp` with broker `LogAppendTime` | Timestamp is overwritten by the broker; documented, not detectable per record |
| Timestamp older than topic retention | Broker may delete the segment early. Documented |
| Crash (`kill -9`) mid-batch | Unacked records are re-read from the committed consumer-group offset and produced again: duplicates, no loss. Preserved fields are derived from the record, so a duplicate carries identical partition, timestamp and headers. Destination writes remain keyed to the same partition, so duplicates land in the same partition |
| Duplicate delivery with `preservePartition` | A re-produced record is appended again to the same partition (at-least-once). Consumers need idempotence, as with any at-least-once mirror |
| Batch produce returns results out of order | Depends on the #308 fix; this change must not merge before it |
| Source `kafka.headers` absent (older source version) | Destination writes no headers (or the lossy fallback); no error |

All new errors carry a stable code, the config path and a suggested fix, per the project error
convention.

## Compatibility, upgrade and rollback

- N+1 readers: new metadata keys are additive; old destinations ignore them. A new destination with
  all flags `false` behaves exactly as today. A new destination reading records from an old source
  sees no `kafka.headers`, `kafka.partition` or `kafka.timestamp`: with `preserveHeaders` it falls
  back as described; with `preservePartition` or `preserveTimestamp` it fails the record loudly.
  Operators must upgrade the source side first. This ordering goes in the release notes.
- No position format change. The SDK position is untouched, so no migration and no position upgrade
  test is needed.
- Rollback: downgrade the destination; new metadata keys are ignored. Data already written with
  preserved partitions or headers stays as it is.
- Source `emitHeaders=false` is the escape hatch if metadata size is a problem.

## Observability

- The destination logs once per pipeline, at info, which passthrough options are active.
- Counters (via the SDK metrics where available, otherwise log fields): records produced with
  preserved partition, header fallbacks used, and failures by code. Metrics names to be agreed with
  the connector metrics conventions; not specified here.
- Per-record failures include topic, partition and the record position so an operator can find the
  offending offset.

## Test plan

- Unit: metadata round trip with repeated header keys, binary and empty header values, nil and empty
  header lists; partition parsing; the partitioner wrapper with and without `kafka.partition`; flag
  validation (`preservePartition` requires `acks=all`).
- Property-based (rapid): encode headers to `kafka.headers` and back is the identity for arbitrary
  byte values and repeats; order of headers is preserved.
- Integration on both Kafka and Redpanda (the repo already runs both): mirror from topic A (N
  partitions, keyed, headers with repeats and binary, explicit timestamps) to topic B and compare
  partition, key, value, headers and timestamp record by record; per-partition order equal to source.
  Negative cases: destination with fewer partitions; malformed `kafka.headers`.
- Tombstone and empty-value mirror test for the payload-only recipe.
- Fault injection and `kill -9`: SIGKILL the connector mid-batch (source and destination), restart,
  and verify at-least-once: every source record appears at least once in the destination, in the
  correct partition, with the original order per partition among first deliveries; duplicates are
  identical to the original. Runs on both brokers. This needs the repo's chaos harness for kafka,
  which does not exist yet and is already a certification item.
- Fuzz the `kafka.headers` decoder (a parser of externally supplied data).
- SDK acceptance suite stays green with all flags off.
- No performance claims are made here. Any claim about header or partitioner overhead needs a benchi
  run with the configuration committed.

## Open questions

1. `emitHeaders` default: `true` (lossless by default, more metadata per record) or `false`
   (no cost for pipelines that never mirror, but then mirroring needs an opt-in on both ends).
   Leaning `true`.
2. The exact `template` expression for a payload-only value, including nil values, has not been run
   against the SDK version the connector pins. If the template format cannot emit a nil value, the
   fix is a small `sdk.record.format` addition in `conduit-connector-sdk` (a payload-only format);
   that is the only possible SDK change, and it is not assumed here. No protocol change is needed.
3. Whether franz-go's per-record `Partition` honoured by a wrapping `BasicConsistentPartitioner`
   behaves as intended with idempotent producers on both brokers needs a spike before
   implementation.
4. Whether to add `kafka.timestamp.type` at all, given the franz-go consumer may not expose it
   uniformly.

## Related

- ConduitIO/conduit-connector-kafka#309 (tracking issue), #308 (batch ack bug, prerequisite)
- `docs/catalog/v0.21-certification-gaps.md` (MirrorMaker 2 parity table, decision 9)
