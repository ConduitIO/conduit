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
  * a `replace` of a listed module to another module path or a local path
    fails, because the version in go.mod then says nothing about the code that
    ships; waive it with a line `waive-replace | <module> | <reason>`;
  * a committed `go.work` is treated like go.mod: its `replace` lines are
    layered over go.mod's, and a `use` of anything but the root fails unless
    waived with `waive-replace | go.work | <reason>`, because a workspace
    builds local code whatever the versions say;
  * a patch release (vX.Y.Z with Z > 0) MUST have at least one entry, and at
    least one entry must be NEW in this release: its commit is not already in
    the previous stable tag (for `conduit`) or in the version of that module
    pinned at the previous tag. A list of old commits does not count;
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
    """Return (entries, waivers). entries are (module, commit, note);
    waivers maps module to the reason from `waive-replace | module | reason`."""
    out, waivers = [], {}
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = [p.strip() for p in line.split("|")]
        if parts[0] == "waive-replace":
            if len(parts) < 3 or not parts[1] or not parts[2]:
                raise GateError(f"line {n}: want `waive-replace | module | reason`")
            waivers[parts[1]] = parts[2]
            continue
        if len(parts) < 2 or not parts[0] or not re.fullmatch(r"[0-9a-f]{7,40}", parts[1]):
            raise GateError(f"line {n}: want `module | commit | note` with a hex commit, got {raw!r}")
        out.append((parts[0], parts[1], parts[2] if len(parts) > 2 else ""))
    return out, waivers


_REQ = re.compile(r"^\s*(?:require\s+)?(\S+)\s+(v\d\S*)")
_REPL = re.compile(r"^\s*(?:replace\s+)?(\S+)(?:\s+v\S+)?\s*=>\s*(\S+)(?:\s+(v\S+))?")


def gomod_versions(text):
    """(requires, replaces) from go.mod. Only lines in a require directive
    set a version; replace lines, in either form, never overwrite one."""
    reqs, repls = {}, {}
    block = None
    for raw in text.splitlines():
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if block:
            if line == ")":
                block = None
            elif block == "require":
                m = _REQ.match(line)
                if m:
                    reqs[m.group(1)] = m.group(2)
            elif block == "replace":
                m = _REPL.match(line)
                if m:
                    repls[m.group(1)] = (m.group(2), m.group(3))
            continue
        m = re.match(r"^(require|replace|exclude|retract|tool|godebug|ignore)\s*\($", line)
        if m:
            block = m.group(1)
        elif line.startswith("require "):
            m = _REQ.match(line)
            if m:
                reqs[m.group(1)] = m.group(2)
        elif line.startswith("replace "):
            m = _REPL.match(line)
            if m:
                repls[m.group(1)] = (m.group(2), m.group(3))
    return reqs, repls


def workspace_uses(text):
    """`use` paths of a go.work, single and block form."""
    out, block = [], False
    for raw in text.splitlines():
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if block:
            if line == ")":
                block = False
            else:
                out.append(line)
        elif re.match(r"^use\s*\($", line):
            block = True
        elif line.startswith("use "):
            out.append(line[4:].strip())
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


_SEMVER = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


def previous_stable_tag(tag, repo="."):
    """Highest stable vX.Y.Z tag in repo strictly below `tag` (by semver), or ''."""
    rc, out = git("tag", "--list", "v[0-9]*", cwd=repo)
    if rc != 0:
        raise GateError(f"cannot list tags: {out}")
    m = TAG.match(tag)
    cur = (int(m.group(1)), int(m.group(2)), int(m.group(3)))
    best = None
    for t in out.split():
        mm = _SEMVER.match(t)
        if mm:
            k = (int(mm.group(1)), int(mm.group(2)), int(mm.group(3)))
            if k < cur and (best is None or k > best[0]):
                best = (k, t)
    return best[1] if best else ""


def resolve_ref(version):
    pm = PSEUDO.search(version)
    return pm.group(1) if pm else version


