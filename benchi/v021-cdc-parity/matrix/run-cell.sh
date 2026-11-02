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

# benchi is a terminal UI: script(1) gives it a pty. The pty's output (the
# UI and any error benchi prints before its log exists) is kept per run.
with_tty() { local ts=$1; shift; script -q -e -c "$(printf '%q ' "$@")" "$ts"; }

# After EVERY run: a progress line in the shard log and the run directory in
# S3, so a stuck run or an instance lost to its cap loses one run, not a cell.
publish_run() { # label rc t0
  local label=$1 rc=$2 t0=$3 rd="$DIR/$1" first=no
  grep -q '"first_record_ms"\|FIRST_MS' "$rd/run.json" 2>/dev/null && first=yes
  [ -s "$rd/samples.jsonl" ] && grep -q '"count": *[1-9]' "$rd/samples.jsonl" 2>/dev/null && first=yes
  echo "$(date -u +%FT%TZ) run $CELL/$label exit $rc $(( ($(date +%s%3N) - t0) / 1000 ))s record=$first" >> "$OUT/shard.log"
  if [ -n "${MATRIX_S3:-}" ]; then
    aws s3 cp --quiet "$OUT/shard.log" "$MATRIX_S3/shard-${MATRIX_SHARD:-0}/shard.log" || true
    aws s3 sync --quiet --exclude 'prof/*.pprof' --exclude '*/token' --exclude 'prof/token' "$rd" "$MATRIX_S3/shard-${MATRIX_SHARD:-0}/$(basename "$DIR")/$label" || true
  fi
}

