# Kafka Connect worker running a pinned Debezium Postgres plugin plus the
# Confluent Avro converter. The Debezium images ship the JsonConverter (part of
# Apache Kafka) but not the Confluent AvroConverter, which most production
# Debezium deployments use, so the golden capture adds it here.
#
# The converter is downloaded at build time and verified by checksum. It is
# never redistributed from this repository.
ARG DEBEZIUM_IMAGE
FROM ${DEBEZIUM_IMAGE}

ARG AVRO_CONVERTER_VERSION=7.9.1
ARG AVRO_CONVERTER_SHA256=d2f18788cfcd039bd27442182d3ed52f4ae2611cd49e674fbd869c1a4ec08d47

ADD --checksum=sha256:${AVRO_CONVERTER_SHA256} \
    https://hub-downloads.confluent.io/api/plugins/confluentinc/kafka-connect-avro-converter/versions/${AVRO_CONVERTER_VERSION}/confluentinc-kafka-connect-avro-converter-${AVRO_CONVERTER_VERSION}.zip \
    /tmp/avro-converter.zip

USER root
RUN cd /tmp && unzip -q avro-converter.zip \
    && mkdir -p /kafka/connect/confluent-avro-converter \
    && cp confluentinc-kafka-connect-avro-converter-*/lib/*.jar /kafka/connect/confluent-avro-converter/ \
    && rm -rf /tmp/avro-converter.zip /tmp/confluentinc-kafka-connect-avro-converter-* \
    && chown -R kafka:kafka /kafka/connect/confluent-avro-converter
USER kafka
