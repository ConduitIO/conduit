#!/bin/bash

# Copyright © 2024 Meroxa, Inc.
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

# checks if the installed Go version is less than the required version.
# returns a warning message if it's less than required.

function version_lt() { test "$(echo "$@" | tr " " "\n" | sort -rV | head -n 1)" != "$1"; }

GO_VERSION=$(go version | { read -r _ _ v _; echo "${v#go}"; })
MIN_GO_VERSION=$(go list -m -f "{{.GoVersion}}")

if version_lt "$GO_VERSION" "$MIN_GO_VERSION"; then
    echo "ERROR: min Go version required is go$MIN_GO_VERSION, version installed is go$GO_VERSION"
fi
