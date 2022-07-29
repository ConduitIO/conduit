#!/bin/bash
CONDUIT_IMAGE=$1

cat << EOF


When interpreting test results, please take into account,
that if built-in plugins are used, their resource usage is part of Conduit's usage too.


EOF

for w in workloads/*.sh; do
  ./run-workload.sh $CONDUIT_IMAGE "$w"
done
