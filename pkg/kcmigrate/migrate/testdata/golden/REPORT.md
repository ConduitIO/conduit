# Kafka Connect migration report

3 connectors, 11 keys: 8 translated, 0 approximated, 3 unsupported, 3 blocking.

## connect-distributed.properties

Role worker. No pipeline written.

2 of 2 keys translated with no remark.

## clean

Class `com.example.ExampleSource`, role source. Pipeline `pipelines/clean.yaml`, written stopped.

5 of 7 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| note | `example.mode` | connectors/clean.properties:6:1 | `kc.key_duplicate` | Overridden by the occurrence at connectors/clean.properties:7:1; the last value wins, as in Kafka Connect. | - |
| blocking | `example.mode` | connectors/clean.properties:7:1 | `kc.key_unsupported` | This mode has no Conduit equivalent. | Use fast or slow. |

## blocking

Class `com.example.Unknown`, role unknown. No pipeline written.

0 of 2 keys translated with no remark.

| | Key | Location | Code | Reason | Fix |
| --- | --- | --- | --- | --- | --- |
| blocking | `connector.class` | connectors/blocking.json#/config/connector.class | `kc.connector_class_unsupported` | The connector class com.example.Unknown has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |
| blocking | `secret.password` | connectors/blocking.json#/config/secret.password | `kc.connector_class_unsupported` | The connector class com.example.Unknown has no Conduit mapping in this version. | Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates. |

## Before you deploy

Nothing beyond the entries above.
