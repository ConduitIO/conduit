#!/usr/bin/env python3
"""Fail when govulncheck reaches a vulnerability that is not in the baseline.

Reads govulncheck's JSON stream and an allowlist of GO-XXXX-NNNN IDs, and
compares the two sets. Writes a Markdown report to stdout (the workflow
appends it to the job summary) and exits 1 when a vulnerability is reachable
that the baseline does not account for.

FAILS CLOSED ON A SCAN THAT DID NOT RUN. This is the property that makes the
gate worth having. govulncheck can die for reasons that have nothing to do
with vulnerabilities - the module proxy is down, the runner OOMs, a pinned
version stops supporting the toolchain - and the workflow tolerates a nonzero
exit because govulncheck exits nonzero whenever it finds anything. So a dead
scan leaves an empty or truncated file, and a naive reader sees zero findings
and reports "no new vulnerabilities" - passing the build while scanning
nothing, and simultaneously claiming every baseline entry was fixed.

The discriminator is govulncheck's own `config` message, which it emits first
on every successful run regardless of what it finds. No config message means
no scan, which is an error, not a clean bill of health.

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
import re
import sys


class ScanDidNotRun(Exception):
    """The scan output is absent, truncated, or not govulncheck's."""


def called_vuln_ids(path):
    """IDs of vulnerabilities govulncheck traced to an actual call site.

    Raises ScanDidNotRun when the output cannot be trusted to represent a
    completed scan.
    """
    try:
        with open(path, encoding="utf-8") as handle:
            raw = handle.read()
    except OSError as err:
        raise ScanDidNotRun(f"cannot read {path}: {err}") from err

    if not raw.strip():
        raise ScanDidNotRun(f"{path} is empty - govulncheck produced no output")

    # govulncheck -format json emits a stream of concatenated JSON objects, not
    # a single document, so it needs incremental decoding rather than json.load.
    decoder = json.JSONDecoder()
    ids = set()
    saw_config = False
    index = 0
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index >= len(raw):
            break
        try:
            obj, index = decoder.raw_decode(raw, index)
        except json.JSONDecodeError as err:
            # Truncated mid-object: the process died partway through writing.
            # Whatever was parsed so far is an undercount, so it cannot be
            # compared against the baseline.
            raise ScanDidNotRun(
                f"{path} is not valid govulncheck JSON at byte {index}: {err}"
            ) from err
        if "config" in obj:
            saw_config = True
        finding = obj.get("finding")
        if not finding:
            continue
        trace = finding.get("trace") or []
        if trace and trace[0].get("function"):
            ids.add(finding["osv"])

    if not saw_config:
        # Parseable JSON that is not govulncheck's stream - an error object, a
        # proxy's HTML error page that happened to decode, a schema change in a
        # future pinned version.
        raise ScanDidNotRun(
            f"{path} contains no govulncheck `config` message - the scan did "
            "not run to completion, so its findings cannot be trusted"
        )
    return ids


# A baseline entry must look like a real advisory ID. A typo silently becomes
# an entry that matches nothing, which quietly re-arms the gate for a
# vulnerability someone believed they had accepted - failing loudly on the
# typo is the only way that stays visible.
VULN_ID_RE = re.compile(r"^GO-\d{4}-\d{4,}$")


def allowlisted_ids(path):
    ids = set()
    try:
        with open(path, encoding="utf-8") as handle:
            lines = handle.readlines()
    except OSError as err:
        # A missing baseline is not an empty baseline. Treating it as empty
        # would fail the build with all 24 findings, which looks like a
        # detection but is really a misconfiguration.
        raise ScanDidNotRun(f"cannot read the baseline {path}: {err}") from err

    for number, line in enumerate(lines, start=1):
        entry = line.split("#", 1)[0].strip()
        if not entry:
            continue
        if not VULN_ID_RE.match(entry):
            raise ScanDidNotRun(
                f"{path}:{number}: {entry!r} is not a GO-YYYY-NNNN advisory ID"
            )
        ids.add(entry)
    return ids


def main():
    scan_path, allowlist_path = sys.argv[1], sys.argv[2]
    try:
        found = called_vuln_ids(scan_path)
        allowed = allowlisted_ids(allowlist_path)
    except ScanDidNotRun as err:
        print("### govulncheck")
        print()
        print(f"**FAILED — the scan did not produce a usable result:** {err}")
        print()
        print("This is NOT a clean result. Nothing was checked. Read the scan "
              "step's log; a passing build here would mean the gate silently "
              "stopped gating.")
        print(f"::error::govulncheck gate could not verify anything: {err}",
              file=sys.stderr)
        return 1

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
