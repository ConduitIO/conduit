#!/usr/bin/env python3
"""Fail when `main`'s release notes disagree with what is published.

WHY: the tag-time gate (release_notes_check.py) already refuses a tag whose
`docs/releases/vX.Y.Z.md` is still marked "draft, not yet released". It checks
the TAGGED tree, which is the right thing to check at a tag. It cannot see what
happens to `main` afterwards.

v0.22.0 is how that goes wrong. The finalise commit was tagged, v0.22.0 shipped
on 2026-11-25, and the commit never landed on `main` — so for two days `main`
said:

    # Conduit v0.22.0 (draft, not yet released)

about a release people were already running, and nothing noticed. Anyone
reading the notes in the repository, or generating anything from them, got
"not yet released" for the current release.

So this asks the opposite question, daily, from the other direction: for every
release GitHub has published, does `main` still describe it as unreleased, or
has it lost notes the tag has? Both inputs come in as arguments rather than
being gathered here, so the check is testable without the network or a git
object database, and the workflow owns the API call and the `git cat-file`.

The second rule is deliberately narrow. "Every published release must have a
notes file" would fail on 29 releases from v0.1.1 to v0.20.5, which predate the
hand-written-notes convention (postmortem item 12) and have no notes at their
own tags either. A check that reports 29 problems on its first run gets ignored
by its third, so it only asks about releases whose notes EXIST at their tag and
are missing here -- which is exactly the v0.20.7 case, where the notes were
written on `release/v0.20.x` and never landed on `main`.

The DRAFT marker is imported from release_notes_check so the two gates cannot
drift on what a draft looks like.

Usage: release_docs_drift.py --released v0.22.0,... [--tagged-notes v0.22.0,...]
                             [--dir docs/releases]
Exits 0 when `main` agrees with the published releases, 1 when it does not, and
2 when it could not tell (fails closed).
"""

import argparse
import os
import re
import sys

from release_notes_check import DRAFT

STABLE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


class DriftError(Exception):
    pass


def stable_versions(values):
    """The stable releases among `values`, as a sorted list of version strings.

    A pre-release (`v0.23.0-nightly.20261127`) is dropped: nightlies have no
    notes file and are not what this is about.
    """
    out = []
    for v in values:
        v = v.strip()
        if STABLE.match(v):
            out.append(v)
    return sorted(set(out), key=lambda v: tuple(int(x) for x in STABLE.match(v).groups()))


def drift(released, notes_dir="docs/releases", tagged_notes=()):
    """Problems with this branch's notes for the given published releases.

    `tagged_notes` is the subset of releases whose notes file exists at its own
    tag; only those are required to have one here. Returns a list of strings,
    empty when there are none. Raises DriftError when it cannot answer, so an
    unreadable tree is never reported as clean.
    """
    if not os.path.isdir(notes_dir):
        raise DriftError(f"{notes_dir} is not a directory")
    versions = stable_versions(released)
    if not versions:
        raise DriftError("no stable release was given, so there is nothing to check against")
    at_tag = set(stable_versions(tagged_notes))

    problems = []
    for v in versions:
        path = os.path.join(notes_dir, f"{v}.md")
        if not os.path.exists(path):
            if v in at_tag:
                problems.append(
                    f"{v} is published and has notes at its tag, but {path} is missing here. "
                    f"They were probably written on a release branch and never landed: "
                    f"`git checkout {v} -- {path}`")
            continue
        try:
            with open(path) as f:
                text = f.read()
        except OSError as e:
            raise DriftError(f"cannot read {path}: {e}") from e
        if DRAFT.search(text):
            problems.append(
                f'{path} is still marked "draft, not yet released", but {v} is published. '
                f"The finalised notes were probably tagged without landing on this branch: "
                f"`git checkout {v} -- {path}`")
    return problems


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--released", required=True,
                    help="comma- or whitespace-separated published tags (e.g. from `gh release list`)")
    ap.add_argument("--tagged-notes", default="",
                    help="published tags whose notes file exists at the tag; only these are "
                         "required to have one on this branch")
    ap.add_argument("--dir", default="docs/releases")
    a = ap.parse_args(argv)
    try:
        problems = drift(re.split(r"[,\s]+", a.released), a.dir,
                         re.split(r"[,\s]+", a.tagged_notes))
    except DriftError as e:
        print(f"cannot check the release notes: {e}", file=sys.stderr)
        return 2
    for p in problems:
        print(f"::error::{p}")
    if problems:
        print(f"{len(problems)} release(s) described wrongly on this branch")
        return 1
    print("every published release's notes on this branch agree that it is released")
    return 0


if __name__ == "__main__":
    sys.exit(main())
