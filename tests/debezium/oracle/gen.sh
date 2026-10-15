#!/usr/bin/env bash
# Regenerate the converter oracle files from the pinned Debezium images.
#   tests/debezium/oracle/gen.sh
# Writes out-<version>.tsv.gz next to this script. The images and digests are
# the ones tests/debezium/capture.sh pins.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
gen() {
  local version=$1 image=$2
  docker run --rm --network none -v "$here:/oracle:ro" --entrypoint java "$image" \
    -cp '/kafka/libs/*' /oracle/Oracle.java 2>/dev/null | gzip -n -9 > "$here/out-$version.tsv.gz"
  echo "$version: $(gzip -dc "$here/out-$version.tsv.gz" | wc -l | tr -d ' ') rows"
}
gen 2.7.4 quay.io/debezium/connect@sha256:5a62d0fb204c3ff3ba66f9e402ec8d628e8b05d527b06185764c72e28dfb1d04
gen 3.7.0 quay.io/debezium/connect@sha256:65a33b215d88122d8db8c1c35f24e8e1aafbadcc148df3064a4d60f3e38ddd6e
