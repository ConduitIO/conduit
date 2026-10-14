#!/usr/bin/env bash
# Builds the linux Conduit binaries the matrix runs, each with the bench-only
# ack-latency hook (patches/bench-ack-latency.patch) applied in a throwaway
# worktree. The hook is inert unless CONDUIT_BENCH_DEST_ACK_LATENCY is set, and
# it is applied to every binary, so all arms carry the same code path. Nothing
# here touches your working tree.
#
#   benchi/archv2-gate/matrix/build.sh <out-dir> <base-ref> <proto-ref> [<main-ref>]
#
# base-ref is the commit the v1 and arch-v2 arms are built from, proto-ref the
# staged-engine prototype (#2978). Use the prototype's merge base as base-ref
# so the engines differ only by the prototype's change. main-ref, if given, adds
# conduit-main for the drift session. Run from the repository root.
set -eu
OUT=$(cd "$(dirname "${1:?out dir}")" && pwd)/$(basename "$1")
BASE=${2:?base ref}; PROTO=${3:?proto ref}; MAIN=${4:-}
ROOT=$(git rev-parse --show-toplevel)
PATCH=$ROOT/benchi/archv2-gate/matrix/patches/bench-ack-latency.patch
GOARCH=${GOARCH:-$(go env GOARCH)}
mkdir -p "$OUT"
tmp=$(mktemp -d)
trap 'git -C "$ROOT" worktree prune; rm -rf "$tmp"' EXIT

build() { # build <name> <ref>
  git -C "$ROOT" worktree add -q --detach "$tmp/$1" "$2"
  git -C "$tmp/$1" apply "$PATCH"
  GOOS=linux GOARCH=$GOARCH CGO_ENABLED=0 go -C "$tmp/$1" build -o "$OUT/conduit-$1" ./cmd/conduit
  git -C "$ROOT" worktree remove --force "$tmp/$1"
  echo "conduit-$1 $(git -C "$ROOT" rev-parse "$2") $(shasum -a 256 "$OUT/conduit-$1" | cut -d' ' -f1)" >> "$OUT/BUILDS.txt"
}
: > "$OUT/BUILDS.txt"
build base "$BASE"
build proto "$PROTO"
[ -n "$MAIN" ] && build main "$MAIN"
cat "$OUT/BUILDS.txt"
