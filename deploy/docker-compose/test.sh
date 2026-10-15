#!/usr/bin/env bash
# Smoke test for the compose quickstart: it comes up healthy, the pipeline runs,
# records reach the destination file, and a restart resumes the pipeline.
#
# Uses its own compose project name and a free ephemeral host port, so it never
# collides with another stack. Cleans up only what it created.
set -euo pipefail

cd "$(dirname "$0")"

export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-conduit-quickstart-test-$$}"
CONDUIT_HTTP_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
export CONDUIT_HTTP_PORT

cleanup() {
  docker compose down -v --timeout 60 >/dev/null 2>&1 || true
}
trap 'status=$?; if [ $status -ne 0 ]; then docker compose logs --no-color conduit | tail -n 60 >&2 || true; fi; cleanup; exit $status' EXIT

base="http://127.0.0.1:${CONDUIT_HTTP_PORT}"

wait_for() { # description, attempts, command...
  local what="$1" tries="$2"; shift 2
  for _ in $(seq "$tries"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "FAIL: timed out waiting for: $what" >&2
  return 1
}

lines() { docker compose exec -T conduit sh -c 'wc -l < /data/records.ndjson' | tr -d '[:space:]'; }
# Capture first: `curl | grep -q` under pipefail fails with SIGPIPE when grep exits early.
pipeline_running() { local body; body="$(curl -fsS "$base/v1/pipelines/quickstart")" && [[ "$body" == *'"state":{"status":"STATUS_RUNNING"'* ]]; }
more_than() { [ "$(lines)" -gt "$1" ]; }

docker compose config --quiet
docker compose up -d --wait --wait-timeout 120

curl -fsS "$base/healthz" >/dev/null
curl -fsS "$base/readyz" >/dev/null
echo "ok: healthy and ready on port $CONDUIT_HTTP_PORT"

wait_for "pipeline to report STATUS_RUNNING" 30 pipeline_running
echo "ok: pipeline quickstart is running"

wait_for "records to reach the destination file" 30 more_than 10
first="$(lines)"
echo "ok: $first records in /data/records.ndjson"

# Restart: SIGTERM must drain, state must persist, the pipeline must come back.
docker compose restart --timeout 60 conduit
docker compose up -d --wait --wait-timeout 120
wait_for "pipeline to resume after restart" 30 pipeline_running
wait_for "records to keep arriving after restart" 30 more_than "$first"
echo "ok: pipeline resumed after restart ($(lines) records)"

echo "PASS"
