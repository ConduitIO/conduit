#!/usr/bin/env python3
"""Host-side resource sampler for one run.

    res_sampler.py OUT.jsonl [CONTAINER ...]

Default containers: the SUT and every infrastructure container, so the CPU a
side shifts onto its destination (Postgres statements, broker requests, MinIO
writes) is visible and charged (review M2).

Every 5 s, for each named container that exists, appends one JSON line with
its cgroup v2 counters: cumulative CPU time (cpu.stat usage_usec, so CPU-seconds
over any window is a difference of two rows), anonymous memory (memory.stat
anon: the process RSS without page cache, so a file sink's cache does not count
as its memory), memory.current, memory.peak, and block IO (io.stat). Every 15 s
it adds docker's network counters (NetIO), which cgroups do not keep.

Host rows, every sample: per-CPU steal and the SUT CPUs' frequency (a c6i is
virtualized; steal is the neighbour signal), and the RPS mask: every veth
receive queue is pointed at the INFRA cpuset, so packet processing for traffic
into the infrastructure containers is not done on SUT cores (the send side
still is: PLAN.md limitations).

Reading cgroup files costs nothing inside the containers, so this runs in every
run, measured or not. It is itself pinned to the INFRA cpuset by run-shard.sh.
"""
import glob
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


DEFAULT = ["cdcparity-sut", "cdcparity-kafka", "cdcparity-postgres", "cdcparity-minio", "cdcparity-registry",
           "cdcparity-receiver", "cdcparity-loader", "cdcparity-sampler", "cdcparity-counter"]


def cpus(spec):
    out = []
    for part in (spec or "").split(","):
        if "-" in part:
            a, b = part.split("-")
            out += list(range(int(a), int(b) + 1))
        elif part.strip():
            out.append(int(part))
    return out


def host_row(sut_cpus):
    steal, mhz = {}, {}
    try:
        with open("/proc/stat") as f:
            for line in f:
                if line.startswith("cpu") and line[3:4].isdigit():
                    p = line.split()
                    steal[int(p[0][3:])] = int(p[8])
    except OSError:
        pass
    try:
        cpu = None
        with open("/proc/cpuinfo") as f:
            for line in f:
                if line.startswith("processor"):
                    cpu = int(line.split(":")[1])
                elif line.startswith("cpu MHz") and cpu in sut_cpus:
                    mhz[cpu] = float(line.split(":")[1])
    except OSError:
        pass
    return {"steal_ticks": steal, "sut_mhz": mhz}


def disks():
    """role -> device, from the user data's /etc/matrix-disks.json."""
    try:
        with open("/etc/matrix-disks.json") as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def diskstats(devs):
    """Cumulative /proc/diskstats counters per device: ops, ms spent, io_ticks
    (ms the device was busy). Utilization and await over any window are
    differences of two rows (analyze.py)."""
    want = set(devs.values())
    out = {}
    try:
        with open("/proc/diskstats") as f:
            for line in f:
                p = line.split()
                if len(p) >= 14 and p[2] in want:
                    out[p[2]] = {"reads": int(p[3]), "read_ms": int(p[6]), "writes": int(p[7]), "write_ms": int(p[10]),
                                 "io_ticks_ms": int(p[12]), "sectors_w": int(p[9])}
    except OSError:
        pass
    return out


def pin_rps(mask_hex):
    if not mask_hex:
        return
    for p in glob.glob("/sys/class/net/veth*/queues/rx-*/rps_cpus"):
        try:
            with open(p, "w") as f:
                f.write(mask_hex)
        except OSError:
            pass


def main():
    out, names = sys.argv[1], (sys.argv[2:] or DEFAULT)
    sut_cpus = set(cpus(os.environ.get("SUT_CPUSET", "")))
    infra = cpus(os.environ.get("INFRA_CPUSET", ""))
    mask = "%x" % sum(1 << c for c in infra) if infra else ""
    devs = disks()
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
            row["host"] = host_row(sut_cpus)
            row["disks"] = {role: d for role, dev in devs.items() for d in [diskstats(devs).get(dev)] if d}
            pin_rps(mask)
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
