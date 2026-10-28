# Debezium Server 3.7.0.Final (JVM) plus the Apicurio Avro converter, for the
# dbzserver-avro arms. The stock image does not ship it (the shakedown's Avro
# arms failed with ClassNotFoundException: io.apicurio...AvroConverter). The
# converter is the one Debezium's own connect images install for 3.7
# (debezium/container-images connect-base/3.7: apicurio-registry-distro-
# connect-converter 3.2.5, same MD5, Jackson jars removed because they
# conflict with the runtime's), placed on run.sh's classpath (lib/*).
FROM quay.io/debezium/server:3.7.0.Final@sha256:9117f3fac0f0ebcc76b364b81d4b914243c65836bc1391a41d23a81d28a15a65
USER root
RUN set -e; \
    curl -fsSL -o /tmp/conv.tgz https://repo1.maven.org/maven2/io/apicurio/apicurio-registry-distro-connect-converter/3.2.5/apicurio-registry-distro-connect-converter-3.2.5.tar.gz; \
    echo "0dc4bef6a31301d1a8da01812e6555bd  /tmp/conv.tgz" | md5sum -c -; \
    mkdir -p /tmp/conv && tar -xzf /tmp/conv.tgz -C /tmp/conv; \
    rm -f /tmp/conv/jackson-core* /tmp/conv/jackson-annotations* /tmp/conv/jackson-databind* /tmp/conv/jackson-dataformat*; \
    cp /tmp/conv/*.jar /debezium/lib/; \
    rm -rf /tmp/conv /tmp/conv.tgz
USER jboss
