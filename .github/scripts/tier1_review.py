#!/usr/bin/env python3
"""Decide the `tier1-review` status for a PR from its body, labels and comments.

THE RULE (CLAUDE.md, "PR review process"; postmortem action item 9):

  * Every PR declares a risk tier in its body: `Risk tier: Tier 1|2|3`
    (HTML comments are ignored, so the template's own placeholder text does
    not count). No declaration fails.
  * Except a GitHub App's PR (Dependabot, github-actions): an App writes a body
    from a fixed template and cannot add the line, so the declaration is not a
    gate it can pass and every bot PR failed on it. An App author with no
    declaration is read as Tier 3. The floors below still apply and still raise
    it to Tier 1, so exempting the DECLARATION does not exempt the TIER (#3389).
  * A PR that touches a Tier 1 path (CODEOWNERS entries owned by the signers
    alone) is Tier 1 whatever it declares: the declaration can raise the tier,
    never lower it below the paths.
  * A dependency manifest (`go.mod`, `go.sum`, at any depth) is the exception:
    it takes its tier from the MODULES it moves, listed in
    .github/tier1-modules.txt, not from the directory it sits in. Every Go bump
    touches `go.mod`, so the file was the wrong granularity (#3389). Tier 1
    without consulting the list: the `go`, `toolchain` or `module` directive, a
    manifest the API returned no diff for, and any line the parser cannot
    classify.
  * Tier 2 and 3 pass (apart from the `do-not-merge` label, which always fails).
  * Tier 1 passes only if, for the CURRENT head commit, there is
      `Review: approve @ <sha>`   (a trusted commenter: the reviewing session), and
      `Sign-off @ <sha>`          (a commenter in --signers: DeVaris),
    and no author's newest verdict is `Review: changes-needed`. A push changes
    the head, so both go stale.
  * Only comments whose author_association is OWNER, MEMBER or COLLABORATOR
    (or whose login is in --reviewers) count. Anyone else can comment; it is
    ignored. Logins are compared case-insensitively, the way GitHub treats
    them, in --signers, --reviewers and against the PR author.
  * A Tier 1 success names how independent the two verdicts were. When the
    approve and the sign-off both come from the PR's own author, the status
    reads `self-certified by author`, because with one write account there is
    no independence to claim and a history that claims it anyway is worse than
    one that admits the gap (#3389). The stamp words the success; it does not
    change who passes.
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
       [--files FILES_JSON --codeowners CODEOWNERS --tier1-modules MODULES]
Prints a JSON object {state, description, detail, label} and exits 0 only on
success. `description` is capped at GitHub's 140 characters; `detail` is not.
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


BOT_SUFFIX = "[bot]"


def is_bot(user):
    """Whether a GitHub App opened this PR. `type` is the API's own answer; the
    `[bot]` login suffix is the fallback and cannot be forged, because a human
    login holds no brackets. A missing user object is not a bot (fail closed:
    the declaration stays required)."""
    user = user or {}
    return user.get("type") == "Bot" or (user.get("login") or "").endswith(BOT_SUFFIX)


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
    # GitHub logins are case-insensitive, so an allowlisted login must match
    # whatever casing the API returns; otherwise the allowlist silently misses.
    allowed = {l.lower() for l in trusted_logins}
    for c in comments:
        author = (c.get("user") or {}).get("login", "")
        if c.get("author_association") not in TRUSTED and author.lower() not in allowed:
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
    """The path floor. Dependency manifests are excluded: they are judged by the
    modules they move (manifest_floor), not by the directory they sit in, so
    `tools/go.mod` does not inherit Tier 1 from CODEOWNERS' `/tools/`."""
    return any(touches(p, f) for f in files if not is_dep_manifest(f) for p in patterns)


MANIFESTS = ("go.mod", "go.sum")


def is_manifest(path):
    """A Go dependency manifest at any depth: `go.mod`, `tools/go.sum`."""
    return path.rsplit("/", 1)[-1] in MANIFESTS


def is_dep_manifest(path):
    """A manifest that declares what the build depends on, so the module floor
    judges it. A manifest under `testdata/` is golden fixture content, not a
    dependency of this build: it keeps taking its tier from its path, like every
    other file in that tree."""
    return is_manifest(path) and "testdata" not in path.split("/")[:-1]


class ManifestFloor(Exception):
    """A manifest change that is Tier 1 without consulting the module list."""


def tier1_modules(text):
    """Module paths from .github/tier1-modules.txt, comments and blanks dropped."""
    out = []
    for raw in (text or "").splitlines():
        line = raw.split("#", 1)[0].strip().rstrip("/")
        if line:
            out.append(line)
    return out