# Fresh state per run. Postgres always (its own volume, on EBS). The broker
# volume only at the cell's first run when the cell drains a backlog (the topic
# is filled once per cell; every run reads it with its own consumer group).
FIRST_RUN=1
reset_state() {
  docker ps -aq --filter network=benchi | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm benchi >/dev/null 2>&1 || true
  local keep_broker keep_input vols
  keep_broker=$(meta keep_broker_volume); keep_input=$(meta keep_input_file)
  vols="pg minio"
  if [ "$keep_broker" != 1 ] || [ "$FIRST_RUN" = 1 ]; then vols="$vols kafka"; fi
  for v in $vols; do mkvol "$v"; done
  docker volume inspect cdcparity-kafka >/dev/null 2>&1 || mkvol kafka
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

# A service's Docker volume, bind-mounted from its own EBS volume
# (/data/<role>, mounted by the user data; review R2-B2), emptied first. Off
# AWS (no /data/<role>) it falls back to a plain named volume.
mkvol() { # role
  local role=$1 name="cdcparity-$1"
  docker volume rm -f "$name" >/dev/null 2>&1 || true
  if mountpoint -q "/data/$role"; then
    rm -rf "/data/$role/vol" && mkdir -p "/data/$role/vol" && chmod 777 "/data/$role/vol"
    docker volume create --driver local --opt type=none --opt o=bind --opt device="/data/$role/vol" "$name" >/dev/null
  else
    docker volume create "$name" >/dev/null
  fi
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

# Resources: cgroups and per-device disk counters (res_sampler.py), and
# iostat's extended per-device view (await, utilization) for every run.
start_res() {
  python3 "$HERE/res_sampler.py" "$1/res.jsonl" & RES_PID=$!
  cp /etc/matrix-disks.json "$1/disks.json" 2>/dev/null || true
  if command -v iostat >/dev/null; then iostat -dxmt 5 > "$1/iostat.txt" 2>/dev/null & IOSTAT_PID=$!; fi
}
stop_res() {
  kill "${RES_PID:-}" "${IOSTAT_PID:-}" 2>/dev/null; wait "${RES_PID:-}" "${IOSTAT_PID:-}" 2>/dev/null; RES_PID=""; IOSTAT_PID=""
}
# Live view of a run (every 60 s): each cdcparity container's state and log
# tail, and the run directory synced to S3, so a run that hangs is visible
# while it hangs.
start_watch() { # run_dir label
  ( n=0; while true; do
      sleep 5; n=$((n + 1))
      # Restarts of the system under test (Debezium Server is restarted on
      # failure, cells.py): the highest count seen, for run.json.
      r=$(docker inspect -f '{{.RestartCount}}' "$SUT_NAME" 2>/dev/null) &&
        [ "$r" -gt "$(cat "$1/sut-restarts" 2>/dev/null || echo 0)" ] && echo "$r" > "$1/sut-restarts"
      [ $((n % 12)) = 0 ] || continue
      mkdir -p "$1/live"
      docker ps -a --filter name=cdcparity --format '{{.Names}} {{.Status}}' > "$1/live/ps.txt" 2>&1
      ps -eo pid,pgid,tpgid,stat,etime,args | grep -E 'benchi|script|timeout|docker compose' | grep -v grep > "$1/live/procs.txt" 2>&1
      for c in $(docker ps -a --filter name=cdcparity --format '{{.Names}}'); do
        docker logs --tail 300 "$c" > "$1/live/$c.log" 2>&1
      done
      [ -n "${MATRIX_S3:-}" ] && aws s3 sync --quiet --exclude 'prof/*' "$1" "$MATRIX_S3/shard-${MATRIX_SHARD:-0}/$(basename "$DIR")/$2" 2>/dev/null
    done ) &
  WATCH_PID=$!
}
stop_watch() { # [run_dir]
  kill "${WATCH_PID:-}" 2>/dev/null; wait "${WATCH_PID:-}" 2>/dev/null; WATCH_PID=""
  if [ -n "${1:-}" ]; then
    local x; x=$(cat "$1"/prof/dbz*.restarts 2>/dev/null | wc -l)  # the scale-out arm's per-process restarts
    echo "SUT_RESTART_COUNT=$(( $(cat "$1/sut-restarts" 2>/dev/null || echo 0) + x ))" >> "$1/hooks.txt"
  fi
}
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
  start_watch "$rd" "$label"
  local t0; t0=$(date +%s%3N)
  # Bounded: 20 minutes past the planned run (10 in smoke) before it is killed.
  local slack=1200; [ "${MATRIX_SMOKE:-0}" = 1 ] && slack=600
  local cap=$(( $(meta run_seconds) + slack + 3600 * $( [ "$MODE" = snapshot ] || [ "$MODE" = correct ] && echo 1 || echo 0) ))
  # -k: if benchi does not exit within 60 s of SIGTERM (a hung cleanup), KILL it.
  # --foreground: timeout must leave benchi in the pty's foreground process
  # group; otherwise its terminal UI's first read gets SIGTTIN and it stops
  # (the shakedown's runs hung exactly so).
  (cd "$rd/cfg" && with_tty "$rd/benchi-tty.log" timeout --foreground -k 60 "$cap" "$BENCHI" -config bench.yml -tool "$arm" -out "$rd/benchi" </dev/null >"$rd/benchi-stdout.log" 2>&1)
  local rc=$?
  stop_watch "$rd"
  # Whatever benchi left behind (a killed cleanup) must not leak into the next run.
  docker ps -aq --filter network=benchi | xargs -r docker rm -f >/dev/null 2>&1 || true
  [ "$diag" = 1 ] && stop_diag
  stop_res
  cat /proc/loadavg > "$rd/load-after.txt"
  python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" "$rc" "$t0" > "$rd/run.json"
  [ "$rc" = 0 ] || echo "run $label exited $rc" >&2
  publish_run "$label" "$rc" "$t0"
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
  start_watch "$rd" "$label"
  # Waits: first record, the kill threshold, the drain after the restart.
  local w1=15m w2=30m w3=45m
  [ "${MATRIX_SMOKE:-0}" = 1 ] && { w1=5m; w2=10m; w3=10m; }
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
  # A run that never delivers (or never reaches the kill threshold) is a
  # failed run: recorded and torn down at once, not waited out.
  local ok=1
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -timeout "$w1" >> "$rd/hooks.txt" 2>&1 &&
    docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -until "$kill_at" -timeout "$w2" >> "$rd/hooks.txt" 2>&1 || ok=0
  if [ "$ok" = 0 ]; then
    echo "RECOVER_ABORTED=1 (no first record or kill threshold)" >> "$rd/hooks.txt"
    for c in $(docker ps -a --filter name=cdcparity --format '{{.Names}}'); do docker logs "$c" > "$rd/$c.log" 2>&1; done
    stop_watch "$rd"; stop_res
    (cd "$cfg" && docker compose -p "$P-sut" -f tools/tool.yml down -v; docker compose -p "$P" -f infra.yml down -v) >> "$rd/compose.log" 2>&1
    docker network rm benchi >/dev/null 2>&1 || true
    python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" 1 "$t0" > "$rd/run.json"
    publish_run "$label" 1 "$t0"
    return
  fi
  # kill -9 the system under test: no shutdown hooks, no flush, no ack.
  echo "KILL_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  docker kill -s KILL "$SUT_NAME" >/dev/null
  docker logs "$SUT_NAME" > "$rd/sut-before-kill.log" 2>&1
  echo "RESTART_MS=$(date +%s%3N)" >> "$rd/hooks.txt"
  docker start "$SUT_NAME" >/dev/null
  local prog=""; [ "$MODE" = recover ] && prog="-progress http://cdcparity-loader:9100/progress"
  # shellcheck disable=SC2086
  docker exec cdcparity-counter /loader/loader first -sinks "$specs" -tables "$tbl" -settle -stable 90s -timeout "$w3" $prog >> "$rd/hooks.txt" 2>&1
  local spec args; args=$(meta verify_args)
  for spec in $(meta verify_sinks); do
    # shellcheck disable=SC2086
    docker exec cdcparity-counter /loader/loader verify -sink "$spec" $args -ordered=true \
      -out "/out/verify-${spec%%:*}.json" > /dev/null 2>> "$rd/hooks.txt"
  done
  docker logs "$SUT_NAME" > "$rd/sut-after-restart.log" 2>&1
  stop_watch "$rd"
  stop_res
  (cd "$cfg" && docker compose -p "$P-sut" -f tools/tool.yml down -v; docker compose -p "$P" -f infra.yml down -v) >> "$rd/compose.log" 2>&1
  docker network rm benchi >/dev/null 2>&1 || true
  python3 "$HERE/runinfo.py" "$rd" "$arm" "$label" 0 "$t0" > "$rd/run.json"
  publish_run "$label" 0 "$t0"
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
    read -r -a DIAG_ARMS <<<"$(meta diag_arms)"
    for arm in "${DIAG_ARMS[@]}"; do run_benchi "diag-$arm" "$arm" 1; done
  fi
fi
echo "finished $(date -u +%FT%TZ)" >> "$DIR/session.txt"
touch "$DIR/DONE"
