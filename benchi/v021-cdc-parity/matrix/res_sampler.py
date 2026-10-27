#!/usr/bin/env python3
"""Host-side resource sampler for one run.

    res_sampler.py OUT.jsonl CONTAINER [CONTAINER ...]

Every 5 s, for each named container that exists, appends one JSON line with
its cgroup v2 counters: cumulative CPU time (cpu.stat usage_usec, so CPU-seconds
over any window is a difference of two rows), anonymous memory (memory.stat
anon: the process RSS without page cache, so a file sink's cache does not count
as its memory), memory.current, memory.peak, and block IO (io.stat). Every 15 s
it adds docker's network counters (NetIO), which cgroups do not keep.

Reading cgroup files costs nothing inside the containers, so this runs in every
run, measured or not.
"""
import json
import os
import subprocess
import sys
import time

CG = "/sys/fs/cgroup"


def container_id(name):
    try:
        out = subprocess.run(["docker", "inspect", "-f", "{{.Id}}", name], capture_output=True, text=True, timeout=10)
    except subprocess.TimeoutExpired:
        return None
    return out.stdout.strip() or None


def cgroup_dir(cid):
    for d in (f"{CG}/system.slice/docker-{cid}.scope", f"{CG}/docker/{cid}"):
        if os.path.isdir(d):
            return d
    return None


def read_kv(path):
    out = {}
    try:
        with open(path) as f:
            for line in f:
                k, _, v = line.partition(" ")
                if v.strip().isdigit():
                    out[k] = int(v)
    except OSError:
        pass
    return out


def read_int(path):
    try:
        with open(path) as f:
            return int(f.read().split()[0])
    except (OSError, ValueError, IndexError):
        return None


def io_bytes(path):
    rb = wb = 0
    try:
        with open(path) as f:
            for line in f:
                for kv in line.split()[1:]:
                    k, _, v = kv.partition("=")
                    if k == "rbytes":
                        rb += int(v)
                    elif k == "wbytes":
                        wb += int(v)
    except OSError:
        pass
    return rb, wb


def netio():
    try:
        out = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{.Name}}\t{{.NetIO}}"],
                             capture_output=True, text=True, timeout=20)
    except subprocess.TimeoutExpired:
        return {}
    res = {}
    for line in out.stdout.splitlines():
        name, _, net = line.partition("\t")
        res[name] = net
    return res


def main():
    out, names = sys.argv[1], sys.argv[2:]
    ids = {}
    last_net = 0.0
    with open(out, "a") as f:
        while True:
            now = time.time()
            row = {"ts_ms": int(now * 1000), "c": {}}
            for n in names:
                if n not in ids or ids[n] is None or cgroup_dir(ids[n]) is None:
                    ids[n] = container_id(n)
                cid = ids.get(n)
                d = cgroup_dir(cid) if cid else None
                if not d:
                    continue
                cpu = read_kv(f"{d}/cpu.stat")
                mem = read_kv(f"{d}/memory.stat")
                rb, wb = io_bytes(f"{d}/io.stat")
                row["c"][n] = {
                    "cpu_usec": cpu.get("usage_usec"),
                    "throttled_usec": cpu.get("throttled_usec"),
                    "anon": mem.get("anon"),
                    "file": mem.get("file"),
                    "mem_current": read_int(f"{d}/memory.current"),
                    "mem_peak": read_int(f"{d}/memory.peak"),
                    "io_rbytes": rb,
                    "io_wbytes": wb,
                }
            if now - last_net >= 15:
                row["net"] = netio()
                last_net = now
            f.write(json.dumps(row) + "\n")
            f.flush()
            time.sleep(max(0.0, 5 - (time.time() - now)))


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
