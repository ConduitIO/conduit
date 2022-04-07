#!/bin/bash

# checks if the installed Go version is less than the required version.
# returns a warning message if it's less than required.

function version_lt() { test "$(echo "$@" | tr " " "\n" | sort -rV | head -n 1)" != "$1"; }

GO_VERSION=$(go version | { read -r _ _ v _; echo "${v#go}"; })
# needs to be changed each time conduit Go version is upgraded
REQUIRED_GO_VERSION="1.18"

if version_lt "$GO_VERSION" "$REQUIRED_GO_VERSION"; then
    echo "WARNING: minimum recommended Go version is go$REQUIRED_GO_VERSION, version installed is go$GO_VERSION"
fi