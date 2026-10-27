#!/usr/bin/env bash
# Destroys everything a run created (instances, bucket and results, role,
# security group). Collect first.
#   matrix/aws/destroy.sh RUN_ID
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$HERE"
terraform workspace select "${1:?run id}" >/dev/null
terraform destroy -input=false -auto-approve -var "run_id=$1" -var "conduit_sha=0000000000000000000000000000000000000000" \
  -var "max_minutes=1" -var "plan=smoke" -var "kit_tarball=/dev/null"
