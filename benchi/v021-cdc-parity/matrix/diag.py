#!/usr/bin/env python3
"""Diagnosis sidecar for one run (started by run-cell.sh, killed at the end).

    diag.py RUN_DIR ARM CELL_JSON

It waits for the first sink record (from the sampler's samples.jsonl), then
for the cell's warm-up, and then, for the measurement window:

  - Conduit arms: scrapes the engine's Prometheus metrics (/metrics on the
    container-loopback API) at the window's start and end, every run. The
    difference is the per-stage timing: source read, processor, destination
    write and ack histograms. It is a few hundred kilobytes over loopback, so
    it runs in measured runs too.
  - JVM arms in a diag run (JVM_DIAG on): async-profiler, itimer mode (CPU
    samples via setitimer; no perf_events privileges inside the container),
    for the whole window, as collapsed stacks and a flame graph.

Conduit's pprof profiles (CPU, heap, block) are written by Conduit itself on
graceful shutdown in diag runs (--dev.* flags set by run-cell.sh). Mutex
profiles are not available: Conduit has no flag that enables them (PLAN.md
limitations).
"""
import json
import os
import subprocess
import sys
import time

SUT = "cdcparity-sut"


def first_record_ms(samples, deadline):
    while time.time() < deadline:
        try:
            with open(samples) as f:
                for line in f:
                    try:
                        r = json.loads(line)
                    except ValueError:
                        continue
                    sinks = r.get("sinks", {})
                    counts = [v.get("count") or 0 for v in sinks.values() if isinstance(v, dict)]
                    if counts and min(counts) > 0:
                        return r["ts_ms"]
        except OSError:
            pass
        time.sleep(2)
    return None


def scrape(path):
    with open(path, "w") as f:
        subprocess.run(["docker", "exec", SUT, "wget", "-qO-", "http://127.0.0.1:8080/metrics"],
                       stdout=f, stderr=subprocess.DEVNULL, timeout=30)


def main():
    rd, arm, meta = sys.argv[1], sys.argv[2], json.load(open(sys.argv[3]))
    is_diag = os.path.basename(rd).startswith("diag-")
    mode = meta["mode"]
    if mode in ("capacity", "drain"):
        warm, window = meta["window"]["warm_s"], meta["window"]["window_s"]
    elif mode == "ramp":
        warm = meta["ramp"]["warm_s"]
        window = meta["ramp"]["step_s"] * len(meta["ramp"]["steps"])
    else:
        return
    first = first_record_ms(os.path.join(rd, "samples.jsonl"), time.time() + 3600)
    if first is None:
        return
    start = first / 1000 + warm
    time.sleep(max(0, start - time.time()))
    conduit = arm.startswith("conduit")
    if conduit:
        scrape(os.path.join(rd, "metrics-window-start.txt"))
    prof = None
    if is_diag and not conduit and not arm.startswith("dbzserver-native"):
        prof = subprocess.Popen(
            ["docker", "exec", SUT, "/opt/async-profiler/bin/asprof", "-d", str(window), "-e", "itimer",
             "-o", "collapsed", "-f", "/prof/cpu.collapsed", "jps"],
            stdout=open(os.path.join(rd, "asprof.log"), "w"), stderr=subprocess.STDOUT)
    time.sleep(window)
    if conduit:
        scrape(os.path.join(rd, "metrics-window-end.txt"))
    if prof:
        prof.wait(timeout=120)


if __name__ == "__main__":
    try:
        main()
    except (KeyboardInterrupt, subprocess.TimeoutExpired):
        pass
