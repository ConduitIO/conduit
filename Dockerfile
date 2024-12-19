# Start with a golang base
FROM golang:1.23-bullseye AS base

# Install core tools
RUN apt-get update &&\
    apt-get install -y curl &&\
    apt-get install -y build-essential &&\
    apt-get install -y git

# Build the full app binary
WORKDIR /app
COPY . .
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build

# Copy built binaries to production slim image.
FROM alpine:3.21 AS final
# HTTP API
EXPOSE 8080/tcp
# gRPC API
EXPOSE 8084/tcp
WORKDIR /app
COPY --from=base /app/conduit /app
CMD ["/app/conduit"]
