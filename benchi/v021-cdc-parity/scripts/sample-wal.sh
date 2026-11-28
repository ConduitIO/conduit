#!/usr/bin/env bash
# Samples WAL pressure on the source database once a second into <dir>/wal.csv,
# for the whole of one arm's run. Diagnostic AND admissibility input: the
# comparison number is still the ground-truth count, but a result whose WAL
# rate matches the measured WAL ceiling was limited by the source database, not
# by the CDC system under test (see ../README.md, "The ceiling rule").
#
# Usage: sample-wal.sh <out_dir>   (run in the background; kill to stop)
#
# Rows, all kind-tagged so one file carries all three shapes:
#   wal,<ts_ms>,<wal_bytes>,,                      WAL insert position
#   slot,<ts_ms>,<flush_lag_bytes>,<slot>,<pid>    per replication slot
#   proc,<ts_ms>,<pid>,<utime_ticks>,<stime_ticks> per process in the DB container
#
# The proc rows are how per-slot walsender CPU is obtained: there is no SQL for
# per-backend CPU, so the sampler reads /proc/<pid>/stat inside the Postgres
# container and summarize.py joins the slot rows' active_pid to them. Ticks are
# USER_HZ (100 on every mainstream Linux); utime+stime over the window divided
# by 100 is CPU-seconds.
#
# Every sample is best-effort: benchi brings the infrastructure up after this
# sampler starts and tears it down before it is killed, so `docker exec`
# failures are normal at both ends of a run and are skipped silently. A missing
# or short wal.csv therefore means "not measured", never "zero".
set -u

dir=${1:?usage: sample-wal.sh <out_dir>}
sql="$(cd "$(dirname "$0")" && pwd)/wal-sample.sql"
out="$dir/wal.csv"

echo "kind,ts_ms,v1,v2,v3" > "$out"

while true; do
  sample=$(docker exec -i cdcparity-postgres \
    psql -U bench -d bench -tA -F, -q -v ON_ERROR_STOP=1 < "$sql" 2>/dev/null) || sample=""
  if [ -n "$sample" ]; then
    printf '%s\n' "$sample" >> "$out"
    # Reuse the Postgres clock timestamp from this same iteration, so the proc
    # rows land on the same timeline as the wal/slot rows.
    ts=$(printf '%s\n' "$sample" | awk -F, '$1 == "wal" { print $2; exit }')
    if [ -n "$ts" ]; then
      # /proc/<pid>/stat field 2 (comm) is parenthesised and may contain
      # spaces, so strip "<pid> (<comm>) " before indexing: utime and stime are
      # fields 12 and 13 of what is left.
      docker exec cdcparity-postgres sh -c '
        for d in /proc/[0-9]*; do
          pid=${d#/proc/}
          awk -v p="$pid" "{ sub(/^[0-9]+ \(.*\) /, \"\"); print p \",\" \$12 \",\" \$13 }" "$d/stat" 2>/dev/null
        done' 2>/dev/null | sed "s/^/proc,$ts,/" >> "$out"
    fi
  fi
  sleep 1
done
