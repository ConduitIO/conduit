#!/bin/bash
# This script will start Conduit and set up a pipeline which demonstrates
# a transform replacing a record's structured payload with one of its fields.

# The pipeline consists of:
# * a generator source, generating structured records
# * a file destination (it writes to file_destination.txt)
# * a processor with the built-in extract-field transform.

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
DEST_FILE="$SCRIPT_DIR/file_destination.txt"
> "$DEST_FILE"

echo "Running Conduit..."
docker run --rm  --name conduit -v "$DEST_FILE":/file_destination.txt -p 8080:8080 -d ghcr.io/conduitio/conduit:latest

# todo: implement healthcheck in docker
sleep 2

echo "Creating pipeline..."
PIPELINE_ID=$(
curl -Ss -X POST 'http://localhost:8080/v1/pipelines' -d '
{
    "config":
    {
        "name": "test-pipeline-builtin-processor",
        "description": "Test pipeline with built-in processor"
    }
}' | jq -r '.id'
)


echo "Creating a generator source..."
SOURCE_CONN_REQ=$(
jq -n --arg pipeline_id "$PIPELINE_ID" '{
    "type": "TYPE_SOURCE",
    "plugin": "builtin:generator",
    "pipeline_id": $pipeline_id,
    "config":
    {
        "name": "my-generator-source",
        "settings":
        {
            "fields": "id:int,name:string,company:string,trial:bool",
            "format": "structured",
            "readTime": "100ms",
            "recordCount": "3"
        }
    }
}'
)
CONNECTOR_ID=$(curl -Ss -X POST 'http://localhost:8080/v1/connectors' -d "$SOURCE_CONN_REQ" | jq -r '.id')

echo "Creating a built-in processor..."
PROCESSOR_REQ=$(
jq -n --arg connector_id "$CONNECTOR_ID" '{
     "name": "extractfieldpayload",
     "parent":
     {
         "type": "TYPE_CONNECTOR",
         "id": $connector_id
     },
     "config":
     {
         "settings":
         {
             "field": "name"
         }
     }
 }'
)
curl -Ss -X POST 'http://localhost:8080/v1/processors' -d "$PROCESSOR_REQ" > /dev/null

echo "Creating a file destination..."
DEST_CONN_REQ=$(
jq -n  --arg pipeline_id "$PIPELINE_ID" '{
     "type": "TYPE_DESTINATION",
     "plugin": "builtin:file",
     "pipeline_id": $pipeline_id,
     "config":
     {
         "name": "my-file-destination",
         "settings":
         {
             "path": "/file_destination.txt"
         }
     }
 }'
)

curl -Ss -X POST 'http://localhost:8080/v1/connectors' -d "$DEST_CONN_REQ" > /dev/null

echo "Starting the pipeline..."
curl -Ss -X POST "http://localhost:8080/v1/pipelines/$PIPELINE_ID/start" > /dev/null

echo "Pipeline started! Let's give the pipeline some time to run."
sleep 1

echo "Let's check the destination file contents:"
cat "$DEST_FILE"

echo "Stopping Conduit..."
docker stop conduit
