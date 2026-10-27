#!/usr/bin/env bash
# Runs instance I's share (of K instances) of one plan on a dedicated bench
# instance. Called by the AWS user data, from the root of a conduit checkout at
# CONDUIT_SHA with this kit in benchi/v021-cdc-parity.
#
#   benchi/v021-cdc-parity/matrix/run-shard.sh K I PLAN OUT
#
# Setup, once per instance: cpusets from the CPU topology; images (Conduit from
# the checkout, its -diag and -http variants, the Connect worker); every pinned
# image pulled by digest; async-profiler and Jolokia, checksum-verified; the
# environment record. The harness itself (this script, the samplers, every
# docker CLI call) runs pinned to the INFRA cpuset, so it never takes SUT CPU.
# After each cell the results are synced to $MATRIX_S3.
set -uo pipefail
K=${1:?K}; I=${2:?I}; PLAN=${3:?plan}; OUT=${4:?out}
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
mkdir -p "$OUT"
log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$OUT/shard.log"; [ -n "${MATRIX_S3:-}" ] && aws s3 cp --quiet "$OUT/shard.log" "$MATRIX_S3/shard-$I/shard.log" || true; }

# cpusets.py prints two KEY=value lines (its own output, no user input).
eval "$(python3 "$HERE/cpusets.py")"
export SUT_CPUSET INFRA_CPUSET
if [ -z "${MATRIX_PINNED:-}" ]; then
  export MATRIX_PINNED=1
  exec taskset -c "$INFRA_CPUSET" "$0" "$@"
fi
log "shard $I of $K, plan $PLAN, conduit $(git -C "$ROOT" rev-parse HEAD), harness $(python3 "$HERE/cells.py" hash)"
log "SUT_CPUSET=$SUT_CPUSET INFRA_CPUSET=$INFRA_CPUSET"
# The kit must be exactly what terraform was told it is (the smoke gate's
# precondition is checked against that declared hash).
H=$(python3 "$HERE/cells.py" hash)
[ "$H" = "$(cat "$HERE/HARNESS_HASH" 2>/dev/null)" ] && [ "$H" = "${MATRIX_HARNESS_HASH:-}" ] || {
  log "harness hash $H differs from the launched kit ($(cat "$HERE/HARNESS_HASH" 2>/dev/null)) or the declared one (${MATRIX_HARNESS_HASH:-unset})"; exit 1; }

export LOADER_BIN="$HERE/../loader/loader"
[ -x "$LOADER_BIN" ] || { log "loader binary missing ($LOADER_BIN)"; exit 1; }

export ASPROF_DIR=/opt/async-profiler
if [ ! -x "$ASPROF_DIR/bin/asprof" ]; then
  curl -fsSL -o /tmp/asprof.tgz https://github.com/async-profiler/async-profiler/releases/download/v4.6/async-profiler-4.6-linux-x64.tar.gz
  echo "1e79ed54d8952889a2e74f993d22ee925e3322460d4f37a2ed95bfb88585da36  /tmp/asprof.tgz" | sha256sum -c - || { log "async-profiler checksum mismatch"; exit 1; }
  mkdir -p "$ASPROF_DIR" && tar -xzf /tmp/asprof.tgz -C "$ASPROF_DIR" --strip-components 1
fi
export JOLOKIA_JAR=/opt/jolokia/jolokia.jar
if [ ! -f "$JOLOKIA_JAR" ]; then
  mkdir -p /opt/jolokia
  read -r JURL JSHA <<<"$(python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(*cells.JOLOKIA)")"
  curl -fsSL -o "$JOLOKIA_JAR" "$JURL"
  echo "$JSHA  $JOLOKIA_JAR" | sha1sum -c - || { log "jolokia checksum mismatch"; exit 1; }
  chmod 644 "$JOLOKIA_JAR"
fi

log "building images"
docker build -q -t matrix-conduit:bench -f "$ROOT/Dockerfile" "$ROOT" >/dev/null || { log "conduit image build failed"; exit 1; }
# The diag build is the measured build plus one build tag: the SAME Dockerfile
# (same base image, Go version, make target and flags), with the pprof file
# added and -tags benchdiag on the same go build (review R2-M3).
python3 "$HERE/images/diag-dockerfile.py" "$ROOT/Dockerfile" "$ROOT/Makefile" > "$ROOT/.matrix-diag.Dockerfile" || { log "diag Dockerfile"; exit 1; }
docker build -q -t matrix-conduit:bench-diag -f "$ROOT/.matrix-diag.Dockerfile" "$ROOT" >/dev/null || { log "conduit-diag image build failed"; exit 1; }
rm -f "$ROOT/.matrix-diag.Dockerfile"
docker build -q -t matrix-conduit:bench-http -f "$HERE/images/conduit-http.Dockerfile" "$HERE/images" >/dev/null || log "conduit-http image build failed (HTTP cells will fail)"
docker build -q -t matrix-connect:3.7.0 -f "$HERE/images/connect.Dockerfile" "$HERE/images" >/dev/null || { log "connect image build failed"; exit 1; }
for img in $(python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(' '.join(v for v in cells.IMG.values() if '@sha256:' in v))"); do
  docker pull -q "$img" >/dev/null || { log "pull failed: $img"; exit 1; }
done

python3 "$HERE/envspec.py" "$ROOT" "$LOADER_BIN" > "$OUT/environment.txt"

mapfile -t UNITS < <(python3 "$HERE/cells.py" shard "$K" "$I" --plan "$PLAN")
log "cells: ${#UNITS[@]}"
for u in "${UNITS[@]}"; do
  cell=${u% *}; rep=${u#* }
  d="$OUT/$cell$([ "$rep" != 0 ] && echo "-rep$rep")"
  if [ -f "$d/DONE" ]; then log "skip $cell (done)"; continue; fi
  log "cell $cell rep $rep"
  bash "$HERE/run-cell.sh" "$cell" "$OUT" "$rep"
  log "cell $cell done"
  [ -n "${MATRIX_S3:-}" ] && aws s3 sync --quiet "$d" "$MATRIX_S3/shard-$I/$(basename "$d")"
done
python3 "$HERE/envspec.py" "$ROOT" "$LOADER_BIN" > "$OUT/environment-end.txt"
log "shard $I finished"
[ -n "${MATRIX_S3:-}" ] && aws s3 sync --quiet "$OUT" "$MATRIX_S3/shard-$I" && aws s3 cp --quiet - "$MATRIX_S3/shard-$I/FINISHED" <<<"$(date -u +%FT%TZ)"
