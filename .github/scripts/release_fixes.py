#!/usr/bin/env python3
"""Fail a release tag that does not contain the fixes it was meant to carry.

Postmortem action item 10 (root cause: a release was cut from what had merged,
not from a list of what had to be in it; v0.20.3 shipped without four fixes
its plan named).

`.github/release-fixes.txt` on the release branch lists, one per line:

    module | commit | note

`module` is a Go module path (resolved to its version in go.mod at this
commit) or `conduit` for this repository. `commit` is a SHA of at least 7 hex
digits. The gate checks that each commit is an ANCESTOR of the resolved module
tag (or of HEAD for `conduit`), so a fix that was merged to a branch but never
tagged into the version this release pins is caught.

Rules:
  * a patch release (vX.Y.Z with Z > 0) MUST have at least one entry; a patch
    with no declared fixes is an error, not a no-op;
  * a non-patch release validates the file only if it exists;
  * an unresolvable module, tag or commit fails (fail closed: a lookup that
    could not run never reads as "the fix is in").

Usage: release_fixes.py TAG [--file F] [--gomod go.mod] [--remote-base URL]
"""

import argparse
import os
import re
import subprocess
import sys
import tempfile

PSEUDO = re.compile(r"-\d{14}-([0-9a-f]{12})$")
TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.\-]+)?$")
SELF = "conduit"


class GateError(Exception):
    pass


def is_patch(tag):
    m = TAG.match(tag)
    if not m:
        raise GateError(f"not a release tag: {tag!r}")
    return int(m.group(3)) > 0


def parse_fixes(text):
    out = []
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = [p.strip() for p in line.split("|")]
        if len(parts) < 2 or not parts[0] or not re.fullmatch(r"[0-9a-f]{7,40}", parts[1]):
            raise GateError(f"line {n}: want `module | commit | note` with a hex commit, got {raw!r}")
        out.append((parts[0], parts[1], parts[2] if len(parts) > 2 else ""))
    return out


def gomod_versions(text):
    out = {}
    for m in re.finditer(r"^\s*(?:require\s+)?(\S+)\s+(v\d\S*)", text, re.M):
        out[m.group(1)] = m.group(2)
    return out


def git(*args, cwd=None):
    r = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    return r.returncode, (r.stdout + r.stderr).strip()


def is_ancestor(repo, commit, ref):
    rc, _ = git("cat-file", "-e", f"{commit}^{{commit}}", cwd=repo)
    if rc != 0:
        raise GateError(f"commit {commit} does not exist in {repo}")
    rc, _ = git("cat-file", "-e", f"{ref}^{{commit}}", cwd=repo)
    if rc != 0:
        raise GateError(f"ref {ref} does not exist in {repo}")
    rc, out = git("merge-base", "--is-ancestor", commit, ref, cwd=repo)
    if rc not in (0, 1):
        raise GateError(f"git merge-base failed: {out}")
    return rc == 0


def check(entries, versions, remote_base, work, self_repo="."):
    """Return a list of human-readable problems; empty means the gate passes."""
    problems = []
    clones = {}
    for module, commit, note in entries:
        try:
            if module == SELF:
                ok, where = is_ancestor(self_repo, commit, "HEAD"), "this release"
            else:
                if module not in versions:
                    raise GateError(f"{module} is not in go.mod")
                version = versions[module]
                name = module.rstrip("/").split("/")[-1]
                if name not in clones:
                    dest = os.path.join(work, name)
                    rc, out = git("clone", "--bare", "--quiet", f"{remote_base}/{name}", dest)
                    if rc != 0:
                        raise GateError(f"cannot clone {name}: {out}")
                    clones[name] = dest
                pm = PSEUDO.search(version)
                ref = pm.group(1) if pm else version
                ok, where = is_ancestor(clones[name], commit, ref), f"{name} {version}"
        except GateError as e:
            problems.append(f"{module} {commit}: {e}")
            continue
        if not ok:
            problems.append(f"{module} {commit} ({note or 'no note'}) is NOT in {where}")
    return problems


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("tag")
    ap.add_argument("--file", default=".github/release-fixes.txt")
    ap.add_argument("--gomod", default="go.mod")
    ap.add_argument("--remote-base", default="https://github.com/ConduitIO")
    a = ap.parse_args(argv)
    try:
        patch = is_patch(a.tag)
        try:
            with open(a.file) as f:
                entries = parse_fixes(f.read())
        except FileNotFoundError:
            entries = []
            if patch:
                raise GateError(f"{a.file} is missing: a patch release must list the fixes it carries")
        if patch and not entries:
            raise GateError(f"{a.file} has no entries: a patch release must list the fixes it carries")
        if not entries:
            print(f"release fixes: no fixes declared for {a.tag}; nothing to check.")
            return 0
        with open(a.gomod) as f:
            versions = gomod_versions(f.read())
        with tempfile.TemporaryDirectory() as work:
            problems = check(entries, versions, a.remote_base, work)
    except (OSError, GateError) as e:
        print(f"release fixes: FAILED: {e}")
        return 1
    if problems:
        print(f"release fixes: FAILED for {a.tag}:")
        for p in problems:
            print(f"  - {p}")
        return 1
    print(f"release fixes: all {len(entries)} listed fixes are in {a.tag}.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
