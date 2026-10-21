# Kafka Connect migration report

1 connectors, 12 keys: 9 translated, 3 approximated, 0 unsupported, 5 blocking.

## sink-s3-orders

- Class: `io.confluent.connect.s3.S3SinkConnector`
- Role: sink
- Pipeline: `pipelines/sink-s3-orders.yaml`, written stopped

9 of 12 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| approximated | `flush.size` | sink-s3-orders.json#/config/flush.size | `kc.key_approximated` | Kafka Connect rotates a file after flush.size records; Conduit writes one object per batch, so sdk.batch.size bounds the records per object instead. | - |
| blocking | `format.class` | sink-s3-orders.json#/config/format.class | `kc.key_approximated` | Conduit's parquet format writes the OpenCDC fields as fixed string columns, GZIP only; Kafka Connect's ParquetFormat writes the record value's own schema. | Keep this connector on Kafka Connect if consumers read the Parquet schema, or accept the difference with --accept-gaps. |
| blocking | `partitioner.class` | sink-s3-orders.json#/config/partitioner.class | `kc.key_approximated` | Kafka Connect writes objects under topics/&lt;topic>/partition=&lt;n>/; Conduit writes them under &lt;prefix>/&lt;UnixNano>.&lt;ext>, so the object layout differs. | Keep this connector on Kafka Connect if consumers read the partitioned object keys, or accept the difference with --accept-gaps. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | sink-s3-orders | `kc.output_shape_differs` | Conduit writes one object per batch as &lt;prefix>/&lt;UnixNano>.&lt;ext>; the Kafka Connect S3 sink names and lays out objects by its partitioner, and its JsonFormat writes the bare record value where Conduit's json format writes one OpenCDC-shaped object per line. Consumers of the exact object keys or line shape break. | Keep this connector on Kafka Connect if consumers read the object keys or the bare-value JSON lines, or accept the difference with --accept-gaps. |
| note | sink-s3-orders | `kc.delivery_semantics_differ` | Object names are not deterministic, so a batch that is retried after a failure or a restart is written again as a new object. A reader can see a record twice, never zero times; Kafka Connect's deterministic naming gives exactly-once object keys. | None needed; consumers should tolerate duplicate objects. |
| note | sink-s3-orders | `kc.consumer_group_shared` | The pipeline's Kafka source reads in the consumer group orders-archive, the group the Kafka Connect sink commits its offsets to, and resumes from them. While the Kafka Connect connector is still running, both consumers are in the group and Kafka splits the partitions between them, so neither sees every record. Records the sink processed after its last committed offset are read again, so they can reach the destination twice. | Stop the Kafka Connect connector (PUT /connectors/&lt;name>/stop, or delete it; pausing keeps its group membership) and wait until kafka-consumer-groups --describe --group orders-archive lists no members, then start the pipeline. |
| blocking | sink-s3-orders | `kc.setting_required` | The builtin:kafka source requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_ORDERS_SOURCE_SERVERS} instead. | Set KC_SINK_S3_ORDERS_SOURCE_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| blocking | sink-s3-orders | `kc.setting_required` | The builtin:s3 destination requires format, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SINK_S3_ORDERS_DESTINATION_FORMAT} instead. | Set KC_SINK_S3_ORDERS_DESTINATION_FORMAT in the environment (see secrets.env.template). |
| note | sink-s3-orders | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | sink-s3-orders | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
