#!/bin/bash
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


echo "Creating a normal source..."
NORMAL_SOURCE=$(
jq -n --arg pipeline_id "$PIPELINE_ID" '{
    "type": "TYPE_SOURCE",
    "plugin": "builtin:generator",
    "pipeline_id": $pipeline_id,
    "config":
    {
        "name": "normal-source",
        "settings":
        {
            "format.type": "structured",
            "format.options": "id:int,name:string,company:string,trial:bool",
            "readTime": "100ms",
            "recordCount": "-1"
        }
    }
}'
)
curl -Ss -X POST 'http://localhost:8080/v1/connectors' -d "$NORMAL_SOURCE" > /dev/null

echo "Creating a burst source..."
BURST_REQ=$(
jq -n --arg pipeline_id "$PIPELINE_ID" '{
    "type": "TYPE_SOURCE",
    "plugin": "builtin:generator",
    "pipeline_id": $pipeline_id,
    "config":
    {
        "name": "burst-source",
        "settings":
        {
            "format.type": "structured",
            "format.options": "id:int,name:string,company:string,trial:bool",
            "readTime": "1ms",
            "burst.sleepTime": "30s",
            "burst.generateTime": "30s",
            "recordCount": "-1"
        }
    }
}'
)
curl -Ss -X POST 'http://localhost:8080/v1/connectors' -d "$BURST_REQ" > /dev/null

echo "Creating a NoOp destination..."
DEST_CONN_REQ=$(
jq -n  --arg pipeline_id "$PIPELINE_ID" '{
     "type": "TYPE_DESTINATION",
     "plugin": "/plugins/conduit-connector-noop-dest",
     "pipeline_id": $pipeline_id,
     "config":
     {
         "name": "my-noop-destination",
         "settings": {}
     }
 }'
)

curl -Ss -X POST 'http://localhost:8080/v1/connectors' -d "$DEST_CONN_REQ" > /dev/null

echo "Starting the pipeline..."
curl -Ss -X POST "http://localhost:8080/v1/pipelines/$PIPELINE_ID/start" > /dev/null
echo "Pipeline started!"
