#!/usr/bin/env python3
"""Fail when a PR lowers a module version in go.mod relative to its base.

A version that goes DOWN in a PR is almost always a stale rebase reverting
somebody else's fix (conduit#3192 reverted #3208 this way), so the gate is
strict where reverting hurts most:

  * any DIRECT requirement (no `// indirect`), and
  * any of the protected modules, direct or not: conduit-connector-sdk,
    conduit-commons, conduit-processor-sdk, conduit-connector-protocol and
    every conduit-connector-* (the built-in connectors).

Indirect requirements outside that list are ignored: `go mod tidy` moves them
for reasons that are not the author's decision.

A deliberate downgrade says so in the PR body with a line
`deps-downgrade: <reason>`. An empty reason does not count. The reason is
printed in the job summary so it is reviewed, not skipped.

FAILS CLOSED: an unreadable or unparsable go.mod on either side is an error,
never "no downgrade".

Usage: gomod_downgrade_guard.py BASE_GOMOD HEAD_GOMOD [--body-file FILE]
"""

import argparse
import re
import sys

PROTECTED_EXACT = {
    "github.com/conduitio/conduit-connector-sdk",
    "github.com/conduitio/conduit-commons",
    "github.com/conduitio/conduit-processor-sdk",
    "github.com/conduitio/conduit-connector-protocol",
}
PROTECTED_PREFIX = "github.com/conduitio/conduit-connector-"

_REQ = re.compile(r"^\s*(?:require\s+)?(\S+)\s+(v\S+?)(\s*//\s*indirect)?\s*(?://.*)?$")
_SEMVER = re.compile(r"^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.\-]+))?(?:\+[0-9A-Za-z.\-]+)?$")
_WAIVER = re.compile(r"(?mi)^[ \t>*-]*deps-downgrade:[ \t]*(\S.*)$")


class GateError(Exception):
    """An input cannot be trusted."""


def is_protected(path):
    return path in PROTECTED_EXACT or path.startswith(PROTECTED_PREFIX)


def parse_gomod(text):
    """Return {module: (version, indirect)} for every requirement."""
    out = {}
    in_block = False
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("//"):
            continue
        if in_block:
            if line == ")":
                in_block = False
                continue
            m = _REQ.match(raw)
            if not m:
                raise GateError(f"unparsable require line: {raw!r}")
            out[m.group(1)] = (m.group(2), bool(m.group(3)))
            continue
        if re.match(r"^require\s*\($", line):
            in_block = True
        elif line.startswith("require "):
            m = _REQ.match(raw)
            if not m:
                raise GateError(f"unparsable require line: {raw!r}")
            out[m.group(1)] = (m.group(2), bool(m.group(3)))
    if in_block:
        raise GateError("unterminated require block")
    if not out:
        raise GateError("no requirements found; refusing to compare")
    return out


def _pre_key(pre):
    # Semver 2.0 precedence: numeric identifiers < alphanumeric; no pre-release
    # sorts after any pre-release. Pseudo-versions compare by timestamp.
    if pre is None:
        return (1,)
    parts = []
    for ident in pre.replace("-", ".").split("."):
        parts.append((0, int(ident), "") if ident.isdigit() else (1, 0, ident))
    return (0, tuple(parts))


def version_key(v):
    m = _SEMVER.match(v)
    if not m:
        raise GateError(f"not a semantic version: {v!r}")
    return (int(m.group(1)), int(m.group(2)), int(m.group(3)), _pre_key(m.group(4)))


def downgrades(base, head):
    """Modules whose version is lower in head than in base, restricted to the
    gated set. Returns a sorted list of (module, base_version, head_version)."""
    out = []
    for mod, (hv, h_indirect) in head.items():
        if mod not in base:
            continue
        bv, b_indirect = base[mod]
        gated = is_protected(mod) or not h_indirect or not b_indirect
        if not gated:
            continue
        if version_key(hv) < version_key(bv):
            out.append((mod, bv, hv))
    return sorted(out)


def waiver(body):
    m = _WAIVER.search(body or "")
    return m.group(1).strip() if m else None


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("head")
    ap.add_argument("--body-file")
    a = ap.parse_args(argv)
    try:
        with open(a.base) as f:
            base = parse_gomod(f.read())
        with open(a.head) as f:
            head = parse_gomod(f.read())
        body = ""
        if a.body_file:
            with open(a.body_file) as f:
                body = f.read()
        bad = downgrades(base, head)
    except (OSError, GateError) as e:
        print(f"go.mod downgrade guard: cannot decide: {e}")
        return 2
    if not bad:
        print("go.mod downgrade guard: no gated module version went down.")
        return 0
    lines = ["| module | base | head |", "| --- | --- | --- |"]
    lines += [f"| `{m}` | {b} | {h} |" for m, b, h in bad]
    reason = waiver(body)
    if reason:
        print("go.mod downgrade guard: downgrade waived in the PR body.\n")
        print("\n".join(lines))
        print(f"\n`deps-downgrade: {reason}`")
        return 0
    print("go.mod downgrade guard: FAILED. These versions are lower than on the base branch:\n")
    print("\n".join(lines))
    print(
        "\nIf this PR was rebased, a stale go.mod is reverting someone else's fix: rebase onto the base branch and "
        "keep its versions.\nIf the downgrade is intended, add a line `deps-downgrade: <reason>` to the PR body."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
