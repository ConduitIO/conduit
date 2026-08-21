#!/usr/bin/env python3
"""Fail when govulncheck reaches a vulnerability that is not in the baseline.

Reads govulncheck's JSON stream and an allowlist of GO-XXXX-NNNN IDs, and
compares the two sets. Writes a Markdown report to stdout (the workflow
appends it to the job summary) and exits 1 when a vulnerability is reachable
that the baseline does not account for.

Only CALLED-level findings count. govulncheck emits a finding for each level
of confidence it can establish - the module is required, the package is
imported, the vulnerable symbol is actually called - and only the last of
those means this codebase can reach the bug. A finding whose trace's first
frame has a `function` is a called-level one; that is the same distinction
govulncheck's own text output draws when it separates "Your code is affected
by N vulnerabilities" from "vulnerabilities in packages you import, but your
code doesn't appear to call these".

Failing on import-level findings instead would make the gate fire on
dependencies nothing in this repo can execute, which is the generic-scanner
behaviour this gate was chosen to avoid.
"""

import json
import sys


def called_vuln_ids(path):
    """IDs of vulnerabilities govulncheck traced to an actual call site."""
    with open(path, encoding="utf-8") as handle:
        raw = handle.read()

    # govulncheck -format json emits a stream of concatenated JSON objects, not
    # a single document, so it needs incremental decoding rather than json.load.
    decoder = json.JSONDecoder()
    ids = set()
    index = 0
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index >= len(raw):
            break
        obj, index = decoder.raw_decode(raw, index)
        finding = obj.get("finding")
        if not finding:
            continue
        trace = finding.get("trace") or []
        if trace and trace[0].get("function"):
            ids.add(finding["osv"])
    return ids


def allowlisted_ids(path):
    ids = set()
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            line = line.split("#", 1)[0].strip()
            if line:
                ids.add(line)
    return ids


def main():
    scan_path, allowlist_path = sys.argv[1], sys.argv[2]
    found = called_vuln_ids(scan_path)
    allowed = allowlisted_ids(allowlist_path)

    new = sorted(found - allowed)
    cleared = sorted(allowed - found)

    print("### govulncheck")
    print()
    print(f"{len(found)} reachable, {len(allowed)} in the baseline.")
    print()

    if cleared:
        # Not a failure: the list shrinking is the point. But a stale entry
        # would let a fixed vulnerability come back silently, so say so loudly
        # enough that someone deletes the line.
        print("**Fixed since the baseline** — delete these from "
              "`.github/govulncheck-allowlist.txt`:")
        print()
        for vuln in cleared:
            print(f"- `{vuln}`")
        print()

    if not new:
        print("No new reachable vulnerabilities.")
        return 0

    print("**FAILED — reachable and not in the baseline:**")
    print()
    for vuln in new:
        print(f"- `{vuln}` — https://pkg.go.dev/vuln/{vuln}")
    print()
    print("Fix it if you can: bump the dependency, bump the Go toolchain, or "
          "stop calling the vulnerable path. If it genuinely cannot be fixed "
          "now, add the ID to `.github/govulncheck-allowlist.txt` **with the "
          "reason on the line above it** — an entry with no justification is "
          "a bug.")

    # Also to stderr: the job summary is not visible in the failing step's log.
    print("::error::govulncheck found reachable vulnerabilities not in the "
          f"baseline: {', '.join(new)}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
