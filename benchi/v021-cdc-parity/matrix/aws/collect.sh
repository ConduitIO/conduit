#!/usr/bin/env bash
# Copies a run's results from its bucket and analyzes them. Read-only on AWS.
#   matrix/aws/collect.sh RUN_ID DEST_DIR
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
RUN_ID=${1:?run id}; DEST=${2:?dest}
cd "$HERE"
terraform workspace select "$RUN_ID" >/dev/null
B=$(terraform output -raw bucket)
mkdir -p "$DEST"
aws s3 sync --quiet "s3://$B/results/" "$DEST/raw/"
for s in "$DEST"/raw/shard-*; do echo "$(basename "$s"): $(tail -1 "$s/status.txt" 2>/dev/null)"; done
# Merge the shards: every cell directory is unique across shards (a repeat is CELL-rep1).
mkdir -p "$DEST/cells"
for d in "$DEST"/raw/shard-*/*/; do [ -d "$d/cell" ] && cp -R "$d" "$DEST/cells/"; done
python3 "$HERE/../analyze.py" "$DEST/cells" --gate "$DEST/gate.json" > "$DEST/results.md"
echo "results: $DEST/results.md   gate: $DEST/gate.json ($(python3 -c "import json; print('PASS' if json.load(open('$DEST/gate.json'))['pass'] else 'FAIL')"))"
