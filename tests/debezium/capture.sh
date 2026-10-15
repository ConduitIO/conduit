#!/usr/bin/env bash
# Capture golden output from real Debezium Postgres.
#
#   tests/debezium/capture.sh <debezium-version> <converter>
#
# <debezium-version> is one of the versions in image_for below; <converter> is one of
# json (JsonConverter, schemas.enable=true), json-numeric (the same with
# decimal.format=NUMERIC), json-txmeta (the same as json with the connector's
# provide.transaction.metadata=true), json-noschema (schemas.enable=false) or avro (Confluent AvroConverter with Schema
# Registry). The result lands in testdata/golden/<version>/<converter>/.
#
# Every run uses its own compose project and ephemeral host ports, and tears
# the stack down on exit. Nothing is left running.
set -euo pipefail

# Pinned Debezium images, by digest. Add a version here to capture it.
image_for() {
  case $1 in
    2.7.4) echo "quay.io/debezium/connect@sha256:5a62d0fb204c3ff3ba66f9e402ec8d628e8b05d527b06185764c72e28dfb1d04" ;;
    3.7.0) echo "quay.io/debezium/connect@sha256:65a33b215d88122d8db8c1c35f24e8e1aafbadcc148df3064a4d60f3e38ddd6e" ;;
  esac
}

version=${1:?usage: capture.sh <debezium-version> <json|json-numeric|json-txmeta|json-noschema|avro>}
converter=${2:?usage: capture.sh <debezium-version> <json|json-numeric|json-txmeta|json-noschema|avro>}
image=$(image_for "$version")
if [[ -z $image ]]; then
  echo "unknown Debezium version $version; known: 2.7.4 3.7.0" >&2
  exit 2
fi

here=$(cd "$(dirname "$0")" && pwd)
out="$here/testdata/golden/$version/$converter"
project="dbzgolden-${version//./}-${converter}-$$"

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

export COMPOSE_PROJECT_NAME=$project
export DEBEZIUM_IMAGE=$image
export KAFKA_HOST_PORT
KAFKA_HOST_PORT=$(free_port)

compose() { docker compose -p "$project" -f "$here/compose.yaml" "$@"; }

cleanup() {
  compose down -v --remove-orphans --rmi local >/dev/null 2>&1 || true
}
trap cleanup EXIT

compose up -d --build --wait

port_of() { compose port "$1" "$2" | sed 's/.*://'; }
connect_url="http://127.0.0.1:$(port_of connect 8083)"
registry_url="http://127.0.0.1:$(port_of schema-registry 8081)"

psql_run() { compose exec -T postgres psql -v ON_ERROR_STOP=1 -q -U postgres -d golden "$@"; }

psql_run < "$here/sql/01-schema.sql"

case $converter in
  json)
    conv='"key.converter": "org.apache.kafka.connect.json.JsonConverter",
          "key.converter.schemas.enable": "true",
          "value.converter": "org.apache.kafka.connect.json.JsonConverter",
          "value.converter.schemas.enable": "true",'
    registry_flag=() ;;
  json-numeric)
    conv='"key.converter": "org.apache.kafka.connect.json.JsonConverter",
          "key.converter.schemas.enable": "true",
          "key.converter.decimal.format": "NUMERIC",
          "value.converter": "org.apache.kafka.connect.json.JsonConverter",
          "value.converter.schemas.enable": "true",
          "value.converter.decimal.format": "NUMERIC",'
    registry_flag=() ;;
  json-txmeta)
    conv='"key.converter": "org.apache.kafka.connect.json.JsonConverter",
          "key.converter.schemas.enable": "true",
          "value.converter": "org.apache.kafka.connect.json.JsonConverter",
          "value.converter.schemas.enable": "true",
          "provide.transaction.metadata": "true",'
    registry_flag=() ;;
  json-noschema)
    conv='"key.converter": "org.apache.kafka.connect.json.JsonConverter",
          "key.converter.schemas.enable": "false",
          "value.converter": "org.apache.kafka.connect.json.JsonConverter",
          "value.converter.schemas.enable": "false",'
    registry_flag=() ;;
  avro)
    conv='"key.converter": "io.confluent.connect.avro.AvroConverter",
          "key.converter.schema.registry.url": "http://schema-registry:8081",
          "value.converter": "io.confluent.connect.avro.AvroConverter",
          "value.converter.schema.registry.url": "http://schema-registry:8081",'
    registry_flag=(-registry "$registry_url") ;;
  *)
    echo "unknown converter $converter" >&2; exit 2 ;;
esac

# Connector settings are Debezium defaults except what a capture needs: the
# plugin, the slot and publication, the table filter and the topic prefix.
curl -sf -X PUT -H 'Content-Type: application/json' "$connect_url/connectors/golden/config" -d @- >/dev/null <<EOF
{
  "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
  "database.hostname": "postgres",
  "database.port": "5432",
  "database.user": "postgres",
  "database.password": "golden",
  "database.dbname": "golden",
  "plugin.name": "pgoutput",
  "slot.name": "dbz_golden",
  "publication.name": "dbz_golden",
  "publication.autocreate.mode": "filtered",
  "schema.include.list": "golden",
  "topic.prefix": "dbz",
  $conv
  "tasks.max": "1"
}
EOF

# The snapshot is done when the connector reports streaming; Debezium exposes
# that through its JMX metrics only, so wait for the snapshot topics to fill
# instead: the last snapshot table (sentinel is empty, nopk has one row) is
# read after the others, and the slot is active once streaming starts.
deadline=$((SECONDS + 180))
until psql_run -tAc "SELECT active FROM pg_replication_slots WHERE slot_name = 'dbz_golden'" | grep -qx t; do
  if (( SECONDS > deadline )); then
    curl -s "$connect_url/connectors/golden/status" >&2 || true
    echo "replication slot never became active" >&2
    exit 1
  fi
  sleep 2
done
# Streaming starts after the snapshot; give the connector time to finish
# flushing the snapshot before the stream workload starts.
sleep 10

psql_run < "$here/sql/02-stream.sql"

state=$(curl -sf "$connect_url/connectors/golden/status")
if ! grep -q '"state":"RUNNING"' <<<"${state//[[:space:]]/}"; then
  echo "connector not running: $state" >&2
  exit 1
fi

rm -rf "$out"
mkdir -p "$out"
go -C "$here/../.." run ./tests/debezium/cmd/capture \
  -brokers "127.0.0.1:$KAFKA_HOST_PORT" "${registry_flag[@]}" -out "$out"

# Record exactly what produced these files.
cat > "$out/MANIFEST" <<EOF
debezium_image=$image
converter=$converter
connector_status=$(tr -d '\n ' <<<"$state")
EOF

# Fail the capture if the connector died during the stream workload.
state=$(curl -sf "$connect_url/connectors/golden/status")
if ! grep -q '"state":"RUNNING"' <<<"${state//[[:space:]]/}"; then
  echo "connector failed during capture: $state" >&2
  exit 1
fi
