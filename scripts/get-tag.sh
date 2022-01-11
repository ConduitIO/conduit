#!/bin/bash
# Commits after the last tag will show up in git describe --tags
TAG=`git describe --tags`
# Check if there are uncommited changes (modified, deleted or untracked files)
git diff-index --quiet HEAD --
if [[ $? -eq 0 ]]
then
  echo $TAG
else
  echo "${TAG}-dirty"
fi
