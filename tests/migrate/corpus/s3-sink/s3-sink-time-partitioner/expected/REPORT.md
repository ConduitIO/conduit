# Kafka Connect migration report

1 connectors, 17 keys: 6 translated, 1 approximated, 10 unsupported, 16 blocking.

## sink-s3-train-movements-v00

- Class: `io.confluent.connect.s3.S3SinkConnector`
- Role: sink
- Pipeline: `pipelines/sink-s3-train-movements-v00.yaml`, written stopped

6 of 17 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `key.converter` | sink-s3-train-movements-v00.json#/key.converter | `kc.key_unsupported` | This converter is not translated by this version of migrate; the wire format of keys would differ. | Keep this connector on Kafka Connect until migrate translates the converter. |
| approximated | `flush.size` | sink-s3-train-movements-v00.json#/flush.size | `kc.key_approximated` | Kafka Connect rotates a file after flush.size records; Conduit writes one object per batch, so sdk.batch.size bounds the records per object instead. | - |
| blocking | `format.class` | sink-s3-train-movements-v00.json#/format.class | `kc.key_unsupported` | Conduit has no Avro object format; AvroFormat stores an Avro container file. | Use JsonFormat or ParquetFormat, or keep this connector on Kafka Connect. |
| blocking | `schema.generator.class` | sink-s3-train-movements-v00.json#/schema.generator.class | `kc.key_unsupported` | The Hive schema generator names the object schema; Conduit has no schema generator. | Keep this connector on Kafka Connect if a Hive metastore reads the schema, or accept the difference with --accept-gaps. |
| blocking | `schema.compatibility` | sink-s3-train-movements-v00.json#/schema.compatibility | `kc.key_unsupported` | Conduit has no schema compatibility mode. | Keep this connector on Kafka Connect if a schema registry or Hive metastore enforces compatibility. |
| blocking | `partitioner.class` | sink-s3-train-movements-v00.json#/partitioner.class | `kc.key_unsupported` | A partitioner whose directory layout Conduit does not reproduce. | Keep this connector on Kafka Connect if consumers read the partitioned object keys, or use the default partitioner and accept the layout difference. |
| blocking | `path.format` | sink-s3-train-movements-v00.json#/path.format | `kc.key_unsupported` | A time-partitioner path pattern; Conduit has no partitioner path format. | Keep this connector on Kafka Connect if consumers read the partitioned object keys. |
| blocking | `timestamp.extractor` | sink-s3-train-movements-v00.json#/timestamp.extractor | `kc.key_unsupported` | A time-partitioner timestamp source; Conduit has no partitioner timestamp extractor. | Keep this connector on Kafka Connect if consumers read the partitioned object keys. |
| blocking | `partition.duration.ms` | sink-s3-train-movements-v00.json#/partition.duration.ms | `kc.key_unsupported` | The time-partitioner width; Conduit has no partitioner. | Keep this connector on Kafka Connect if consumers read the partitioned object keys. |
| blocking | `locale` | sink-s3-train-movements-v00.json#/locale | `kc.key_unsupported` | A partitioner locale; Conduit has no partitioner. | Keep this connector on Kafka Connect if consumers read the partitioned object keys. |
| blocking | `timezone` | sink-s3-train-movements-v00.json#/timezone | `kc.key_unsupported` | A partitioner timezone; Conduit has no partitioner. | Keep this connector on Kafka Connect if consumers read the partitioned object keys. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | sink-s3-train-movements-v00 | `kc.output_shape_differs` | Conduit writes one object per batch as &lt;prefix>/&lt;UnixNano>.&lt;ext>; the Kafka Connect S3 sink names and lays out objects by its partitioner, and its JsonFormat writes the bare record value where Conduit's json format writes one OpenCDC-shaped object per line. Consumers of the exact object keys or line shape break. | Keep this connector on Kafka Connect if consumers read the object keys or the bare-value JSON lines, or accept the difference with --accept-gaps. |
| note | sink-s3-train-movements-v00 | `kc.delivery_semantics_differ` | Object names are not deterministic, so a batch that is retried after a failure or a restart is written again as a new object. A reader can see a record twice, never zero times; Kafka Connect's deterministic naming gives exactly-once object keys. | None needed; consumers should tolerate duplicate objects. |
| blocking | sink-s3-train-movements-v00 | `kc.setting_required` | The builtin:kafka source requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_TRAIN_MOVEMENTS_V00_SOURCE_SERVERS} instead. | Set KC_SINK_S3_TRAIN_MOVEMENTS_V00_SOURCE_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| blocking | sink-s3-train-movements-v00 | `kc.setting_required` | The builtin:s3 destination requires aws.accessKeyId, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_TRAIN_MOVEMENTS_V00_DESTINATION_AWS_ACCESSKEYID} instead. | Set KC_SINK_S3_TRAIN_MOVEMENTS_V00_DESTINATION_AWS_ACCESSKEYID in the environment (see secrets.env.template). |
| blocking | sink-s3-train-movements-v00 | `kc.setting_required` | The builtin:s3 destination requires aws.secretAccessKey, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_TRAIN_MOVEMENTS_V00_DESTINATION_AWS_SECRETACCESSKEY} instead. | Set KC_SINK_S3_TRAIN_MOVEMENTS_V00_DESTINATION_AWS_SECRETACCESSKEY in the environment (see secrets.env.template). |
| blocking | sink-s3-train-movements-v00 | `kc.setting_required` | The builtin:s3 destination requires format, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_TRAIN_MOVEMENTS_V00_DESTINATION_FORMAT} instead. | Set KC_SINK_S3_TRAIN_MOVEMENTS_V00_DESTINATION_FORMAT in the environment (see secrets.env.template). |
| blocking | sink-s3-train-movements-v00 | `kc.converter_unknown` | Neither the connector nor a worker file sets value.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set value.converter on the connector, then re-run. |
| note | sink-s3-train-movements-v00 | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | sink-s3-train-movements-v00 | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
