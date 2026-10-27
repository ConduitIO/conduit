#!/usr/bin/env python3
"""Prints SUT_CPUSET=... and INFRA_CPUSET=... for this machine.

The system under test gets the last two physical cores, both hardware threads
of each (4 vCPUs); everything else (Postgres, broker, MinIO, loader, sampler)
gets the remaining cores. Whole cores, so the SUT never shares a core's
execution units with the infrastructure through hyperthreading.
"""
import glob
import os
import sys


def main():
    cores = {}
    for d in sorted(glob.glob("/sys/devices/system/cpu/cpu[0-9]*")):
        cpu = int(os.path.basename(d)[3:])
        try:
            pkg = int(open(f"{d}/topology/physical_package_id").read())
            core = int(open(f"{d}/topology/core_id").read())
        except OSError:
            continue
        cores.setdefault((pkg, core), []).append(cpu)
    keys = sorted(cores)
    if len(keys) < 4:
        sys.exit(f"cpusets: need at least 4 physical cores, found {len(keys)}")
    sut = sorted(c for k in keys[-2:] for c in cores[k])
    infra = sorted(c for k in keys[:-2] for c in cores[k])
    print(f"SUT_CPUSET={','.join(map(str, sut))}")
    print(f"INFRA_CPUSET={','.join(map(str, infra))}")


if __name__ == "__main__":
    main()
