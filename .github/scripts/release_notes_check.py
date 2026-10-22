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

And, if `.github/release-fixes.txt` lists fixes, each one must be NAMED in the
Fixed section: by its commit prefix (the first 7 hex characters, as a whole
token), or by its note as a whole phrase of at least 12 characters. A raw
substring match does not count (a short note such as "slot" would match any
sentence). This keeps the notes and the gate in release_fixes.py describing the
same set.

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


def strip_comments(text):
    """Drop HTML comments (multi-line; an unclosed one runs to the end) so a
    hidden draft marker, Fixed section or prefix cannot satisfy or dodge a rule."""
    return re.sub(r"<!--.*?-->", "", text, flags=re.S).split("<!--", 1)[0]


def fixed_section(text):
    m = re.search(r"(?ms)^##\s+Fixed\s*$(.*?)(?=^##\s|\Z)", text)
    return m.group(1) if m else None


def names_fix(fixed, commit, note):
    """True if the Fixed section names the fix by commit prefix or by a
    whole-word note of at least 12 characters."""
    if re.search(r"(?<!\w)" + re.escape(commit[:7]) + r"[0-9a-f]*(?!\w)", fixed, re.I):
        return True
    note = (note or "").strip()
    return len(note) >= 12 and re.search(r"(?<!\w)" + re.escape(note) + r"(?!\w)", fixed, re.I) is not None


def problems_for(text, fixes):
    text = strip_comments(text)
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
            if not names_fix(fixed, commit, note):
                out.append(f"listed fix {commit[:7]} ({module}) is not named in the Fixed section "
                           "(use its 7-character commit prefix, or its note as a whole phrase of 12+ characters)")
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
                fixes, _ = release_fixes.parse_fixes(f.read())
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
