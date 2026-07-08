#!/bin/bash
set -e

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

# Get the directory where the script is located
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

source "${SCRIPT_DIR}/common.sh"

TAG=$1

if ! check_semver "$TAG"; then
    echo "$TAG is NOT a valid semver string"
    exit 1
fi

# Check if yq is installed
if ! command -v yq &> /dev/null; then
    echo "Error: yq is not installed. Please install it and try again."
    exit 1
fi

V_TAG="v$TAG"

BRANCH=$(git rev-parse --abbrev-ref HEAD)
CURRENT_TAG=$(get_spec_version connector.yaml)
MSG="You are about to bump the version from ${CURRENT_TAG} to ${V_TAG} on branch '${BRANCH}'.\n"
while true; do
    printf "${MSG}"
    read -p "Are you sure you want to continue? [y/n]" yn
    echo
    case $yn in
        [Yy]* )
            BRANCH_NAME="update-version-$V_TAG"
            git checkout -b "$BRANCH_NAME"
            yq e ".specification.version = \"${V_TAG}\"" -i connector.yaml
            git commit -am "Update version to $V_TAG"
            git push origin "$BRANCH_NAME"

            # Check if gh is installed
            if command -v gh &> /dev/null; then
                echo "Creating pull request..."
                gh pr create \
                    --base main \
                    --title "Update version to $V_TAG" \
                    --body "Automated version update to $V_TAG" \
                    --head "$BRANCH_NAME"
            else
                echo "GitHub CLI (gh) is not installed. To create a PR, please install gh or create it manually."
                echo "Branch '$BRANCH_NAME' has been pushed to origin."
            fi

            echo "Once the change has been merged, you can use scripts/tag.sh to push a new tag."
            break;;
        [Nn]* ) exit;;
        * ) echo "Please answer yes or no.";;
    esac
done
