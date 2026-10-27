#!/usr/bin/env python3
"""Collects one run's facts into run.json: the hook timestamps benchi logged
(KEY=<int> lines, see the loader's now/first helpers), the exit code, and
where the run's files are.

    runinfo.py RUN_DIR ARM LABEL EXIT_CODE START_MS
"""
import glob
import json
import os
import re
import sys

KEY = re.compile(r"\b([A-Z][A-Z0-9_]+_(?:MS|COUNT))=([0-9]+)")


def main():
    rd, arm, label, rc, t0 = sys.argv[1:6]
    vals = {}
    logs = glob.glob(os.path.join(rd, "benchi", "**", "benchi.log"), recursive=True) + [os.path.join(rd, "hooks.txt")]
    for p in logs:
        try:
            with open(p, errors="replace") as f:
                for line in f:
                    for k, v in KEY.findall(line):
                        vals[k] = int(v)  # the last value wins
        except OSError:
            pass
    verify = {}
    for p in glob.glob(os.path.join(rd, "verify-*.json")):
        try:
            with open(p) as f:
                verify[os.path.basename(p)[7:-5]] = json.load(f)
        except (OSError, ValueError):
            verify[os.path.basename(p)] = {"error": "unreadable"}
    print(json.dumps({"arm": arm, "label": label, "exit_code": int(rc), "start_ms": int(t0),
                      "hooks": vals, "verify": verify}, indent=2))


if __name__ == "__main__":
    main()