def check(entries, versions, replaces, waivers, remote_base, work, prev_tag="", prev_versions=None,
          self_repo="."):
    """Return (problems, new_count). Problems empty means the fixes are all in;
    new_count is how many entries are not already in the previous release."""
    problems, new = [], 0
    clones = {}

    def clone(name):
        if name not in clones:
            dest = os.path.join(work, name)
            rc, out = git("clone", "--bare", "--quiet", f"{remote_base}/{name}", dest)
            if rc != 0:
                raise GateError(f"cannot clone {name}: {out}")
            clones[name] = dest
        return clones[name]

    for module, commit, note in entries:
        try:
            if module == SELF:
                ok, where = is_ancestor(self_repo, commit, "HEAD"), "this release"
                if ok and prev_tag and not is_ancestor(self_repo, commit, prev_tag):
                    new += 1
            else:
                if module not in versions:
                    raise GateError(f"{module} is not in go.mod")
                target = replaces.get(module)
                version = versions[module]
                if target:
                    tpath, tver = target
                    if tpath == module and tver:
                        version = tver
                    elif module not in waivers:
                        raise GateError(f"go.mod replaces {module} with {tpath}; the version says nothing about "
                                        f"the code that ships (waive with `waive-replace | {module} | reason`)")
                name = module.rstrip("/").split("/")[-1]
                repo = clone(name)
                ok, where = is_ancestor(repo, commit, resolve_ref(version)), f"{name} {version}"
                if ok:
                    pv = (prev_versions or {}).get(module)
                    if not pv or not is_ancestor(repo, commit, resolve_ref(pv)):
                        new += 1
        except GateError as e:
            problems.append(f"{module} {commit}: {e}")
            continue
        if not ok:
            problems.append(f"{module} {commit} ({note or 'no note'}) is NOT in {where}")
    return problems, new


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("tag")
    ap.add_argument("--file", default=".github/release-fixes.txt")
    ap.add_argument("--gomod", default="go.mod")
    ap.add_argument("--gowork", default="go.work", help="committed go.work, if any")
    ap.add_argument("--remote-base", default="https://github.com/ConduitIO")
    a = ap.parse_args(argv)
    try:
        patch = is_patch(a.tag)
        try:
            with open(a.file) as f:
                entries, waivers = parse_fixes(f.read())
        except FileNotFoundError:
            entries, waivers = [], {}
            if patch:
                raise GateError(f"{a.file} is missing: a patch release must list the fixes it carries")
        if patch and not entries:
            raise GateError(f"{a.file} has no entries: a patch release must list the fixes it carries")
        if not entries:
            print(f"release fixes: no fixes declared for {a.tag}; nothing to check.")
            return 0
        with open(a.gomod) as f:
            versions, replaces = gomod_versions(f.read())
        if os.path.exists(a.gowork):
            with open(a.gowork) as f:
                work = f.read()
            _, work_repls = gomod_versions(work)
            replaces = {**replaces, **work_repls}
            extra_uses = [u for u in workspace_uses(work) if u.rstrip("/") not in (".", "")]
            if extra_uses and "go.work" not in waivers:
                raise GateError(f"{a.gowork} uses {extra_uses}: a workspace builds local code whatever go.mod says "
                                "(waive with `waive-replace | go.work | reason`)")
        prev_tag, prev_versions = "", {}
        if patch:
            prev_tag = previous_stable_tag(a.tag)
            if not prev_tag:
                raise GateError(f"no previous stable tag below {a.tag}; cannot tell which fixes are new")
            rc, out = git("show", f"{prev_tag}:go.mod")
            if rc != 0:
                raise GateError(f"cannot read go.mod at {prev_tag}: {out}")
            prev_versions, _ = gomod_versions(out)
        with tempfile.TemporaryDirectory() as work:
            problems, new = check(entries, versions, replaces, waivers, a.remote_base, work, prev_tag, prev_versions)
        if patch and not problems and new == 0:
            problems.append(f"no listed fix is new since {prev_tag}: every commit is already in the previous release")
    except (OSError, GateError) as e:
        print(f"release fixes: FAILED: {e}")
        return 1
    if problems:
        print(f"release fixes: FAILED for {a.tag}:")
        for p in problems:
            print(f"  - {p}")
        return 1
    print(f"release fixes: all {len(entries)} listed fixes are in {a.tag}" + (f" ({new} new since {prev_tag})." if patch else "."))
    return 0


if __name__ == "__main__":
    sys.exit(main())
