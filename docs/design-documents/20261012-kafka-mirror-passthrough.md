# Kafka-to-Kafka mirroring: header, partition and timestamp passthrough

## Summary

`conduit-connector-kafka` cannot mirror a topic faithfully today. The source flattens headers into
strings and drops the partition; the destination writes no headers, no timestamp and lets the client
pick the partition. This document designs passthrough for the three, as opt-in options on both ends:
the source emits lossless metadata only when asked, the destination writes it only when asked. A
single destination `mirror` mode replaces four separate booleans (decided, see _Decisions_). It also records
what a payload-only record format recipe does and does not preserve (it does not preserve empty values, which
is why `mirror` bypasses it) and sets aside topic include/exclude as a separate item.
Spike results against franz-go (v1.20.7 for the round-3 spike, see the version note under _Spike
results_) on Apache Kafka 4.0.0 and Redpanda v25.1.5 are in _Spike
results_ and several decisions below depend on them. Offset translation, checkpoints, heartbeats and topic
config sync are out of scope. Tracked in `ConduitIO/conduit-connector-kafka#309`. Roadmap item:
v0.21 connector-certification. Risk tier: Tier 1 (changes the record written to Kafka, and
partition choice touches ordering, invariant 4).

## Context

What the code does today (`conduit-connector-kafka`, read 2026-10-12):

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
- The destination batch path had an ack bug (#308, completion-order index). That prerequisite is
  satisfied: `conduit-connector-kafka#312` fixed it (merged 2026-10-13, released in v0.12.6 and v0.13.0),
  and this design builds on the per-index results it introduced without touching that code.

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
  see _Decisions_, item 1(b), for the one place where an SDK feature would simplify the recipe.
- No speculative generality: only what the three passthrough items need.

## Decision

### Source: lossless metadata, off by default

New source option `emitMirrorMetadata` (bool, default `false`). When `false` the source emits exactly
what it emits today. When `true` it additionally emits the keys below. Default-off is required, not a
preference: metadata is carried to every destination, and destinations that serialize it (the Kafka
destination's default `opencdc/json` value, S3 JSON) would write different bytes for pipelines that
never asked for mirroring. Mirroring needs the option on at the source and `mirror` on
the destination; the destination errors name the source option when the metadata is missing.

| Key | Value |
| --- | --- |
| `kafka.partition` | decimal partition number |
| `kafka.partitions` | decimal partition count of the source topic when the record was read (see below) |
| `kafka.offset` | decimal offset (informational; not used by the destination) |
| `kafka.timestamp` | milliseconds since epoch as decimal; the record timestamp as Kafka stores it |
| `kafka.timestamp.type` | `create` or `logappend` (from `Record.Attrs.TimestampType()`, which both brokers populate; see Spike results). Informational |
| `kafka.headers` | JSON array, always present when the option is on (`[]` for a record with no headers), format below |
| `kafka.key.null`, `kafka.value.null` | `true` when the Kafka key or value is null, `false` when it is non-null (possibly empty). Always present when the option is on, so the destination can tell "non-null" from "source did not say" (see _Key and value rules_) |

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

#### Non-UTF-8 header values and the legacy `kafka.header.<k>` entries

The problem, verified: OpenCDC metadata is `map[string]string`, and a protobuf `string` field must be
valid UTF-8. `proto.Marshal` of the `opencdc.v1.Record` built by `Record.ToProto` (`conduit-commons` v0.8.0)
from a record whose metadata holds `kafka.header.bin = "\xff\xfe\x00"` fails with
`string field contains invalid UTF-8`; the same record with a plain value marshals, and an invalid-UTF-8
metadata key (`kafka.header.\xff\xfe\x00`) fails the same way (run 2026-10-14). So a
record that carries one binary header in a legacy entry cannot be marshalled at all, and every path that
marshals the record as that message (the gRPC hop of a standalone connector among them) fails for the
whole record, not for the one entry. Destinations that put metadata through JSON have a second, silent
form of the hazard: a JSON encoder replaces invalid UTF-8 with U+FFFD. `kafka.headers` avoids both by
base64-encoding keys and values, which is the reason for that encoding. Mirror mode does not read the
legacy entries (except under `allowLossyHeaders`).

Options for the legacy entry of a header value that is not valid UTF-8:

| Option | Result | Verdict |
| --- | --- | --- |
| (a) Keep emitting `string(h.Value)` | The record fails to marshal on every protobuf path; binary headers break the pipeline | Rejected: it is the bug |
| (b) Escape or replace the value (U+FFFD, `\x` escapes, base64 in the legacy key) | The record marshals, but a consumer of `kafka.header.<k>` receives bytes that differ from what Kafka holds, with nothing marking the change | Rejected: silent mangling, which invariant 6 forbids |
| (c) Drop the record or fail it | No mangling, but a valid Kafka record becomes unmirrorable because of a view nobody in mirror mode reads | Rejected for mirror mode |
| (d) When `emitMirrorMetadata` is on, omit the legacy entry for that header, keep the raw bytes in `kafka.headers`, count the omission | The record marshals; the lossless copy is intact; the absence of the human-readable view is counted, not silent | Chosen |

Decided by DeVaris 2026-10-14 (Decisions, item 5): option (d). With `emitMirrorMetadata: true`, the
source omits `kafka.header.<k>` for every header whose value is not valid UTF-8 (`utf8.Valid`), and for
every header whose key is not, because the metadata key has the same constraint (verified above); it writes
the header unchanged into `kafka.headers`, increments
`kafka_source_legacy_header_omitted_total` once per omitted entry, and logs a WARN once per header key
per pipeline naming the key (never the value). The value is never escaped or replaced. A header key that
repeats is judged on the value that would have won (the last one), so the legacy view never shows an
earlier value in place of an omitted later one. With `emitMirrorMetadata: false` the source emits what it
emits today, including the hazard; that pre-existing behaviour is outside this decision and is a separate
connector issue.

Upgrade note: a pipeline that turns on `emitMirrorMetadata` loses `kafka.header.<k>` entries only for
headers that could not cross a protobuf hop anyway. Processors that read those entries must read
`kafka.headers` instead.

Size: base64 inflates headers by about a third and the metadata is carried with every record in the
pipeline, which is the other reason the option is off by default.

### Destination: `mirror` mode

New destination option `mirror` (bool, default `false`). Decided (Decisions, item 1): it replaces the four
booleans the first drafts proposed (`preserveHeaders`, `preserveTimestamp`, `preservePartition`,
`preserveValue`). The four are only useful together for a mirror, the validation rules couple them
(partition preservation needs `acks=all`, timestamp preservation needs no `deliveryTimeout`, value
preservation needs the format untouched), and a half-enabled mirror silently produces something that is not
one. `allowLossyHeaders` and `allowUncheckedTopicConfig` stay as separate explicit escape hatches, valid only
with `mirror`. Default `false` means no change for anyone who does not set it.

| Option | Type | Default | Limits | Meaning |
| --- | --- | --- | --- | --- |
| `mirror` | bool | `false` | none | Enables header, timestamp, partition and value passthrough and every check below. Requires `acks=all`; rejects `deliveryTimeout` > 0 and a user-set `sdk.record.format` |
| `allowLossyHeaders` | bool | `false` | only with `mirror` | Rebuild headers from `kafka.header.<k>` when `kafka.headers` is absent |
| `allowUncheckedTopicConfig` | bool | `false` | only with `mirror` | Skip every topic-config check when DescribeConfigs is not authorized |
| `produceTimeout` | duration | `2m` | min `30s`, max `1h`, `0` rejected, hard error outside the range | Connector-side bound on a `Write` (enqueue plus confirmation) |
| `topicConfigRefreshInterval` | duration | `5m` | min `10s` | How often cached topic configuration is re-read |
| `topicConfigRefreshMaxFailures` | int | `3` | min `1` | Consecutive failed refreshes tolerated before the pipeline halts |
| `maxMessageBytesOverride` | int | `0` (use the topic's `max.message.bytes`) | `>= 0` | Replaces the topic limit in the size check |

The `produceTimeout` default and range are decided (Decided by DeVaris 2026-10-14, Decisions, item 3) as
starting defaults; none of the three numbers was measured. The defaults for `topicConfigRefreshInterval`
and `topicConfigRefreshMaxFailures` are proposals, not measured.

Behaviours under `mirror`:

- Headers: write headers from `kafka.headers`, in order, repeats intact, values decoded from base64, `v: null`
  written as a null header value. If `kafka.headers` is present but malformed, fail the record
  (`KAFKA_HEADERS_INVALID`); never skip silently. If `kafka.headers` is absent, fail the record with
  `KAFKA_HEADERS_MISSING` (suggested fix: set `emitMirrorMetadata: true` on the source). With the source option
  on, an empty header list is `[]`, not absent, so absence always means the source did not emit it. The only
  exception is the explicit `allowLossyHeaders` (bool, default `false`): then a record without
  `kafka.headers` is written with headers rebuilt from its `kafka.header.<k>` entries (string values, repeats
  collapsed, binary values as the source stringified them, order not defined), a warning is logged once per
  pipeline and a counter is incremented. Without `allowLossyHeaders` nothing lossy ever happens silently.
- Timestamp: set the Kafka record timestamp from `kafka.timestamp`. Missing or unparsable `kafka.timestamp`:
  fail the record. Timestamp handling has more failure modes than the draft assumed; see _Timestamp
  preconditions_ below.
- Partition: write to the partition number in `kafka.partition`. Implemented by creating the franz-go client
  with `kgo.ManualPartitioner()`, which makes the client use `Record.Partition`. The draft proposed a wrapper
  around the default partitioner so that records without `kafka.partition` would still work. That is
  unnecessary: a record without a valid `kafka.partition` fails (`KAFKA_PARTITION_INVALID`), so every record
  on a mirror client carries a partition. The spike showed that both approaches work with the idempotent
  producer on both brokers, and that a wrapper is easy to get wrong (it must forward `PartitionByBackup` and
  `OnNewBatch` or the client panics), so the simpler client-wide option is chosen. A unit test asserts that a
  record with `Partition: 3` and a key lands on partition 3, and that the same record on a default client does
  not (Spike results 1: the default partitioner ignores `Record.Partition`).
- Key and value: see _Key and value rules_.
- Validation: `mirror` requires `acks=all` (idempotent writes), requires `deliveryTimeout` unset or zero (see
  below), rejects a user-set `sdk.record.format` (see _Key and value rules_), and requires `produceTimeout`
  within its range. Each fails config validation with an error that names the option.
Partition count checks, in order:

1. At `Open`, for a static `topic`, the destination fetches topic metadata. In `mirror` mode
   it does not auto-create the topic (`AllowAutoTopicCreation` is omitted from the client options when
   `mirror` is set; `franz.go:50` today always sets it), because a broker-default topic
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

Many-to-one templates: if the template maps several source topics onto one destination topic, partition
numbers collide. Source topic A partition 2 and source topic B partition 2 both land on destination
partition 2 and interleave there. Order within each source partition still holds (each is written in
order to one partition), but the destination partition no longer corresponds to one source partition, and
nothing orders A's records against B's. The partition-count check (step 2 above) is keyed on the
destination topic and uses the largest `kafka.partitions` seen for it, so it fails when any one of the
merged sources has more partitions than the destination. Documented; not prevented, because a merge may be
intended.

#### Key and value rules

Under `mirror` the destination bypasses `sdk.record.format` and the key encoder and writes bytes taken
directly from the record, so neither can mangle them (this removes both hazards of the template recipe
below). Rules, each a fail-the-record error with a stable code, never a coercion:

- `Payload.After` that is not `opencdc.RawData` (structured data, for example after a processor
  parsed it) is rejected, `KAFKA_MIRROR_PAYLOAD_NOT_RAW`. A mirror must not re-serialize.
- Value: `kafka.value.null=true` writes a null value (tombstone); the record must then have an empty or
  nil `After`, otherwise `KAFKA_MIRROR_VALUE_CONFLICT`. `kafka.value.null=false` writes `After` verbatim,
  and an empty `After` is written as an empty non-null value. If `kafka.value.null` is absent and `After`
  is empty or nil, the record fails with `KAFKA_MIRROR_VALUE_NULLNESS_UNKNOWN`: it is never silently
  written as empty, because null versus empty can be lost on the way (an unverified risk across the gRPC
  hop, see What was not verified) and writing the wrong one turns a value into a tombstone or the reverse.
  If `kafka.value.null` is absent and `After` is non-empty, it is written verbatim.
- Key: `kafka.key.null=true` writes a null key; the record's key must then be empty or nil
  (`KAFKA_MIRROR_KEY_CONFLICT`). `false` writes the key bytes verbatim, an empty key as an empty non-null
  key. Absent with an empty or nil key fails `KAFKA_MIRROR_KEY_NULLNESS_UNKNOWN`. A non-`RawData` key is
  rejected (`KAFKA_MIRROR_PAYLOAD_NOT_RAW`).
- A user-set `sdk.record.format` or `sdk.record.format.options` with `mirror` is a config validation
  error, `KAFKA_MIRROR_FORMAT_SET`, because it would be silently ignored. Whether the destination can
  distinguish an explicit setting from the SDK default is unverified. If it cannot, the check is "differs
  from the default `opencdc/json` with default options". An explicit setting equal to the default is then
  accepted, which is harmless: the format is ignored in this mode either way.
- Schema-attached records are rejected as not raw. The SDK destination middleware decodes the key and
  payload of a record that carries a schema subject and version into `opencdc.StructuredData` by default
  (`sdk.schema.extract.key.enabled` and `sdk.schema.extract.payload.enabled`, both default `true`; read
  from `conduit-connector-sdk` `destination_middleware.go`, not run). A decoded payload or key is not
  `opencdc.RawData`, so the record fails with `KAFKA_MIRROR_PAYLOAD_NOT_RAW`, the same code as any other
  structured payload. Suggested fix in the error: do not enable `sdk.schema.extract.*` on the mirror
  source, and set both `sdk.schema.extract.*.enabled` to `false` on the destination. Mirror mode never
  re-encodes a schema'd record, because that would be silent re-serialization (invariant 6).

Honest tension, flagged for review: a literal "empty `After` without `kafka.value.null` fails" rule would
make a genuine empty value unmirrorable if the source only marked nulls. That is why the source emits
`kafka.value.null`/`kafka.key.null` as `true` or `false` always, and the destination fails on absence
rather than on emptiness. This changes the first-round format ("present only when null").

#### Topic configuration checks

On the first record for each destination topic the destination reads the topic's configuration with
DescribeConfigs (once, cached) and applies the rules below. The cache is refreshed every
`topicConfigRefreshInterval`, and immediately after a broker `INVALID_TIMESTAMP` or `MESSAGE_TOO_LARGE`
error, because topic configuration can change after `Open` (an operator lowers `retention.ms`, flips the
timestamp type, shrinks `max.message.bytes`). A changed configuration applies to records prepared after
the refresh; records already enqueued, and records produced between two refreshes, are not re-checked,
so the retention rule (which the broker does not reject but silently honours) has an exposure window of up
to the interval. That window is stated, not closed. Checks run only when `mirror` is on.

Refresh failure handling. The destination keeps the last successfully read configuration for each topic.
Every DescribeConfigs outcome is classified with the rules below, evaluated in this order. The types are read
from kadm v1.17.2 (`configs.go`, `errors.go`) and kerr in franz-go v1.20.7; only the authorization case was
run (Spike results 9).

1. Top-level error from `kadm.DescribeTopicConfigs`:
   - `errors.As(err, &authErr)` with `*kadm.AuthError`: authorization failure (`kadm` raises it for
     `CLUSTER_`, `TOPIC_`, `GROUP_`, `TRANSACTIONAL_ID_` and `DELEGATION_TOKEN_AUTHORIZATION_FAILED`). Fail
     immediately, at the first read or at any refresh, with `KAFKA_TOPIC_CONFIG_UNREADABLE`. A config that was
     readable and stops being readable means the ACLs changed, which is an operator action to surface, not
     to ride out.
   - `errors.As(err, &shardErrs)` with `*kadm.ShardErrors`: request-level failure (connection, timeout, a
     shard that could not be mapped to a broker). `ShardErrors.Unwrap() []error` returns every shard's error
     (kadm v1.17.2 `errors.go`, read), so `errors.Is` and `errors.As` see through it. Before treating it as
     transient, test the shards: if any wraps `kerr.SaslAuthenticationFailed`, `kerr.UnsupportedSaslMechanism`
     or `kerr.IllegalSaslState` (kgo returns the `kerr` value from the SASL handshake unwrapped,
     `kgo/broker.go`, read), or `kgo.ErrClientClosed`, or is a `*kadm.AuthError`, it is fail-fast like the
     top-level cases: credentials will not heal by waiting, and a closed client will not reopen. Otherwise it
     is transient at a refresh. Context cancellation is not counted: it means shutdown. A SASL failure that
     the client-side mechanism reports as a plain error, or the `ErrFirstReadEOF` "is SASL missing?" guess,
     cannot be told apart from a network error, so it stays transient and is bounded by
     `topicConfigRefreshMaxFailures`.
   - Any other error type: not classified, so not assumed transient. Fail fast.
2. No error at the top level, then inspect the entry for the topic in the returned `ResourceConfigs`. A
   missing or empty entry is unreadable (fail). Otherwise read `ResourceConfig.Err` (`kadm` fills it with
   `kerr.ErrorForCode`), in this order:
   1. `UNKNOWN_TOPIC_OR_PARTITION`: the topic is missing. Fail immediately with
      `KAFKA_DESTINATION_TOPIC_MISSING`, at a refresh as well as on the first read. This is checked before
      `kerr.IsRetriable`, because kerr marks `UNKNOWN_TOPIC_OR_PARTITION` retriable and the retriable rule
      would otherwise retry a deleted topic until the failure budget ran out.
   2. `kerr.IsRetriable(err)` true: transient at a refresh (for example `LEADER_NOT_AVAILABLE`).
   3. Anything else (non-retriable, non-auth, for example `INVALID_REQUEST`, `INVALID_TOPIC_EXCEPTION`):
      fail fast with `KAFKA_TOPIC_CONFIG_UNREADABLE`, at a refresh too. One rule, chosen because retrying a
      request the broker has refused as invalid cannot succeed and only delays the halt.
3. The first read for a topic has no last good config to fall back on, so a transient outcome there also
   fails the record with `KAFKA_TOPIC_CONFIG_UNREADABLE`.
4. A transient outcome at a refresh keeps the last good config. The destination logs a warning, increments
   a counter, and retries the read no later than 10 s after the failure instead of waiting a full interval.
   After `topicConfigRefreshMaxFailures` consecutive failed reads (int, default `3`, minimum `1`) it fails the
   next record with `KAFKA_TOPIC_CONFIG_UNREADABLE`, naming the last error and the failure count. A
   successful read resets the count.
The cost of keeping the last good config is a longer exposure window for the retention rule: up to
`topicConfigRefreshInterval` plus `topicConfigRefreshMaxFailures` times 10 s. Stated, not closed.

DescribeConfigs not authorized: the destination fails with `KAFKA_TOPIC_CONFIG_UNREADABLE` on the first
record for the topic (or at the next refresh), naming the topic and the missing ACL (DESCRIBE_CONFIGS on
the topic). It never warns and continues. The operator can explicitly accept the risk with
`allowUncheckedTopicConfig: true`, which skips every check in this section for that pipeline, logs a
warning at start and once per refresh, and increments a counter; retention, timestamp bounds and size are
then enforced only by the broker (and retention silently). Observed on Kafka 4.0.0 with ACLs
(Spike results 9): the principal's produce and metadata calls succeeded while `kadm.DescribeTopicConfigs`
returned a top-level `TOPIC_AUTHORIZATION_FAILED` error and no resources, so this case is detectable
and is not confused with an empty result. Other errors on the first read also fail the record; at a
refresh they follow the transient rules above.
Brokers other than Kafka 4.0.0 (Redpanda, older Kafka, managed services) were not tested; if one returns
success with an empty config list the destination treats that as unreadable too.

##### Timestamp preconditions

Spike results (below) found three ways timestamp preservation can silently or noisily misbehave.

| Condition | Behaviour |
| --- | --- |
| `message.timestamp.type=LogAppendTime` | Fail with `KAFKA_TIMESTAMP_TYPE_LOG_APPEND`. The broker would overwrite the preserved value, so preservation cannot be honoured. Suggested fix: use a `CreateTime` topic or turn off `mirror` |
| `message.timestamp.before.max.ms` / `after.max.ms` (Kafka 3.6+) or `message.timestamp.difference.max.ms` (before 4.0) is finite and the record timestamp is outside the window | Fail the record before producing, `KAFKA_TIMESTAMP_OUT_OF_RANGE`, naming the config. Kafka rejects the whole produce batch with `INVALID_TIMESTAMP` if one record is out of range (spike), so catching it client-side keeps valid neighbours out of the failure |
| `cleanup.policy` contains `delete` (`delete` or `delete,compact`), `retention.ms` is finite and the record timestamp is older than `now - retention.ms` | Fail the record, `KAFKA_TIMESTAMP_OLDER_THAN_RETENTION`. Kafka's time-based retention is evaluated on record timestamps: in the spike 400 records with a CreateTime 10 days old on a topic with `retention.ms=90000` were deleted within the first 10 s sample while the control topic kept its records for about 90 s. The broker acks the write and then deletes the data, which is a silent loss from the mirror's point of view. Suggested fix: raise `retention.ms` on the destination topic or turn off `mirror` |
| `cleanup.policy=compact` only | Time retention does not apply, so the retention row is skipped (read from Kafka documentation; not run in the spike). Old timestamps are not rejected. Compaction behaviour that keys off timestamps (`min.compaction.lag.ms`, `max.compaction.lag.ms`, `delete.retention.ms` for tombstones) can make an old-timestamped record or tombstone eligible for cleaning immediately; the mirror source has the same property, so this is faithful, but it was not observed and a test is planned. Per-key order on a compacted topic follows offset order, not timestamp order, and a mirror writes records in offset order |
| DescribeConfigs not authorized | Fail, `KAFKA_TOPIC_CONFIG_UNREADABLE`, unless `allowUncheckedTopicConfig` (see _Topic configuration checks_). Never warn and skip. Other DescribeConfigs errors also fail |
| `max.message.bytes` (topic level) | See below |
| Redpanda | v25.1.5 does not return `message.timestamp.before.max.ms`/`after.max.ms`/`difference.max.ms` from DescribeConfigs and did not enforce them in the spike, so only the `message.timestamp.type` and `retention.ms` rules apply. Redpanda retention behaviour with old CreateTime was not observed (see What was not verified) |

##### Record size

`max.message.bytes` is a topic-level config read by the same DescribeConfigs call. On Kafka it limits the
record batch as the broker receives it, after compression (read from Kafka documentation; the spike
rejected a single oversized record, `MESSAGE_TOO_LARGE`, but did not test a batch of small records over
the limit). Headers count toward it, and mirror mode adds headers a default pipeline does not write, so a
record that fit before can now fail. The destination checks in `prepareRecord`:

- Estimated record size (key + value + sum of header key and value lengths + 16 bytes per header + 64
  bytes of record and batch overhead; a conservative estimate, the exact framing was not measured)
  against the lower of the topic `max.message.bytes` and the client `batchBytes` (default 1000012,
  `destination/config.go:57`, passed to `ProducerBatchMaxBytes`). The estimate is of the uncompressed
  size. For `batchBytes` that is correct: franz-go applies `ProducerBatchMaxBytes` before compression
  (read from the franz-go docs for `ProducerBatchMaxBytes`, not run). For the topic limit it is
  conservative, because the broker applies `max.message.bytes` to the batch after compression (Kafka
  documentation), so a well-compressing record that the broker would accept can be rejected here. The
  override is `maxMessageBytesOverride`: when non-zero it replaces the topic's `max.message.bytes` in both
  this check and the `batchBytes` check below, so an operator who knows their data compresses can raise it
  and let the broker be the judge. The broker's `MESSAGE_TOO_LARGE` then remains the backstop and halts the
  pipeline as described in _Ordering_. The `batchBytes` cap itself is client-side and not overridden by
  this option. Over: the record fails,
  `KAFKA_RECORD_TOO_LARGE`, with the sizes and both limits. Because the check is in `prepareRecord`, the
  batch stops there and later records are not enqueued (see _Ordering_).
- At the first record for a topic, if the client `batchBytes` is larger than the topic `max.message.bytes`,
  fail with `KAFKA_BATCH_BYTES_EXCEEDS_TOPIC_MAX`, suggesting `batchBytes` at or below the topic limit.
  Otherwise the client can assemble a batch that the broker rejects as a whole after enqueue, which is
  the reorder case in _Ordering_.
- The broker's own limit on the producer connection (`message.max.bytes` and the client-side
  `BrokerMaxWriteBytes`) is a second limit not read from the topic; it is the backstop and the reason the
  broker error still halts the pipeline.

##### Delivery timeout and bounded waits

`mirror` requires `deliveryTimeout` to be unset or zero. franz-go measures
`RecordDeliveryTimeout` from `Record.Timestamp` (`kgo/sink.go`, `isTimedOut`), so any record whose
preserved timestamp is older than the timeout fails immediately with "records have timed out before
they were able to be produced" (spike, re-run on v1.20.7 in Spike results 10), and franz fails every
buffered record in that partition with it. Config validation rejects the combination with
`KAFKA_DELIVERY_TIMEOUT_WITH_TIMESTAMP`.

Without any bound, a produce to an unreachable broker waits forever: the pipeline stalls silently, loses
nothing, and shows no error. Round 3 replaced that with a connector-side bound that does not read
`Record.Timestamp`, and spiked it (Spike results 10).

`produceTimeout` (see _Destination_ for the option and its range). Mirror mode only; outside it
`deliveryTimeout` behaves as today. Mechanism:

1. Every produce uses `Client.Produce` with a per-record callback, including the single-record path,
   which today uses `ProduceSync` (`destination/franz.go`). `ProduceSync` waits on the promises and does
   not honour its context while waiting.
2. One timer per `Write` call starts before the first enqueue and covers the enqueue loop as well as the
   wait for callbacks. `Client.Produce` blocks when the client buffer is full (franz-go v1.20.7
   `kgo/producer.go`, read; observed on franz-go v1.20.7 in a reviewer's spike, see _What was not
   verified_): it returns when space frees up, when the passed context is done, or when the client is
   closed. So each `Produce` gets a context that carries the timer's deadline, and the timer's expiry also
   closes the client, which releases a `Produce` stuck in the enqueue loop. The timer is never derived
   from `Record.Timestamp`. Old `CreateTime` values have no effect on it.
3. Poison flag. The connector holds an atomic flag (or `sync.Once` around `Close`) that is set before
   `Client.Close()` is called and checked before every `Produce`. A produce on a poisoned producer returns the
   poisoned error and never reaches the client, because a `Produce` issued after `Close()` can go without ever
   calling back (observed on franz-go v1.20.7, see _What was not verified_), and a `ProduceSync` in that
   state deadlocks (reported).
   The flag check does not close the race with a concurrent `Close()` (for example from `Teardown`) between
   the check and the `Produce`, so the wait never depends on callbacks alone: it selects on all callbacks
   done, the timer, `ctx.Done()` and a `closed` channel that the connector closes inside the same
   `sync.Once` as `Client.Close()`. When a `Produce` loses the race with `Close()` and never calls back,
   the `closed` case ends the wait at once instead of leaving it to the timer.
   No mutex is held across a `Produce` call or across the wait; the flag is the only shared state on that
   path, and results are written to per-index slots that the waiter reads after it stops waiting.
4. When the waiter stops waiting for any reason other than all callbacks done (timer expiry or `ctx.Done()`),
   it evaluates the slots once and does the same thing on both paths. If every promise has already resolved
   with a nil error, `Write` returns nil: the records are confirmed, nothing is nacked, and the producer is not
   closed. Otherwise the connector poisons the producer and calls `Client.Close()`, which fails every
   unresolved promise with "client closed" at once (observed, Spike results 10), and nacks from the index of
   the first record whose promise did not resolve successfully. On timer expiry the error is
   `KAFKA_PRODUCE_TIMEOUT`; on `ctx.Done()` it is the context error. A cancelled context is not left
   half-handled: abandoning the wait without closing would leave in-flight produces running whose records
   the engine has already been told failed, and the next `Write` would race them.
   The closed channel case (point 3) takes the same path with the `closed` error.
   Records before that index are acked, the rest are nacked (the SDK acks `batch[:n]` and nacks `batch[n:]`).
   This relies on per-index results, i.e. the #308 fix. Callbacks that run after the waiter returned write
   to slots nobody reads; they cannot change the result.
5. Every later produce call on the poisoned producer returns the same error without touching the client.
   The pipeline halts under the error policy. `Teardown` then closes the client; the connector routes that
   through the same `sync.Once`, so a second `Close` never happens (whether franz-go's `Close` is idempotent
   was not tested).
6. A record is acked only if its own promise resolved with a nil error. A timeout, a close or an abort is
   never an ack (invariant 1).

Records that were reported as errors may still have been written. In the paused-broker run the five records
were produced while the broker was paused (presumably accepted into the socket buffers; the mechanism was
not examined), and after unpause all five appeared in the topic although every promise had resolved with an
error. In the stopped-broker run (connection refused) none
appeared. So an error means "not confirmed", and redelivery can produce a duplicate. That is at-least-once,
not loss, and the duplicate carries identical partition, timestamp and headers. It does extend the
reorder caveat in _Ordering_ to the timeout path.

Range of `produceTimeout`, Decided by DeVaris 2026-10-14 (Decisions, item 3): default `2m`, minimum `30s`,
maximum `1h`, `0` rejected. These are starting defaults and have not been measured.

- `0` and any unbounded setting are rejected, and there is no `allowUnboundedProduce` escape hatch. The reason
  is invariant 7: a stuck `Write` has no other bound (see _Shutdown_), so an unbounded option would let a
  dead broker stall a pipeline forever and make a graceful stop impossible. An escape hatch that merely marks
  the connector not Certified would still ship that failure mode to anyone who sets it, and a label is not a
  guard. A finite maximum keeps the worst-case stall visible. Operators who need to ride out a long broker
  outage rely on halt and restart, not on waiting inside `Write`.
- Maximum `1h`: finite and deliberately generous, so it does not constrain real deployments; it is a ceiling
  on silent stall, not a tuning target. Not measured.
- Minimum `30s` is a hard config error, not a WARN. Expiry closes the client and halts the pipeline, so a
  too-small value turns a leader election or a slow broker into an outage; a warning in a log is easy to miss
  in a mode where the consequence is a halted mirror. `30s` equals the engine's `exitTimeout`. Trading a
  lower floor for finishing a stuck `Write` inside the graceful window would halt pipelines on ordinary broker
  hiccups, and the unfinished-shutdown case is already covered by at-least-once recovery (_Shutdown_). The
  value is a judgement, not derived from a measurement.
- Violations fail config validation with `KAFKA_PRODUCE_TIMEOUT_OUT_OF_RANGE`, the option path and the
  allowed range.

A bound that is too small still turns a slow broker into a halted pipeline. `produceTimeout` should be well
above the broker's worst healthy produce latency.

###### Shutdown

Read from the engine (`conduit` `pkg/lifecycle/stream/destination.go`, `pkg/connector/destination.go`,
`pkg/lifecycle/service.go`, `pkg/conduit/runtime.go`) and from `conduit-connector-sdk` v0.15.0
`destination.go` (the version Conduit `main` requires; it includes `conduit-connector-sdk#401`). All of it
is read, not run:

- The engine's `Destination.Write` does not call the connector's `Write`. It only hands the record to the
  gRPC stream: `stream.Send` (`pkg/connector/destination.go`, `Write` at line 239, the `Send` at line 251). The
  connector's own `Write(ctx)` runs inside the SDK `Run` loop (`destinationPluginAdapter.Run`,
  `destination.go:188`, the write call at line 202) with the stream context.
- The destination node's cleanup runs only after its message loop has returned
  (`pkg/lifecycle/stream/destination.go`, deferred func): `Destination.Stop`, then `openMsgTracker.Wait()`
  (all open messages acked or nacked), then `Destination.Teardown`, where the connector's `Close` runs. The
  real stall points are `Destination.Stop`, in which the SDK waits up to `stopTimeout` (1 m,
  `source.go:38`, used at `destination.go:254`) for the last position to be written and then up to 1 m to
  flush (`destination.go:269`), and `openMsgTracker.Wait()`. Both run before `Teardown`.
- SDK v0.15.0 Teardown semantics (`conduit-connector-sdk#401`, `destination.go:283-335`). `Teardown` can
  arrive without a prior `Stop` (an error-triggered stop) while the connector's `Write` is still running.
  The SDK then waits for that `Write` for at most `min(ctx, destinationTeardownTimeout)`, 1 m
  (`source.go:45`), and refuses new ones: records received after `Teardown` started are never written and
  are left unanswered, neither acked nor nacked, so they are redelivered after a restart and never reach a
  DLQ. If the `Write` is still running when the wait expires, the SDK calls the connector's `Teardown`
  anyway, concurrently with that `Write`, returns an error saying so, and does not ack the abandoned
  `Write`'s records whatever it later returns (`writeStrategySingle.Write`, `destination.go:450-500`).
  For mirror mode this means: with the default `produceTimeout` (2 m) longer than the 1 m wait, the
  connector's `Teardown` can run while a `Write` is in flight. `Teardown` closes the client through the
  same `sync.Once` as the timer (point 5 above), which ends the in-flight `Write` through the `closed`
  channel. That `Write`'s records are not acked by the SDK, so they are redelivered: duplicates possible, no
  loss (invariants 1 and 3). The concurrent `Teardown` is why the poison flag and the `closed` channel
  must be safe under `-race` (see Test plan).
- The things that end a stuck connector `Write` are `produceTimeout` expiring, the stream context being
  cancelled (the connector's wait selects on `ctx.Done()`), the connector's `Teardown` closing the client
  after the SDK's 1 m wait (above), or the broker coming back. The legacy lifecycle
  never cancels that context on SIGTERM: `StopAll` is graceful only (`pkg/lifecycle/service.go:661`), and the
  forced stop after a timeout is an open TODO (`service.go:689`). The stream context is cancelled by
  `Teardown` (`stopStream`, `pkg/connector/destination.go`), which runs after the stall.
- On SIGTERM the legacy runtime calls `StopAll` and then waits `exitTimeout`, 30 s
  (`pkg/conduit/runtime.go:85`, `ls.Wait(exitTimeout)` at line 909), after which it logs that pipelines did
  not stop in time and carries on shutting down.
- Arch-v2 differs: `registerCleanupV2` (`runtime.go`, from line 915) calls `StopAll(ctx, false)`, and on
  the last of six 5 s intervals calls `StopAll(ctx, true)`, so the pipelines are force-stopped about 25 s
  into the 30 s window. Whether a forced stop cancels the connector's `Write` context there was not
  examined.
- SIGTERM with the broker unreachable therefore does not complete a graceful drain on the legacy engine: the
  default `produceTimeout` (2m) is longer than `exitTimeout`, so the runtime stops waiting first. For that
  pipeline this is equivalent to `kill -9`, which invariant 7 already requires to be recoverable. Nothing is
  acked for records the broker did not confirm, the position does not advance past the last confirmed
  record, and the restart re-reads from the committed offset. Duplicates possible, no loss. What is lost
  is only the final checkpoint flush.
- `Client.Close()` itself is not the long pole: observed under 10 ms with the broker paused and with it stopped,
  with no context, and it made a `ProduceSync` that had been blocked for 25 s return `client closed`.
- `kill -9` is unchanged: unacked records are re-read.
- Not verified: the real shutdown time through a running pipeline, on either engine. The test plan adds a
  pipeline-level test.

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

The decided `mirror` mode (_Decisions_, item 1) supersedes this recipe for mirroring:
it writes `Payload.After` bytes directly and fixes both the empty-value and the silent-fallback hazards.
The recipe findings stay as a record of why. Pipelines whose source topics can contain empty
non-null values must not use the template recipe. The README recipe, when written, states this limit.
The shipping example matches the decided design: source `emitMirrorMetadata: true`, destination
`mirror: true`, no `sdk.record.format`, and the strict DLQ config from _Error policy_. It comes with a test
that fails if any value written to the destination differs from the source value (see Test plan).

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

This reorder is not introduced by this change. The destination's batch path today enqueues every record
of the batch, collects the results, and returns the index of the first error (`destination/franz.go`
`Produce`, results collected in completion order, which is the #308 problem), so a broker rejection after
enqueue already leaves the later records of the batch written and then redelivered, with the same
duplicate and reorder, for any pipeline, mirror or not. That is read from the connector code; the spike
reproduced it with franz-go directly, not through the connector. What this change adds is more ways for
a record to be rejected (size with headers, timestamp bounds), which is why those checks move before
enqueue.

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
- `mirror` requires `acks=all`. With `acks` below `all` the connector disables idempotence
  (`franz.go:58-61`) and a retry can reorder; config validation rejects the combination, naming
  `acks`. Idempotence was confirmed active in the spike (a producer ID was allocated on both brokers).
- Records from different source partitions may interleave arbitrarily; that is also true of MM2.

Sign-off, stated plainly. Decided by DeVaris 2026-10-14 (Decisions, item 6): invariant 4 holds per
source partition only until the first broker or network error after enqueue on that destination
partition. After such an error, the partition may hold later records before an earlier one, and
duplicates. This is accepted, on three conditions, all part of this change:

1. A loud metric. Every produce error reported by the broker or client after enqueue increments
   `kafka_destination_post_enqueue_errors_total`, labelled with the destination topic and partition. Any
   increase means that partition may now be out of order, and an alert on any increase is the
   detection. A gauge that stays set until re-seed was considered and dropped: the pipeline halts and
   restarts, the connector keeps no state across a restart, and a gauge that silently resets to 0 on
   restart would hide the condition it reports. The counter's increase survives in the metrics store.
2. A WARN. The connector logs at WARN, once per error, naming the topic, partition, the first nacked
   record's position and the error, with the text "per-partition order is no longer guaranteed for this
   destination partition; re-seed it". The pipeline also halts (_Error policy_), so the error shows as a
   pipeline failure as well.
3. A documented remedy. Re-seed the affected destination partition. Kafka cannot truncate the tail of a
   partition (`kafka-delete-records` only advances the log start offset), and a single partition cannot be
   re-created, so re-seeding means: stop the pipeline, delete and re-create the destination topic with the
   same partition count (or point the pipeline at a new topic), reset the source consumer group to the
   earliest offset the source topic still retains, and restart. If the source has already expired records
   that the destination held, a full re-seed is not possible; the runbook then says so, and the choice is
   between keeping the out-of-order partition (consumers must tolerate it) and re-seeding what is left. The
   runbook entry under `docs/operations/` gives the exact commands for both brokers.

Where the metric cannot be exported, see _Observability_: the export path is an open prerequisite, and
until it exists the WARN is the only signal, which is not enough for Certified.

Records are not re-keyed. Key bytes are written as received, so consumers that re-hash see the same
key. Because partition is preserved explicitly, a destination with a different partition count still
preserves the source mapping for the partitions that exist; it does not preserve key-hash locality,
which is documented.

### Error policy: halt, do not dead-letter

The errors this design adds are not per-record data problems; they mean the destination topic or the
pipeline is configured wrongly (`KAFKA_PARTITION_OUT_OF_RANGE`, `KAFKA_PARTITION_COUNT_MISMATCH`,
`KAFKA_PARTITION_INVALID`, `KAFKA_HEADERS_INVALID`, `KAFKA_HEADERS_MISSING`, `KAFKA_TIMESTAMP_*`,
`KAFKA_DESTINATION_TOPIC_MISSING`, `KAFKA_TOPIC_CONFIG_UNREADABLE`, `KAFKA_PRODUCE_TIMEOUT`). They must
stop the pipeline, not be routed to a DLQ.

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
the first draft. Lost: in `mirror` mode a record without a valid partition fails anyway, so the
fallback path is dead code, and the wrapper must forward two extra franz interfaces
(`PartitionByBackup`, `OnNewBatch`) or the client panics (spike). `kgo.ManualPartitioner()` does the job.

**Make the destination preserve everything unconditionally.** Simplest config. Lost: it changes bytes
written by existing pipelines (headers appear, timestamps change, partitioning changes) and breaks
the backward-compatibility constraint; also partition preservation is unsafe by default with
auto-created topics. Rejected in favour of the two opt-in options, `emitMirrorMetadata` on the source and
`mirror` on the destination.

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
| `kafka.partition` >= destination partition count after a metadata refresh | Record fails, `KAFKA_PARTITION_OUT_OF_RANGE`, includes topic, partition, partition count and the config path `mirror`. Suggested fix: pre-create the topic with at least N partitions or disable `mirror`. Without our check franz-go itself fails only that record, immediately, with "invalid record partitioning choice of 9 from 4 available" (Spike results 3); it does not hang or fail neighbours |
| Destination has fewer partitions than `kafka.partitions` | First record for the topic fails, `KAFKA_PARTITION_COUNT_MISMATCH`, before any record is written to it |
| Destination has more partitions than the source | Allowed; warning once per topic; key-hash locality not preserved (documented) |
| Destination topic missing in `mirror` mode | `Open` fails (static topic) or the first record for it fails (templated), `KAFKA_DESTINATION_TOPIC_MISSING`; no auto-creation in this mode |
| Destination topic grown while running | Metadata refresh as described above; records are not failed until the bounded retry expires |
| `kafka.partition` missing or non-numeric in `mirror` mode | Record fails, `KAFKA_PARTITION_INVALID`; never defaults. Fix names `emitMirrorMetadata` on the source |
| `kafka.headers` malformed (bad JSON, bad base64, unknown or duplicate fields, wrong types) | Record fails, `KAFKA_HEADERS_INVALID`; never dropped |
| `kafka.headers` absent in `mirror` mode | Record fails, `KAFKA_HEADERS_MISSING`, unless `allowLossyHeaders` is set, in which case headers are rebuilt from `kafka.header.*`, one warning, counter incremented |
| Header null versus empty value | Both preserved: `v: null` and `v: ""` round trip as null and empty (spike) |
| Header with empty key or key that is not UTF-8 | Both brokers accept and return these (spike); carried as base64 key bytes, no failure |
| Header value (or key) not valid UTF-8, `emitMirrorMetadata` on | Legacy `kafka.header.<k>` entry omitted, raw bytes kept in `kafka.headers`, `kafka_source_legacy_header_omitted_total` incremented, WARN once per key. Never escaped. Without the omission the record fails `proto.Marshal` (verified) |
| Same, `emitMirrorMetadata` off | Unchanged from today: the legacy entry carries invalid UTF-8 and the record cannot cross a protobuf hop. Pre-existing, outside this design, separate issue |
| Header total size exceeds topic `max.message.bytes` / client `batchBytes` | See _Record size_: checked in `prepareRecord`, fails that record and stops the batch; the broker error is the backstop |
| `message.timestamp.type=LogAppendTime` on the destination topic | Record fails, `KAFKA_TIMESTAMP_TYPE_LOG_APPEND` (see _Timestamp preconditions_) |
| Record timestamp outside `message.timestamp.before/after.max.ms` (or `difference.max.ms`) | Record fails client-side, `KAFKA_TIMESTAMP_OUT_OF_RANGE`. If the broker still returns `INVALID_TIMESTAMP` (config changed, or not described), the whole produce batch fails on Kafka, including valid records in it (spike); the destination re-reads the topic config once and reports the config values |
| Record timestamp older than `now - retention.ms` | Record fails, `KAFKA_TIMESTAMP_OLDER_THAN_RETENTION`. Kafka deletes such records within seconds of the segment closing, regardless of how recently they were written (spike) |
| Broker unreachable or paused while records are in flight (mirror mode) | `produceTimeout` expires (the timer covers the enqueue loop too), the producer is poisoned and closed, `KAFKA_PRODUCE_TIMEOUT`, unconfirmed records nacked, none acked. Reported-error records may still land (paused-broker spike): duplicates on redelivery, no loss. Pipeline halts |
| SIGTERM with in-flight produces and an unreachable broker | The engine does not run `Stop`, the open-message wait or `Teardown` until the connector's `Write` returns (read), so only `produceTimeout` ends a stuck `Write`. The runtime stops waiting at `exitTimeout` (30 s, read), earlier than the default `2m`: that is a hard stop for the pipeline. Unconfirmed records are not acked, position not advanced, restart re-reads: duplicates, no loss. See _Shutdown_ |
| Transient DescribeConfigs failure at refresh (`*kadm.ShardErrors`, retriable code) | Last good config kept, warning and counter, retry within 10 s; `KAFKA_TOPIC_CONFIG_UNREADABLE` after `topicConfigRefreshMaxFailures` consecutive failures. Authorization errors and non-retriable codes fail immediately; `UNKNOWN_TOPIC_OR_PARTITION` is a missing topic, not transient |
| Schema-attached record (decoded to structured data by the SDK) | Record fails, `KAFKA_MIRROR_PAYLOAD_NOT_RAW`; fix: disable `sdk.schema.extract.*` |
| `mirror` with `deliveryTimeout` > 0 | Config validation fails, `KAFKA_DELIVERY_TIMEOUT_WITH_TIMESTAMP`; otherwise franz-go times out old-timestamp records immediately (spike) |
| `produceTimeout` of `0`, below `30s` or above `1h` | Config validation fails, `KAFKA_PRODUCE_TIMEOUT_OUT_OF_RANGE`, naming the option and the allowed range; no unbounded setting exists |
| `Produce` after the producer was closed (timeout or `Teardown`) | Poison flag checked before every produce: returns the poisoned error without calling the client. The wait never relies on callbacks alone, so a late or racing produce cannot hang the call |
| Timer fires but every promise resolved nil | `Write` returns success; nothing is nacked and the producer is not closed |
| DescribeConfigs not authorized (`*kadm.AuthError`) | Record fails, `KAFKA_TOPIC_CONFIG_UNREADABLE`, unless the operator set `allowUncheckedTopicConfig` (warning at start and per refresh, counter). Never warn and skip by default. Transient errors are handled separately (row above) |
| Topic config changes after `Open` | Picked up at the next `topicConfigRefreshInterval` refresh or after a broker `INVALID_TIMESTAMP`/`MESSAGE_TOO_LARGE`; exposure window up to the interval, notably for `retention.ms` which the broker never rejects |
| `cleanup.policy=compact` | Retention check skipped; timestamp-keyed compaction behaviour documented, not observed |
| Record plus headers over topic `max.message.bytes` or `batchBytes` | Record fails in `prepareRecord`, `KAFKA_RECORD_TOO_LARGE` (uncompressed estimate; `maxMessageBytesOverride` relaxes the topic part); `batchBytes` above the topic limit fails the first record, `KAFKA_BATCH_BYTES_EXCEEDS_TOPIC_MAX` |
| Empty or nil value/key without `kafka.value.null`/`kafka.key.null`, conflicting marker and bytes, non-`RawData` payload, user-set `sdk.record.format` | Record fails (or config validation fails) with the `KAFKA_MIRROR_*` codes in _Key and value rules_; never written as empty or re-serialized |
| Many source topics onto one destination topic | Partition numbers collide and records interleave on the destination partition; allowed, documented |
| Broker rejects a record after enqueue (for example `MESSAGE_TOO_LARGE`, auth, network) | Pipeline halts. Per-partition order is not guaranteed after this: later records of the batch may already be in the partition and are redelivered (Spike results 4, _Ordering_). `kafka_destination_post_enqueue_errors_total` increments for the partition, WARN logged. Remedy: re-seed that destination partition (accepted, Decisions, item 6) |
| Crash (`kill -9`) mid-batch | Unacked records are re-read from the committed consumer-group offset and produced again: duplicates, no loss. Preserved fields are derived from the record, so a duplicate carries identical partition, timestamp and headers and lands in the same partition |
| Duplicate delivery in `mirror` mode | A re-produced record is appended again to the same partition (at-least-once). Consumers need idempotence, as with any at-least-once mirror |
| Batch produce returns results out of order | Fixed by `conduit-connector-kafka#312` (#308), released in v0.12.6 and v0.13.0; prerequisite satisfied |
| Source `emitMirrorMetadata` off or older source version | `mirror` fails loudly with the codes above; nothing is written without the preserved field |
| Empty non-null value mirrored through the template recipe | Becomes a tombstone (spike). Superseded by `mirror` mode; see the recipe section |
| Template recipe fails to execute | `Record.Bytes()` falls back to the OpenCDC envelope silently (spike). Not possible in `mirror` mode, which bypasses the format |

All new errors carry a stable code, the config path and a suggested fix, per the project error
convention.

## Compatibility, upgrade and rollback

- N+1 readers: new metadata keys are additive and, being opt-in at the source, absent unless the
  operator asks for them; old destinations ignore them. A new destination with `mirror` `false`
  behaves exactly as today, and a new source with `emitMirrorMetadata=false` emits exactly what it does
  today, so no existing pipeline's output bytes change.
- A new destination reading records from an old source, or from a new source with the option off, sees
  no `kafka.headers`, `kafka.partition` or `kafka.timestamp`: with `mirror` on it fails
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
  `allowUncheckedTopicConfig` in use (also once per refresh), a DescribeConfigs refresh that failed
  transiently (each time, with the consecutive count). Fail-fast `KAFKA_TOPIC_CONFIG_UNREADABLE` is an
  error, not a warning.
- Metrics. Names follow the engine's Prometheus style (`snake_case`, `_total` for counters), prefixed
  with the connector name. Every metric carries the pipeline ID as a label.

  | Metric | Type | Labels | Meaning |
  | --- | --- | --- | --- |
  | `kafka_destination_mirror_records_total` | counter | `topic` | Records produced in `mirror` mode with preserved partition, timestamp and headers |
  | `kafka_destination_lossy_header_fallback_total` | counter | `topic` | Records written with headers rebuilt under `allowLossyHeaders` |
  | `kafka_destination_topic_config_refresh_failures_total` | counter | `topic`, `class` (`transient`, `fatal`) | Failed DescribeConfigs reads |
  | `kafka_destination_unchecked_topic_config` | gauge | `topic` | 1 while `allowUncheckedTopicConfig` skips the checks |
  | `kafka_destination_produce_timeouts_total` | counter | `topic` | `produceTimeout` expiries (`KAFKA_PRODUCE_TIMEOUT`) |
  | `kafka_destination_post_enqueue_errors_total` | counter | `topic`, `partition` | Produce errors reported after enqueue; each one voids the ordering guarantee for that partition |
  | `kafka_destination_record_failures_total` | counter | `code` | Records failed, by stable error code |
  | `kafka_source_legacy_header_omitted_total` | counter | `topic` | Legacy `kafka.header.<k>` entries omitted because the header is not valid UTF-8 |

  Export path, an open prerequisite: `conduit-connector-sdk` v0.15.0 has no metrics API for connectors
  (no counter or gauge constructors in the module, checked 2026-10-14), and the engine's registry is in
  `conduit` `pkg/foundation/metrics`, which a standalone connector cannot reach. Until a connector metrics
  path exists, these values are emitted only as structured log fields with the same names. That is not
  enough for `kafka_destination_post_enqueue_errors_total`, which is meant to be alerted on, so the export path blocks
  Certified status for `mirror` mode. It is a cross-repo item to file against the SDK.
- Per-record failures include topic, partition and the record position so an operator can find the
  offending offset.
- Runbook entries under `docs/operations/` for each halt-class code: symptom, diagnosis, remediation
  (pre-create the topic with N partitions, raise `retention.ms`, set the source option).

## Test plan

- Unit: metadata round trip with repeated header keys, binary, empty and null header values, and empty
  and nil header lists; `kafka.headers` decoder rejecting each malformed class listed in _Source_;
  partition parsing; config validation (`mirror` requires `acks=all`, rejects `deliveryTimeout` > 0 and a
  user-set `sdk.record.format`; `allowLossyHeaders` and `allowUncheckedTopicConfig` require `mirror`;
  `produceTimeout` of `0`, `29s` and `1h1s` each fail with `KAFKA_PRODUCE_TIMEOUT_OUT_OF_RANGE`, `30s`, `2m`
  and `1h` pass); a client built in mirror mode honours `Record.Partition` and a default client ignores it.
- Property-based (rapid): encode headers to `kafka.headers` and back is the identity for arbitrary
  byte keys and values, including null versus empty and non-UTF-8 keys, and repeats; header order is
  preserved. Same for null versus empty key and value through `kafka.key.null`/`kafka.value.null`.
- Non-UTF-8 header values: with `emitMirrorMetadata: true`, a record with headers `bin = \xff\xfe\x00`,
  `ok = plain`, and a repeated key whose last value is not valid UTF-8 has no `kafka.header.bin` and no
  legacy entry for the repeated key, has `kafka.header.ok`, carries all three headers byte for byte in
  `kafka.headers`, marshals with `proto.Marshal`, and increments `kafka_source_legacy_header_omitted_total`
  by two. No metadata value contains U+FFFD or an escape. With `emitMirrorMetadata: false` output is
  byte-identical to the previous release (the golden test below). End to end: the record crosses a
  standalone connector's gRPC hop and is mirrored with identical header bytes.
- Ordering observability: the broker-rejection test below also asserts that
  `kafka_destination_post_enqueue_errors_total` increments for that partition only and the WARN names
  the partition.
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
  results) and stays red or skipped with a link to the decision (Decisions, item 1); it is the gate for
  claiming a faithful mirror. A test that a failing template does not write envelopes also belongs here.
- Topic-config tests: DescribeConfigs not authorized fails with `KAFKA_TOPIC_CONFIG_UNREADABLE` and
  `allowUncheckedTopicConfig` lets records through with a warning (ACL setup as in Spike results 9);
  `retention.ms` lowered while running is caught within `topicConfigRefreshInterval`;
  `cleanup.policy=delete,compact` applies the retention rule and `compact` does not; a record with large
  headers over `max.message.bytes` fails in `prepareRecord` and the batch stops there; `batchBytes` over
  the topic limit fails the first record.
- Bounded wait: a producer test with a broker that never answers (a TCP black hole or a paused container)
  asserts `produceTimeout` returns `KAFKA_PRODUCE_TIMEOUT` within the bound plus a small margin, for the
  single-record and batch paths, with records whose `Record.Timestamp` is 10 days old; no promise resolves
  with a nil error after expiry; later calls fail fast. Re-run against a stopped broker (connection refused).
  Also assert that records reported as errors may appear in the topic after the broker returns, and that
  the redelivered copies are byte- and field-identical. Additional cases:
  - Enqueue covered by the timer: client built with `MaxBufferedRecords(1)` and a black-holed broker, a batch of
    several records; the call returns within the bound while still enqueuing, not after.
  - Poison: `Produce` after `Close()` returns the poisoned error without calling `Client.Produce` (assert with a
    fake producer that would never call back), including a `Close()` from `Teardown` racing an in-flight call
    (run under `-race`); no mutex is held across a produce or a wait.
  - Timer fires with every promise already resolved nil: the call returns nil, the producer is not poisoned.
  - Timer fires with a mix: records before the first unresolved index are acked, the rest nacked, and late
    callbacks after the return do not change the result or race.
  - The wait also returns on `ctx.Done()`, and then behaves as on timer expiry: the producer is poisoned, the
    client is closed, and nack starts from the first unresolved record.
  - A `Close()` that wins the race against a `Produce` which then never calls back ends the wait through the
    `closed` channel, not the timer (assert the return time is far below `produceTimeout`).
- SDK v0.15.0 Teardown race: `Teardown` arriving without `Stop` while a `Write` is blocked on a paused
  broker; the SDK's 1 m wait expires (shortened in the test), the connector's `Teardown` closes the
  client, the `Write` returns through the `closed` channel, none of its records is acked, and nothing
  races under `-race`.
- Shutdown: a pipeline-level test sends SIGTERM with a paused destination broker and in-flight records;
  asserts that the runtime stops waiting at `exitTimeout` (30 s), no ack past the last confirmed record, and a
  restart that loses nothing. Same with SIGKILL. It also records whether the `Write` context is cancelled on
  SIGTERM, which this document has only read from the engine (legacy engine; on arch-v2 the forced stop
  fires at about 25 s).
- Config refresh: DescribeConfigs failing transiently (broker paused for less than the failure budget) keeps
  the last good config and does not halt; `topicConfigRefreshMaxFailures` consecutive failures halt with
  `KAFKA_TOPIC_CONFIG_UNREADABLE`; an authorization failure at refresh halts at once.
- Error classification: table-driven over constructed values: `*kadm.AuthError` (wrapped, `errors.As`) fails
  at once; `*kadm.ShardErrors` of plain transport errors is transient at refresh and fails the record on the first
  read, while one wrapping `kerr.SaslAuthenticationFailed` or `kgo.ErrClientClosed` fails at once; a
  `ResourceConfig.Err` of `UNKNOWN_TOPIC_OR_PARTITION` yields `KAFKA_DESTINATION_TOPIC_MISSING` even though
  `kerr.IsRetriable` is true for it (ordering test); a retriable code (for example `LEADER_NOT_AVAILABLE`) is
  transient at refresh; a non-retriable, non-auth code (for example `INVALID_REQUEST`) fails at once; a missing
  or empty resource entry for the topic is unreadable. At least one case per class runs against a real broker.
- Schema-attached record: a record whose payload was decoded by the SDK schema middleware fails with
  `KAFKA_MIRROR_PAYLOAD_NOT_RAW`.
- Record size: a well-compressing record over the topic `max.message.bytes` fails by default and passes with
  `maxMessageBytesOverride` set (and is accepted or rejected by the broker, whichever the broker does).
- Key and value rules: null versus empty key and value, conflicting marker and bytes, absent marker with
  empty bytes, structured payload, and a user-set `sdk.record.format` each give the specified result, and
  the value-fidelity test above runs against `mirror` mode and must pass.
- Many-to-one template: two source topics onto one destination topic interleave on shared partitions and
  per-source-partition order holds among first deliveries.
- Ordering test: record rejected by the broker in the middle of a same-partition batch; asserts the
  documented behaviour (tail nacked, later records may already be present) so a change in it is noticed.
- Fault injection and `kill -9`: SIGKILL the connector mid-batch (source and destination), restart,
  and verify at-least-once: every source record appears at least once in the destination, in the
  correct partition, with the original order per partition among first deliveries; duplicates are
  identical to the original. Runs on both brokers. This needs the repo's chaos harness for kafka,
  which does not exist yet and is already a certification item.
- SDK acceptance suite stays green with `mirror` and `emitMirrorMetadata` off, and a golden test shows source output with
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

9. DescribeConfigs authorization (Kafka only). `apache/kafka:4.0.0` (the JVM image; the native image
   failed SASL PLAIN login on the broker side with a JAAS `Subject.current()` error, so it could not be used),
   `StandardAuthorizer`, `allow.everyone.if.no.acl.found=false`, a SASL_PLAINTEXT listener with user
   `alice`, ACLs granting `alice` WRITE and DESCRIBE on one 2-partition topic and nothing else. Observed:
   `kadm.DescribeTopicConfigs` returned top-level `TOPIC_AUTHORIZATION_FAILED: Not authorized to access
   topics` and zero resources; `ListTopics` (metadata) succeeded with 2 partitions; `ProduceSync` to
   partition 1 succeeded. So the destination can write to a topic it cannot read the config of, which is
   exactly the case the unreadable-config rule covers. Output: `out/acl-kafka.txt` in the scratch area.

10. Bounded produce wait (round 3). Own spike module (`spikes/kafka-timeout`), pinned to franz-go v1.20.7
    and kadm v1.17.2 (the connector's versions, confirmed from the module graph), Apache Kafka
    `apache/kafka-native:4.0.0`, one broker, unique compose project, ephemeral port, idempotent producer
    (`acks=all`), no `RecordDeliveryTimeout`. Each scenario first produced one record to warm the
    connection, then paused (`docker pause`) or stopped (`docker stop`) the broker container. Outputs are in
    `out/*.txt` of that module.
    - `ProduceSync` with a 3 s context deadline: still blocked after 25 s, on both pause and stop. `Close()`
      then returned at once and the blocked call returned `client closed`.
    - `Produce` with a 3 s context deadline per record (broker paused after the connection was warm): no
      promise resolved in 25 s. franz-go's documentation says an idempotent record with a request already
      sent cannot be failed. With the broker paused before the first produce (no metadata), the context did
      fail the record (`context deadline exceeded`, seen by the 6 s sample).
    - `ProduceRequestTimeout(2s)` with `RequestTimeoutOverhead(2s)`: no promise resolved in 40 s. The client
      keeps retrying.
    - `Flush(ctx 3s)` and `AbortBufferedRecords(ctx 3s)` with five buffered records: both returned
      `context deadline exceeded` at 3 s and left the records unresolved; only `Close()` resolved them.
    - Callbacks plus own 3 s timer, then `Close()` (the design): the wait returned at 3.0 s, `Close()` took
      under 10 ms, and within the 5 s settle window all 5 promises had resolved with `client closed`, none
      with nil. Identical with the record `Timestamp` set 10 days in the past (no `RecordDeliveryTimeout`),
      and identical with the broker stopped (connection refused) instead of paused.
    - After unpausing, the five records reported as errors were all present in the topic (end offset 1 to
      6). After a stop (connection refused), none were (end offset stayed 1). The unconfirmed-but-written
      case is real.
    - Control, healthy broker: `RecordDeliveryTimeout(30s)` with a 2-day-old `Timestamp` fails at once with
      "records have timed out before they were able to be produced"; the same record with no timeout is
      produced. This re-confirms result 6 on v1.20.7.

    Version note: results 1 to 9 were run in `spikes/kafka-passthrough`, whose module resolves
    `github.com/twmb/franz-go` v1.22.2 today (pulled up by the SDK dependency). The text above this round
    said v1.20.7. Whether the module resolved to v1.22.2 when those results were captured is not known, and
    results 1 to 5 and 7 to 9 were not re-run on v1.20.7. Result 6 was (item 10). Treat those results as
    observed on v1.22.2 or v1.20.7, unconfirmed which.

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
- DescribeConfigs authorization failure on Redpanda, older Kafka and managed services; only Kafka 4.0.0
  with `StandardAuthorizer` was run.
- Compacted topics with old timestamps (`cleanup.policy=compact`), tombstone retention behaviour, and a
  batch of small records over `max.message.bytes` (only a single oversized record was run).
- Whether the destination can tell an explicit `sdk.record.format` from the SDK default, and what the gRPC
  hop does to an invalid-UTF-8 metadata string.
- Whether `ForceMetadataRefresh` taking 5.0 s on Redpanda is the client `MetadataMinAge` or the broker.
- The bounded wait on Redpanda, with TLS or SASL connections, or with several partitions and brokers; and
  through the connector's own `Produce` (the spike used franz-go directly).
- Observed on franz-go v1.20.7 in a reviewer's spike (not in this repo; I did not re-run it): a
  300-iteration race between `Produce` and `Close()`, worst `Write` 2.002 s against a 2 s timeout, 12
  TOCTOU hits (a `Produce` that lost the race to `Close()` and never called back), no hangs. A `Produce`
  blocked on a full buffer was released by its context deadline (1.5 s) and by `Close()` (500 ms).
  The `closed` channel in the waiter exists for the TOCTOU hits, which otherwise wait for the timer.
- Whether a forced stop on arch-v2 cancels the connector's `Write` context. The legacy engine's behaviour
  and the arch-v2 force-stop timing (about 25 s) are read from the engine, not run.
- Classification of DescribeConfigs errors: the types and codes are read from kadm v1.17.2 and kerr (franz-go
  v1.20.7); only the authorization case was run against a broker (Spike results 9, and that run did not record
  the Go type of the returned error). The transient/fail-fast split, the 10 s retry interval and the default of 3
  failures are proposals.
- That the SDK schema middleware decodes into `StructuredData` for every schema'd record in a running
  pipeline (read from source, not run).

## Decisions

Items 1, 2 and 4 were recorded as decided in round 4. Items 3, 5 and 6 were decided by DeVaris on
2026-10-14. No decision is pending on this document; the metrics export path (_Observability_) is an open
prerequisite, not a decision.

1. Destination option shape and the value path. One `mirror: true` option replaces the four booleans
   (`preserveHeaders`, `preserveTimestamp`, `preservePartition`, `preserveValue`). It implies header, timestamp,
   partition and value preservation, with the _Key and value rules_ above (writes `Payload.After` bytes
   verbatim, bypasses `sdk.record.format`, rejects non-`RawData` payloads, a user-set format and ambiguous
   null/empty). Rejected alternatives: (a) four booleans plus `preserveValue`, more flexible and easier to
   misconfigure into a half mirror; (b) a payload-only format in `conduit-connector-sdk`, an SDK release and a
   wider surface; (c) keep the template recipe and document its empty-value and silent-fallback limits.
2. Default `sdk.record.format` with preserve behaviour: moot. Under `mirror` the format is ignored and a set
   one is rejected (`KAFKA_MIRROR_FORMAT_SET`).
3. Produce wait bound. Decided by DeVaris 2026-10-14. Unbounded produce waits are not accepted in mirror
   mode. `produceTimeout` defaults to `2m`, must be finite, with a minimum of `30s` and a maximum of `1h`;
   `0` is rejected and there is no escape hatch (no `allowUnboundedProduce`). Both limits are hard config
   errors (`KAFKA_PRODUCE_TIMEOUT_OUT_OF_RANGE`). Justification is under _Delivery timeout and bounded
   waits_. The three numbers are starting defaults; none was measured.
4. Non-retriable, non-auth DescribeConfigs errors fail fast (see _Topic configuration checks_).
5. Non-UTF-8 header values. Decided by DeVaris 2026-10-14. With mirror metadata on, the source omits the
   legacy `kafka.header.<k>` entry for a header that is not valid UTF-8, keeps the raw bytes in
   `kafka.headers`, and counts each omission in `kafka_source_legacy_header_omitted_total`. The value is
   never escaped or replaced (invariant 6). The design applies the same rule to a header key that is not
   valid UTF-8, since the metadata key fails `proto.Marshal` the same way. See _Non-UTF-8 header values_.
6. Ordering after a post-enqueue error. Decided by DeVaris 2026-10-14. Invariant 4 holds per partition
   only until the first broker error after enqueue; this is accepted. It requires the loud metric
   (`kafka_destination_post_enqueue_errors_total`, alerted on any increase), a WARN, and the
   documented re-seed remedy. See _Ordering_.

## Related

- `ConduitIO/conduit-connector-kafka#309` (tracking issue), #308 (batch ack bug, prerequisite, fixed by
  #312, released in v0.12.6 and v0.13.0)
- `docs/catalog/v0.21-certification-gaps.md` (MirrorMaker 2 parity table, decision 9)
