#!/usr/bin/env bash
# The engine matrix: v1, arch-v2 and the staged-batch prototype across shapes,
# ack delivery and destination batching. See matrix/README.md for what it
# measures and how to reproduce it. Run from the repository root.
#
#   benchi/archv2-gate/matrix/run.sh <bins-dir> <out-dir>
#
# <bins-dir> holds linux conduit binaries named conduit-base (v1 and arch-v2
# arms), conduit-proto (prototype arm) and, for the drift session, conduit-main.
# Build them with matrix/build.sh. Every session is interleaved: each arm runs
# twice per round, so each arm has its own A/A pair in the same session.
set -u
BINS=${1:?bins dir}; OUT=${2:?out dir}
GATE=${GATE:-go run ./benchi/archv2-gate}
ROUNDS=${ROUNDS:-5}; WARMUP=${WARMUP:-8s}; WINDOW=${WINDOW:-25s}
BUF="CONDUIT_BENCH_DEST_ACK_LATENCY=1us"   # acks delivered through a pump goroutine (gRPC-like)

run() { # run <name> <shape> [extra harness args...]
  name=$1; shape=$2; shift 2
  [ -s "$OUT/$name/summary.md" ] && { echo "skip $name (done)"; return; }
  echo "=== $name $(date -u +%FT%TZ)"
  $GATE -session matrix -shape "$shape" -rounds "$ROUNDS" -warmup "$WARMUP" -window "$WINDOW" \
    -out "$OUT/$name" "$@" 2>&1 | grep -v '^warning: -window' | tail -n 3
}
ARMS=(-arm "v1=v1:$BINS/conduit-base" -arm "v2=v2:$BINS/conduit-base" -arm "proto=v2:$BINS/conduit-proto")

# Priority order: default config first, then buffered acks, then the
# diagnostics, then destination batching.
for n in 1 2 4; do run "s${n}x${n}-unbuffered"  "${n}x${n}"  "${ARMS[@]}"; done
for n in 1 2 4; do run "s${n}x${n}-buffered"    "${n}x${n}"  "${ARMS[@]}" -env "$BUF"; done
# Alternative A in isolation: arch-v2 with the source batch at 100, beside the
# default-config arms of the same session.
run "alt-a-1x1" 1x1 "${ARMS[@]}" -arm "v2-alta=v2:$BINS/conduit-base@1x1-srcbatch100"
for n in 1 2 4; do run "s${n}x${n}-destbatch-unbuffered" "${n}x${n}-destbatch" "${ARMS[@]}"; done
for n in 1 2 4; do run "s${n}x${n}-destbatch-buffered"   "${n}x${n}-destbatch" "${ARMS[@]}" -env "$BUF"; done
echo "=== done $(date -u +%FT%TZ)"
