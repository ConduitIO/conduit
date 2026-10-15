# Kafka Connect migration report

1 connectors, 11 keys: 0 translated, 0 approximated, 11 unsupported, 11 blocking.

## jdbc-sink

Class `io.debezium.connector.jdbc.JdbcSinkConnector`, role unknown. No pipeline written.

0 of 11 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `connector.class` | connector.json#/config/connector.class | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `tasks.max` | connector.json#/config/tasks.max | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `topics` | connector.json#/config/topics | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `connection.url` | connector.json#/config/connection.url | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `connection.username` | connector.json#/config/connection.username | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `connection.password` | connector.json#/config/connection.password | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `insert.mode` | connector.json#/config/insert.mode | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `delete.enabled` | connector.json#/config/delete.enabled | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `primary.key.mode` | connector.json#/config/primary.key.mode | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `primary.key.fields` | connector.json#/config/primary.key.fields | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `schema.evolution` | connector.json#/config/schema.evolution | `kc.connector_class_unsupported` | The connector class io.debezium.connector.jdbc.JdbcSinkConnector has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |

## Before you deploy

Nothing beyond the entries above.
