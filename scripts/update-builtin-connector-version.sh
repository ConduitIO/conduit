#!/bin/bash
set -euo pipefail

# Argument 1: The new version string (e.g., "v1.2.3" or "v1.3.0-develop")
NEW_VERSION=$1
FILE="pkg/plugin/connector/builtin/registry.go"

echo "Updating built-in connector version to ${NEW_VERSION} in ${FILE}"

# Use sed to update the version string in the specified Go file.
# The regex captures the "const Version = \"" and the trailing "\""
# and replaces only the content between them with the NEW_VERSION.
sed -i -E "s/(const Version = \")[^\"]+(\")/\1${NEW_VERSION}\2/g" "${FILE}"

echo "Git committing and pushing changes"

# Configure Git user for the automated commit
git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"

# Add the modified file, commit the change, and push to the main branch
git add "${FILE}"
git commit -m "chore: update built-in connector version to ${NEW_VERSION}"
git push origin main
