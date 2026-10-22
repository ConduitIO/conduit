#!/usr/bin/env python3
"""Decide the `tier1-review` status for a PR from its body, labels and comments.

THE RULE (CLAUDE.md, "PR review process"; postmortem action item 9):

  * Every PR declares a risk tier in its body: `Risk tier: Tier 1|2|3`
    (HTML comments are ignored, so the template's own placeholder text does
    not count). No declaration fails.
  * A PR that touches a Tier 1 path (CODEOWNERS entries owned by the signers
    alone) is Tier 1 whatever it declares: the declaration can raise the tier,
    never lower it below the paths.
  * Tier 2 and 3 pass (apart from the `do-not-merge` label, which always fails).
  * Tier 1 passes only if, for the CURRENT head commit, there is
      `Review: approve @ <sha>`   (a trusted commenter: the reviewing session), and
      `Sign-off @ <sha>`          (a commenter in --signers: DeVaris),
    and no author's newest verdict is `Review: changes-needed`. A push changes
    the head, so both go stale.
  * Only comments whose author_association is OWNER, MEMBER or COLLABORATOR
    (or whose login is in --reviewers) count. Anyone else can comment; it is
    ignored.
  * Per author, only the NEWEST verdict counts, ordered by updated_at (an
    edited comment moves to its edit time; a deleted comment is gone from the
    API). `Review: changes-needed` at ANY earlier head still blocks until that
    author's later approve: a pushed fix does not erase a finding.

Comments, not the review API, because GitHub does not let an account approve
its own PR and every PR here is opened from one account. The consequence is
stated rather than hidden: with one account, a trusted commenter cannot tell
the reviewing session from the author's session. Sign-off authority is the
--signers list; with a second (machine) account the review verdict can move to
real reviews. See the postmortem, "Branch protection".

LIMIT: a required status context can be set by any workflow in the same
repository that has `statuses: write`, so a PR that adds such a workflow can
spoof `tier1-review`. The structural fix is a GitHub App that posts a check
run (only that App can write it) and is named as the required check's source.
Until then CODEOWNERS on `.github/` (owned by DeVaris alone) is the control.

FAILS CLOSED: malformed input is an error status, never success.

Usage: tier1_review.py PR_JSON COMMENTS_JSON [--signers a,b] [--reviewers a,b]
       [--files FILES_JSON --codeowners CODEOWNERS]
Prints a JSON object {state, description, label} and exits 0 only on success.
"""

import argparse
import fnmatch
import json
import re
import sys

HTML_COMMENT = re.compile(r"<!--.*?-->", re.S)


def strip_comments(text):
    """Remove HTML comments, multi-line included. An unclosed `<!--` hides
    everything after it (that is how a renderer treats it), so it is stripped to
    the end rather than left as visible text."""
    return HTML_COMMENT.sub("", text or "").split("<!--", 1)[0]


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
    tiers = [int(t) for t in TIER.findall(strip_comments(body))]
    # More than one declaration: the riskiest (lowest number, Tier 1) wins, so a
    # second line cannot lower the tier.
    return min(tiers) if tiers else None


def same_sha(a, b):
    a, b = a.lower(), b.lower()
    return len(a) >= 7 and len(b) >= 7 and (a.startswith(b) or b.startswith(a))


TRUSTED = {"OWNER", "MEMBER", "COLLABORATOR"}


def verdicts(comments, trusted_logins=frozenset()):
    """Chronological list of (kind, sha, author, when). Kinds: approve,
    changes-needed, signoff. Only comments by a trusted author count; a
    comment without author_association is not trusted (fail closed)."""
    out = []
    for c in comments:
        author = (c.get("user") or {}).get("login", "")
        if c.get("author_association") not in TRUSTED and author not in trusted_logins:
            continue
        when = (c.get("updated_at") or c.get("created_at") or "", c.get("id", 0))
        body = strip_comments(c.get("body"))
        for m in VERDICT.finditer(body):
            out.append((m.group(1).lower(), m.group(2), author, when))
        for m in SIGNOFF.finditer(body):
            out.append(("signoff", m.group(1), author, when))
    return sorted(out, key=lambda v: v[3])


def newest_per_author(vs):
    """Latest review verdict (approve or changes-needed) per author, and the
    latest sign-off per author."""
    review, sign = {}, {}
    for kind, sha, author, when in vs:
        if kind == "signoff":
            sign[author] = (sha, when)
        else:
            review[author] = (kind, sha, when)
    return review, sign


