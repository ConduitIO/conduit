# Start with a golang base
FROM golang:1.17 AS base

# Install core tools
RUN apt-get update &&\
    apt-get install -y curl &&\
    apt-get install -y build-essential &&\
    apt-get install -y git

# Install Node@v12
RUN curl -sL https://deb.nodesource.com/setup_12.x | bash - &&\
    apt-get install -y nodejs &&\
    npm update &&\
    npm i -g yarn@1.22.17

# Build the full app binary
WORKDIR /app
COPY . .
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build

# Copy built binaries to production slim image.
FROM alpine:3.14 AS final
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
