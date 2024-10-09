#!/bin/bash

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
