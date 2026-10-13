#!/usr/bin/env bash
# Interleaved base/head engine benchmark with an A/A control, for local use.
#
# Two sequential `make bench-engine` runs on identical code routinely differ by
# several percent (thermal state, background load drift between invocations), so
# a before/after comparison made that way reports noise as signal. This runs the
# base build, the working tree, and the base build again in rotating order, one
# round at a time, so drift hits all three alike. The second base run (A/A) is
# the noise floor: a head delta smaller than the A/A delta is not a result.
#
# Usage: scripts/bench-engine-ab.sh [BASE_REF] [ROUNDS]
#   BASE_REF  git ref to compare against (default: origin/main)
#   ROUNDS    interleaved rounds (default: 5)
#
# The base is built with the working tree's pkg/internal/enginetest harness
# copied over it, so it also works when BASE_REF has no harness yet. Output goes
# to $BENCH_ENGINE_AB_DIR (default: a temp dir, printed), as base-a.txt,
# base-b.txt and head.txt, and benchstat prints head-vs-base and the A/A control.
set -euo pipefail

BASE_REF="${1:-origin/main}"
ROUNDS="${2:-5}"
BENCH_TIME="${BENCH_ENGINE_TIME:-3x}"
BENCHSTAT_VERSION="${BENCHSTAT_VERSION:-v0.0.0-20261009192801-be2c69fb417e}"
PKG=./pkg/internal/enginetest/engines

root="$(git rev-parse --show-toplevel)"
out="${BENCH_ENGINE_AB_DIR:-$(mktemp -d)}"
mkdir -p "$out"
cd "$root"

base_dir="$out/base-tree"
git worktree add --detach "$base_dir" "$BASE_REF" >/dev/null
trap 'git worktree remove --force "$base_dir" >/dev/null 2>&1 || true' EXIT

echo ">> building head and base ($BASE_REF) test binaries"
go test -c -o "$out/head.test" "$PKG"
rm -rf "$base_dir/pkg/internal/enginetest"
mkdir -p "$base_dir/pkg/internal"
cp -R pkg/internal/enginetest "$base_dir/pkg/internal/enginetest"
(cd "$base_dir" && go test -c -o "$out/base.test" "$PKG")

: >"$out/base-a.txt"; : >"$out/base-b.txt"; : >"$out/head.txt"
run() { "$1" -test.run='^$' -test.bench='^BenchmarkEngine$' -test.benchtime="$BENCH_TIME" -test.count=1 >>"$2" || true; }

for round in $(seq 1 "$ROUNDS"); do
  echo ">> round $round/$ROUNDS"
  case $((round % 3)) in
    1) order="a h b" ;;
    2) order="h b a" ;;
    0) order="b a h" ;;
  esac
  for who in $order; do
    case "$who" in
      a) run "$out/base.test" "$out/base-a.txt" ;;
      b) run "$out/base.test" "$out/base-b.txt" ;;
      h) run "$out/head.test" "$out/head.txt" ;;
    esac
  done
done

stat() { go run "golang.org/x/perf/cmd/benchstat@$BENCHSTAT_VERSION" "$@"; }
echo
echo "=== head vs base ($BASE_REF) ==="
stat "$out/base-a.txt" "$out/head.txt"
echo
echo "=== A/A control: base vs base (this is the noise floor) ==="
stat "$out/base-a.txt" "$out/base-b.txt"
echo
echo "raw results in $out"
