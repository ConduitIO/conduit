# Kafka Connect migration report

1 connectors, 12 keys: 8 translated, 0 approximated, 4 unsupported, 7 blocking.

## shop-cdc

- Class: `io.debezium.connector.postgresql.PostgresConnector`
- Role: source
- Pipeline: `pipelines/shop-cdc.yaml`, written stopped

8 of 12 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `topic.prefix` | connector.json#/config/topic.prefix | `kc.target_capability_missing` | topic.prefix becomes debezium.topic.prefix, which requires the Debezium-compatible output mode (debezium.enabled on builtin:postgres); that setting is not in the released connector. | Keep this connector on Kafka Connect until #3032 ships the Debezium output mode, or accept the difference with --accept-gaps. |
| blocking | `table.include.list` | connector.json#/config/table.include.list | `kc.target_capability_missing` | table.include.list lists tables in more than one schema (inventory, sales). The builtin:postgres source finds bare table names through one search_path and does not accept schema-qualified names (Postgres connector issue #277), so with several schemas a same-name table of an earlier schema can be read instead. | Split the connector into one Kafka Connect connector per schema (each with its own slot.name and publication.name) and migrate each, or keep this connector on Kafka Connect until Postgres connector issue #277 ships. |
| blocking | `key.converter` | connector.json#/config/key.converter | `kc.key_unsupported` | This converter is not translated by this version of migrate; the wire format of keys would differ. | Keep this connector on Kafka Connect until migrate translates the converter. |
| blocking | `value.converter` | connector.json#/config/value.converter | `kc.key_unsupported` | This converter is not translated by this version of migrate; the wire format of values would differ. | Keep this connector on Kafka Connect until migrate translates the converter. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | shop-cdc | `kc.output_shape_differs` | Debezium-compatible output mode is not available. The released builtin:postgres source has no debezium.enabled setting and the debezium.encode processor has not landed, so the pipeline emits OpenCDC-shaped records, not the Debezium envelope (before/after structs, source, op, ts_ms) or its topic naming. Consumers of the Debezium envelope break. | Keep this connector on Kafka Connect until the Debezium output mode ships (#3032), or accept the difference with --accept-gaps. |
| blocking | shop-cdc | `kc.setting_required` | The builtin:postgres source requires tables, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SHOP_CDC_SOURCE_TABLES} instead. | Set KC_SHOP_CDC_SOURCE_TABLES in the environment (see secrets.env.template). |
| blocking | shop-cdc | `kc.setting_required` | The builtin:kafka destination requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_SHOP_CDC_DESTINATION_SERVERS} instead. | Set KC_SHOP_CDC_DESTINATION_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| note | shop-cdc | `kc.secret_plaintext` | The input holds at least one secret as a literal value. Where migrate used the setting it wrote a variable reference instead (a dropped setting has no variable), but the input file itself contains the secret. | Fill the variables in secrets.env.template from your secret store, and replace the literal in the Kafka Connect config with a config-provider reference. |
| note | shop-cdc | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | shop-cdc | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
