# Kafka Connect migration report

1 connectors, 12 keys: 8 translated, 0 approximated, 4 unsupported, 10 blocking.

## inventory-source

- Class: `io.debezium.connector.postgresql.PostgresConnector`
- Role: source
- Pipeline: `pipelines/inventory-source.yaml`, written stopped

8 of 12 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `topic.prefix` | inventory-source.json#/topic.prefix | `kc.target_capability_missing` | topic.prefix becomes debezium.topic.prefix, which requires the Debezium-compatible output mode (debezium.enabled on builtin:postgres); that setting is not in the released connector. | Keep this connector on Kafka Connect until #3032 ships the Debezium output mode, or accept the difference with --accept-gaps. |
| blocking | `schema.include.list` | inventory-source.json#/schema.include.list | `kc.key_unsupported` | Schema selection folds into the source's tables list only when a literal table.include.list is also set; alone it cannot be expressed. | List the tables explicitly in table.include.list (schema.table), or keep this connector on Kafka Connect. |
| blocking | `slot.failover` | inventory-source.json#/slot.failover | `kc.key_unsupported` | A failover slot survives a primary switch; Conduit's slot does not. | Keep this connector on Kafka Connect until the connector creates failover slots. |
| blocking | `snapshot.mode` | inventory-source.json#/snapshot.mode | `kc.key_unsupported` | This snapshot mode has no Conduit equivalent (only initial and never are supported). | Use snapshot.mode=initial or never, or keep this connector on Kafka Connect. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | inventory-source | `kc.output_shape_differs` | Debezium-compatible output mode is not available. The released builtin:postgres source has no debezium.enabled setting and the debezium.encode processor has not landed, so the pipeline emits OpenCDC-shaped records, not the Debezium envelope (before/after structs, source, op, ts_ms) or its topic naming. Consumers of the Debezium envelope break. | Keep this connector on Kafka Connect until the Debezium output mode ships (#3032), or accept the difference with --accept-gaps. |
| blocking | inventory-source | `kc.setting_required` | The builtin:postgres source requires tables, and no key of the Kafka Connect config sets it; the pipeline references ${KC_INVENTORY_SOURCE_SOURCE_TABLES} instead. | Set KC_INVENTORY_SOURCE_SOURCE_TABLES in the environment (see secrets.env.template). |
| blocking | inventory-source | `kc.setting_required` | The builtin:kafka destination requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_INVENTORY_SOURCE_DESTINATION_SERVERS} instead. | Set KC_INVENTORY_SOURCE_DESTINATION_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| blocking | inventory-source | `kc.converter_unknown` | Neither the connector nor a worker file sets key.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set key.converter on the connector, then re-run. |
| blocking | inventory-source | `kc.converter_unknown` | Neither the connector nor a worker file sets value.converter, so the format Kafka Connect used is unknown. | Pass the worker properties with --worker, or set value.converter on the connector, then re-run. |
| note | inventory-source | `kc.default_differs` | Kafka Connect wrote a column's default in place of NULL, because replace.null.with.default is true by default and neither key.converter.replace.null.with.default nor value.converter.replace.null.with.default is set to false. Conduit writes NULL. Consumers that relied on the default must handle null. | None needed in Conduit. To compare outputs, set key.converter.replace.null.with.default=false and value.converter.replace.null.with.default=false on the Kafka Connect side. |
| note | inventory-source | `kc.secret_plaintext` | The input holds at least one secret as a literal value. Where migrate used the setting it wrote a variable reference instead (a dropped setting has no variable), but the input file itself contains the secret. | Fill the variables in secrets.env.template from your secret store, and replace the literal in the Kafka Connect config with a config-provider reference. |
| blocking | inventory-source | `kc.default_differs` | table.include.list is absent, so Debezium reads every table of every non-system schema (or of the schemas a schema.include.list or schema.exclude.list selects). The builtin:postgres source has no equivalent: its tables "*" reads only the public schema's tables. migrate does not write tables. | Set table.include.list to the tables to read, as schema.table entries in one schema (one Kafka Connect connector per schema), then re-run. |
| note | inventory-source | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | inventory-source | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
