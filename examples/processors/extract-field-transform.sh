#!/bin/bash
# In this example, we have a processor which replaces a record's
# structured payload with one of its fields.

# The whole pipeline is define in pipeline-extract-field-transform.yml.
# It has a generator source which will generates records.
# The records are in the format of this example:
#{
#  "company": "string 5c778521-ca78-4aab-8ddd-03e541f38fcc",
#  "id": 384814097,
#  "name": "string 85227f17-120e-447c-9e46-c7d57c6dc8ec",
#  "trial": false
#}
# The records are replaced with the name of the "name" field, using a built-in processor,
# and the output is then written to a destination file.

# Prepare the destination file
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

# Conduit records come in the OpenCDC format
# (more information is available at: https://github.com/ConduitIO/conduit/blob/main/docs/design-documents/20220309-opencdc.md).
# We use jq to extract just the payload.
echo "Let's check the destination file contents:"
cat "$DEST_FILE" | jq -r '.payload.after | @base64d'

echo "Stopping Conduit..."
docker stop conduit
