#!/usr/bin/env bash
# Runs the v0.21 CDC parity benchmark: Conduit vs Kafka Connect (Debezium
# Postgres) vs Redpanda Connect, on an equivalent Postgres CDC workload, with
# an A/A control (conduit vs conduit-aa), a Postgres-destination parity pair
# (conduit-pg vs connect-pg) and an S3-destination parity pair (conduit-s3 vs
# connect-s3).
#
# Each benchi invocation runs ONE arm, with fresh infrastructure, for a 60s
# measured window. The ground-truth metric is, per destination kind:
#   - file-sink arms:  the number of lines the sink files accumulated in the
#     window (wc -l on /sink/out*.txt),
#   - Postgres-destination arms: the net rows the destination tables
#     accumulated (SELECT count(*) on bench_dest_*),
#   - S3-destination arms: the records the sink wrote to the MinIO bucket
#     (wc -l across every object in the bucket).
# Each is reported as ROWS_AFTER - ROWS_BEFORE, divided by 60s. See README.md
# and bench.yml.
#
# Usage: benchi/v021-cdc-parity/run.sh [ROUNDS] [ARM ...]
#   ROUNDS        number of full rounds (default 3). One warmup round is
#                 discarded first.
#   ARM ...       subset of arms to run (default all eight).
#                 conduit conduit-aa connect redpanda-connect conduit-pg connect-pg conduit-s3 connect-s3
#
# Env: BENCHI (benchi binary, default benchi), OUT (results dir), LOADER_RATE,
# TABLES (number of tables/source+destination pairs, default 1; sweep 1, 2, 4),
# CONDUIT_BATCH_SIZE (sdk.batch.size, default 1000), CONDUIT_BATCH_DELAY
# (sdk.batch.delay, default 100ms), LOADER_SLOT_TIMEOUT (default 600s — how
# long the loader waits for an arm's replication slot(s) before failing).
set -euo pipefail
cd "$(dirname "$0")"

