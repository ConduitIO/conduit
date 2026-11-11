# Kafka Connect migration report

1 connectors, 15 keys: 9 translated, 1 approximated, 5 unsupported, 8 blocking.

## sink-s3-events

- Class: `io.confluent.connect.s3.S3SinkConnector`
- Role: sink
- Pipeline: `pipelines/sink-s3-events.yaml`, written stopped

9 of 15 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| approximated | `flush.size` | sink-s3-events.json#/config/flush.size | `kc.key_approximated` | Kafka Connect rotates a file after flush.size records; Conduit writes one object per batch, so sdk.batch.size bounds the records per object instead. | - |
| blocking | `format.class` | sink-s3-events.json#/config/format.class | `kc.key_unsupported` | Conduit has no Avro object format; AvroFormat stores an Avro container file. | Use JsonFormat or ParquetFormat, or keep this connector on Kafka Connect. |
| blocking | `transforms` | sink-s3-events.json#/config/transforms | `kc.smt_unsupported` | The transform chain is not translated, so no processor of it is written: transforms lists "drop" twice, and Kafka Connect rejects duplicate aliases; transforms.hoist.field is a config-provider reference migrate cannot read offline; transform drop: The transform names the predicate "nowhere", which predicates does not list, and Kafka Connect rejects the config; transform missing: transforms lists "missing" but transforms.missing.type is not set, and Kafka Connect rejects the config. | Make every transform of the chain translatable (see the entries of each), or keep this connector on Kafka Connect. With --accept-gaps the pipeline runs without any of the chain's transforms. |
| blocking | `transforms.drop.type` | sink-s3-events.json#/config/transforms.drop.type | `kc.smt_unsupported` | Part of the transform drop (org.apache.kafka.connect.transforms.Filter), which is not translated. The transform names the predicate "nowhere", which predicates does not list, and Kafka Connect rejects the config. | Define the predicate in predicates, or remove transforms.drop.predicate, then re-run. |
| blocking | `transforms.drop.predicate` | sink-s3-events.json#/config/transforms.drop.predicate | `kc.smt_unsupported` | The transform names the predicate "nowhere", which predicates does not list, and Kafka Connect rejects the config. | Define the predicate in predicates, or remove transforms.drop.predicate, then re-run. |
| blocking | `transforms.hoist.field` | sink-s3-events.json#/config/transforms.hoist.field | `kc.smt_unsupported` | The value is a config-provider reference. A transform's behavior depends on its settings, which migrate cannot read offline. Kafka Connect resolves it even where the setting is not used, and fails the connector when it cannot. | Set the literal value in the Kafka Connect config, or resolve the reference and re-run. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | sink-s3-events | `kc.output_shape_differs` | Conduit writes one object per batch as &lt;prefix>/&lt;UnixNano>.&lt;ext>; the Kafka Connect S3 sink names and lays out objects by its partitioner, and its JsonFormat writes the bare record value where Conduit's json format writes one OpenCDC-shaped object per line. Consumers of the exact object keys or line shape break. | Keep this connector on Kafka Connect if consumers read the object keys or the bare-value JSON lines, or accept the difference with --accept-gaps. |
| note | sink-s3-events | `kc.delivery_semantics_differ` | Object names are not deterministic, so a batch that is retried after a failure or a restart is written again as a new object. A reader can see a record twice, never zero times; Kafka Connect's deterministic naming gives exactly-once object keys. | None needed; consumers should tolerate duplicate objects. |
| note | sink-s3-events | `kc.default_differs` | Kafka Connect reads a sink's topics in the consumer group connect-&lt;name>, and the Kafka source defaults to a new random group. groupID is set to connect-sink-s3-events, so the pipeline resumes from the offsets the Kafka Connect sink committed to that group. | - |
| note | sink-s3-events | `kc.default_differs` | Kafka Connect's consumer defaults auto.offset.reset to earliest, and the Kafka source defaults to the end of each partition. readFromBeginning is set to true: a partition the group has no committed offset for is read from its first record, as Kafka Connect does, and a partition with a committed offset resumes from it. | - |
| note | sink-s3-events | `kc.consumer_group_shared` | The pipeline's Kafka source reads in the consumer group connect-sink-s3-events, the group the Kafka Connect sink commits its offsets to, and resumes from them. While the Kafka Connect connector is still running, both consumers are in the group and Kafka splits the partitions between them, so neither sees every record. Records the sink processed after its last committed offset are read again, so they can reach the destination twice. | Stop the Kafka Connect connector (PUT /connectors/&lt;name>/stop, or delete it; pausing keeps its group membership) and wait until kafka-consumer-groups --describe --group connect-sink-s3-events lists no members, then start the pipeline. |
| blocking | sink-s3-events | `kc.setting_required` | The builtin:kafka source requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_EVENTS_SOURCE_SERVERS} instead. | Set KC_SINK_S3_EVENTS_SOURCE_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| blocking | sink-s3-events | `kc.setting_required` | The builtin:s3 destination requires format, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_EVENTS_DESTINATION_FORMAT} instead. | Set KC_SINK_S3_EVENTS_DESTINATION_FORMAT in the environment (see secrets.env.template). |
| note | sink-s3-events | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | sink-s3-events | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
