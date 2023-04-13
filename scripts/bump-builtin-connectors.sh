#!/bin/bash
# Bumps the versions of built-in connectors.
# Requires GitHub CLI.

git checkout main
git pull origin main
git checkout -b bump-builtin-connectors

for conn in 'file' 'kafka' 'generator' 's3' 'postgres' 'log'
do
  go get github.com/conduitio/conduit-connector-$conn@latest
done

go mod tidy

git commit -am "Bump built-in connectors"
git push origin bump-builtin-connectors

gh pr create --fill --head bump-builtin-connectors
