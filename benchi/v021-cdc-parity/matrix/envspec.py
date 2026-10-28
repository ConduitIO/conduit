#!/usr/bin/env python3
"""Writes the instance's environment record (environment.txt): everything a
reader needs to reproduce the run or to doubt it.

    envspec.py CONDUIT_ROOT LOADER_BIN
"""
import hashlib
import json
import os
import platform
import subprocess
import sys
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import cells  # noqa: E402


def sh(*cmd):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=60).stdout.strip()
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"


def imds(path):
    try:
        tok = urllib.request.urlopen(urllib.request.Request(
            "http://169.254.169.254/latest/api/token", method="PUT",
            headers={"X-aws-ec2-metadata-token-ttl-seconds": "60"}), timeout=2).read().decode()
        return urllib.request.urlopen(urllib.request.Request(
            f"http://169.254.169.254/latest/meta-data/{path}", headers={"X-aws-ec2-metadata-token": tok}), timeout=2).read().decode()
    except OSError:
        return "unknown"


def read(p):
    try:
        with open(p) as f:
            return f.read().strip()
    except OSError:
        return "unknown"


def main():
    root, loader = sys.argv[1], sys.argv[2]
    with open(loader, "rb") as f:
        loader_sha = hashlib.sha256(f.read()).hexdigest()
    here = os.path.dirname(os.path.abspath(__file__))
    lines = {
        "harness_hash": cells.harness_hash(),
        "kit_commit": read(os.path.join(here, "KIT_COMMIT")),
        "debezium_image_pins": read(os.path.join(here, "images", "PINS.json")),
        "loader_sha256": loader_sha,
        "thp": read("/sys/kernel/mm/transparent_hugepage/enabled"),
        "swappiness": read("/proc/sys/vm/swappiness"),
        "dirty_ratio": read("/proc/sys/vm/dirty_ratio"),
        "dirty_background_ratio": read("/proc/sys/vm/dirty_background_ratio"),
        "perf_event_paranoid": read("/proc/sys/kernel/perf_event_paranoid"),
        "clocksource": read("/sys/devices/system/clocksource/clocksource0/current_clocksource"),
        "dockerd_affinity": sh("sh", "-c", "taskset -cp $(pidof dockerd) 2>/dev/null"),
        "conduit_sha": sh("git", "-C", root, "rev-parse", "HEAD"),
        "conduit_describe": sh("git", "-C", root, "describe", "--tags", "--always"),
        "kit_dirty_files": sh("git", "-C", root, "status", "--porcelain", "--", "benchi/v021-cdc-parity"),
        "instance_type": imds("instance-type"),
        "ami": imds("ami-id"),
        "az": imds("placement/availability-zone"),
        "kernel": platform.release(),
        "os": sh("sh", "-c", ". /etc/os-release && echo $PRETTY_NAME"),
        "cpu_model": sh("sh", "-c", "grep -m1 'model name' /proc/cpuinfo | cut -d: -f2"),
        "nproc": sh("nproc"),
        "mem_kb": sh("sh", "-c", "awk '/MemTotal/{print $2}' /proc/meminfo"),
        "sut_cpuset": os.environ.get("SUT_CPUSET", ""),
        "infra_cpuset": os.environ.get("INFRA_CPUSET", ""),
        "sut_mem_limit": cells.SUT_MEM,
        "docker": sh("docker", "version", "--format", "{{.Server.Version}}"),
        "benchi": sh("benchi", "-v"),
        "disk": sh("sh", "-c", "df -h / | tail -1"),
        "go_conduit_build": "Dockerfile in the checkout (golang base pinned there)",
        "conduit_connectors": sh("sh", "-c", f"grep -E 'conduit-connector-(postgres|s3|file|kafka|sdk) ' {root}/go.mod"),
        "images": {k: v for k, v in cells.IMG.items()},
        "built_images": {t: sh("docker", "image", "inspect", "-f", "{{.Id}}", t)
                         for t in ("matrix-conduit:bench", "matrix-conduit:bench-diag", "matrix-conduit:bench-http", "matrix-connect:3.7.0", "matrix-dbzserver:3.7.0")},
        "kafka_broker": "apache/kafka 4.0.0 (pinned digest), heap 4 GiB, RF 1, one broker",
        "connect_runtime": sh("docker", "run", "--rm", "--entrypoint", "sh", "matrix-connect:3.7.0", "-c",
                              "ls /kafka/libs | grep -E '^connect-runtime-'"),
        "connect_default_heap": sh("docker", "run", "--rm", "--entrypoint", "sh", "matrix-connect:3.7.0", "-c",
                                   "java -XX:+PrintFlagsFinal -version 2>/dev/null | grep -E ' MaxHeapSize '"),
        "dbzserver_java": sh("docker", "run", "--rm", "--entrypoint", "sh", cells.IMG["dbzserver"], "-c", "java -version 2>&1 | head -1"),
        "postgres_server": sh("docker", "run", "--rm", "--entrypoint", "postgres", cells.IMG["postgres"], "--version"),
        "ramp_steps": cells.RAMP_STEPS, "ramp_step_s": cells.RAMP_STEP, "ramp_settle_s": cells.RAMP_SETTLE,
        "capacity_warm_s": cells.CAP_WARM, "capacity_window_s": cells.CAP_WINDOW, "rounds": cells.ROUNDS,
        "smoke": cells.SMOKE,
    }
    for k, v in lines.items():
        print(f"{k} {json.dumps(v) if not isinstance(v, str) else v}")


if __name__ == "__main__":
    main()
