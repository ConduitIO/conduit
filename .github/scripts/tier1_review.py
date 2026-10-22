#!/usr/bin/env python3
"""Decide the `tier1-review` status for a PR from its body, labels and comments.

THE RULE (CLAUDE.md, "PR review process"; postmortem action item 9):

  * Every PR declares a risk tier in its body: `Risk tier: Tier 1|2|3`
    (HTML comments are ignored, so the template's own placeholder text does
    not count). No declaration fails.
  * Tier 2 and 3 pass (apart from the `do-not-merge` label, which always fails).
  * Tier 1 passes only if, for the CURRENT head commit, there is
      `Review: approve @ <sha>`   (any commenter: the reviewing session), and
      `Sign-off @ <sha>`          (a commenter in --signers: DeVaris),
    and no `Review: changes-needed @ <sha>` is newer than the latest
    `Review: approve`. A push changes the head, so both go stale.
  * `Review: changes-needed` at ANY earlier head still blocks until a later
    approve: a fix that is pushed does not erase a finding.

Comments, not the review API, because GitHub does not let an account approve
its own PR and every PR here is opened from one account. The consequence is
stated rather than hidden: with one account, "any commenter" cannot tell the
reviewing session from the author's session. Sign-off authority is the
--signers list; with a second (machine) account the review verdict can move to
real reviews. See the postmortem, "Branch protection".

FAILS CLOSED: malformed input is an error status, never success.

Usage: tier1_review.py PR_JSON COMMENTS_JSON [--signers login,login]
Prints a JSON object {state, description, label} and exits 0 only on success.
"""

import argparse
import json
import re
import sys

HTML_COMMENT = re.compile(r"<!--.*?-->", re.S)
TIER = re.compile(r"(?i)risk\s+tier[^A-Za-z0-9\n]*tier\s*([123])\b")
VERDICT = re.compile(r"(?im)^[ \t>*-]*Review:\s*(approve|changes-needed)\s*@\s*([0-9a-f]{7,40})\b")
SIGNOFF = re.compile(r"(?im)^[ \t>*-]*Sign-off\s*@\s*([0-9a-f]{7,40})\b")
BLOCK_LABEL = "do-not-merge"


class GateError(Exception):
    pass


def declared_tier(body, labels):
    """Return 1, 2 or 3, or None when the PR declares nothing. The `tier-1`
    label forces Tier 1 whatever the body says."""
    if "tier-1" in labels:
        return 1
    m = TIER.search(HTML_COMMENT.sub("", body or ""))
    return int(m.group(1)) if m else None


def same_sha(a, b):
    a, b = a.lower(), b.lower()
    return len(a) >= 7 and len(b) >= 7 and (a.startswith(b) or b.startswith(a))


def verdicts(comments):
    """Chronological list of (kind, sha, author). Kinds: approve,
    changes-needed, signoff."""
    out = []
    for c in sorted(comments, key=lambda c: (c.get("created_at", ""), c.get("id", 0))):
        body = HTML_COMMENT.sub("", c.get("body") or "")
        author = (c.get("user") or {}).get("login", "")
        for m in VERDICT.finditer(body):
            out.append((m.group(1).lower(), m.group(2), author))
        for m in SIGNOFF.finditer(body):
            out.append(("signoff", m.group(1), author))
    return out


def unresolved_changes_needed(comments):
    """The latest changes-needed verdict that no later approve answers, as
    (sha, author), else None. Used by the connector release gate too."""
    pending = None
    for kind, sha, author in verdicts(comments):
        if kind == "changes-needed":
            pending = (sha, author)
        elif kind == "approve":
            pending = None
    return pending


def decide(pr, comments, signers):
    labels = {l["name"] if isinstance(l, dict) else l for l in pr.get("labels", [])}
    head = (pr.get("head") or {}).get("sha", "")
    if len(head) < 7:
        raise GateError("PR JSON has no head sha")
    if BLOCK_LABEL in labels:
        return fail(f"labelled {BLOCK_LABEL}")
    tier = declared_tier(pr.get("body"), labels)
    if tier is None:
        return fail("no `Risk tier: Tier 1|2|3` line in the PR body")
    if tier != 1:
        return ok(f"Tier {tier}: no Tier 1 review required")
    pending = unresolved_changes_needed(comments)
    if pending:
        return fail(f"Review: changes-needed @ {pending[0][:7]} has no later approve", label=True)
    vs = verdicts(comments)
    approved = any(k == "approve" and same_sha(s, head) for k, s, _ in vs)
    signed = any(k == "signoff" and same_sha(s, head) and a in signers for k, s, a in vs)
    missing = []
    if not approved:
        missing.append(f"`Review: approve @ {head[:7]}`")
    if not signed:
        missing.append(f"`Sign-off @ {head[:7]}` from {'/'.join(sorted(signers)) or 'a signer'}")
    if missing:
        return fail("Tier 1 needs " + " and ".join(missing) + " for the current head")
    return ok(f"Tier 1: review approved and signed off @ {head[:7]}")


def ok(desc):
    return {"state": "success", "description": desc[:140], "label": False}


def fail(desc, label=False):
    return {"state": "failure", "description": desc[:140], "label": label}


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("pr")
    ap.add_argument("comments")
    ap.add_argument("--signers", default="devarismeroxa")
    a = ap.parse_args(argv)
    try:
        with open(a.pr) as f:
            pr = json.load(f)
        with open(a.comments) as f:
            comments = json.load(f)
        if not isinstance(pr, dict) or not isinstance(comments, list):
            raise GateError("unexpected JSON shape")
        res = decide(pr, comments, {s.strip() for s in a.signers.split(",") if s.strip()})
    except (OSError, ValueError, GateError) as e:
        res = {"state": "error", "description": f"cannot decide: {e}"[:140], "label": False}
    print(json.dumps(res))
    return 0 if res["state"] == "success" else 1


if __name__ == "__main__":
    sys.exit(main())
