# Start with a golang base
FROM golang:1.26.9-bookworm AS base

# Build the full app binary
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 make build

# Copy built binaries to production slim image.
FROM alpine:3.24 AS final

# Run as a fixed, non-root user. The numeric id lets Kubernetes verify
# runAsNonRoot without resolving a name. /data is created here, owned by that
# user, so a named volume mounted on it starts out writable.
RUN addgroup -g 10001 -S conduit \
    && adduser -u 10001 -S -G conduit -h /app -s /sbin/nologin conduit \
    && mkdir -p /data \
    && chown conduit:conduit /data

# Inside a container the API must listen on all interfaces, or published ports
# cannot reach it. Conduit refuses to start on a non-loopback address unless an
# API token is set, so supply one with CONDUIT_API_TOKEN or CONDUIT_API_TOKEN_FILE,
# and publish the ports on the host loopback only unless something
# authenticated sits in front:
#
#   docker run -e CONDUIT_API_TOKEN="$(openssl rand -hex 32)" \
#     -p 127.0.0.1:8080:8080 -p 127.0.0.1:8084:8084 ...
ENV CONDUIT_API_HTTP_ADDRESS=0.0.0.0:8080 \
    CONDUIT_API_GRPC_ADDRESS=0.0.0.0:8084

# HTTP API
EXPOSE 8080/tcp
# gRPC API
EXPOSE 8084/tcp
WORKDIR /app
COPY --from=base --chown=conduit:conduit /app/conduit /app
RUN chown conduit:conduit /app
USER 10001:10001
CMD ["/app/conduit", "run"]
