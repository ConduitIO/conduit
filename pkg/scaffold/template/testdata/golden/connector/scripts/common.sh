#!/bin/bash

# Copyright © 2025 Meroxa, Inc.
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

check_semver() {
    local version=$1
    local SV_REGEX="^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(\.(0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(\+([0-9a-zA-Z-]+(\.[0-9a-zA-Z-]+)*))?$"

    if ! [[ $version =~ $SV_REGEX ]]; then
        echo "$version is NOT a valid semver string"
        return 1
    fi
    return 0
}

get_spec_version() {
    local yaml_file=$1

    if command -v yq &> /dev/null; then
        yq '.specification.version' "$yaml_file"
    else
        sed -n '/specification:/,/version:/ s/.*version: //p' "$yaml_file" | tail -1
    fi
}
