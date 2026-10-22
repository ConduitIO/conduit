#!/usr/bin/env python3
"""Fail a release whose notes are only the generated changelog.

Postmortem action item 12. The GitHub release body is generated from
conventional commits; for a patch release that is exactly what nobody should
have to read. A stable release tag vX.Y.Z (Z > 0) must have a hand-written
notes file `docs/releases/vX.Y.Z.md` (docs/releases.md, "Release notes") in the
tagged commit that:

  * exists and is not a draft (no "draft, not yet released" marker),
  * has a `## Fixed` section with at least one bullet,
  * has prose beyond bullets: at least one paragraph line that is not a list
    item or heading, and
  * is not a changelog dump: it fails when most bullets start with a
    conventional-commit prefix (`fix:`, `feat(scope):`) or end in a PR link
    pattern like `by @user in #123`, which is the shape GoReleaser generates.

And, if `.github/release-fixes.txt` lists fixes, every listed commit's first
7 characters or its note must appear in the Fixed section, so the notes and the
gate in release_fixes.py describe the same set. A fix that is in the tag but
not in the notes, or the reverse, fails.

Minor and pre-release tags are not checked (their notes are reviewed as a
document). FAILS CLOSED: an unreadable file is a failure.

Usage: release_notes_check.py TAG [--dir docs/releases] [--fixes .github/release-fixes.txt]
"""

import argparse
import os
import re
import sys

import release_fixes

CONVENTIONAL = re.compile(r"^[-*]\s*(feat|fix|docs|refactor|perf|test|chore|ci|build|deps)(\([^)]*\))?!?:\s", re.I)
PR_TRAILER = re.compile(r"\bby @\S+ in (#\d+|https://\S+)")
DRAFT = re.compile(r"(?i)draft,?\s+not\s+yet\s+released")


class GateError(Exception):
    pass


def fixed_section(text):
    m = re.search(r"(?ms)^##\s+Fixed\s*$(.*?)(?=^##\s|\Z)", text)
    return m.group(1) if m else None


def problems_for(text, fixes):
    out = []
    if DRAFT.search(text):
        out.append('still marked "draft, not yet released"')
    fixed = fixed_section(text)
    bullets = []
    if fixed is None:
        out.append("no `## Fixed` section")
    else:
        bullets = [l for l in fixed.splitlines() if re.match(r"^\s*[-*]\s+\S", l)]
        if not bullets:
            out.append("the `## Fixed` section has no bullets")
    prose = [
        l for l in text.splitlines()
        if l.strip() and not re.match(r"^\s*([-*#>|]|\d+\.)", l) and not l.startswith("```")
    ]
    if not prose:
        out.append("no prose: the notes are only lists and headings")
    all_bullets = [l for l in text.splitlines() if re.match(r"^\s*[-*]\s+\S", l)]
    gen = [l for l in all_bullets if CONVENTIONAL.match(l.strip()) or PR_TRAILER.search(l)]
    if all_bullets and len(gen) * 2 > len(all_bullets):
        out.append(f"{len(gen)} of {len(all_bullets)} bullets look like generated changelog lines")
    if fixed is not None and fixes:
        for module, commit, note in fixes:
            if commit[:7] not in fixed and not (note and note in fixed):
                out.append(f"listed fix {commit[:7]} ({module}) is not in the Fixed section")
    return out


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("tag")
    ap.add_argument("--dir", default="docs/releases")
    ap.add_argument("--fixes", default=".github/release-fixes.txt")
    a = ap.parse_args(argv)
    try:
        m = release_fixes.TAG.match(a.tag)
        if not m:
            raise GateError(f"not a release tag: {a.tag!r}")
        if m.group(4) or int(m.group(3)) == 0:
            print(f"release notes: {a.tag} is not a stable patch release; not checked here.")
            return 0
        path = os.path.join(a.dir, f"{a.tag}.md")
        try:
            with open(path) as f:
                text = f.read()
        except OSError as e:
            raise GateError(f"{path} is missing or unreadable ({e.strerror}): a patch release needs hand-written notes")
        fixes = []
        if os.path.exists(a.fixes):
            with open(a.fixes) as f:
                fixes = release_fixes.parse_fixes(f.read())
        problems = problems_for(text, fixes)
    except (GateError, release_fixes.GateError) as e:
        print(f"release notes: FAILED: {e}")
        return 1
    if problems:
        print(f"release notes: FAILED for {a.tag} ({path}):")
        for p in problems:
            print(f"  - {p}")
        return 1
    print(f"release notes: {path} is hand-written and matches the declared fixes.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
