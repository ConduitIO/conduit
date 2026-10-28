#!/usr/bin/env bash
# Relaunches a run's missing instances (a spot interruption) on the same kit
# and vars. Each instance resumes from the DONE markers in S3: only the cell
# that was interrupted runs again.
#   matrix/aws/resume.sh RUN_ID
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
RUN_ID=${1:?run id}; RUNDIR="$HERE/runs/$RUN_ID"
[ -f "$RUNDIR/vars" ] || { echo "no saved vars for $RUN_ID" >&2; exit 1; }
cd "$HERE"
terraform workspace select "$RUN_ID" >/dev/null
args=(); while IFS='=' read -r k v; do [ "$k" = market ] || args+=(-var "$k=$v"); done < "$RUNDIR/vars"
MARKET=$(grep '^market=' "$RUNDIR/vars" | tail -1 | cut -d= -f2); MARKET=${MARKET:-spot}
terraform apply -input=false -auto-approve "${args[@]}" -var "market=$MARKET"
B=$(terraform output -raw bucket)
echo "resume $MARKET $(date -u +%FT%TZ)" | aws s3 cp --quiet - "s3://$B/MARKET-$(date -u +%Y%m%dT%H%M%SZ)"
