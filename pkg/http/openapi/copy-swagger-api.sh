#!/usr/bin/env sh

# This script is used to copy the swagger API that is generated remotely by buf
# into a local folder used by the swagger-ui.
cp ../../../proto/api/v1/api.swagger.json ./swagger-ui/api/v1/api.swagger.json
