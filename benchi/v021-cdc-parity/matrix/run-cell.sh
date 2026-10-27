#!/usr/bin/env bash
# Runs ONE pre-registered cell on THIS machine (a dedicated bench instance:
# never a laptop, never next to other work).
#
#   matrix/run-cell.sh CELL OUT [REP]
#
# REP (default 0) is the cell's instance index (cells with instances > 1 run on
# several instances). Results land in OUT/CELL[-repN]/: one directory per run
# (warmup-<arm>, r<round>-<arm>, diag-<arm>, kill<round>-<arm>) with benchi's
# output, samples.jsonl, res.jsonl (cgroup CPU, memory and IO of the SUT and of
# every infrastructure container, host steal), run.json, and the post-run
# collections (kafka-hist, s3-index, pg-hist, http-hist, pgstat, verify-*.json).
#
# Order: one warmup round per arm (discarded), then ROUNDS rounds, each in the
# seeded, blocked order `cells.py order` gives (primary arms adjacent, AB/BA).
# Needs run-shard.sh's setup (images, LOADER_BIN, ASPROF_DIR, JOLOKIA_JAR,
# SUT_CPUSET, INFRA_CPUSET).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
CELL=${1:?cell id}
OUT=${2:?results root}
REP=${3:-0}
: "${SUT_CPUSET:?run-shard.sh sets SUT_CPUSET}" "${INFRA_CPUSET:?}" "${LOADER_BIN:?}" "${ASPROF_DIR:?}" "${JOLOKIA_JAR:?}"
BENCHI=${BENCHI:-benchi}

