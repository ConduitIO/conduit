#!/bin/bash
CONDUIT_IMAGE=$1

for w in workloads/*.sh; do
  ./run-workload.sh $CONDUIT_IMAGE "$w"
done
