#!/bin/bash
# usage: run_docker.sh <linux-binary> <v1|v2> <shape> <outdir> <warmup_s> <window_s> [prof]
# Same as run_native.sh but the process runs in a Linux container (Docker
# Desktop VM), sink on a docker volume, counted at the sink like the harness.
set -u
BIN=$1; ENG=$2; SHAPE=$3; OUT=$4; WARM=$5; WIN=$6; PROF=${7:-}
HERE=$(cd "$(dirname "$0")" && pwd)
SHAPES=$HERE/shapes; [ -d "$SHAPES" ] || SHAPES=$HERE/../shapes
mkdir -p "$OUT"; OUT=$(cd "$OUT" && pwd)
NAME=prof-$$-$RANDOM; VOL=$NAME
docker volume create $VOL >/dev/null
FLAGS="run --log.level error --api.enabled=false --db.type badger --db.badger.path /tmp/db --pipelines.path /pipeline.yml --connectors.path /tmp/c --processors.path /tmp/p"
[ "$ENG" = v2 ] && FLAGS="$FLAGS --preview.pipeline-arch-v2"
ENVS="-e CONDUIT_STAGE_STATS=/out/stages.txt -e CONDUIT_LOOP_LOG=/out/looplog.txt ${EXTRA_ENVS:-}"
if [ -n "$PROF" ]; then
  FLAGS="$FLAGS --dev.cpuprofile /out/cpu.pprof"
  ENVS="$ENVS -e CONDUIT_PROF_TRACE=/out/trace.out -e CONDUIT_PROF_TRACE_DELAY=$((WARM+2))s -e CONDUIT_PROF_TRACE_DUR=${TRACE_DUR:-2s} -e CONDUIT_PROF_MUTEX=/out/mutex.pprof -e CONDUIT_PROF_BLOCK=/out/block.pprof"
fi
SINKS=$(grep -o '/sink/[a-z0-9-]*.jsonl' $SHAPES/$SHAPE/pipeline.yml)
[ -n "${NOSINK:-}" ] && SINKS=""
LA0=$(docker exec $NAME cat /proc/loadavg 2>/dev/null)
docker run -d --name $NAME -v $VOL:/sink -v $OUT:/out -v $BIN:/app/conduit:ro -v $SHAPES/$SHAPE/pipeline.yml:/pipeline.yml:ro $ENVS ${DOCKER_EXTRA:-} alpine:3.20 /app/conduit $FLAGS >/dev/null
for i in $(seq 1 120); do
  [ -n "${NOSINK:-}" ] && break
  ok=1; for s in $SINKS; do [ "$(docker exec $NAME stat -c%s $s 2>/dev/null || echo 0)" -gt 0 ] || ok=0; done
  [ $ok = 1 ] && break; sleep 0.5
done
sleep $WARM
n=0; declare -a S0 S1
for s in $SINKS; do S0[$n]=$(docker exec $NAME stat -c%s $s); n=$((n+1)); done
CPU0=$(docker exec $NAME sh -c "grep usage_usec /sys/fs/cgroup/cpu.stat" | cut -d" " -f2)
T0=$(python3 -c 'import time;print(time.time())'); LA1=$(docker exec $NAME cat /proc/loadavg | cut -d' ' -f1-3)
sleep $WIN
n=0; for s in $SINKS; do S1[$n]=$(docker exec $NAME stat -c%s $s); n=$((n+1)); done
CPU1=$(docker exec $NAME sh -c "grep usage_usec /sys/fs/cgroup/cpu.stat" | cut -d" " -f2)
T1=$(python3 -c 'import time;print(time.time())'); LA2=$(docker exec $NAME cat /proc/loadavg | cut -d' ' -f1-3)
docker kill -s INT $NAME >/dev/null; docker wait $NAME >/dev/null; 
HOSTLA=$(uptime | sed 's/.*load averages: //')
tot=0; n=0
for s in $SINKS; do
  a=${S0[$n]}; b=${S1[$n]}
  c=$(docker run --rm -v $VOL:/sink alpine:3.20 sh -c "tail -c +$((a+1)) $s | head -c $((b-a)) | tr -cd '\n' | wc -c")
  tot=$((tot+c)); n=$((n+1))
done
docker rm $NAME >/dev/null; docker volume rm $VOL >/dev/null
if [ -n "${NOSINK:-}" ]; then
python3 - <<PY
t0=$T0; t1=$T1
rows=[tuple(map(int,l.split())) for l in open('$OUT/looplog.txt') if l.strip()]
def at(t):
    best=min(rows,key=lambda r:abs(r[0]/1e9-t)); return best
a=at(t0); b=at(t1)
t=t1-t0
print(f"{(b[1]-a[1])/((b[0]-a[0])/1e9):.0f} loops/s (no sink counting)  cores_used={($CPU1-$CPU0)/1e6/t:.2f} vmload={'$LA1'} host_after={'$HOSTLA'.strip()}")
PY
else
python3 - <<PY
t=$T1-$T0; tot=$tot; n=$n
print(f"{tot/n/t:.0f} rec/s/sink  ({tot} recs over {t:.1f}s, {n} sinks)  cores_used={($CPU1-$CPU0)/1e6/t:.2f} vmload={'$LA1'} host_after={'$HOSTLA'.strip()}")
PY
fi
