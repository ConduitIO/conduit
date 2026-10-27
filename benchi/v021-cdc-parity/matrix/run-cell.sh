#!/usr/bin/env bash
# Runs ONE pre-registered cell on THIS machine (a dedicated bench instance:
# never a laptop, never next to other work).
#
#   matrix/run-cell.sh CELL OUT [REP]
#
# CELL is an id from `cells.py list`; OUT is the results root; REP (default 0)
# is 1 for a cell's cross-instance repeat. Results land in OUT/CELL[-rep1]/:
# one directory per run (warmup-<arm>, r<round>-<arm>, diag-<arm>, or
# kill<round>-<arm>) holding benchi's output, samples.jsonl (the sampler's time
# series), res.jsonl (cgroup CPU/memory/IO of the SUT, broker and Postgres),
# run.json (the hook timestamps) and, where the mode has them, verify-*.json,
# profiles, GC logs and Conduit metrics scrapes.
#
# Needs: run-shard.sh's setup (images built, LOADER_BIN, ASPROF_DIR, cpusets in
# SUT_CPUSET / INFRA_CPUSET). Order of arms rotates per round (Latin square) so
# drift across the session lands on every arm equally; one warmup round per
# arm is discarded first.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
CELL=${1:?cell id}
OUT=${2:?results root}
REP=${3:-0}
: "${SUT_CPUSET:?run-shard.sh sets SUT_CPUSET}" "${INFRA_CPUSET:?}" "${LOADER_BIN:?}" "${ASPROF_DIR:?}"
BENCHI=${BENCHI:-benchi}

