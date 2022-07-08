#!/bin/bash
# This script will start Conduit and set up a pipeline which demonstrates
# a transform replacing a record's structured payload with one of its fields.

# The pipeline consists of:
# * a generator source, generating structured records
# * a file destination (it writes to file_destination.txt)
# * a processor with the built-in extract-field transform.

rm -f file_destination.txt
touch file_destination.txt

DEST_FILE=$(realpath file_destination.txt)

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
        "name": "perf-test",
        "description": "Test performance"
    }
}' | jq -r '.id'
)


echo "Creating a generator source..."
SOURCE_CONN_REQ_1=$(
jq -n --arg pipeline_id "$PIPELINE_ID" '{
    "type": "TYPE_SOURCE",
    "plugin": "builtin:generator",
    "pipeline_id": $pipeline_id,
    "config":
    {
        "name": "generator-source-1",
        "settings":
        {
            "fields": "id:int,name:string,company:string,trial:bool",
            "format": "structured",
            "readTime": "1s",
            "recordCount": "18"
        }
    }
}'
)
CONNECTOR_ID=$(curl -Ss -X POST 'http://localhost:8080/v1/connectors' -d "$SOURCE_CONN_REQ_1" | jq -r '.id')

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
echo "Pipeline started!"
date


