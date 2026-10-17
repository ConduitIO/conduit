# Kafka Connect migration report

1 connectors, 9 keys: 7 translated, 0 approximated, 2 unsupported, 6 blocking.

## inventory-connector

- Class: `io.debezium.connector.postgresql.PostgresConnector`
- Role: source
- Pipeline: `pipelines/inventory-connector.yaml`, written stopped

7 of 9 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `topic.prefix` | connector.json#/config/topic.prefix | `kc.target_capability_missing` | topic.prefix becomes debezium.topic.prefix, which requires the Debezium-compatible output mode (debezium.enabled on builtin:postgres); that setting is not in the released connector. | Keep this connector on Kafka Connect until |
| blocking | `schema.include.list` | connector.json#/config/schema.include.list | `kc.key_unsupported` | Schema selection folds into the source's tables list only when a literal table.include.list is also set; alone it cannot be expressed. | List the tables explicitly in table.include.list (schema.table), or keep this connector on Kafka Connect. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | inventory-connector | `kc.output_shape_differs` | Debezium-compatible output mode is not available. The released builtin:postgres source has no debezium.enabled setting and the debezium.encode processor has not landed, so the pipeline emits OpenCDC-shaped records, not the Debezium envelope (before/after structs, source, op, ts_ms) or its topic naming. Consumers of the Debezium envelope break. | Keep this connector on Kafka Connect until the Debezium output mode ships (#3032), or accept the difference with --accept-gaps. |
| blocking | inventory-connector | `kc.setting_required` | The builtin:kafka destination requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_INVENTORY_CONNECTOR_DESTINATION_SERVERS} instead. | Set KC_INVENTORY_CONNECTOR_DESTINATION_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| blocking | inventory-connector | `kc.converter_unknown` | Neither the connector nor a worker file sets key.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set key.converter on the connector, then re-run. |
| blocking | inventory-connector | `kc.converter_unknown` | Neither the connector nor a worker file sets value.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set value.converter on the connector, then re-run. |
| note | inventory-connector | `kc.secret_plaintext` | The input holds at least one secret as a literal value. migrate wrote a variable reference instead, but the input file itself contains the secret. | Fill the variables in secrets.env.template from your secret store, and replace the literal in the Kafka Connect config with a config-provider reference. |
| note | inventory-connector | `kc.default_differs` | plugin.name is absent, so Debezium uses decoderbufs; Conduit decodes with pgoutput only, and decoder-specific differences are not emulated. | Set plugin.name=pgoutput in the Kafka Connect config, or accept the difference. |
| note | inventory-connector | `kc.default_differs` | table.include.list is absent, so Debezium reads every table; the source is set to tables "*", whose schema coverage is connector-defined. | Set table.include.list to the tables to read, or accept the difference. |
| note | inventory-connector | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | inventory-connector | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
