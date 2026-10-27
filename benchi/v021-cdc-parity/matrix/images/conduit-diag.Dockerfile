# Diagnosis build of Conduit: the same checkout as matrix-conduit:bench plus
# images/diag/pprof_benchdiag.go (build tag benchdiag), which serves pprof for
# window-scoped CPU, mutex, block and trace profiles. Used ONLY in unmeasured
# diag runs. Build context: the repository root (run-shard.sh).
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY . .
RUN cp benchi/v021-cdc-parity/matrix/images/diag/pprof_benchdiag.go cmd/conduit/ \
    && CGO_ENABLED=0 go build -tags benchdiag -trimpath -o /out/conduit ./cmd/conduit
FROM matrix-conduit:bench
COPY --from=build --chown=10001:10001 /out/conduit /app/conduit
