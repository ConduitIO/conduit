# Kafka Connect migration report

1 connectors, 12 keys: 10 translated, 0 approximated, 2 unsupported, 2 blocking.

## mm2-basicuserdata

- Class: `org.apache.kafka.connect.mirror.MirrorSourceConnector`
- Role: source
- Pipeline: `pipelines/mm2-basicuserdata.yaml`, written stopped

10 of 12 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| unsupported | `replication.factor` | connector.json#/config/replication.factor | `kc.key_unsupported` | MirrorMaker creates remote topics with this replication factor; Conduit's Kafka destination creates topics with the broker's defaults. | Create the destination topics ahead of time with the replication factor you need. |
| unsupported | `offset-syncs.topic.replication.factor` | connector.json#/config/offset-syncs.topic.replication.factor | `kc.key_unsupported` | Offset syncs are not written. | None needed. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | mm2-basicuserdata | `kc.output_shape_differs` | Records keep their key, value and topic name (per the replication policy), but not their partition, headers or timestamp. This version does not write the Kafka mirror settings; the destination partitions by key and stamps the time of writing. | Keep this flow on MirrorMaker if consumers rely on partition numbers, headers or record timestamps, or accept the difference with --accept-gaps. |
| note | mm2-basicuserdata | `kc.default_differs` | refresh.topics.enabled is absent (true), so MirrorMaker discovers new topics; the pipeline reads a fixed topic list. | - |
| blocking | mm2-basicuserdata | `kc.default_differs` | sync.topic.configs.enabled is absent (true), so MirrorMaker syncs topic configs to the destination; the pipeline does not. | Create the destination topics ahead of time with the source topics' configs, or set sync.topic.configs.enabled=false if they already match. |
| note | mm2-basicuserdata | `kc.default_differs` | emit.offset-syncs.enabled is absent (true), so MirrorMaker writes offset syncs for checkpointing; the pipeline does not, and consumer offsets cannot be translated for this flow. | - |
| note | mm2-basicuserdata | `kc.default_differs` | heartbeats.replication.enabled is absent (true), so MirrorMaker replicates heartbeat topics; the pipeline reads only the listed topics. | - |
| note | mm2-basicuserdata | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
