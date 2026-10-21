# Kafka Connect migration report

1 connectors, 16 keys: 15 translated, 0 approximated, 1 unsupported, 5 blocking.

## mm2-msc-plaintext

- Class: `org.apache.kafka.connect.mirror.MirrorSourceConnector`
- Role: source
- Pipeline: `pipelines/mm2-msc-plaintext.yaml`, written stopped

15 of 16 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `source.cluster.security.protocol` | mm2-msc.json#/source.cluster.security.protocol | `kc.key_unsupported` | SASL_PLAINTEXT sends the SASL credentials without TLS. The Kafka connector (v0.15.0 and later) refuses SASL without TLS, and the migration never writes a plaintext SASL setup by itself. | Enable TLS on the cluster and use SASL_SSL, or set saslAllowPlaintext true (with tls.enabled false) on the migrated pipeline by hand, knowing that it exposes the SASL credentials on the network. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | mm2-msc-plaintext | `kc.output_shape_differs` | Records keep their key, value and topic name (per the replication policy), but not their partition, headers or timestamp. This version does not write the Kafka mirror settings; the destination partitions by key and stamps the time of writing. | Keep this flow on MirrorMaker if consumers rely on partition numbers, headers or record timestamps, or accept the difference with --accept-gaps. |
| blocking | mm2-msc-plaintext | `kc.offsets_not_migrated` | MirrorMaker resumes every partition from the offsets Kafka Connect stored for it, and reads a partition with none from the beginning unless auto.offset.reset says otherwise. migrate does not carry the stored offsets over. The pipeline reads every partition from the beginning (readFromBeginning: true), so the records MirrorMaker already copied are copied again. | Stop the MirrorMaker connector before starting the pipeline, and accept the re-copied or skipped records with --accept-gaps, which records kc.offsets_not_migrated in acceptedGaps. |
| note | mm2-msc-plaintext | `kc.secret_plaintext` | The input holds at least one secret as a literal value. migrate wrote a variable reference instead, but the input file itself contains the secret. | Fill the variables in secrets.env.template from your secret store, and replace the literal in the Kafka Connect config with a config-provider reference. |
| note | mm2-msc-plaintext | `kc.default_differs` | refresh.topics.enabled is absent (true), so MirrorMaker discovers new topics; the pipeline reads a fixed topic list. | - |
| blocking | mm2-msc-plaintext | `kc.default_differs` | sync.topic.configs.enabled is absent (true), so MirrorMaker syncs topic configs to the destination; the pipeline does not. | Create the destination topics ahead of time with the source topics' configs, or set sync.topic.configs.enabled=false if they already match. |
| blocking | mm2-msc-plaintext | `kc.default_differs` | sync.topic.acls.enabled is absent (true), so MirrorMaker syncs topic ACLs to the destination; the pipeline does not. | Set the ACLs on the destination topics ahead of time, or set sync.topic.acls.enabled=false. |
| note | mm2-msc-plaintext | `kc.default_differs` | replication.factor is absent, so MirrorMaker creates remote topics with replication factor 2; Conduit's Kafka destination creates topics with the broker's defaults. | - |
| note | mm2-msc-plaintext | `kc.default_differs` | emit.offset-syncs.enabled is absent (true), so MirrorMaker writes offset syncs for checkpointing; the pipeline does not, and consumer offsets cannot be translated for this flow. | - |
| note | mm2-msc-plaintext | `kc.default_differs` | heartbeats.replication.enabled is absent (true), so MirrorMaker replicates heartbeat topics; the pipeline reads only the listed topics. | - |
| note | mm2-msc-plaintext | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | mm2-msc-plaintext | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
