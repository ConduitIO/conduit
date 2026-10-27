#!/usr/bin/env bash
# Launches one pre-registered plan on AWS. Runs NOTHING locally but a
# cross-compile and a tarball: every benchmark runs on the instances.
#
#   matrix/aws/launch.sh RUN_ID CONDUIT_SHA INSTANCES PLAN [INSTANCE_TYPE] [--gate GATE_JSON]
#
# PLAN: smoke, pilot, flagship, core, holdout (PLAN.md section 12, in that
# order). Every plan after smoke needs --gate: the gate.json analyze.py wrote
# for a PASSING smoke run with the SAME harness hash as the kit being
# launched. A harness change after smoke means smoke runs again.
#
# The kit tarball is `git archive` of the committed kit (uncommitted edits are
# refused) plus the loader cross-compiled for linux/amd64. Each instance's cap
# is its shard's estimate plus 25%.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
KIT=$(cd "$HERE/../.." && pwd)
RUN_ID=${1:?run id}; SHA=${2:?conduit sha}; N=${3:?instances}; PLAN=${4:?plan}; TYPE=${5:-c6i.4xlarge}
GATE=""; prev=""; for a in "$@"; do [ "$prev" = --gate ] && GATE=$a; prev=$a; done
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || { echo "CONDUIT_SHA must be a full 40-character SHA" >&2; exit 1; }
case "$PLAN" in smoke|pilot|flagship|core|holdout) ;; *) echo "unknown plan $PLAN" >&2; exit 1 ;; esac
if [ -n "$(git -C "$KIT" status --porcelain -- .)" ]; then
  echo "the kit has uncommitted changes; commit them first (the tarball is built from HEAD)" >&2; exit 1
fi
HASH=$(cd "$HERE/.." && python3 cells.py hash)
if [ "$PLAN" != smoke ]; then
  [ -n "$GATE" ] || { echo "plan $PLAN needs --gate <gate.json> from a passing smoke run" >&2; exit 1; }
  python3 - "$GATE" "$HASH" <<'PY' || exit 1
import json, sys
g = json.load(open(sys.argv[1]))
if not g.get("pass"):
    sys.exit("smoke gate did not pass: " + "; ".join(g.get("failures", [])[:10]))
if g.get("harness_hash") != sys.argv[2]:
    sys.exit(f"smoke gate is for harness {g.get('harness_hash')}, the kit is {sys.argv[2]}: run smoke again")
PY
fi
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
(cd "$KIT/loader" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$WORK/loader" .)
git -C "$KIT" archive --prefix=v021-cdc-parity/ HEAD -- . | tar -x -C "$WORK"
cp "$WORK/loader" "$WORK/v021-cdc-parity/loader/loader"
git -C "$KIT" rev-parse HEAD > "$WORK/v021-cdc-parity/matrix/KIT_COMMIT"
echo "$HASH" > "$WORK/v021-cdc-parity/matrix/HARNESS_HASH"
tar -czf "$WORK/kit.tgz" -C "$WORK" v021-cdc-parity
MAXMIN=$(cd "$HERE/.." && MATRIX_SMOKE=$([ "$PLAN" = smoke ] && echo 1 || echo 0) python3 -c "
import cells
print(int(max(l for l, _ in cells.shard($N, '$PLAN')) * 1.25 + cells.SETUP_MIN + 30))
")
echo "run $RUN_ID: $N x $TYPE, plan $PLAN, harness $HASH, cap $MAXMIN min per instance"
cd "$HERE"
terraform init -input=false >/dev/null
terraform workspace select -or-create "$RUN_ID" >/dev/null
terraform apply -input=false -auto-approve \
  -var "run_id=$RUN_ID" -var "conduit_sha=$SHA" -var "instances=$N" -var "plan=$PLAN" \
  -var "instance_type=$TYPE" -var "max_minutes=$MAXMIN" -var "kit_tarball=$WORK/kit.tgz"
terraform output