def module_hit(module, patterns):
    """The list entry covering this module path, or None. An entry covers the
    module and everything under it, so `.../conduit-commons` covers
    `.../conduit-commons/v2` and no major-version lines are needed."""
    m = module.lower()
    return next((p for p in patterns if m == p.lower() or m.startswith(p.lower() + "/")), None)


# A module path: a host with a dot in it, then at least one element. A version
# (`v1.2.3`, `v1.2.3/go.mod`) has no dot-then-letters before its first slash, so
# it cannot match; neither can a base64 hash (no `.` in that alphabet).
MODULE_PATH = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._~-]*\.[A-Za-z]{2,}(?:/[^\s/]+)+$")
# go.mod directives that introduce entries. The entry itself carries the module.
ENTRY_DIRECTIVES = ("require", "replace", "exclude", "retract", "tool", "godebug", "ignore")
# Directives whose change is Tier 1 on its own: the runtime under the whole data
# path, and this module's own identity.
SELF_TIER1 = {"go": "`go` directive", "toolchain": "`toolchain` directive", "module": "`module` directive"}


def manifest_modules(name, patch):
    """Module paths whose entries a manifest diff adds or removes.

    Raises ManifestFloor for a change that is Tier 1 by itself: a missing diff,
    a `go`/`toolchain`/`module` directive, or a line it cannot classify. The
    caller turns that into the floor, so an unreadable manifest never passes as
    Tier 3."""
    if patch is None:
        raise ManifestFloor(f"no diff for {name}")
    mods = set()
    for raw in patch.splitlines():
        if raw[:1] not in ("+", "-") or raw[:3] in ("+++", "---"):
            continue
        line = raw[1:].split("//", 1)[0].strip()
        if not line or line == ")":
            continue
        tokens = line.rstrip("(").split()
        if not tokens:
            continue
        if tokens[0] in SELF_TIER1:
            raise ManifestFloor(f"{name} changes the {SELF_TIER1[tokens[0]]}")
        if tokens[0] in ENTRY_DIRECTIVES:
            tokens = tokens[1:]
        if not tokens:
            continue  # a block opener: `require (`
        found = [x for x in tokens if MODULE_PATH.match(x)]
        if not found:
            raise ManifestFloor(f"unclassifiable line in {name}: {line[:40]}")
        mods.update(found)
    return mods


def manifest_floor(manifests, modules):
    """Why the dependency-manifest changes make this PR Tier 1, or "".

    `manifests` is (filename, patch or None) per changed manifest."""
    if not manifests:
        return ""
    if not modules:
        # Fail closed: an empty or unwired list makes every bump Tier 1 rather
        # than none of them.
        return "no Tier 1 module list"
    hits = []
    for name, patch in manifests:
        try:
            mods = manifest_modules(name, patch)
        except ManifestFloor as e:
            return str(e)
        hits += [m for m in sorted(mods) if module_hit(m, modules) and m not in hits]
    if not hits:
        return ""
    more = f" +{len(hits) - 1} more" if len(hits) > 1 else ""
    return f"module {hits[0]}{more}"


def floor_reason(files, tier1_paths, patches=None, tier1_mods=()):
    """Why this PR is Tier 1 by its files alone, or "". Two independent floors:
    the CODEOWNERS paths, and the modules the dependency manifests move."""
    if touches_tier1(files, tier1_paths):
        return "touches a Tier 1 path"
    seen, manifests = set(), []
    for f in files:
        if is_dep_manifest(f) and f not in seen:
            seen.add(f)
            manifests.append((f, (patches or {}).get(f)))
    return manifest_floor(manifests, tier1_mods)


def certification(author, approvers, signoffs, head):
    """The Tier 1 success line, naming how independent its verdicts were.

    CLAUDE.md asks for a human reviewing "with fresh context in a separate
    session from the author". With one write account the author, the reviewing
    session and the signer are one login, and a second account would buy audit
    noise rather than a second pair of eyes, so the gate cannot manufacture the
    independence. It states the truth instead: a Tier 1 history that reads as
    peer-reviewed when it was not is the thing the postmortem warns against."""
    sha = head[:7]
    if not author:
        # Fail closed on the wording: with no author we cannot show the
        # verdicts came from anyone else, so we do not say they did.
        return f"Tier 1: approved and signed off @ {sha} (author unknown, independence unverified)"
    # Case-insensitively, because GitHub logins are: `DeVarisMeroxa` and
    # `devarismeroxa` are one account, and the unsafe direction here is reading
    # the author's own verdict as somebody else's.
    me = author.lower()
    others_approved = [a for a in approvers if a.lower() != me]
    others_signed = [a for a in signoffs if a.lower() != me]
    if others_approved and others_signed:
        return f"Tier 1: review approved and signed off @ {sha}"
    if not others_approved and not others_signed:
        return f"Tier 1: self-certified by author {author} @ {sha}"
    if others_approved:
        return f"Tier 1: approved by {'/'.join(others_approved)}, self-signed by author @ {sha}"
    return f"Tier 1: self-approved by author, signed off by {'/'.join(others_signed)} @ {sha}"


