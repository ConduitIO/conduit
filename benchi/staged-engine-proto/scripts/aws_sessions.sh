# Sourced by the EC2 user-data script (benchi/archv2-gate/aws/userdata.sh.tmpl
# with patches/aws-userdata-staged.patch applied). Expects from the caller:
# W OUT ROUNDS START_TS KIT BIN_MAIN BIN_PROTO and the functions status, upload.
#
# Every session is the staged_session.py method: v1, v2-main and v2-proto (and
# v2-altA where marked) each twice per round in a palindrome, so every
# comparison has a same-session A/A pair. Sessions are listed in priority order;
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

ALTA="--alta-shape 1x1-srcbatch100"
# 4-arm sessions (about 38 min), 3-arm sessions (about 29 min).
session 01-1x1-bufferedacks     38 1x1 $ALTA --env CONDUIT_BENCH_DEST_ACK_LATENCY=1us
session 02-2x2                  29 2x2
session 03-4x4                  29 4x4
session 04-1x1                  38 1x1 $ALTA
session 05-1x1-streambuf64      29 1x1 --env CONDUIT_BENCH_STREAM_BUFFER=64
session 06-2x2-streambuf64      29 2x2 --env CONDUIT_BENCH_STREAM_BUFFER=64
session 07-1x1-noschema         29 1x1-noschema
session 08-1x1-lat1ms           29 1x1 --env CONDUIT_BENCH_DEST_ACK_LATENCY=1ms
session 09-1x1-destbatch100     29 1x1-destbatch
session 10-1x1-srcbatch100      29 1x1-srcbatch100
