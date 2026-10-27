# Conduit plus the conduitio-labs HTTP destination (v0.4.0, Apache-2.0), for
# the HTTP cells only. Conduit ships no HTTP destination of its own; this is
# the only one in the ConduitIO and conduitio-labs inventory (built on SDK
# v0.14, older than the built-in connectors' v0.17). Its results say how a
# labs plugin performs, not how a built-in would.
ARG BASE=matrix-conduit:bench
FROM golang:1.26.10-bookworm AS http
RUN git clone -q https://github.com/conduitio-labs/conduit-connector-http.git /src \
    && cd /src && git checkout -q 9bc6ada3f141ad659cf11ef17cb518b6d2da4359 \
    && CGO_ENABLED=0 go build -trimpath -o /out/conduit-connector-http ./cmd/connector
FROM ${BASE}
COPY --from=http --chown=10001:10001 /out/conduit-connector-http /app/connectors/conduit-connector-http
