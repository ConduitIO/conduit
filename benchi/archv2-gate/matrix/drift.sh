#!/usr/bin/env bash
# Drift check: how much did main move since the commit the matrix was built
# from? Runs v1 and arch-v2 from both commits in one interleaved session, so
# "v1 on the base" against "v1 on main" is an engine-change measurement and the
# arms' A/A floors say whether it is resolvable. Needs conduit-main (the 4th
# argument of build.sh). Run from the repository root.
#
#   benchi/archv2-gate/matrix/drift.sh <bins-dir> <out-dir> [shape ...]
set -u
BINS=${1:?bins dir}; OUT=${2:?out dir}; shift 2
GATE=${GATE:-go run ./benchi/archv2-gate}
for shape in "${@:-1x1}"; do
  [ -s "$OUT/drift-$shape/summary.md" ] && { echo "skip drift-$shape (done)"; continue; }
  echo "=== drift-$shape $(date -u +%FT%TZ)"
  $GATE -session matrix -shape "$shape" -rounds "${ROUNDS:-5}" -warmup "${WARMUP:-8s}" -window "${WINDOW:-25s}" \
    -out "$OUT/drift-$shape" \
    -arm "v1-base=v1:$BINS/conduit-base" -arm "v1-main=v1:$BINS/conduit-main" \
    -arm "v2-base=v2:$BINS/conduit-base" -arm "v2-main=v2:$BINS/conduit-main" 2>&1 | grep -v '^warning: -window' | tail -n 3
done
