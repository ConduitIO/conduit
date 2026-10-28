# Debezium Server 3.7.0.Final (JVM) plus the Apicurio Avro converter, for the
# dbzserver-avro arms. The stock image does not ship it (the shakedown's Avro
# arms failed with ClassNotFoundException: io.apicurio...AvroConverter). The
# converter is the one Debezium's own connect images install for 3.7
# (debezium/container-images connect-base/3.7: apicurio-registry-distro-
# connect-converter 3.2.5, same MD5, Jackson jars removed because they
# conflict with the runtime's), placed on run.sh's classpath (lib/*).
# Extracted in an alpine stage: the server image has no gzip.
# BASE: quay.io/debezium/server:3.7.0.Final@<digest>, resolved per run (images/pin.py).
ARG BASE=quay.io/debezium/server:3.7.0.Final
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS conv
RUN set -e; \
    wget -q -O /tmp/conv.tgz https://repo1.maven.org/maven2/io/apicurio/apicurio-registry-distro-connect-converter/3.2.5/apicurio-registry-distro-connect-converter-3.2.5.tar.gz; \
    echo "0dc4bef6a31301d1a8da01812e6555bd  /tmp/conv.tgz" | md5sum -c -; \
    mkdir -p /conv && tar -xzf /tmp/conv.tgz -C /conv; \
    rm -f /conv/jackson-core* /conv/jackson-annotations* /conv/jackson-databind* /conv/jackson-dataformat*; \
    ls /conv/*.jar >/dev/null
FROM ${BASE}
COPY --from=conv /conv/*.jar /debezium/lib/
