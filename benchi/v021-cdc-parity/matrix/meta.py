#!/usr/bin/env python3
"""Reads one field of a generated cell.json for the shell runners.

    meta.py CELL_JSON PATH[:csv]

PATH is dotted (recover.ops, slots.connect-s). A list prints space-joined, or
comma-joined with :csv; a bool prints 1 or 0. A missing field prints nothing.
"""
import json
import sys


def main() -> int:
    path, _, fmt = sys.argv[2].partition(":")
    with open(sys.argv[1]) as f:
        v = json.load(f)
    for part in path.split("."):
        if not isinstance(v, dict) or part not in v:
            return 0
        v = v[part]
    if isinstance(v, list):
        print(("," if fmt == "csv" else " ").join(str(x) for x in v))
    elif isinstance(v, bool):
        print("1" if v else "0")
    elif v is not None:
        print(v)
    return 0


if __name__ == "__main__":
    sys.exit(main())
