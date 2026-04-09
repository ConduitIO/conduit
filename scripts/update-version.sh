#!/bin/bash
# This script updates the version constant in pkg/conduit/version.go.

set -euo pipefail

NEW_VERSION="$1"
FILE="pkg/conduit/version.go"

if [ -z "$NEW_VERSION" ]; then
    echo "Usage: $0 <version>"
    exit 1
fi

# Use perl -pi -e for in-place editing across different OS (macOS/Linux)
# The pattern specifically targets 'var version = "..."'
perl -pi -e 's/^(var version = ).*$/${1}"'$NEW_VERSION'"/' "$FILE"

echo "Updated $FILE to version: $NEW_VERSION"
