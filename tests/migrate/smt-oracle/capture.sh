#!/usr/bin/env bash
# Capture real Kafka Connect 4.2 and Debezium SMT output.
#
#   tests/migrate/smt-oracle/capture.sh [<out-root>]
#
# Two stages, both with pinned images (see "Pinned images" below and the README):
#
#   1. Source. A real Debezium Postgres connector streams the fixture database
#      (pg/*.sql) to a real Kafka broker. java/Consume.java reads the topics back
#      and writes <out-root>/fixtures/debezium-postgres.json, with only clock and
#      transaction identity normalized. SKIP_SOURCE=1 reuses the committed file.
#   2. SMTs. java/Oracle.java runs every suite in cases/ through the Kafka Connect
#      4.2 and Debezium 3.7 SMT classes, on hand-written records and on the
#      captured Debezium records, and writes one canonical JSON file per suite to
#      <out-root>/goldens/. It calls Transformation and Predicate directly, with no
#      worker, no broker and no network.
#
# <out-root> defaults to this directory, so a plain run rewrites the committed
# files and `git diff` shows what changed.
#
# Escape hatches for when a pinned image is gone (see the README):
#   KAFKA_IMAGE, JDK_IMAGE, DEBEZIUM_IMAGE, POSTGRES_IMAGE  a mirror of the image
#   KAFKA_LIBS_DIR      the Kafka 4.2.0 libs/ jars; the SMT stage then runs in JDK_IMAGE
#   DEBEZIUM_JARS_DIR   the Debezium 3.7.0.Final jars (debezium-*.jar)
#   SKIP_SOURCE=1       skip stage 1 and use the committed fixtures/ file
set -euo pipefail

# Pinned by digest. Bump with a commit that re-captures everything.
KAFKA_IMAGE=${KAFKA_IMAGE:-apache/kafka@sha256:9516fb7634bad307d17c33b589fde9023003b0cb761374f500002b980a3149b9} # 4.2.0
JDK_IMAGE=${JDK_IMAGE:-eclipse-temurin@sha256:ebfbb18d11ff163ee83b21258acf12248af48d22a0fdfdc68fa1a1516c05976a} # 21-jdk
DEBEZIUM_IMAGE=${DEBEZIUM_IMAGE:-quay.io/debezium/connect@sha256:b9b983eda59570c526ddffa86ff73e56c53180d2676158ba5a5184cfd1b59a79} # 3.7.0.Final
POSTGRES_IMAGE=${POSTGRES_IMAGE:-quay.io/debezium/postgres@sha256:16f982a4ae010d1795db1974a0c995292869755ec2902586512a274d8d9971bd} # 16

here=$(cd "$(dirname "$0")" && pwd)
root=${1:-$here}
mkdir -p "$root/goldens" "$root/fixtures"
root=$(cd "$root" && pwd)

work=$(mktemp -d "${TMPDIR:-/tmp}/smt-oracle.XXXXXX")
name="smtoracle-$$"
compose() { docker compose -p "$name" -f "$here/compose.yaml" "$@"; }
cleanup() {
  # Only this run's containers and compose project; nothing else on the host is touched.
  COMPOSE_PROJECT_NAME=$name POSTGRES_IMAGE=$POSTGRES_IMAGE KAFKA_IMAGE=$KAFKA_IMAGE DEBEZIUM_IMAGE=$DEBEZIUM_IMAGE \
    compose down -v --remove-orphans >/dev/null 2>&1 || true
  docker rm -f "$name-kafka" "$name-dbz" "$name-javac" "$name-run" "$name-consume" >/dev/null 2>&1 || true
  rm -rf "$work"
}

trap cleanup EXIT

