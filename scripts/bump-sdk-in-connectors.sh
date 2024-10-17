#!/bin/bash

# Copyright © 2023 Meroxa, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

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
	cd ../conduit-connector-$conn || exit

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

	cd ../conduit || exit
done
