#!/bin/bash
# This script will start Conduit and set up a pipeline which demonstrates
# a transform replacing a record's structured payload with one of its fields.
# The pipeline is define in pipeline-extract-field-transform.yml.
# The generator source will generate 3 records, which are in the format of this example:

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
DEST_FILE="$SCRIPT_DIR/file_destination.txt"
> "$DEST_FILE"

echo "Running Conduit..."
docker run --rm  \
    --name conduit \
    -v "$SCRIPT_DIR/pipeline-extract-field-transform.yml":/app/pipelines/pipeline.yml \
    -v "$DEST_FILE":/file_destination.txt \
    -p 8080:8080 \
    -d \
    ghcr.io/conduitio/conduit:latest

echo "Pipeline started! Let's give the pipeline some time to run."
sleep 1

echo "Let's check the destination file contents:"
cat "$DEST_FILE" | jq -r '.payload.after | @base64d'

echo "Stopping Conduit..."
docker stop conduit