# Fail early, and say where the way out is, when a pinned image is gone.
pull() {
  docker image inspect "$1" >/dev/null 2>&1 || docker pull -q "$1" >/dev/null || {
    echo "cannot pull $1" >&2
    echo "the image may have been removed; see \"If an image disappears\" in $here/README.md" >&2
    exit 1
  }
}
pull "$JDK_IMAGE"
if [[ -z ${KAFKA_LIBS_DIR:-} ]]; then pull "$KAFKA_IMAGE"; fi
if [[ -z ${DEBEZIUM_JARS_DIR:-} ]]; then pull "$DEBEZIUM_IMAGE"; fi
if [[ -z ${SKIP_SOURCE:-} ]]; then pull "$POSTGRES_IMAGE"; if [[ -n ${KAFKA_LIBS_DIR:-} ]]; then pull "$KAFKA_IMAGE"; fi; if [[ -n ${DEBEZIUM_JARS_DIR:-} ]]; then pull "$DEBEZIUM_IMAGE"; fi; fi

mkdir -p "$work/kafka-libs" "$work/dbz" "$work/classes" "$work/out" "$work/fixtures"
uid=$(id -u):$(id -g)

# Kafka jars: from the pinned image, or a directory the caller supplies.
if [[ -n ${KAFKA_LIBS_DIR:-} ]]; then
  cp "$KAFKA_LIBS_DIR"/*.jar "$work/kafka-libs/"
  run_image=$JDK_IMAGE; java_bin=java; libs=/kafka-libs
else
  docker create --name "$name-kafka" "$KAFKA_IMAGE" >/dev/null
  docker cp "$name-kafka:/opt/kafka/libs/." "$work/kafka-libs/"
  run_image=$KAFKA_IMAGE; java_bin=/opt/java/openjdk/bin/java; libs=/opt/kafka/libs
fi

# Debezium jars: the SMTs live in debezium-connect-plugins; the rest are its
# direct dependencies. Kafka classes come from the Kafka image only.
if [[ -n ${DEBEZIUM_JARS_DIR:-} ]]; then
  cp "$DEBEZIUM_JARS_DIR"/debezium-*.jar "$work/dbz/"
else
  docker create --name "$name-dbz" "$DEBEZIUM_IMAGE" >/dev/null
  docker cp "$name-dbz:/kafka/connect/debezium-connector-postgres/." "$work/dbz/"
fi
find "$work/dbz" -type f ! -name 'debezium-*.jar' -delete

# The Kafka image ships a JRE only, so compile in a JDK and run on the Kafka
# image's own Java.
docker run --rm --name "$name-javac" --network none --hostname localhost --user "$uid" \
  -v "$work/kafka-libs:/kafka-libs:ro" -v "$here/java:/src:ro" -v "$work/classes:/classes" \
  "$JDK_IMAGE" javac --release 21 -proc:none -Xlint:-options -cp '/kafka-libs/*' -d /classes /src/Oracle.java /src/Consume.java

java_run() { # java_run <extra docker args...> -- <java args...>
  local extra=()
  while [[ $1 != -- ]]; do extra+=("$1"); shift; done
  shift
  docker run --rm --user "$uid" --hostname localhost --entrypoint "$java_bin" \
    -v "$work/kafka-libs:/kafka-libs:ro" -v "$work/dbz:/dbz:ro" -v "$work/classes:/classes:ro" \
    "${extra[@]}" "$run_image" \
    -Duser.timezone=Asia/Kolkata -Duser.language=en -Duser.country=US -Dfile.encoding=UTF-8 \
    -Dlog4j2.level=ERROR -cp "/classes:$libs/*:/dbz/*" "$@"
}

# ---- stage 1: the same database through real Debezium and real Conduit -------------------
# Debezium (Kafka Connect worker) writes JsonConverter messages to Kafka; Conduit's
# built-in Postgres source writes OpenCDC records to a file. Both start streaming
# before the workload's second half runs, so both see the same snapshot and the
# same changes. Their schemas, and the record counts, are cross-checked.
if [[ -n ${SKIP_SOURCE:-} ]]; then
  cp "$here/fixtures/debezium-postgres.json" "$here/fixtures/debezium-postgres-default-converter.json" "$here/fixtures/conduit-postgres.json" "$here/fixtures/conduit-schemas.json" "$work/fixtures/"
else
  export COMPOSE_PROJECT_NAME=$name POSTGRES_IMAGE KAFKA_IMAGE DEBEZIUM_IMAGE
  # A JVM container can be OOM-killed on a host that is short of memory (exit 137); retry once.
  if ! compose up -d --wait; then
    compose logs --tail 20 >&2
    echo "the stack did not come up; retrying once" >&2
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    compose up -d --wait || { compose logs --tail 40 >&2; exit 1; }
  fi

  psql_run() { compose exec -T postgres psql -v ON_ERROR_STOP=1 -q -U postgres -d oracle "$@"; }
  # The Postgres image's health check can pass on the init-time server that
  # restarts once; wait for the real one.
  until psql_run -tAc 'select 1' >/dev/null 2>&1; do sleep 2; done
  sleep 3
  psql_run < "$here/pg/01-schema.sql"

  compose exec -T connect curl -sS --fail-with-body -X PUT -H 'Content-Type: application/json' \
    http://localhost:8083/connectors/smt/config -d @- >/dev/null <<JSON
{
  "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
  "database.hostname": "postgres",
  "database.port": "5432",
  "database.user": "postgres",
  "database.password": "oracle",
  "database.dbname": "oracle",
  "plugin.name": "pgoutput",
  "slot.name": "dbz_smt",
  "publication.name": "dbz_smt",
  "publication.autocreate.mode": "disabled",
  "schema.include.list": "public",
  "topic.prefix": "dbz",
  "key.converter": "org.apache.kafka.connect.json.JsonConverter",
  "key.converter.schemas.enable": "true",
  "key.converter.replace.null.with.default": "false",
  "value.converter": "org.apache.kafka.connect.json.JsonConverter",
  "value.converter.schemas.enable": "true",
  "value.converter.replace.null.with.default": "false",
  "tasks.max": "1"
}
JSON

  # The same connector again with the converters' defaults (replace.null.with.default=true): what a
  # sink connector reads from a topic written by a default worker. Its own slot and topic prefix.
  compose exec -T connect curl -sS --fail-with-body -X PUT -H 'Content-Type: application/json' \
    http://localhost:8083/connectors/smtdef/config -d @- >/dev/null <<JSON
{
  "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
  "database.hostname": "postgres",
  "database.port": "5432",
  "database.user": "postgres",
  "database.password": "oracle",
  "database.dbname": "oracle",
  "plugin.name": "pgoutput",
  "slot.name": "dbz_smt_def",
  "publication.name": "dbz_smt",
  "publication.autocreate.mode": "disabled",
  "schema.include.list": "public",
  "topic.prefix": "dbzd",
  "key.converter": "org.apache.kafka.connect.json.JsonConverter",
  "key.converter.schemas.enable": "true",
  "value.converter": "org.apache.kafka.connect.json.JsonConverter",
  "value.converter.schemas.enable": "true",
  "tasks.max": "1"
}
JSON

  # Conduit: built from this checkout, against an in-memory Confluent-compatible
  # registry that records every schema the source registers.
  pg_port=$(compose port postgres 5432 | sed 's/.*://')
  go -C "$here/../../.." build -o "$work/conduit" ./cmd/conduit
  go -C "$here/../../.." build -o "$work/registry" ./tests/migrate/smt-oracle/cmd/registry
  mkdir -p "$work/conduit-run/pipelines"
  : >"$work/conduit-run/records.jsonl"
  pg_url="postgres://postgres:oracle@127.0.0.1:$pg_port/oracle?sslmode=disable"
  cat >"$work/conduit-run/pipelines/p.yaml" <<YAML
version: "2.2"
pipelines:
  - id: oracle
    status: running
    connectors:
      # Default settings, Avro schemas attached: what users run. Connector v0.21 handles NULLs,
      # jsonb and TOAST here (an unchanged TOASTed column fails closed, so the workload only
      # makes such an update on a table with REPLICA IDENTITY FULL or on the table below).
      - id: pg
        type: source
        plugin: builtin:postgres
        settings:
          url: $pg_url
          tables: items,a,ab,strict
          cdcMode: logrepl
          logrepl.slotName: conduit_smt
          logrepl.publicationName: conduit_smt
          snapshotMode: initial
      # Tables the connector cannot build an Avro schema for (arrays, a dotted name, the Kelvin
      # sign) and the table whose update leaves a TOASTed column unchanged: with the schema off the
      # update is emitted without that column. These records carry no schema.
      - id: pgnoavro
        type: source
        plugin: builtin:postgres
        settings:
          url: $pg_url
          tables: toast,noschema
          cdcMode: logrepl
          logrepl.slotName: conduit_smt_noavro
          logrepl.publicationName: conduit_smt_noavro
          snapshotMode: initial
          logrepl.withAvroSchema: "false"
      - id: out
        type: destination
        plugin: builtin:file
        settings:
          path: $work/conduit-run/records.jsonl
YAML
  "$work/registry" -dump "$work/conduit-run/schemas.json" >"$work/conduit-run/registry.url" 2>"$work/conduit-run/registry.err" &
  registry_pid=$!
  until [[ -s $work/conduit-run/registry.url ]]; do sleep 1; done
  "$work/conduit" run --db.type inmemory --api.enabled=false --pipelines.path "$work/conduit-run/pipelines" \
    --connectors.file.allowed-dirs "$work/conduit-run" \
    --schema-registry.type confluent --schema-registry.confluent.connection-string "$(cat "$work/conduit-run/registry.url")" \
    >"$work/conduit-run/conduit.log" 2>&1 &
  conduit_pid=$!
  # Conduit first, so nothing registers after the registry starts its dump.
  stop_conduit() {
    kill "$conduit_pid" 2>/dev/null || true
    wait "$conduit_pid" 2>/dev/null || true
    kill "$registry_pid" 2>/dev/null || true
    wait "$registry_pid" 2>/dev/null || true
  }
  trap 'stop_conduit; cleanup' EXIT

  deadline=$((SECONDS + 240))
  for slot in dbz_smt dbz_smt_def conduit_smt conduit_smt_noavro; do
    until [[ $(psql_run -tAc "SELECT active FROM pg_replication_slots WHERE slot_name = '$slot'") == t ]]; do
      if (( SECONDS > deadline )); then
        echo "replication slot $slot never became active" >&2
        tail -5 "$work/conduit-run/conduit.log" >&2
        exit 1
      fi
      sleep 2
    done
  done
  sleep 10 # let both snapshots flush before the stream workload starts
  psql_run < "$here/pg/02-stream.sql"

  # Wait until both sides have every change: Debezium's non-tombstone messages
  # and Conduit's records agree, and the count stops moving.
  prev=
  for _ in $(seq 1 40); do
    sleep 5
    # Conduit retries a failed pipeline forever, so look for the failure in its log.
    if sed 's/\x1b\[[0-9;]*m//g' "$work/conduit-run/conduit.log" | grep ERR | grep -v 'context canceled' >"$work/conduit-run/errors.log"; then
      echo "conduit failed:" >&2
      head -3 "$work/conduit-run/errors.log" >&2
      exit 1
    fi
    java_run --name "$name-consume" --network "${name}_default" -v "$work/fixtures:/out" -- \
      Consume kafka:9092 dbz.public. /out/debezium-postgres.json >"$work/consume.log" 2>&1 || { cat "$work/consume.log" >&2; exit 1; }
    java_run --name "$name-consume" --network "${name}_default" -v "$work/fixtures:/out" -- \
      Consume kafka:9092 dbzd.public. /out/debezium-postgres-default-converter.json >"$work/consume.log" 2>&1 || { cat "$work/consume.log" >&2; exit 1; }
    dbz_n=$(python3 -c 'import json,sys; print(sum(1 for e in json.load(open(sys.argv[1])) if e["value"] is not None))' "$work/fixtures/debezium-postgres.json")
    dbzd_n=$(python3 -c 'import json,sys; print(sum(1 for e in json.load(open(sys.argv[1])) if e["value"] is not None))' "$work/fixtures/debezium-postgres-default-converter.json")
    cond_n=$(grep -c . "$work/conduit-run/records.jsonl" || true)
    cur="$dbz_n/$dbzd_n/$cond_n"
    if [[ $dbz_n == "$cond_n" && $dbzd_n == "$cond_n" && $cur == "$prev" ]]; then break; fi
    prev=$cur
  done
  if [[ $dbz_n != "$cond_n" || $dbzd_n != "$cond_n" ]]; then
    echo "Debezium wrote $dbz_n changes (default converters: $dbzd_n) but Conduit wrote $cond_n" >&2
    exit 1
  fi
  for connector in smt smtdef; do
    state=$(compose exec -T connect curl -sf "http://localhost:8083/connectors/$connector/status")
    if ! grep -q '"state":"RUNNING"' <<<"${state//[[:space:]]/}"; then
      echo "connector $connector not running: $state" >&2
      exit 1
    fi
  done
  stop_conduit
  if [[ ! -s $work/conduit-run/schemas.json ]]; then
    echo "the schema registry wrote no dump:" >&2
    cat "$work/conduit-run/registry.err" >&2
    exit 1
  fi
  go -C "$here/../../.." run ./tests/migrate/smt-oracle/cmd/normalize "$work/conduit-run/records.jsonl" "$work/fixtures/conduit-postgres.json"
  cp "$work/conduit-run/schemas.json" "$work/fixtures/conduit-schemas.json"
fi

# ---- stage 2: the SMT chains ---------------------------------------------------------------
if ! java_run --name "$name-run" --network none -v "$here/cases:/cases:ro" -v "$work/fixtures:/fixtures:ro" -v "$work/out:/out" -e TZ=Asia/Kolkata -- \
  Oracle /cases /out /fixtures >"$work/run.log" 2>&1; then
  # The SMTs log their own warnings and errors for bad input on purpose; show them only when the run fails.
  cat "$work/run.log" >&2
  exit 1
fi

conduit_mod() { go -C "$here/../../.." list -m -f '{{.Version}}' "$1"; }
postgres_connector=$(conduit_mod github.com/conduitio/conduit-connector-postgres)
file_connector=$(conduit_mod github.com/conduitio/conduit-connector-file)

# What the captured files came from. Changing a pin changes this file, so a bump shows up in review.
cat >"$work/out/provenance.json" <<JSON
{
  "kafka": {"version": "4.2.0", "image": "${KAFKA_IMAGE##*@}"},
  "debezium": {"version": "3.7.0.Final", "image": "${DEBEZIUM_IMAGE##*@}"},
  "postgres": {"version": "16", "image": "${POSTGRES_IMAGE##*@}"},
  "jdk": {"version": "21", "image": "${JDK_IMAGE##*@}"},
  "conduit": {"postgresConnector": "$postgres_connector", "fileConnector": "$file_connector"},
  "timezone": "Asia/Kolkata"
}
JSON

# Drop stale goldens for suites that no longer exist, then install the new ones.
for f in "$root"/goldens/*.json; do
  [[ -e $f ]] || continue
  [[ -e $work/out/$(basename "$f") ]] || rm -f "$f"
done
cp "$work/out"/*.json "$root/goldens/"
cp "$work/fixtures"/*.json "$root/fixtures/"
echo "wrote $(find "$work/out" -name '*.json' | wc -l | tr -d ' ') goldens and the source fixture to $root"
