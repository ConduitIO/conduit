# Kafka Connect worker for the Debezium arms (connect and connect-pg).
#
# quay.io/debezium/connect:3.2 ships the Debezium source/sink connectors on its
# plugin path (/kafka/connect) and Kafka 4.0.0's connect-file JAR on its
# classpath (/kafka/libs) — but the classpath plugin discovery only exposes the
# mirror connectors, not FileStreamSinkConnector. Copying the bundled
# connect-file JAR into a plugin-path directory makes FileStreamSinkConnector
# discoverable so the `connect` arm can use the SAME file sink (one line per CDC
# event) as the Conduit and Redpanda arms.
#
# It also installs the Confluent JDBC sink connector
# (io.confluent.connect.jdbc.JdbcSinkConnector) for the `connect-pg` arm:
# Debezium Postgres source -> Kafka -> JDBC sink writing bench_source_i ->
# bench_dest_i. The connector JAR is fetched from Confluent's Maven repo; the
# Postgres JDBC driver and the connector's re2j/jsqlparser runtime deps are
# fetched from Maven Central, all pinned to exact versions.
FROM quay.io/debezium/connect:3.2

USER root
RUN mkdir -p /kafka/connect/kafka-connect-file \
    && cp /kafka/libs/connect-file-4.0.0.jar /kafka/connect/kafka-connect-file/ \
    && mkdir -p /kafka/connect/kafka-connect-jdbc \
    && curl -fsSL -o /kafka/connect/kafka-connect-jdbc/kafka-connect-jdbc-10.9.6.jar \
         https://packages.confluent.io/maven/io/confluent/kafka-connect-jdbc/10.9.6/kafka-connect-jdbc-10.9.6.jar \
    && curl -fsSL -o /kafka/connect/kafka-connect-jdbc/postgresql-42.7.11.jar \
         https://repo1.maven.org/maven2/org/postgresql/postgresql/42.7.11/postgresql-42.7.11.jar \
    && curl -fsSL -o /kafka/connect/kafka-connect-jdbc/re2j-1.7.jar \
         https://repo1.maven.org/maven2/com/google/re2j/re2j/1.7/re2j-1.7.jar \
    && curl -fsSL -o /kafka/connect/kafka-connect-jdbc/jsqlparser-4.9.jar \
         https://repo1.maven.org/maven2/com/github/jsqlparser/jsqlparser/4.9/jsqlparser-4.9.jar
USER kafka
