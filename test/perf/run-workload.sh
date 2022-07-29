#!/bin/bash
CONDUIT_IMAGE=$1
WORKLOAD=$2

echo "$CONDUIT_IMAGE"
echo "$WORKLOAD"

docker stop conduit-perf-test || true

docker run --rm --name conduit-perf-test --memory 1g --cpus=2 -v "$(pwd)/plugins":/plugins -p 8080:8080 -d "$CONDUIT_IMAGE"
sleep 1

bash "$WORKLOAD"

# todo print all results from this run into the same file
go run main.go --interval=5m --duration=5m --print-to=csv --workload="$WORKLOAD"

docker stop conduit-perf-test || true
