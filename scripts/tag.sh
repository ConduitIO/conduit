#!/bin/bash
TAG=$1

SV_REGEX="^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(\.(0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(\+([0-9a-zA-Z-]+(\.[0-9a-zA-Z-]+)*))?$"

if ! [[ $TAG =~ $SV_REGEX ]]; then
    echo "$TAG is NOT a valid semver string"
    exit 1
fi

V_TAG="v$TAG"
HAS_UNCOMMITTED=`git status --porcelain=v1 2>/dev/null | wc -l | awk '{print $1}'`
if (( $HAS_UNCOMMITTED != 0 )); then
  echo "You have uncommitted changes, cannot tag."
  exit 1
fi

LAST_COMMIT=`git log -1 --oneline`
BRANCH=`git rev-parse --abbrev-ref HEAD`
CURRENT_TAG=`git describe --tags --abbrev=0`
MSG="You are about to bump the version from ${CURRENT_TAG} to ${V_TAG}.\nCurrent commit is '${LAST_COMMIT}' on branch '${BRANCH}'.\nThe release process is automatic and quick, so if you make a mistake, everyone will see it very soon.\n"
while true; do
    printf "${MSG}"
    read -p "Are you sure you want to continue? [y/n]" yn
    echo
    case $yn in
        [Yy]* ) git tag -a $V_TAG -m "Release: $V_TAG" && git push origin $V_TAG; break;;
        [Nn]* ) exit;;
        * ) echo "Please answer yes or no.";;
    esac
done
