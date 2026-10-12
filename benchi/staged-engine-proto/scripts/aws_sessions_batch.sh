# Run 3 (source batching on every arm). Sourced by the EC2 user-data script (benchi/archv2-gate/aws/userdata.sh.tmpl
# with patches/aws-userdata-staged.patch applied). Expects from the caller:
# W OUT ROUNDS START_TS KIT BIN_MAIN BIN_PROTO and the functions status, upload.
#
# Every session is the staged_session.py method: v1+batch, v2-main+batch,
# v2-proto+batch (source sdk.batch.size=100 / delay=10ms on every source) and the
# anchor v2-main-nobatch (arch-v2 main, no source batching), each twice per round
# in a palindrome, so every comparison has a same-session A/A pair. Sessions are listed in priority order;
# one that cannot finish inside the instance budget is skipped, not cut off.
#
# EARLY, NON-GATING READ ON A VIRTUALIZED INSTANCE; NOT PUBLISHED.

BUDGET_MIN=${BUDGET_MIN:-345}   # no session may be started if it could end after this many minutes
WARMUP=${WARMUP:-10}
WINDOW=${WINDOW:-40}
NOTE="early, non-gating read on a virtualized instance; not published"

session() { # name est_minutes shape [staged_session.py args...]
  local name=$1 est=$2 shape=$3; shift 3
  local elapsed=$(( ($(date +%s) - START_TS) / 60 ))
  if [ $((elapsed + est)) -gt "$BUDGET_MIN" ]; then
    status "session $name SKIPPED: ${elapsed} min elapsed + ${est} min estimate exceeds the ${BUDGET_MIN} min budget"
    return
  fi
  status "session $name start (shape $shape, $ROUNDS rounds, ${WARMUP}s warmup, ${WINDOW}s window, args: $*)"
  # The hard timeout is twice the estimate: a hung arm costs seconds inside
  # run_docker.sh, this only guards against something worse.
  timeout $((est * 2 * 60)) python3 "$KIT/scripts/staged_session.py" --shape "$shape" \
    --main "$BIN_MAIN" --proto "$BIN_PROTO" --out "$OUT/results/$name" \
    --rounds "$ROUNDS" --warmup "$WARMUP" --window "$WINDOW" --note "$NOTE" "$@" \
    > "$OUT/results/$name.stdout" 2> "$OUT/results/$name.stderr"
  status "session $name exit $?"
  rm -rf "$OUT/results/$name/tmp"
  upload
  sync; echo 3 > /proc/sys/vm/drop_caches
}


# name est shape; every session is 4 arms x 2 runs x 5 rounds = 40 runs, about 38 min.
B="--suffix +batch"
session 01-2x2                  38 2x2-srcbatch100 $B --anchor-shape 2x2
session 02-4x4                  38 4x4-srcbatch100 $B --anchor-shape 4x4
session 03-1x1-lat1ms           38 1x1-srcbatch100 $B --anchor-shape 1x1 --env CONDUIT_BENCH_DEST_ACK_LATENCY=1ms
session 04-1x1-bufferedacks     38 1x1-srcbatch100 $B --anchor-shape 1x1 --env CONDUIT_BENCH_DEST_ACK_LATENCY=1us
session 05-2x2-streambuf64      38 2x2-srcbatch100 $B --anchor-shape 2x2 --env CONDUIT_BENCH_STREAM_BUFFER=64
session 06-1x1-destbatch100     38 1x1-srcbatch100-destbatch100 $B --anchor-shape 1x1-destbatch
session 07-4x4-lat1ms           38 4x4-srcbatch100 $B --anchor-shape 4x4 --env CONDUIT_BENCH_DEST_ACK_LATENCY=1ms
