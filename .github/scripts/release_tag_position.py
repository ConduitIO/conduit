#!/usr/bin/env python3
"""Where does the tag being released sit among the repository's releases?

release.yml used to assume the tag it is building is the newest version. It is
not, whenever a patch is cut on an older release line: tagging v0.20.7 after
v0.22.0 shipped moved the floating `latest` Docker tag to the v0.20 line, set
the GitHub release as "latest", and handed GoReleaser v0.21.2 as the previous
tag, which is not an ancestor of v0.20.7 (see the 2026-11-27 incident and
`docs/releases.md`, "Release branches").

Three decisions depend on the answer, so they are computed once here instead of
three times in shell:

  is_highest   this tag is the greatest stable version in the repository, so
               the floating pointers (`latest`, the GitHub release's "latest"
               flag) should move to it.
  previous_tag the greatest stable tag strictly below this one — the release
               this one follows ON ITS OWN LINE. For v0.20.7 that is v0.20.6,
               not v0.21.2.

Only `vMAJOR.MINOR.PATCH` counts as stable. Anything with a suffix (a nightly,
an rc) is ignored for both answers, and a tag that is not stable is never
`is_highest`.

FAILS CLOSED: a tag this script cannot parse yields `is_highest=false` and an
empty `previous_tag`. A missed `latest` update is a one-command fix; `latest`
pointing at an older line is served to everyone who pulls the default tag.

Usage: release_tag_position.py CURRENT_TAG [TAGS_FILE]
       (TAGS_FILE: one tag per line; defaults to stdin)
Writes `name=value` lines to $GITHUB_OUTPUT when set, and always to stdout.
"""

import os
import re
import sys

STABLE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


def version(tag):
    """(major, minor, patch) for a stable tag, else None."""
    m = STABLE.match(tag.strip())
    if not m:
        return None
    return tuple(int(p) for p in m.groups())


def decide(current, tags):
    """Return {"is_highest": bool, "previous_tag": str} for current among tags."""
    cur = version(current)
    stable = {t.strip(): v for t in tags if (v := version(t.strip())) is not None}

    if cur is None:
        # Not a stable release (nightly, rc, or malformed): never move the
        # floating pointers, and offer no previous tag.
        return {"is_highest": False, "previous_tag": ""}

    if not stable:
        # No stable tags at all. In a real release the tag being built is
        # itself in the list, so an empty list means the tags were not
        # fetched (a shallow checkout) rather than that this is the first
        # release ever. Fail closed: do not move the floating pointers.
        return {"is_highest": False, "previous_tag": ""}

    higher = [v for v in stable.values() if v > cur]
    lower = [(v, t) for t, v in stable.items() if v < cur]
    previous = max(lower)[1] if lower else ""
    return {"is_highest": not higher, "previous_tag": previous}


def main(argv):
    if not 2 <= len(argv) <= 3:
        print(__doc__, file=sys.stderr)
        return 2
    current = argv[1]
    if len(argv) == 3:
        with open(argv[2]) as f:
            tags = f.read().splitlines()
    else:
        tags = sys.stdin.read().splitlines()

    out = decide(current, tags)
    lines = [
        "is_highest={}".format("true" if out["is_highest"] else "false"),
        "previous_tag={}".format(out["previous_tag"]),
    ]
    for line in lines:
        print(line)
    path = os.environ.get("GITHUB_OUTPUT")
    if path:
        with open(path, "a") as f:
            for line in lines:
                f.write(line + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
