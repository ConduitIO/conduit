# Kafka Connect worker for the matrix: Debezium 3.7.0.Final on Kafka Connect
# (the SAME Debezium version as the Debezium Server arms), plus the sinks the
# cells use. Every artifact is pinned to an exact version; the base by digest.
#
#   - FileStreamSource/FileStreamSink: Apache Kafka's connect-file JAR, which
#     the image ships on its classpath but not on its plugin path; copied into
#     a plugin directory so the worker discovers it.
#   - Confluent JDBC sink 10.9.6 + PostgreSQL JDBC 42.7.11 (Confluent
#     Community License: free to use, source-available, not OSI open source).
#   - Confluent S3 sink 12.1.11 (Confluent Community License).
# BASE: quay.io/debezium/connect:3.7.0.Final@<digest>, resolved per run
# (images/pin.py: quay deletes superseded 3.7.0.Final manifests).
ARG BASE=quay.io/debezium/connect:3.7.0.Final
FROM ${BASE}

USER root
RUN set -e; \
    mkdir -p /kafka/connect/kafka-connect-file; \
    cp /kafka/libs/connect-file-*.jar /kafka/connect/kafka-connect-file/; \
    mkdir -p /kafka/connect/kafka-connect-jdbc; \
    curl -fsSL -o /kafka/connect/kafka-connect-jdbc/kafka-connect-jdbc-10.9.6.jar \
      https://packages.confluent.io/maven/io/confluent/kafka-connect-jdbc/10.9.6/kafka-connect-jdbc-10.9.6.jar; \
    curl -fsSL -o /kafka/connect/kafka-connect-jdbc/postgresql-42.7.11.jar \
      https://repo1.maven.org/maven2/org/postgresql/postgresql/42.7.11/postgresql-42.7.11.jar; \
    curl -fsSL -o /kafka/connect/kafka-connect-jdbc/re2j-1.7.jar \
      https://repo1.maven.org/maven2/com/google/re2j/re2j/1.7/re2j-1.7.jar; \
    curl -fsSL -o /kafka/connect/kafka-connect-jdbc/jsqlparser-4.9.jar \
      https://repo1.maven.org/maven2/com/github/jsqlparser/jsqlparser/4.9/jsqlparser-4.9.jar; \
    mkdir -p /kafka/connect/kafka-connect-s3; \
    curl -fsSL -o /tmp/s3.zip \
      https://hub-downloads.confluent.io/api/plugins/confluentinc/kafka-connect-s3/versions/12.1.11/confluentinc-kafka-connect-s3-12.1.11.zip; \
    unzip -q /tmp/s3.zip -d /tmp/s3; \
    cp /tmp/s3/confluentinc-kafka-connect-s3-12.1.11/lib/*.jar /kafka/connect/kafka-connect-s3/; \
    rm -rf /tmp/s3.zip /tmp/s3; \
    sha256sum /kafka/connect/kafka-connect-jdbc/*.jar /kafka/connect/kafka-connect-file/*.jar > /kafka/connect/MATRIX-SHA256SUMS; \
    chown -R kafka:kafka /kafka/connect/kafka-connect-file /kafka/connect/kafka-connect-jdbc /kafka/connect/kafka-connect-s3
# The plugin directories added above belong to the kafka user: with
# ENABLE_APICURIO_CONVERTERS=true the image's entrypoint links the Apicurio
# converter jars into every plugin directory at start (the shakedown's Connect
# worker died on "Permission denied" without this).
USER kafka
