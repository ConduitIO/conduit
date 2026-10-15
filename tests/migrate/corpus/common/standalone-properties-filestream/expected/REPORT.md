# Kafka Connect migration report

1 connectors, 5 keys: 0 translated, 0 approximated, 5 unsupported, 5 blocking.

## local-file-source

Class `FileStreamSource`, role unknown. No pipeline written.

0 of 5 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `name` | connect-file-source.properties:16:1 | `kc.connector_class_unsupported` | The connector class FileStreamSource has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `connector.class` | connect-file-source.properties:17:1 | `kc.connector_class_unsupported` | The connector class FileStreamSource has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `tasks.max` | connect-file-source.properties:18:1 | `kc.connector_class_unsupported` | The connector class FileStreamSource has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `file` | connect-file-source.properties:19:1 | `kc.connector_class_unsupported` | The connector class FileStreamSource has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `topic` | connect-file-source.properties:20:1 | `kc.connector_class_unsupported` | The connector class FileStreamSource has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |

## Before you deploy

Nothing beyond the entries above.
