#!/bin/bash
CONDUIT_IMAGE=$1

for w in workloads/*.sh; do
  docker stop conduit-perf-test || true

  docker run --rm  --name conduit-perf-test -v "$(pwd)/plugins":/plugins -p 8080:8080 -d "$CONDUIT_IMAGE"
  sleep 1

  bash "$w" || break

  # todo print all results from this run into the same file
  go run main.go --interval=5m --duration=5m --print-to=csv --workload="$w"
done
