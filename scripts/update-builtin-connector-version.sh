#!/bin/bash

set -euo pipefail

# Get the latest git tag in the format vX.Y.Z
LATEST_TAG=$(git describe --tags --abbrev=0)
echo "Latest Git Tag: $LATEST_TAG"

# Check if the tag matches the expected format vX.Y.Z
if [[ ! "$LATEST_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Error: Latest tag '$LATEST_TAG' does not match the expected format vX.Y.Z."
    exit 1
fi

# Extract major, minor, patch versions
MAJOR=$(echo "$LATEST_TAG" | sed -E 's/^v([0-9]+)\..*$/\1/')
MINOR=$(echo "$LATEST_TAG" | sed -E 's/^v[0-9]+\.([0-9]+)\..*$/\1/')

# Calculate the next minor version with a development suffix
NEXT_MINOR=$((MINOR + 1))
NEXT_DEV_VERSION="v${MAJOR}.${NEXT_MINOR}.0-develop"
echo "Calculated Next Development Version: $NEXT_DEV_VERSION"

# File to update
VERSION_FILE="pkg/plugin/connector/builtin/version.go"

# Check if the file exists
if [ ! -f "$VERSION_FILE" ]; then
    echo "Error: Version file '$VERSION_FILE' not found."
    exit 1
fi

# Update the const Version in the Go file
# It matches the line `const Version = "..."` and replaces the string content.
sed -i -E 's/(const Version = ")[^"]+(")/\1'"$NEXT_DEV_VERSION"'\2/' "$VERSION_FILE"

echo "Updated $VERSION_FILE to $NEXT_DEV_VERSION"

# Verify the change
if grep -q "const Version = \"$NEXT_DEV_VERSION\"" "$VERSION_FILE"; then
    echo "Verification successful: $VERSION_FILE now contains const Version = \"$NEXT_DEV_VERSION\""
else
    echo "Verification failed: $VERSION_FILE was not updated correctly."
    exit 1
fi

# Configure Git for commit
git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"

# Commit and push the changes
git add "$VERSION_FILE"
git commit -m "chore(builtin): update built-in connector version to $NEXT_DEV_VERSION"
git push origin HEAD

echo "Successfully committed and pushed the version update."
