#!/usr/bin/env bash
# Runs instance I's share of the matrix (of K instances) on a dedicated bench
# instance. Called by the AWS user data (aws/user-data.sh), from the root of a
# conduit checkout at CONDUIT_SHA with this kit in benchi/v021-cdc-parity.
#
#   benchi/v021-cdc-parity/matrix/run-shard.sh K I PHASES OUT
#
# Setup, once per instance: images (Conduit from the checkout, the Connect
# worker, the HTTP plugin image), every pinned image pulled by digest,
# async-profiler, cpusets from the CPU topology, the environment record. Then
# each assigned cell in turn; after each cell the results are synced to
# $MATRIX_S3 (if set), so an instance lost halfway loses one cell at most.
set -uo pipefail
K=${1:?K}; I=${2:?I}; PHASES=${3:-1}; OUT=${4:?out}
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
mkdir -p "$OUT"
log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$OUT/shard.log"; [ -n "${MATRIX_S3:-}" ] && aws s3 cp --quiet "$OUT/shard.log" "$MATRIX_S3/shard-$I/shard.log" || true; }

log "shard $I of $K, phases $PHASES, conduit $(git -C "$ROOT" rev-parse HEAD)"

# cpusets: the SUT gets the last two physical cores (both hyperthreads of
# each, 4 vCPUs); everything else gets the rest. Same 4 vCPUs on any instance
# size, so the SUT budget does not change with the instance type.
eval "$(python3 "$HERE/cpusets.py")"
export SUT_CPUSET INFRA_CPUSET
log "SUT_CPUSET=$SUT_CPUSET INFRA_CPUSET=$INFRA_CPUSET"

export LOADER_BIN="$HERE/../loader/loader"
[ -x "$LOADER_BIN" ] || { log "loader binary missing ($LOADER_BIN)"; exit 1; }

export ASPROF_DIR=/opt/async-profiler
if [ ! -x "$ASPROF_DIR/bin/asprof" ]; then
  curl -fsSL -o /tmp/asprof.tgz https://github.com/async-profiler/async-profiler/releases/download/v4.6/async-profiler-4.6-linux-x64.tar.gz
  echo "1e79ed54d8952889a2e74f993d22ee925e3322460d4f37a2ed95bfb88585da36  /tmp/asprof.tgz" | sha256sum -c - || { log "async-profiler checksum mismatch"; exit 1; }
  sudo mkdir -p "$ASPROF_DIR" && sudo tar -xzf /tmp/asprof.tgz -C "$ASPROF_DIR" --strip-components 1
fi

log "building images"
docker build -q -t matrix-conduit:bench -f "$ROOT/Dockerfile" "$ROOT" >/dev/null || { log "conduit image build failed"; exit 1; }
docker build -q -t matrix-conduit:bench-http -f "$HERE/images/conduit-http.Dockerfile" "$HERE/images" >/dev/null || log "conduit-http image build failed (HTTP cells will fail)"
docker build -q -t matrix-connect:3.7.0 -f "$HERE/images/connect.Dockerfile" "$HERE/images" >/dev/null || { log "connect image build failed"; exit 1; }
for img in $(python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(' '.join(v for k,v in cells.IMG.items() if '@sha256:' in v))"); do
  docker pull -q "$img" >/dev/null || { log "pull failed: $img"; exit 1; }
done

python3 "$HERE/envspec.py" "$ROOT" > "$OUT/environment.txt"

mapfile -t UNITS < <(python3 "$HERE/cells.py" shard "$K" "$I" --phase "$PHASES")
log "cells: ${#UNITS[@]}"
for u in "${UNITS[@]}"; do
  cell=${u% *}; rep=${u#* }
  d="$OUT/$cell$([ "$rep" = 1 ] && echo -rep1)"
  if [ -f "$d/DONE" ]; then log "skip $cell (done)"; continue; fi
  log "cell $cell rep $rep"
  bash "$HERE/run-cell.sh" "$cell" "$OUT" "$rep"
  log "cell $cell done"
  [ -n "${MATRIX_S3:-}" ] && aws s3 sync --quiet "$d" "$MATRIX_S3/shard-$I/$(basename "$d")"
done
cp "$OUT/environment.txt" "$OUT/environment-end.txt"
log "shard $I finished"
[ -n "${MATRIX_S3:-}" ] && aws s3 sync --quiet "$OUT" "$MATRIX_S3/shard-$I" && aws s3 cp --quiet - "$MATRIX_S3/shard-$I/FINISHED" <<<"$(date -u +%FT%TZ)"
