#!/usr/bin/env python3
"""Fail a connector tag while a PR merged since the previous tag is still blocked.

Postmortem action item 11. The S3 resume stack, mysql#232 and postgres#433
were merged with an open `changes-needed` finding or the `do-not-merge` label,
and shipped in the next tag because nothing looked. This gate looks.

For the PRs that landed between the previous release tag and the one being
cut, it fails if any
  * carries the `do-not-merge` label, or
  * has a `Review: changes-needed @ <sha>` comment with no later
    `Review: approve @ <sha>` (the verdict grammar of tier1_review.py).

It does NOT require every PR to have an approval: that would fail every
historical PR. It only refuses to ship a finding someone raised and nobody
answered. FAILS CLOSED: if the previous tag or the PR list cannot be
resolved, the gate fails.

Used from connector repositories through the reusable workflow
`.github/workflows/connector-release-gate.yml`.

Usage:
  connector_release_gate.py prev TAG < tags       (previous stable tag below TAG)
  connector_release_gate.py collect REPO PREV_TAG HEAD_REF OUT.json
  connector_release_gate.py evaluate PRS.json

Only PRs that are MERGED count (the commit-to-PR API also lists open PRs that
contain the commit), and only verdict comments from OWNER, MEMBER or
COLLABORATOR authors (tier1_review.TRUSTED), so a drive-by comment can neither
block nor clear a tag.
"""

import json
import re
import subprocess
import sys

import tier1_review

BLOCK_LABEL = tier1_review.BLOCK_LABEL


class GateError(Exception):
    pass


def run_gh(*args):
    r = subprocess.run(["gh", *args], capture_output=True, text=True)
    if r.returncode != 0:
        raise GateError(f"gh {' '.join(args[:2])} failed: {r.stderr.strip()[:200]}")
    return r.stdout


_SEMVER = re.compile(r"^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.\-]+))?$")


def _key(tag):
    m = _SEMVER.match(tag)
    if not m:
        return None
    pre = m.group(4)
    ids = () if pre is None else tuple((0, int(i), "") if i.isdigit() else (1, 0, i) for i in pre.split("."))
    # A release sorts after its own pre-releases (semver), which `sort -V` gets wrong.
    return (int(m.group(1)), int(m.group(2)), int(m.group(3)), 1 if pre is None else 0, ids)


def previous_stable(tag, tags):
    """The highest stable tag strictly below `tag` (an rc counts as below its release)."""
    k = _key(tag)
    if k is None:
        raise GateError(f"not a release tag: {tag!r}")
    below = [t for t in tags if _SEMVER.match(t) and "-" not in t and _key(t) < k]
    return max(below, key=_key) if below else ""


def json_stream(text):
    """Parse concatenated JSON values (what `gh api --paginate --jq '.[]'` prints)."""
    dec, i, out = json.JSONDecoder(), 0, []
    text = text.strip()
    while i < len(text):
        v, j = dec.raw_decode(text, i)
        out.append(v)
        i = j
        while i < len(text) and text[i].isspace():
            i += 1
    return out


def collect(repo, prev_tag, head_ref, gh=run_gh):
    """PRs merged between prev_tag and head_ref, with labels and comments."""
    cmp_ = json.loads(gh("api", f"repos/{repo}/compare/{prev_tag}...{head_ref}", "--jq", "{n: .total_commits, c: [.commits[].sha]}"))
    shas = cmp_["c"]
    if cmp_["n"] and len(shas) < cmp_["n"]:
        # The compare API truncates at 250 commits; an incomplete list must not read as "all clear".
        raise GateError(f"compare returned {len(shas)} of {cmp_['n']} commits; narrow the range")
    seen = {}
    for sha in shas:
        for pr in json.loads(gh("api", f"repos/{repo}/commits/{sha}/pulls")):
            seen[pr["number"]] = pr
    out = []
    for num in sorted(seen):
        pr = json.loads(gh("api", f"repos/{repo}/pulls/{num}"))
        if not pr.get("merged"):
            continue
        comments = json_stream(gh("api", "--paginate", f"repos/{repo}/issues/{num}/comments", "--jq", ".[]"))
        out.append({"number": num, "title": pr.get("title", ""), "labels": [l["name"] for l in pr.get("labels", [])],
                    "comments": comments})
    return out


def evaluate(prs):
    """Return a list of problems; empty means the tag may be cut."""
    problems = []
    for pr in prs:
        labels = {l["name"] if isinstance(l, dict) else l for l in pr.get("labels", [])}
        if BLOCK_LABEL in labels:
            problems.append(f"#{pr['number']} {pr.get('title','')!r} was merged with the {BLOCK_LABEL} label")
        pending = tier1_review.unresolved_changes_needed(pr.get("comments", []))
        if pending:
            problems.append(f"#{pr['number']} {pr.get('title','')!r} has an unanswered `Review: changes-needed @ {pending[0][:7]}`")
    return problems


def main(argv=None, gh=run_gh):
    argv = list(sys.argv[1:] if argv is None else argv)
    try:
        if argv[:1] == ["prev"] and len(argv) == 2:
            print(previous_stable(argv[1], sys.stdin.read().split()))
            return 0
        if argv[:1] == ["collect"] and len(argv) == 5:
            prs = collect(argv[1], argv[2], argv[3], gh=gh)
            with open(argv[4], "w") as f:
                json.dump(prs, f)
            print(f"connector release gate: {len(prs)} PRs merged since {argv[2]}.")
            return 0
        if argv[:1] == ["evaluate"] and len(argv) == 2:
            with open(argv[1]) as f:
                prs = json.load(f)
            if not isinstance(prs, list):
                raise GateError("PR list is not a list")
            problems = evaluate(prs)
            if problems:
                print("connector release gate: FAILED. Resolve these before tagging:")
                for p in problems:
                    print(f"  - {p}")
                return 1
            print(f"connector release gate: {len(prs)} PRs checked, none blocked.")
            return 0
        print(__doc__)
        return 2
    except (OSError, ValueError, KeyError, GateError) as e:
        print(f"connector release gate: FAILED (cannot decide): {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
