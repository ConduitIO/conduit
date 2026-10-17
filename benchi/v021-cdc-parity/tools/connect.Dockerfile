# Kafka Connect worker for the Debezium arm.
#
# quay.io/debezium/connect:3.2 ships the Debezium source/sink connectors on its
# plugin path (/kafka/connect) and Kafka 4.0.0's connect-file JAR on its
# classpath (/kafka/libs) — but the classpath plugin discovery only exposes the
# mirror connectors, not FileStreamSinkConnector. Copying the bundled
# connect-file JAR into a plugin-path directory makes FileStreamSinkConnector
# discoverable so the arm can use the SAME file sink (one line per CDC event)
# as the Conduit and Redpanda arms.
FROM quay.io/debezium/connect:3.2

USER root
RUN mkdir -p /kafka/connect/kafka-connect-file \
    && cp /kafka/libs/connect-file-4.0.0.jar /kafka/connect/kafka-connect-file/
USER kafka
