#!/bin/bash
# Bumps the SDK version in the built-in connectors.
# The script assumes that the Conduit repo and the repos
# for all built-in connectors are in the same directory.
# Requires GitHub CLI.
if [ $# -eq 0 ]
then
  echo "No arguments supplied"
  exit 1
fi

if [ -z "$1" ]
then
  echo "Version is empty"
  exit 1
fi

SDK_V=$1

for conn in 'file' 'kafka' 'generator' 's3' 'postgres' 'log'
do
	cd ../conduit-connector-$conn

	echo
	echo "Working on conduit-connector-$conn"

	git checkout main
	git pull origin main
	git checkout -b bump-sdk-version-$SDK_V

	go get github.com/conduitio/conduit-connector-sdk@$SDK_V
	go mod tidy

	git commit -am "Bump SDK version to $SDK_V"
	git push origin bump-sdk-version-$SDK_V

	gh pr create --fill --head bump-sdk-version-$SDK_V

	cd ../conduit
done