ROUNDS=${1:-3}
shift || true
ARMS=("$@")
if [ ${#ARMS[@]} -eq 0 ]; then
  ARMS=(conduit conduit-aa connect redpanda-connect conduit-pg connect-pg conduit-s3 connect-s3 conduit-kafka connect-kafka)
fi

# Number of source tables (and source+destination pairs). The workload is
# SPLIT, not multiplied: the loader's fixed rate is divided round-robin across
# TABLES tables, and each arm gets TABLES sources + TABLES sinks. Exported so
# docker compose `${TABLES:-1}` interpolation (loader --tables, counter env for
# the connect registration hook) sees it. Default 1.
export TABLES=${TABLES:-1}
# Conduit source batching (sdk.batch.size / sdk.batch.delay), baked into the
# generated pipeline by scripts/generate.sh. Debezium's max.batch.size defaults
# to 2048; Conduit's batch-of-one default is the known gap this surfaces.
export CONDUIT_BATCH_SIZE=${CONDUIT_BATCH_SIZE:-1000}
export CONDUIT_BATCH_DELAY=${CONDUIT_BATCH_DELAY:-100ms}
# How long the loader waits for an arm's replication slot(s) before failing
# (see run() below). The slot names themselves are per-tool and computed in
# run(); the file-sink arms have no slot and write immediately. 600s is generous
# on purpose: a slow Kafka/Connect/JDBC startup on AWS must not trip it.
export LOADER_SLOT_TIMEOUT=${LOADER_SLOT_TIMEOUT:-600s}

# Regenerate the N-dependent artifacts (pipelines/conduit.yml,
# pipelines/conduit-pg.yml, pipelines/conduit-s3.yml, pipelines/conduit-kafka.yml,
# infra/init.sql) for the TABLES / CONDUIT_BATCH_SIZE above.
bash scripts/generate.sh

BENCHI=${BENCHI:-benchi}
OUT=${OUT:-results/$(date +%Y%m%dT%H%M%S)}
mkdir -p "$OUT"

# benchi creates and always removes a docker network named "benchi"; never run
# while another benchi holds it.
if [ -n "$(docker network ls --filter name='^benchi$' -q)" ]; then
  echo "a docker network named benchi exists (another benchi run?); not starting" >&2
  exit 1
fi

# benchi is a TUI and needs a terminal; script(1) provides one.
with_tty() {
  if script --version >/dev/null 2>&1; then
    script -q -e -c "$(printf '%q ' "$@")" /dev/null
  else
    script -q /dev/null "$@"
  fi
}

ARCH=$(docker info --format '{{.Architecture}}' | sed 's/aarch64/arm64/;s/x86_64/amd64/')

echo "==> building loader (linux/$ARCH)"
(cd loader && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o loader .)

echo "==> building conduit image (conduit-bench:v021-cdc-parity) from the working tree"
docker build -q -t conduit-bench:v021-cdc-parity -f ../../Dockerfile ../.. >/dev/null

echo "==> building connect image (connect-debezium:3.2) — Debezium connect + FileStreamSink + JDBC sink + S3 sink"
docker build -q -t connect-debezium:3.2 -f tools/connect.Dockerfile tools/ >/dev/null

echo "==> ensuring sink volume"
docker volume create cdcparity-sink >/dev/null

echo "==> recording environment"
{
  echo "git $(git -C ../.. rev-parse HEAD) $(git -C ../.. describe --tags --always 2>/dev/null || true)"
  echo "benchi $("$BENCHI" -v 2>&1 | head -1)"
  echo "docker $(docker version --format '{{.Server.Version}}') arch=$(docker info --format '{{.Architecture}}')"
  echo "arms ${ARMS[*]}"
  echo "rounds $ROUNDS"
  echo "loader_rate ${LOADER_RATE:-20000}"
  echo "tables $TABLES"
  echo "conduit_batch_size $CONDUIT_BATCH_SIZE"
  echo "conduit_batch_delay $CONDUIT_BATCH_DELAY"
  echo "loader_slot_timeout ${LOADER_SLOT_TIMEOUT:-600s}"
} > "$OUT/environment.txt"

# slot_list <base> — comma-separated replication slot names for TABLES tables,
# e.g. "conduit_slot_0,conduit_slot_1". Each CDC source creates one slot per
# table; the loader waits for all of them before writing.
slot_list() {
  local base=$1 out="" i
  for ((i = 0; i < TABLES; i++)); do
    out="${out}${out:+,}${base}_${i}"
  done
  echo "$out"
}

run() { # tool label
  local tool=$1 label=$2
  local dir="$OUT/$label"
  mkdir -p "$dir"
  cat /proc/loadavg > "$dir/load-before.txt" 2>/dev/null || uptime > "$dir/load-before.txt"
  # The loader waits for the arm's replication slot(s) before writing (the
  # slot-aware replacement for the old blind LOADER_DELAY): a Postgres-destination
  # arm's row count must reflect only post-slot steady-state changes, so the
  # loader polls pg_replication_slots and starts writing once every named slot
  # exists, bounded by LOADER_SLOT_TIMEOUT. The S3-/Kafka-destination arms use the
  # same snapshotMode=never source and get their slot(s) too, so the loader's
  # initial fill lands after the slot exists. The file-sink arms (and redpanda)
  # count events, not net rows, have no slot to wait for, and pass an empty list,
  # so the loader starts immediately — unchanged.
  local loader_slots=""
  case "$tool" in
    conduit-pg|conduit-s3)     loader_slots=$(slot_list conduit_slot) ;;
    connect-pg)                loader_slots=$(slot_list debezium_pg_slot) ;;
    connect-s3)                loader_slots=$(slot_list debezium_slot) ;;
    connect-kafka)             loader_slots=$(slot_list debezium_kafka_slot) ;;
    conduit-kafka)             loader_slots=$(slot_list conduit_kafka_slot) ;;
  esac
  # Bounded: a stuck arm cannot hold the machine (each arm has its own
  # registration/warmup waits, so 900s is generous).
  LOADER_SLOTS="$loader_slots" LOADER_SLOT_TIMEOUT="${LOADER_SLOT_TIMEOUT:-600s}" \
    LOADER_RATE="${LOADER_RATE:-20000}" \
    with_tty timeout 900 "$BENCHI" -config bench.yml -tool "$tool" -out "$dir" < /dev/null > /dev/null 2>&1 ||
    echo "run $dir exited $?" >&2
  cat /proc/loadavg > "$dir/load-after.txt" 2>/dev/null || uptime > "$dir/load-after.txt"
  local before after
  before=$(grep -o 'ROWS_BEFORE=[0-9]*' "$dir/benchi.log" 2>/dev/null | grep -o '[0-9]*' | tail -1 || true)
  after=$(grep -o 'ROWS_AFTER=[0-9]*' "$dir/benchi.log" 2>/dev/null | grep -o '[0-9]*' | tail -1 || true)
  if [ -n "${before:-}" ] && [ -n "${after:-}" ]; then
    echo "$label,$tool,$before,$after,$(( after - before ))" >> "$OUT/runs.csv"
  else
    echo "$label,$tool,NA,NA,NA" >> "$OUT/runs.csv"
  fi
}

echo "round,arm,tool,rows_before,rows_after,rows_delta" > "$OUT/runs.csv"

# Warmup round (discarded): brings up every arm once so the first real round
# is not the machine's first cold start.
for arm in "${ARMS[@]}"; do
  run "$arm" "warmup-$arm"
done

# Rotated order (Latin square): round r starts at arm (r-1) mod N, so drift
# across the session lands on every arm equally.
N=${#ARMS[@]}
for ((r = 1; r <= ROUNDS; r++)); do
  for k in $(seq 0 $((N - 1))); do
    arm=${ARMS[$(( (r - 1 + k) % N ))]}
    run "$arm" "r$r-$arm"
  done
done

echo "results in $OUT/runs.csv"