def unresolved_changes_needed(comments, trusted_logins=frozenset()):
    """A standing `changes-needed` as (sha, author), else None: some trusted
    author's newest review verdict is changes-needed. Used by the connector
    release gate too."""
    review, _ = newest_per_author(verdicts(comments, trusted_logins))
    for author in sorted(review):
        kind, sha, _ = review[author]
        if kind == "changes-needed":
            return (sha, author)
    return None


def tier1_patterns(codeowners_text, signers):
    """CODEOWNERS patterns owned by the signers alone (the Tier 1 block)."""
    want = {"@" + s.lower() for s in signers}
    out = []
    for raw in (codeowners_text or "").splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        pat, *owners = line.split()
        if pat != "*" and owners and {o.lower() for o in owners} <= want:
            out.append(pat)
    return out


def touches(pattern, path):
    anchored = pattern.startswith("/")
    pat = pattern.lstrip("/")
    if pat.endswith("/"):
        return path.startswith(pat) if anchored else ("/" + path).find("/" + pat) >= 0
    if "*" in pat:
        return fnmatch.fnmatch(path, pat)
    return path == pat or (not anchored and path.endswith("/" + pat))


def touches_tier1(files, patterns):
    return any(touches(p, f) for f in files for p in patterns)


def decide(pr, comments, signers, reviewers=frozenset(), files=(), tier1_paths=()):
    labels = {l["name"] if isinstance(l, dict) else l for l in pr.get("labels", [])}
    head = (pr.get("head") or {}).get("sha", "")
    if len(head) < 7:
        raise GateError("PR JSON has no head sha")
    if BLOCK_LABEL in labels:
        return fail(f"labelled {BLOCK_LABEL}")
    tier = declared_tier(pr.get("body"), labels)
    if tier is None:
        return fail("no `Risk tier: Tier 1|2|3` line in the PR body")
    floor = touches_tier1(files, tier1_paths)
    if floor:
        tier = 1
    if tier != 1:
        return ok(f"Tier {tier}: no Tier 1 review required")
    trusted = set(reviewers) | set(signers)
    pending = unresolved_changes_needed(comments, trusted)
    if pending:
        return fail(f"Review: changes-needed @ {pending[0][:7]} by {pending[1]} has no later approve", label=True)
    review, sign = newest_per_author(verdicts(comments, trusted))
    approved = any(k == "approve" and same_sha(s, head) for k, s, _ in review.values())
    signed = any(same_sha(s, head) for a, (s, _) in sign.items() if a in signers)
    missing = []
    why = " (touches a Tier 1 path)" if floor else ""
    if not approved:
        missing.append(f"`Review: approve @ {head[:7]}`")
    if not signed:
        missing.append(f"`Sign-off @ {head[:7]}` from {'/'.join(sorted(signers)) or 'a signer'}")
    if missing:
        return fail("Tier 1" + why + " needs " + " and ".join(missing) + " for the current head")
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
    ap.add_argument("--reviewers", default="", help="extra logins whose verdicts count")
    ap.add_argument("--files", help="JSON list of the PR's changed files (GitHub pulls/N/files)")
    ap.add_argument("--codeowners", help="CODEOWNERS file from the default branch")
    a = ap.parse_args(argv)
    try:
        with open(a.pr) as f:
            pr = json.load(f)
        with open(a.comments) as f:
            comments = json.load(f)
        if not isinstance(pr, dict) or not isinstance(comments, list):
            raise GateError("unexpected JSON shape")
        signers = {s.strip() for s in a.signers.split(",") if s.strip()}
        reviewers = {s.strip() for s in a.reviewers.split(",") if s.strip()}
        files, paths = [], []
        if a.files:
            with open(a.files) as f:
                # A rename out of a Tier 1 path touches that path too.
                files = [n for x in json.load(f) for n in (x["filename"], x.get("previous_filename")) if n]
        if a.codeowners:
            with open(a.codeowners) as f:
                paths = tier1_patterns(f.read(), signers)
        res = decide(pr, comments, signers, reviewers, files, paths)
    except (OSError, ValueError, KeyError, TypeError, GateError) as e:
        res = {"state": "error", "description": f"cannot decide: {e}"[:140], "label": False}
    print(json.dumps(res))
    return 0 if res["state"] == "success" else 1


if __name__ == "__main__":
    sys.exit(main())
