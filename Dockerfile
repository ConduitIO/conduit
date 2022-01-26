# Start with a golang base
FROM golang:1.17 AS base

# Install core tools
RUN apt-get update &&\
    apt-get install -y curl &&\
    apt-get install -y build-essential &&\
    apt-get install -y git

# Install Node@v16
RUN curl -sL https://deb.nodesource.com/setup_16.x | bash - &&\
    apt-get install -y nodejs &&\
    npm update &&\
    npm i -g yarn@1.22.17

# Build the full app binary
WORKDIR /app
COPY . .
# The Kafka plugin currently uses Confluent's Go client for Kafka
# which uses librdkafka, a C library under the hood, so we set CGO_ENABLED=1.
# Soon we should switch to another, CGo-free, client, so we'll be able to set CGO_ENABLED to 0.
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=1 make build

# Copy built binaries to production slim image.
# minideb provides glibc, which librdkafka needs.
FROM bitnami/minideb:bullseye AS final
# HTTP API
EXPOSE 8080/tcp
# gRPC API
EXPOSE 8084/tcp
WORKDIR /app
COPY --from=base /app/conduit /app
COPY --from=base /app/pkg/plugins/generator/generator /app/pkg/plugins/generator/generator
COPY --from=base /app/pkg/plugins/file/file /app/pkg/plugins/file/file
COPY --from=base /app/pkg/plugins/pg/pg /app/pkg/plugins/pg/pg
COPY --from=base /app/pkg/plugins/s3/s3 /app/pkg/plugins/s3/s3
COPY --from=base /app/pkg/plugins/kafka/kafka /app/pkg/plugins/kafka/kafka
ENTRYPOINT ["/app/conduit"]