def decide(pr, comments, signers, reviewers=frozenset(), files=(), tier1_paths=(),
           patches=None, tier1_mods=()):
    labels = {l["name"] if isinstance(l, dict) else l for l in pr.get("labels", [])}
    head = (pr.get("head") or {}).get("sha", "")
    if len(head) < 7:
        raise GateError("PR JSON has no head sha")
    if BLOCK_LABEL in labels:
        return fail(f"labelled {BLOCK_LABEL}")
    tier = declared_tier(pr.get("body"), labels)
    bot = tier is None and is_bot(pr.get("user"))
    if bot:
        # An App cannot write the line, so failing on it only ever blocked the
        # dependency queue. Tier 3 is what it gets for free; the floors below
        # still raise it (#3389).
        tier = 3
    if tier is None:
        return fail("no `Risk tier: Tier 1|2|3` line in the PR body")
    floor = floor_reason(files, tier1_paths, patches, tier1_mods)
    if floor:
        tier = 1
    if tier != 1:
        # Named, so the status history separates a tier a bot was given from one
        # a human declared.
        return ok(f"Tier {tier}{' (bot default)' if bot else ''}: no Tier 1 review required")
    trusted = set(reviewers) | set(signers)
    pending = unresolved_changes_needed(comments, trusted)
    if pending:
        return fail(f"Review: changes-needed @ {pending[0][:7]} by {pending[1]} has no later approve", label=True)
    review, sign = newest_per_author(verdicts(comments, trusted))
    # The logins, not just a boolean: the success stamp names who certified.
    approvers = sorted(a for a, (k, s, _) in review.items() if k == "approve" and same_sha(s, head))
    sign_logins = {x.lower() for x in signers}
    signoffs = sorted(a for a, (s, _) in sign.items()
                      if a.lower() in sign_logins and same_sha(s, head))
    missing = []
    why = f" ({floor})" if floor else ""
    if not approvers:
        missing.append(f"`Review: approve @ {head[:7]}`")
    if not signoffs:
        missing.append(f"`Sign-off @ {head[:7]}` from {'/'.join(sorted(signers)) or 'a signer'}")
    if missing:
        return fail("Tier 1" + why + " needs " + " and ".join(missing) + " for the current head")
    return ok(certification((pr.get("user") or {}).get("login", ""), approvers, signoffs, head))


def ok(desc):
    return {"state": "success", "description": desc[:140], "detail": desc, "label": False}


def fail(desc, label=False):
    # GitHub truncates a status description at 140 characters, which a long
    # module path can reach; `detail` keeps the whole line for the step summary.
    return {"state": "failure", "description": desc[:140], "detail": desc, "label": label}


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("pr")
    ap.add_argument("comments")
    ap.add_argument("--signers", default="devarismeroxa")
    ap.add_argument("--reviewers", default="", help="extra logins whose verdicts count")
    ap.add_argument("--files", help="JSON list of the PR's changed files (GitHub pulls/N/files)")
    ap.add_argument("--codeowners", help="CODEOWNERS file from the default branch")
    ap.add_argument("--tier1-modules", help="module paths whose bumps are Tier 1 (#3389)")
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
        files, paths, patches, mods = [], [], {}, []
        if a.files:
            with open(a.files) as f:
                changed = json.load(f)
            # A rename out of a Tier 1 path touches that path too.
            files = [n for x in changed for n in (x["filename"], x.get("previous_filename")) if n]
            # `patch` is absent for a diff the API will not render, which
            # manifest_modules treats as the Tier 1 floor.
            patches = {x["filename"]: x["patch"] for x in changed
                       if is_dep_manifest(x["filename"]) and x.get("patch")}
        if a.codeowners:
            with open(a.codeowners) as f:
                paths = tier1_patterns(f.read(), signers)
        if a.tier1_modules:
            with open(a.tier1_modules) as f:
                mods = tier1_modules(f.read())
        res = decide(pr, comments, signers, reviewers, files, paths, patches, mods)
    except (OSError, ValueError, KeyError, TypeError, GateError) as e:
        res = {"state": "error", "description": f"cannot decide: {e}"[:140], "label": False}
    print(json.dumps(res))
    return 0 if res["state"] == "success" else 1


if __name__ == "__main__":
    sys.exit(main())
