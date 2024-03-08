#!/bin/bash

# Absolute path to this script, e.g. /home/user/bin/foo.sh
SCRIPT=$(readlink -f "$0")
# Absolute path this script is in, thus /home/user/bin
BASEDIR=$(dirname "$SCRIPT")

t="$1"
if [ -z "$t" ]
then
  echo "Missing argument: package name"
  exit 1
fi

for len in $(seq ${#t} -1 3)
do
  line=$(grep "${t:0:len}" "${BASEDIR}/../go.mod")
  grep_status=$?
  if [ $grep_status -eq 0 ]; then
    if [ "$(echo "$line" | wc -l)" -gt 1 ]; then
      printf "Found multiple matches for %s in go.mod.\n%s" "$t" "$line"
      exit 1
    fi

    awk -v "IMPORT=$t" '{print IMPORT "@" $2}' <<< "$line"
    break 2
  fi
done

exit $grep_status