DIR="$OUT/$CELL$([ "$REP" = 1 ] && echo -rep1)"
GEN="$DIR/cell"
mkdir -p "$DIR"
python3 "$HERE/cells.py" gen "$CELL" "$GEN"
META="$GEN/cell.json"
meta() { python3 "$HERE/meta.py" "$META" "$1"; }
MODE=$(meta mode)
ROUNDS=$(meta rounds)
DIAG=$(meta diag)
read -r -a ARMS <<<"$(meta arms)"
N=${#ARMS[@]}

with_tty() { script -q -e -c "$(printf '%q ' "$@")" /dev/null; }

reset_state() {
  # Fresh sink/broker/object-store state for every run: the volumes are
  # removed and recreated, so no run sees another's data.
  docker ps -aq --filter network=benchi | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm benchi >/dev/null 2>&1 || true
  for v in cdcparity-sink cdcparity-kafka cdcparity-minio; do
    docker volume rm -f "$v" >/dev/null 2>&1 || true
    docker volume create "$v" >/dev/null
  done
  docker run --rm -v cdcparity-sink:/sink "${ALPINE_IMG}" chmod 777 /sink
  sync
  echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null 2>&1 || true
}
ALPINE_IMG=$(python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(cells.IMG['alpine'])")

export_env() { # run_dir arm diag
  local rd=$1 arm=$2 diag=$3
  export RUN_DIR="$rd" PROF_DIR="$rd/prof" LOADER_BIN SUT_CPUSET INFRA_CPUSET ASPROF_DIR
  mkdir -p "$PROF_DIR" && chmod 777 "$PROF_DIR" "$rd"
  echo "$arm" > "$rd/arm"
  export LOADER_SLOTS
  LOADER_SLOTS=$(meta "slots.$arm:csv")
  export CONDUIT_DIAG_ARGS="" JVM_DIAG_OPTS=""
  if [ "$diag" = 1 ]; then
    # Diagnosis runs only (never a measured rate): profiles cost throughput.
    export CONDUIT_DIAG_ARGS="--dev.cpuprofile /prof/cpu.pprof --dev.memprofile /prof/mem.pprof --dev.blockprofile /prof/block.pprof"
  fi
}

# Host-side resource sampler: cgroup v2 CPU, memory and IO of the SUT, the
# broker and Postgres, every 5 s, plus docker's network counters every 15 s.
start_res() {
  python3 "$HERE/res_sampler.py" "$1/res.jsonl" cdcparity-sut cdcparity-kafka cdcparity-postgres &
  RES_PID=$!
}
stop_res() { kill "${RES_PID:-}" 2>/dev/null; wait "${RES_PID:-}" 2>/dev/null; RES_PID=""; }

# Diagnosis sidecar: after the warm-up, Conduit's metrics are scraped at the
# window's start and end (per-stage timings), and the JVM sides are profiled
# with async-profiler (itimer mode: no perf_events privileges needed) for the
# whole window.
start_diag() { # run_dir arm
  python3 "$HERE/diag.py" "$1" "$2" "$META" &
  DIAG_PID=$!
}
stop_diag() { kill "${DIAG_PID:-}" 2>/dev/null; wait "${DIAG_PID:-}" 2>/dev/null; DIAG_PID=""; }

run_benchi() { # label arm diag
  local label=$1 arm=$2 diag=$3 rd="$DIR/$1"
  rm -rf "$rd"; mkdir -p "$rd"
  reset_state
  export_env "$rd" "$arm" "$diag"
  cat /proc/loadavg > "$rd/load-before.txt"
  start_res "$rd"
  start_diag "$rd" "$arm"
  local t0; t0=$(date +%s%3N)
  # Bounded: a stuck arm cannot hold the instance.
  local cap=$(( $(meta run_seconds) + 3600 ))
  (cd "$GEN" && with_tty timeout "$cap" "$BENCHI" -config bench.yml -tool "$arm" -out "$rd/benchi" </dev/null >/dev/null 2>&1)
  local rc=$?
  stop_diag; stop_res
  cat /proc/loadavg > "$rd/load-after.txt"
  python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" "$rc" "$t0" > "$rd/run.json"
  [ "$rc" = 0 ] || echo "run $label exited $rc" >&2
}

# Recovery runs: benchi cannot kill and restart a tool mid-test, so this mode
# drives docker compose directly with the same generated compose files.
run_recover() { # label arm
  local label=$1 arm=$2 rd="$DIR/$1"
  rm -rf "$rd"; mkdir -p "$rd"
  reset_state
  export_env "$rd" "$arm" 0
  docker network create benchi >/dev/null
  start_res "$rd"
  local P="cdcparity-$$"
  local t0; t0=$(date +%s%3N)
  (cd "$GEN" && docker compose -p "$P" -f infra.yml up -d --wait) > "$rd/compose.log" 2>&1
  echo "TOOL_START_MS=$(date +%s%3N)" > "$rd/hooks.txt"
  (cd "$GEN" && docker compose -p "$P-sut" -f "tools/$arm.yml" up -d) >> "$rd/compose.log" 2>&1
  if [[ "$arm" == connect-* ]]; then
    python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(cells.REGISTER_SH)" > "$rd/register.sh"
    docker cp "$rd/register.sh" cdcparity-counter:/tmp/register.sh
    docker exec cdcparity-counter sh /tmp/register.sh >> "$rd/hooks.txt" 2>&1
  fi
  local specs tbl kill_after
  specs=$(meta sinks:csv); tbl=$(meta dest_tables); kill_after=$(meta recover.kill_after_s)
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -timeout 15m >> "$rd/hooks.txt"
  sleep "$kill_after"
  # kill -9 the system under test: no shutdown hooks, no flush, no ack.
  echo "KILL_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  docker kill -s KILL "$SUT_NAME" >/dev/null
  docker logs "$SUT_NAME" > "$rd/sut-before-kill.log" 2>&1
  echo "RESTART_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  docker start "$SUT_NAME" >/dev/null
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -settle -stable 60s -timeout 40m \
    -progress http://cdcparity-loader:9100/progress >> "$rd/hooks.txt"
  local spec
  for spec in $(meta verify_sinks); do
    local name=${spec%%:*}
    docker exec cdcparity-counter /loader/loader verify -sink "$spec" -tables "$(meta par)" -payload "$(meta payload)" \
      -total "$(meta recover.ops)" -ordered=true -out "/out/verify-$name.json" > /dev/null 2>> "$rd/hooks.txt"
  done
  docker logs "$SUT_NAME" > "$rd/sut-after-restart.log" 2>&1
  stop_res
  (cd "$GEN" && docker compose -p "$P-sut" -f "tools/$arm.yml" down -v; docker compose -p "$P" -f infra.yml down -v) >> "$rd/compose.log" 2>&1
  docker network rm benchi >/dev/null 2>&1 || true
  python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" 0 "$t0" > "$rd/run.json"
}
SUT_NAME=$(python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(cells.SUT)")

{
  echo "cell $CELL rep $REP mode $MODE rounds $ROUNDS arms ${ARMS[*]}"
  echo "started $(date -u +%FT%TZ)"
} > "$DIR/session.txt"

if [ "$MODE" = recover ]; then
  for ((r = 1; r <= ROUNDS; r++)); do
    for k in $(seq 0 $((N - 1))); do
      arm=${ARMS[$(( (r - 1 + k) % N ))]}
      run_recover "kill$r-$arm" "$arm"
    done
  done
else
  if [ "$MODE" != correct ] && [ "${MATRIX_SMOKE:-0}" != 1 ]; then
    for arm in "${ARMS[@]}"; do run_benchi "warmup-$arm" "$arm" 0; done
  fi
  for ((r = 1; r <= ROUNDS; r++)); do
    for k in $(seq 0 $((N - 1))); do
      arm=${ARMS[$(( (r - 1 + k) % N ))]}
      run_benchi "r$r-$arm" "$arm" 0
    done
  done
  if [ "$DIAG" = 1 ]; then
    for arm in "${ARMS[@]}"; do run_benchi "diag-$arm" "$arm" 1; done
  fi
fi
echo "finished $(date -u +%FT%TZ)" >> "$DIR/session.txt"
touch "$DIR/DONE"
