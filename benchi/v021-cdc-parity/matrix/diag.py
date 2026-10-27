#!/usr/bin/env python3
"""Diagnosis sidecar for one DIAG run (run-cell.sh starts it only for diag-*
runs, which are never measured).

    diag.py RUN_DIR ARM CELL_JSON

It waits for the first sink record and the cell's warm-up, then, inside the
measurement window only (review M8):

  Conduit (the -diag build: pprof on :6060, mutex 1/10, block 100 us)
    - CPU profile, 60 s, from the window's start
    - mutex and block profiles at the window's start and end (the analysis
      subtracts them: contention inside the window only)
    - an execution trace of 10 s and a goroutine dump, mid-window (off-CPU:
      what each stage is waiting on)
    - the engine's Prometheus metrics polled every 10 s (per-stage histograms
      as a series, Go GC CPU fraction and allocation rate)
  JVM sides
    - async-profiler 4.6: itimer (CPU) for the first part of the window, then
      wall clock (off-CPU), both as collapsed stacks
    - Jolokia polled every 10 s: Debezium streaming metrics (queue remaining
      capacity, ms behind source, events seen), Kafka Connect source and sink
      task metrics, producer metrics (batch-size-avg, records-per-request-avg,
      request-latency-avg), consumer fetch metrics, GC and heap

Everything is fetched through the counter container on the bench network;
nothing is published on the host.
"""
import json
import os
import subprocess
import sys
import time

SUT = "cdcparity-sut"
COUNTER = "cdcparity-counter"
JOLOKIA_READS = [
    {"type": "read", "mbean": "debezium.postgres:type=connector-metrics,context=streaming,*"},
    {"type": "read", "mbean": "debezium.postgres:type=connector-metrics,context=snapshot,*"},
    {"type": "read", "mbean": "kafka.connect:type=source-task-metrics,*"},
    {"type": "read", "mbean": "kafka.connect:type=sink-task-metrics,*"},
    {"type": "read", "mbean": "kafka.connect:type=task-error-metrics,*"},
    {"type": "read", "mbean": "kafka.producer:type=producer-metrics,*"},
    {"type": "read", "mbean": "kafka.consumer:type=consumer-fetch-manager-metrics,*"},
    {"type": "read", "mbean": "java.lang:type=GarbageCollector,*"},
    {"type": "read", "mbean": "java.lang:type=Memory"},
]


def first_record_ms(samples, deadline):
    while time.time() < deadline:
        try:
            with open(samples) as f:
                for line in f:
                    try:
                        r = json.loads(line)
                    except ValueError:
                        continue
                    counts = [v.get("count") or 0 for v in r.get("sinks", {}).values() if isinstance(v, dict)]
                    if counts and min(counts) > 0:
                        return r["ts_ms"]
        except OSError:
            pass
        time.sleep(2)
    return None


def fetch(url, out_name, timeout=120):
    """GET url from inside the counter container into /out/prof/<out_name>."""
    subprocess.run(["docker", "exec", COUNTER, "wget", "-q", "-T", str(timeout), "-O", f"/out/prof/{out_name}", url],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=timeout + 30)


def bg(cmd):
    return subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def main():
    rd, arm, meta = sys.argv[1], sys.argv[2], json.load(open(sys.argv[3]))
    mode = meta["mode"]
    if mode in ("capacity", "drain", "idle"):
        warm, window = meta["window"]["warm_s"], meta["window"]["window_s"]
    elif mode == "ramp":
        warm, window = meta["ramp"]["warm_s"], meta["ramp"]["step_s"] * len(meta["ramp"]["steps"])
    else:
        return
    first = first_record_ms(os.path.join(rd, "samples.jsonl"), time.time() + 3600)
    if first is None:
        return
    time.sleep(max(0, first / 1000 + warm - time.time()))
    t_start = time.time()
    procs = []
    if arm.startswith("conduit"):
        p = "http://cdcparity-sut:6060/debug/pprof"
        fetch(f"{p}/mutex", "mutex-start.pprof", 30)
        fetch(f"{p}/block", "block-start.pprof", 30)
        procs.append(bg(["docker", "exec", COUNTER, "wget", "-q", "-T", "120", "-O", "/out/prof/cpu-window.pprof",
                         f"{p}/profile?seconds=60"]))
        procs.append(bg(["docker", "exec", COUNTER, "/loader/loader", "poll", "-url", "http://cdcparity-sut:8080/metrics",
                         "-token-file", "/out/prof/token", "-every", "10s", "-out", "/out/poll-metrics.jsonl"]))
        time.sleep(max(0, window / 2 - (time.time() - t_start)))
        fetch(f"{p}/trace?seconds=10", "trace-window.out", 60)
        fetch(f"{p}/goroutine?debug=2", "goroutines-mid.txt", 30)
        time.sleep(max(0, window - (time.time() - t_start) - 5))
        fetch(f"{p}/mutex", "mutex-end.pprof", 30)
        fetch(f"{p}/block", "block-end.pprof", 30)
        fetch(f"{p}/heap", "heap-end.pprof", 30)
    elif not arm.startswith("dbzserver-native"):
        with open(os.path.join(rd, "prof", "jolokia.json"), "w") as f:
            json.dump(JOLOKIA_READS, f)
        procs.append(bg(["docker", "exec", COUNTER, "/loader/loader", "poll", "-url", "http://cdcparity-sut:8778/jolokia/",
                         "-body", "/out/prof/jolokia.json", "-every", "10s", "-out", "/out/poll-jolokia.jsonl"]))
        half = max(30, window // 2 - 10)
        for event, name in (("itimer", "cpu-window.collapsed"), ("wall", "wall-window.collapsed")):
            with open(os.path.join(rd, f"asprof-{event}.log"), "w") as log:
                subprocess.run(["docker", "exec", SUT, "/opt/async-profiler/bin/asprof", "-d", str(half), "-e", event,
                                "-o", "collapsed", "-f", f"/prof/{name}", "jps"],
                               stdout=log, stderr=subprocess.STDOUT, timeout=half + 120)
    time.sleep(max(0, window - (time.time() - t_start)))
    for p in procs:
        p.terminate()
    for p in procs:
        try:
            p.wait(timeout=30)
        except subprocess.TimeoutExpired:
            p.kill()
    subprocess.run(["docker", "exec", COUNTER, "pkill", "-f", "loader poll"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    try:
        main()
    except (KeyboardInterrupt, subprocess.TimeoutExpired):
        pass
