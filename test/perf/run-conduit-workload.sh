#!/bin/bash
CONDUIT_IMAGE=$1
WORKLOAD=$2

printf "\n-- Running %s with %s\n" "$WORKLOAD" "$CONDUIT_IMAGE"

docker stop conduit-perf-test || true

docker run --rm --name conduit-perf-test --memory 1g --cpus=2 -v "$(pwd)/plugins":/plugins -p 8080:8080 -d "$CONDUIT_IMAGE"
sleep 1

./run-workload.sh "http://localhost:8080" "$WORKLOAD"

docker stop conduit-perf-test || true
