# Kafka Connect migration report

1 connectors, 11 keys: 5 translated, 4 approximated, 2 unsupported, 9 blocking.

## sink-s3-00

- Class: `io.confluent.connect.s3.S3SinkConnector`
- Role: sink
- Pipeline: `pipelines/sink-s3-00.yaml`, written stopped

5 of 11 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| approximated | `tasks.max` | sink-s3-00.json#/tasks.max | `kc.key_approximated` | A Conduit pipeline reads its source with one reader; Kafka Connect ran several tasks. Parallel readers arrive with partition claims (v0.24). | For sinks, raise sdk.batch.size on the destination if throughput falls short. |
| approximated | `flush.size` | sink-s3-00.json#/flush.size | `kc.key_approximated` | Kafka Connect rotates a file after flush.size records; Conduit writes one object per batch, so sdk.batch.size bounds the records per object instead. | - |
| blocking | `format.class` | sink-s3-00.json#/format.class | `kc.key_approximated` | Conduit's json format writes one OpenCDC-shaped object per line (Operation, Position, Payload, Key, Metadata); Kafka Connect's JsonFormat writes the bare record value. | Keep this connector on Kafka Connect if consumers read the bare-value JSON lines, or accept the difference with --accept-gaps. |
| blocking | `schema.generator.class` | sink-s3-00.json#/schema.generator.class | `kc.key_unsupported` | The Hive schema generator names the object schema; Conduit has no schema generator. | Keep this connector on Kafka Connect if a Hive metastore reads the schema, or accept the difference with --accept-gaps. |
| blocking | `schema.compatibility` | sink-s3-00.json#/schema.compatibility | `kc.key_unsupported` | Conduit has no schema compatibility mode. | Keep this connector on Kafka Connect if a schema registry or Hive metastore enforces compatibility. |
| blocking | `partitioner.class` | sink-s3-00.json#/partitioner.class | `kc.key_approximated` | Kafka Connect writes objects under topics/&lt;topic>/partition=&lt;n>/; Conduit writes them under &lt;prefix>/&lt;UnixNano>.&lt;ext>, so the object layout differs. | Keep this connector on Kafka Connect if consumers read the partitioned object keys, or accept the difference with --accept-gaps. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | sink-s3-00 | `kc.output_shape_differs` | Conduit writes one object per batch as &lt;prefix>/&lt;UnixNano>.&lt;ext>; the Kafka Connect S3 sink names and lays out objects by its partitioner, and its JsonFormat writes the bare record value where Conduit's json format writes one OpenCDC-shaped object per line. Consumers of the exact object keys or line shape break. | Keep this connector on Kafka Connect if consumers read the object keys or the bare-value JSON lines, or accept the difference with --accept-gaps. |
| note | sink-s3-00 | `kc.delivery_semantics_differ` | Object names are not deterministic, so a batch that is retried after a failure or a restart is written again as a new object. A reader can see a record twice, never zero times; Kafka Connect's deterministic naming gives exactly-once object keys. | None needed; consumers should tolerate duplicate objects. |
| blocking | sink-s3-00 | `kc.setting_required` | The builtin:kafka source requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_00_SOURCE_SERVERS} instead. | Set KC_SINK_S3_00_SOURCE_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| blocking | sink-s3-00 | `kc.setting_required` | The builtin:s3 destination requires format, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_00_DESTINATION_FORMAT} instead. | Set KC_SINK_S3_00_DESTINATION_FORMAT in the environment (see secrets.env.template). |
| blocking | sink-s3-00 | `kc.converter_unknown` | Neither the connector nor a worker file sets key.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set key.converter on the connector, then re-run. |
| blocking | sink-s3-00 | `kc.converter_unknown` | Neither the connector nor a worker file sets value.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set value.converter on the connector, then re-run. |
| note | sink-s3-00 | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | sink-s3-00 | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