DIR="$OUT/$CELL$([ "$REP" != 0 ] && echo "-rep$REP")"
GEN="$DIR/cell"
mkdir -p "$DIR"
python3 "$HERE/cells.py" gen "$CELL" "$GEN"
META="$GEN/cell.json"
meta() { python3 "$HERE/meta.py" "$META" "$1"; }
MODE=$(meta mode)
ROUNDS=$(meta rounds)
DIAG=$(meta diag)
read -r -a ARMS <<<"$(meta arms)"
SUT_NAME=cdcparity-sut
ALPINE_IMG=$(python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(cells.IMG['alpine'])")

with_tty() { script -q -e -c "$(printf '%q ' "$@")" /dev/null; }

# Fresh state per run. Postgres always (its own volume, on EBS). The broker
# volume only at the cell's first run when the cell drains a backlog (the topic
# is filled once per cell; every run reads it with its own consumer group).
FIRST_RUN=1
reset_state() {
  docker ps -aq --filter network=benchi | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm benchi >/dev/null 2>&1 || true
  local keep_broker keep_input vols
  keep_broker=$(meta keep_broker_volume); keep_input=$(meta keep_input_file)
  vols="cdcparity-pg cdcparity-minio"
  if [ "$keep_broker" != 1 ] || [ "$FIRST_RUN" = 1 ]; then vols="$vols cdcparity-kafka"; fi
  for v in $vols; do docker volume rm -f "$v" >/dev/null 2>&1 || true; docker volume create "$v" >/dev/null; done
  docker volume create cdcparity-kafka >/dev/null 2>&1 || true
  if [ "$keep_input" = 1 ] && [ "$FIRST_RUN" != 1 ]; then
    docker run --rm -v cdcparity-sink:/sink "$ALPINE_IMG" sh -c 'find /sink -mindepth 1 ! -name "in_*.txt" ! -name .fill_done -delete; chmod 777 /sink'
  else
    docker volume rm -f cdcparity-sink >/dev/null 2>&1 || true
    docker volume create cdcparity-sink >/dev/null
    docker run --rm -v cdcparity-sink:/sink "$ALPINE_IMG" chmod 777 /sink
  fi
  FIRST_RUN=0
  sync
  echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
}

# The per-run copy of the arm's generated directory, with @RUN@ (consumer
# groups, Connect's group and storage topics, connector names) made unique.
prepare_run_dir() { # label arm -> echoes the run's config dir
  local label=$1 arm=$2 rd="$DIR/$1"
  rm -rf "$rd"; mkdir -p "$rd/prof" "$rd/cfg"
  chmod 777 "$rd" "$rd/prof"
  cp -R "$GEN/arms/$arm/." "$rd/cfg/"
  local rid; rid=$(echo "$CELL-$label-$REP" | tr -c 'a-zA-Z0-9\n' '-' | tr 'A-Z' 'a-z')
  grep -rl '@RUN@' "$rd/cfg" | xargs -r sed -i "s/@RUN@/$rid/g"
  echo "$arm" > "$rd/arm"
}

export_env() { # run_dir arm diag
  local rd=$1 arm=$2 diag=$3
  export RUN_DIR="$rd" PROF_DIR="$rd/prof" LOADER_BIN SUT_CPUSET INFRA_CPUSET ASPROF_DIR JOLOKIA_JAR
  export LOADER_SLOTS; LOADER_SLOTS=$(meta "slots.$arm:csv")
  export SAMPLE_INTERVAL; SAMPLE_INTERVAL=$(meta "sample_interval.$arm")
  export CONDUIT_IMAGE=matrix-conduit:bench CONDUIT_API_ADDR=127.0.0.1:8080 CONDUIT_TOKEN_FILE="" JVM_DIAG_OPTS=""
  if [ "$diag" = 1 ]; then
    # Diagnosis runs only (never a measured rate): the pprof build, the API
    # reachable on the bench network with a token so its metrics can be
    # polled, and Jolokia in the JVMs.
    export CONDUIT_IMAGE=matrix-conduit:bench-diag CONDUIT_API_ADDR=0.0.0.0:8080
    head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$rd/prof/token"
    chmod 644 "$rd/prof/token"
    export CONDUIT_TOKEN_FILE=/prof/token
    export JVM_DIAG_OPTS="-javaagent:/opt/jolokia.jar=host=0.0.0.0,port=8778"
  fi
}

start_res() { python3 "$HERE/res_sampler.py" "$1/res.jsonl" & RES_PID=$!; }
stop_res() { kill "${RES_PID:-}" 2>/dev/null; wait "${RES_PID:-}" 2>/dev/null; RES_PID=""; }
start_diag() { python3 "$HERE/diag.py" "$1" "$2" "$META" & DIAG_PID=$!; }
stop_diag() { kill "${DIAG_PID:-}" 2>/dev/null; wait "${DIAG_PID:-}" 2>/dev/null; DIAG_PID=""; }

run_benchi() { # label arm diag
  local label=$1 arm=$2 diag=$3 rd="$DIR/$1"
  reset_state
  prepare_run_dir "$label" "$arm"
  export_env "$rd" "$arm" "$diag"
  cat /proc/loadavg > "$rd/load-before.txt"
  start_res "$rd"
  [ "$diag" = 1 ] && start_diag "$rd" "$arm"
  local t0; t0=$(date +%s%3N)
  # Bounded: 20 minutes past the planned run before it is killed.
  local cap=$(( $(meta run_seconds) + 1200 + 3600 * $( [ "$MODE" = snapshot ] || [ "$MODE" = correct ] && echo 1 || echo 0) ))
  (cd "$rd/cfg" && with_tty timeout "$cap" "$BENCHI" -config bench.yml -tool "$arm" -out "$rd/benchi" </dev/null >/dev/null 2>&1)
  local rc=$?
  [ "$diag" = 1 ] && stop_diag
  stop_res
  cat /proc/loadavg > "$rd/load-after.txt"
  python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" "$rc" "$t0" > "$rd/run.json"
  [ "$rc" = 0 ] || echo "run $label exited $rc" >&2
}

# Recovery runs: benchi cannot kill and restart a tool mid-test, so this mode
# drives docker compose directly with the same generated files. The kill is
# triggered by DELIVERED count (30% of the expected events) under a paced load
# every side tracks, so every side is killed mid-stream with the loader still
# running, whatever its speed (review B1).
run_recover() { # label arm
  local label=$1 arm=$2 rd="$DIR/$1"
  reset_state
  prepare_run_dir "$label" "$arm"
  export_env "$rd" "$arm" 0
  docker network create benchi >/dev/null
  start_res "$rd"
  local P="cdcparity-$$" cfg="$rd/cfg" t0; t0=$(date +%s%3N)
  (cd "$cfg" && docker compose -p "$P" -f infra.yml up -d --wait) > "$rd/compose.log" 2>&1
  if [ "$MODE" = recover-snapshot ]; then
    docker exec cdcparity-counter /loader/loader wait-file -path /sink/.fill_done -timeout 40m >> "$rd/hooks.txt"
  fi
  echo "TOOL_START_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  (cd "$cfg" && docker compose -p "$P-sut" -f tools/tool.yml up -d) >> "$rd/compose.log" 2>&1
  if [[ "$arm" == connect-* ]]; then
    python3 -c "import sys; sys.path.insert(0,'$HERE'); import cells; print(cells.REGISTER_SH)" > "$rd/register.sh"
    docker cp "$rd/register.sh" cdcparity-counter:/tmp/register.sh
    docker exec cdcparity-counter sh /tmp/register.sh >> "$rd/hooks.txt" 2>&1
  fi
  local specs tbl expected kill_at
  specs=$(meta sinks:csv); tbl=$(meta dest_tables)
  if [ "$MODE" = recover-snapshot ]; then expected=$(meta recover.snapshot_rows); else
    expected=$(( $(meta recover.ops) + 10000 * $(meta par) )); fi
  kill_at=$(python3 -c "print(int($expected * $(meta recover.kill_fraction)))")
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -timeout 15m >> "$rd/hooks.txt"
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -until "$kill_at" -timeout 30m >> "$rd/hooks.txt"
  # kill -9 the system under test: no shutdown hooks, no flush, no ack.
  echo "KILL_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  docker kill -s KILL "$SUT_NAME" >/dev/null
  docker logs "$SUT_NAME" > "$rd/sut-before-kill.log" 2>&1
  echo "RESTART_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  docker start "$SUT_NAME" >/dev/null
  local prog=""; [ "$MODE" = recover ] && prog="-progress http://cdcparity-loader:9100/progress"
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -settle -stable 90s -timeout 45m $prog >> "$rd/hooks.txt"
  local spec args; args=$(meta verify_args)
  for spec in $(meta verify_sinks); do
    # shellcheck disable=SC2086
    docker exec cdcparity-counter /loader/loader verify -sink "$spec" $args -ordered=true \
      -out "/out/verify-${spec%%:*}.json" > /dev/null 2>> "$rd/hooks.txt"
  done
  docker logs "$SUT_NAME" > "$rd/sut-after-restart.log" 2>&1
  stop_res
  (cd "$cfg" && docker compose -p "$P-sut" -f tools/tool.yml down -v; docker compose -p "$P" -f infra.yml down -v) >> "$rd/compose.log" 2>&1
  docker network rm benchi >/dev/null 2>&1 || true
  python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" 0 "$t0" > "$rd/run.json"
}

{
  echo "cell $CELL rep $REP mode $MODE rounds $ROUNDS arms ${ARMS[*]}"
  echo "harness $(meta harness_hash)"
  echo "started $(date -u +%FT%TZ)"
} > "$DIR/session.txt"

if [[ "$MODE" == recover* ]]; then
  for ((r = 1; r <= ROUNDS; r++)); do
    read -r -a ORDER <<<"$(python3 "$HERE/cells.py" order "$CELL" "$r")"
    echo "round $r: ${ORDER[*]}" >> "$DIR/session.txt"
    for arm in "${ORDER[@]}"; do run_recover "kill$r-$arm" "$arm"; done
  done
else
  if [ "$MODE" != correct ] && [ "${MATRIX_SMOKE:-0}" != 1 ]; then
    for arm in "${ARMS[@]}"; do run_benchi "warmup-$arm" "$arm" 0; done
  fi
  for ((r = 1; r <= ROUNDS; r++)); do
    read -r -a ORDER <<<"$(python3 "$HERE/cells.py" order "$CELL" "$r")"
    echo "round $r: ${ORDER[*]}" >> "$DIR/session.txt"
    for arm in "${ORDER[@]}"; do run_benchi "r$r-$arm" "$arm" 0; done
  done
  if [ "$DIAG" = 1 ]; then
    for arm in "${ARMS[@]}"; do run_benchi "diag-$arm" "$arm" 1; done
  fi
fi
echo "finished $(date -u +%FT%TZ)" >> "$DIR/session.txt"
touch "$DIR/DONE"
