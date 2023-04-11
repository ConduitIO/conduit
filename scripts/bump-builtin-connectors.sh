#!/bin/bash
# Bumps the versions of built-in connectors

git checkout main
git pull origin main
git checkout -b bump-builtin-connectors

go get github.com/conduitio/conduit-connector-file@latest
go get github.com/conduitio/conduit-connector-generator@latest
go get github.com/conduitio/conduit-connector-kafka@latest
go get github.com/conduitio/conduit-connector-log@latest
go get github.com/conduitio/conduit-connector-postgres@latest
go get github.com/conduitio/conduit-connector-s3@latest

go mod tidy

git commit -am "Bump built-in connectors"
git push origin bump-builtin-connectors

gh pr create --fill --head bump-builtin-connectors
