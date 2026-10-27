#!/usr/bin/env bash
# Launches the matrix on AWS. Runs NOTHING locally but a cross-compile and a
# tarball: every benchmark runs on the instances.
#
#   matrix/aws/launch.sh RUN_ID CONDUIT_SHA [instances] [phases] [instance_type] [--smoke]
#
# The kit tarball is `git archive` of the committed kit (uncommitted edits are
# refused: a published run must be reproducible from a commit), plus the
# loader cross-compiled for linux/amd64. Each instance's cap is the estimate of
# its shard plus 25%, so a stuck run cannot bill for days.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
KIT=$(cd "$HERE/../.." && pwd)              # benchi/v021-cdc-parity
RUN_ID=${1:?run id}; SHA=${2:?conduit sha}; N=${3:-2}; PHASES=${4:-1}; TYPE=${5:-c6i.4xlarge}
SMOKE=false; for a in "$@"; do [ "$a" = --smoke ] && SMOKE=true; done
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || { echo "CONDUIT_SHA must be a full 40-character SHA" >&2; exit 1; }
if [ -n "$(git -C "$KIT" status --porcelain -- .)" ]; then
  echo "the kit has uncommitted changes; commit them first (the tarball is built from HEAD)" >&2; exit 1
fi
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
(cd "$KIT/loader" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$WORK/loader" .)
git -C "$KIT" archive --prefix=v021-cdc-parity/ HEAD -- . | tar -x -C "$WORK"
cp "$WORK/loader" "$WORK/v021-cdc-parity/loader/loader"
git -C "$KIT" rev-parse HEAD > "$WORK/v021-cdc-parity/matrix/KIT_COMMIT"
tar -czf "$WORK/kit.tgz" -C "$WORK" v021-cdc-parity
MAXMIN=$(cd "$HERE/.." && MATRIX_SMOKE=$([ "$SMOKE" = true ] && echo 1 || echo 0) python3 -c "
import cells
k, ph = $N, tuple(int(x) for x in '$PHASES'.split(','))
print(int(max(l for l, _ in cells.shard(k, ph)) * 1.25 + cells.SETUP_MIN + 30))
")
echo "run $RUN_ID: $N x $TYPE, phases $PHASES, smoke $SMOKE, cap $MAXMIN min per instance"
cd "$HERE"
terraform init -input=false >/dev/null
terraform workspace select -or-create "$RUN_ID" >/dev/null
terraform apply -input=false -auto-approve \
  -var "run_id=$RUN_ID" -var "conduit_sha=$SHA" -var "instances=$N" -var "phases=$PHASES" \
  -var "instance_type=$TYPE" -var "max_minutes=$MAXMIN" -var "smoke=$SMOKE" -var "kit_tarball=$WORK/kit.tgz"
terraform output
