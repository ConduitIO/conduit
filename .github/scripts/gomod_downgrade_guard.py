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

The same rules apply to every go.mod in the repository (root, `tools/`, any
nested module) and to `replace` directives: a replace that pins a gated module
to a LOWER version counts as a downgrade, and a replace that points a gated
module at a fork or a local path (added or changed against the base) needs the
waiver too, because it swaps the code without touching the version.

A committed `go.work` is part of the same check: its `replace` lines override
go.mod's for every module, and a `use` directive that appears or changes swaps
in local code, so any difference in `go.work` against the base needs the waiver.

A deliberate downgrade says so in the PR body with a line
`deps-downgrade: <reason>`. An empty reason does not count. The reason is
printed in the job summary so it is reviewed, not skipped.

FAILS CLOSED: an unreadable or unparsable go.mod on either side is an error,
never "no downgrade".

Usage: gomod_downgrade_guard.py --base REV --head REV [--body-file FILE]
       (compares every go.mod that exists at both revisions; REV is a git
       revision; for a push use --base <previous tip> --head HEAD and put the
       merged PRs' bodies and the commit messages in --body-file)
"""

import argparse
import re
import subprocess
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
_HTML_COMMENT = re.compile(r"<!--.*?-->", re.S)
_REPL = re.compile(r"^\s*(?:replace\s+)?(\S+)(?:\s+(v\S+))?\s*=>\s*(\S+)(?:\s+(v\S+))?\s*(?://.*)?$")


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


def parse_replaces(text):
    """Return {module: (target, target_version_or_None)} from replace
    directives (single-line and block form)."""
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
        elif re.match(r"^replace\s*\($", line):
            in_block = True
            continue
        elif not line.startswith("replace "):
            continue
        m = _REPL.match(raw)
        if not m:
            raise GateError(f"unparsable replace line: {raw!r}")
        out[m.group(1)] = (m.group(3), m.group(4))
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


def effective(reqs, repls, mod):
    """(version, replaced_by) in force for mod. A same-path replace with a
    version overrides the require version; a replace to another path or a
    local path keeps the require version and reports the target."""
    ver = reqs[mod][0]
    if mod in repls:
        target, tver = repls[mod]
        if target == mod and tver:
            return tver, None
        if target != mod:
            return ver, target if not tver else f"{target} {tver}"
    return ver, None


def downgrades(base, head, base_repl=None, head_repl=None):
    """Problems in head relative to base for the gated set. Returns a sorted
    list of (module, base_desc, head_desc): a lower effective version, or a
    replace to another module/path that is new or changed."""
    base_repl, head_repl = base_repl or {}, head_repl or {}
    out = []
    for mod, (_, h_indirect) in head.items():
        if mod not in base:
            continue
        b_indirect = base[mod][1]
        if not (is_protected(mod) or not h_indirect or not b_indirect):
            continue
        bv, brep = effective(base, base_repl, mod)
        hv, hrep = effective(head, head_repl, mod)
        if version_key(hv) < version_key(bv):
            out.append((mod, bv, hv))
        elif hrep and hrep != brep:
            out.append((mod, bv + (f" => {brep}" if brep else ""), f"{hv} => {hrep}"))
    return sorted(out)


def waiver(body):
    # HTML comments are template text; a waiver inside one is not a waiver.
    m = _WAIVER.search(_HTML_COMMENT.sub("", body or ""))
    return m.group(1).strip() if m else None


def git(*args):
    r = subprocess.run(["git", *args], capture_output=True, text=True)
    if r.returncode != 0:
        raise GateError(f"git {' '.join(args)} failed: {r.stderr.strip()[:200]}")
    return r.stdout


def gomod_files(rev):
    return {p for p in git("ls-tree", "-r", "--name-only", rev).splitlines() if p == "go.mod" or p.endswith("/go.mod")}


def read_gowork(rev):
    """The committed go.work at rev, or None."""
    r = subprocess.run(["git", "show", f"{rev}:go.work"], capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else None


def gowork_uses(text):
    """The set of `use` paths in a go.work (single and block form)."""
    out, block = set(), False
    for raw in (text or "").splitlines():
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if block:
            if line == ")":
                block = False
            else:
                out.add(line)
        elif re.match(r"^use\s*\($", line):
            block = True
        elif line.startswith("use "):
            out.add(line[4:].strip())
    return out


def compare(base_rev, head_rev):
    """Problems across every go.mod present at both revisions, with a
    committed go.work's replace lines layered over each go.mod's own."""
    base_files, head_files = gomod_files(base_rev), gomod_files(head_rev)
    if "go.mod" not in base_files or "go.mod" not in head_files:
        raise GateError("go.mod is missing at the base or the head")
    bw, hw = read_gowork(base_rev), read_gowork(head_rev)
    bad = []
    if hw is not None:
        # A workspace swaps local modules in without touching any version.
        if gowork_uses(hw) != gowork_uses(bw):
            bad.append(("go.work", "(use)", ", ".join(sorted(gowork_uses(bw))) or "none", ", ".join(sorted(gowork_uses(hw)))))
    brep_w = parse_replaces(bw) if bw is not None else {}
    hrep_w = parse_replaces(hw) if hw is not None else {}
    for path in sorted(base_files & head_files):
        bt, ht = git("show", f"{base_rev}:{path}"), git("show", f"{head_rev}:{path}")
        brep = {**parse_replaces(bt), **brep_w}
        hrep = {**parse_replaces(ht), **hrep_w}
        for mod, b, h in downgrades(parse_gomod(bt), parse_gomod(ht), brep, hrep):
            bad.append((path, mod, b, h))
    return bad


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--head", required=True)
    ap.add_argument("--body-file")
    a = ap.parse_args(argv)
    try:
        body = ""
        if a.body_file:
            with open(a.body_file) as f:
                body = f.read()
        bad = compare(a.base, a.head)
    except (OSError, GateError) as e:
        print(f"go.mod downgrade guard: cannot decide: {e}")
        return 2
    if not bad:
        print("go.mod downgrade guard: no gated module version went down.")
        return 0
    lines = ["| go.mod | module | base | head |", "| --- | --- | --- | --- |"]
    lines += [f"| `{p}` | `{m}` | {b} | {h} |" for p, m, b, h in bad]
    reason = waiver(body)
    if reason:
        print("go.mod downgrade guard: downgrade waived.\n")
        print("\n".join(lines))
        print(f"\n`deps-downgrade: {reason}`")
        return 0
    print("go.mod downgrade guard: FAILED. These versions are lower than on the base, or a replace swapped the code:\n")
    print("\n".join(lines))
    print(
        "\nIf this PR was rebased, a stale go.mod is reverting someone else's fix: rebase onto the base branch and "
        "keep its versions.\nIf the change is intended, add a line `deps-downgrade: <reason>` to the PR body "
        "(for a push: to the body of the PR that was merged)."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
