#!/usr/bin/env python3
"""Bump the pinned ghcr.io/conduitio/conduit:vX.Y.Z image tag in the deploy examples.

Used by .github/workflows/bump-image-pin.yml after a stable release is published
and its image has been verified. Rewrites every pin under the target paths to the
given tag and prints the files it changed, one per line, on stdout.

Rules, each one deliberate:

  * STABLE TAGS ONLY. The tag must be exactly vX.Y.Z. Nightlies and prereleases
    (-nightly.*, -rc.*) are refused, so an example can never pin a build that is
    not a release.
  * NEVER DOWNGRADE. A pin is only rewritten to a strictly newer version. A patch
    release on an older line (v0.20.2 published after main pins v0.21.0) leaves
    main alone instead of moving the examples backwards.
  * ONLY THE IMAGE PIN. The pattern is the full image reference with a version
    tag. `:latest`, a digest pin, and bare version strings in prose are not
    touched, and docs/releases.md (whose version numbers are illustrative
    cosign examples) is not in the target set.
  * --dry-run changes nothing and still prints what would change.
"""

import argparse
import os
import re
import sys

IMAGE = "ghcr.io/conduitio/conduit"
# The tag must end at a non-version character, so v0.20.0 never matches the
# prefix of v0.20.01 or v0.20.0-nightly.
PIN_RE = re.compile(re.escape(IMAGE) + r":v(\d+)\.(\d+)\.(\d+)(?![\w.-])")
TAG_RE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")

# Where the examples pin the image. Directories are walked; README.md is the
# repo README's quickstart section. Add a path here when a new doc pins the image.
TARGETS = ["deploy", "README.md"]
TEXT_SUFFIXES = {".md", ".yaml", ".yml", ".json", ".hcl", ".service", ".example", ".sh", ".txt"}


class BumpError(Exception):
    pass


def parse_tag(tag):
    match = TAG_RE.match(tag)
    if not match:
        raise BumpError(
            f"{tag!r} is not a stable release tag (want vX.Y.Z); "
            "nightlies and prereleases are never pinned")
    return tuple(int(part) for part in match.groups())


def iter_files(root):
    for target in TARGETS:
        path = os.path.join(root, target)
        if os.path.isfile(path):
            yield path
        elif os.path.isdir(path):
            for dirpath, _, names in os.walk(path):
                for name in sorted(names):
                    full = os.path.join(dirpath, name)
                    if os.path.splitext(name)[1] in TEXT_SUFFIXES:
                        yield full


def bump_text(text, new):
    """Returns (new_text, number_of_pins_rewritten)."""
    new_tag = "v%d.%d.%d" % new
    count = 0

    def replace(match):
        nonlocal count
        old = tuple(int(part) for part in match.groups())
        if new <= old:
            return match.group(0)
        count += 1
        return f"{IMAGE}:{new_tag}"

    return PIN_RE.sub(replace, text), count


def run(root, tag, dry_run):
    new = parse_tag(tag)
    changed = []
    pins_seen = 0
    for path in iter_files(root):
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
        pins_seen += len(PIN_RE.findall(text))
        updated, count = bump_text(text, new)
        if count == 0:
            continue
        changed.append(os.path.relpath(path, root))
        if not dry_run:
            with open(path, "w", encoding="utf-8") as handle:
                handle.write(updated)
    if pins_seen == 0:
        # Zero pins means the targets moved and this job would silently do
        # nothing forever. That is a bug in the job, not a clean result.
        raise BumpError(f"found no {IMAGE}:vX.Y.Z pin under {TARGETS}; update TARGETS")
    return changed


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--tag", required=True, help="stable release tag, e.g. v0.21.0")
    parser.add_argument("--root", default=".", help="repository root")
    parser.add_argument("--dry-run", action="store_true", help="print the files that would change; write nothing")
    args = parser.parse_args()
    try:
        changed = run(args.root, args.tag, args.dry_run)
    except BumpError as err:
        print(f"::error::{err}", file=sys.stderr)
        return 1
    for path in changed:
        print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
