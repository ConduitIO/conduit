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

# Check if the correct number of arguments are provided
if [ "$#" -ne 1 ]; then
    echo "Usage: $0 <repository_directory>"
    exit 1
fi

# Move into the repository directory
cd "$1" || { echo "Failed to move into repository directory"; exit 1; }

# Fetch tags
git fetch --tags || { echo "Failed to fetch tags"; exit 1; }

# Get the latest tag across all branches
latest_tag=$(git describe --tags "$(git rev-list --tags --max-count=1)") || { echo "Failed to get latest tag"; exit 1; }

# Get the owner and repository name from the remote URL
# Get the remote URL
remote_url=$(git config --get remote.origin.url)

# Extract owner/repo from the URL
if [[ $remote_url =~ ^https://github.com/ ]]; then
    # For HTTPS URLs
    owner_repo=$(echo $remote_url | sed -E 's|https://github.com/||' | sed -E 's/\.git$//')
elif [[ $remote_url =~ ^git@ ]]; then
    # For SSH URLs
    owner_repo=$(echo $remote_url | sed -E 's/^git@github.com://' | sed -E 's/\.git$//')
else
    echo "Unrecognized GitHub URL format"
    exit 1
fi

# Create a link to compare the latest tag and the main branch
comparison_link="https://github.com/$owner_repo/compare/$latest_tag...main"

echo "Comparison Link: $comparison_link"
