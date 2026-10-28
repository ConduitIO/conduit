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
case "$PLAN" in shakedown|smoke|pilot|flagship|core|holdout) ;; *) echo "unknown plan $PLAN" >&2; exit 1 ;; esac
if [ -n "$(git -C "$KIT" status --porcelain -- .)" ]; then
  echo "the kit has uncommitted changes; commit them first (the tarball is built from HEAD)" >&2; exit 1
fi
HASH=$(cd "$HERE/.." && python3 cells.py hash)
if [ "$PLAN" != smoke ] && [ "$PLAN" != shakedown ]; then
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
# Per-run image pins (quay.io deletes superseded Debezium manifests).
python3 "$HERE/../images/pin.py" "$WORK/v021-cdc-parity/matrix/images/PINS.json"
echo "$HASH" > "$WORK/v021-cdc-parity/matrix/HARNESS_HASH"
# No macOS metadata (AppleDouble ._* files, xattrs) in the tarball.
if tar --version 2>/dev/null | grep -qi bsdtar; then
  COPYFILE_DISABLE=1 tar --no-mac-metadata --no-xattrs -czf "$WORK/kit.tgz" -C "$WORK" v021-cdc-parity
else
  tar -czf "$WORK/kit.tgz" -C "$WORK" v021-cdc-parity
fi
tar -tzf "$WORK/kit.tgz" | grep -q '/\._' && { echo "kit tarball contains ._ files" >&2; exit 1; }
MAXMIN=$(cd "$HERE/.." && MATRIX_SMOKE=$([ "$PLAN" = smoke ] || [ "$PLAN" = shakedown ] && echo 1 || echo 0) python3 -c "
import cells
print(int(max(l for l, _ in cells.shard($N, '$PLAN')) * 1.25 + cells.SETUP_MIN + 30))
")
[ -n "${MAX_MINUTES:-}" ] && [ "$MAX_MINUTES" -lt "$MAXMIN" ] && MAXMIN=$MAX_MINUTES  # a budget cap
echo "run $RUN_ID: $N x $TYPE, plan $PLAN, harness $HASH, cap $MAXMIN min per instance"
# The run's kit and vars are kept (aws/runs/RUN_ID/, gitignored) so
# aws/resume.sh can relaunch an interrupted spot instance on the same kit.
RUNDIR="$HERE/runs/$RUN_ID"; mkdir -p "$RUNDIR"; cp "$WORK/kit.tgz" "$RUNDIR/kit.tgz"
cat > "$RUNDIR/vars" <<VARS
run_id=$RUN_ID
conduit_sha=$SHA
instances=$N
plan=$PLAN
instance_type=$TYPE
max_minutes=$MAXMIN
kit_tarball=$RUNDIR/kit.tgz
harness_hash=$HASH
gate_file=${GATE:+$(cd "$(dirname "$GATE")" && pwd)/$(basename "$GATE")}
VARS
cd "$HERE"
terraform init -input=false >/dev/null
terraform workspace select -or-create "$RUN_ID" >/dev/null
# Spot first; on a capacity failure retry spot once, then on-demand. The
# market used is recorded in the bucket (MARKET) and in the run's vars.
apply() { # market
  local args=() k v
  while IFS='=' read -r k v; do args+=(-var "$k=$v"); done < "$RUNDIR/vars"
  terraform apply -input=false -auto-approve "${args[@]}" -var "market=$1" 2>&1 | tee "$RUNDIR/apply-$1.log"
  return "${PIPESTATUS[0]}"
}
MARKET=""
for m in spot spot on-demand; do
  if apply "$m"; then MARKET=$m; break; fi
  grep -qiE 'InsufficientInstanceCapacity|capacity-not-available|MaxSpotInstanceCountExceeded|SpotMaxPriceTooLow|UnfulfillableCapacity|InsufficientCapacity' "$RUNDIR/apply-$m.log" || { echo "apply failed (not a capacity error); see $RUNDIR/apply-$m.log" >&2; exit 1; }
  echo "spot capacity failure ($m); retrying" >&2
done
[ -n "$MARKET" ] || { echo "no capacity" >&2; exit 1; }
echo "market=$MARKET" >> "$RUNDIR/vars"
B=$(terraform output -raw bucket)
echo "$MARKET $(date -u +%FT%TZ)" | aws s3 cp --quiet - "s3://$B/MARKET-$(date -u +%Y%m%dT%H%M%SZ)"
echo "market: $MARKET"
terraform output
