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

# Get the latest tag
latest_tag=$(git describe --tags --abbrev=0) || { echo "Failed to get latest tag"; exit 1; }

# Get the owner and repository name from the remote URL
repo_url=$(git config --get remote.origin.url)
IFS='/' read -ra parts <<< "$repo_url"
owner="${parts[-2]}"
repo="${parts[-1]%.git}" # Remove the .git extension if it exists

# Create a link to compare the latest tag and the main branch
comparison_link="https://github.com/$owner/$repo/compare/$latest_tag...main"

echo "Comparison Link: $comparison_link"
