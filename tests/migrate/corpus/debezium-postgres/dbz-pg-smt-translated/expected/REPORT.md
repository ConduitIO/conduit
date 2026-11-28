# Kafka Connect migration report

1 connectors, 33 keys: 26 translated, 0 approximated, 7 unsupported, 5 blocking.

## orders-cdc

- Class: `io.debezium.connector.postgresql.PostgresConnector`
- Role: source
- Pipeline: `pipelines/orders-cdc.yaml`, written stopped

26 of 33 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `topic.prefix` | connector.json#/config/topic.prefix | `kc.target_capability_missing` | topic.prefix becomes debezium.topic.prefix, which requires the Debezium-compatible output mode (debezium.enabled on builtin:postgres); that setting is not in the released connector. | Keep this connector on Kafka Connect until #3032 ships the Debezium output mode, or accept the difference with --accept-gaps. |
| blocking | `key.converter` | connector.json#/config/key.converter | `kc.key_unsupported` | This converter is not translated by this version of migrate; the wire format of keys would differ. | Keep this connector on Kafka Connect until migrate translates the converter. |
| unsupported | `key.converter.schemas.enable` | connector.json#/config/key.converter.schemas.enable | `kc.key_unsupported` | A converter setting; the converter itself is reported on key.converter. | None beyond the key.converter entry. |
| unsupported | `key.converter.replace.null.with.default` | connector.json#/config/key.converter.replace.null.with.default | `kc.key_unsupported` | A converter setting; the converter itself is reported on key.converter. | None beyond the key.converter entry. |
| blocking | `value.converter` | connector.json#/config/value.converter | `kc.key_unsupported` | This converter is not translated by this version of migrate; the wire format of values would differ. | Keep this connector on Kafka Connect until migrate translates the converter. |
| unsupported | `value.converter.schemas.enable` | connector.json#/config/value.converter.schemas.enable | `kc.key_unsupported` | A converter setting; the converter itself is reported on value.converter. | None beyond the value.converter entry. |
| unsupported | `value.converter.replace.null.with.default` | connector.json#/config/value.converter.replace.null.with.default | `kc.key_unsupported` | A converter setting; the converter itself is reported on value.converter. | None beyond the value.converter entry. |

## Before you deploy

| | Connector | Code | Difference | Fix |
| --- | --- | --- | --- | --- |
| blocking | orders-cdc | `kc.output_shape_differs` | Debezium-compatible output mode is not available. The released builtin:postgres source has no debezium.enabled setting and the debezium.encode processor has not landed, so the pipeline emits OpenCDC-shaped records, not the Debezium envelope (before/after structs, source, op, ts_ms) or its topic naming. Consumers of the Debezium envelope break. | Keep this connector on Kafka Connect until the Debezium output mode ships (#3032), or accept the difference with --accept-gaps. |
| blocking | orders-cdc | `kc.setting_required` | The builtin:kafka destination requires servers, and no key of the Kafka Connect config sets it; the pipeline references ${KC_ORDERS_CDC_DESTINATION_SERVERS} instead. | Set KC_ORDERS_CDC_DESTINATION_SERVERS in the environment (see secrets.env.template), or pass the worker file with --worker if it holds bootstrap.servers. |
| note | orders-cdc | `kc.secret_plaintext` | The input holds at least one secret as a literal value. Where migrate used the setting it wrote a variable reference instead (a dropped setting has no variable), but the input file itself contains the secret. | Fill the variables in secrets.env.template from your secret store, and replace the literal in the Kafka Connect config with a config-provider reference. |
| note | orders-cdc | `kc.default_differs` | Kafka Connect does not retry a failed operation by default (errors.retry.timeout=0). Conduit restarts a failed pipeline through pipeline recovery, so a transient failure that stopped the connector may not stop the pipeline. | - |
| note | orders-cdc | `kc.default_differs` | Kafka Connect's default header converter types header values; Conduit carries headers as metadata strings. | - |
