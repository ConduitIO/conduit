#!/usr/bin/env sh

# This script is used to copy the swagger API that is generated remotely by buf
# into a local folder used by the swagger-ui.

rm -f ./swagger-ui/api/v1/api.swagger.json
bufdir=$(go list -m -f '{{.Dir}}' go.buf.build/conduitio/conduit/conduitio/conduit)
cp ${bufdir}/api/v1/api.swagger.json ./swagger-ui/api/v1/api.swagger.json