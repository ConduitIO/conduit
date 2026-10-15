#!/usr/bin/env python3
"""Delete expired nightly images from GHCR together with their signatures.

The release workflow pushes a multi-arch image per nightly and signs it with
cosign. GHCR has no OCI referrers API, so cosign v3 stores the signature with
the referrers tag schema: a tag `sha256-<hex of the signed digest>` points at
an OCI index whose entries are the Sigstore bundle manifests. Attestations
(SBOMs) land in the same index, or in `sha256-<hex>` tags of the per-platform
digests when those are attested. None of those tags contain "nightly", so a
cleanup that matches `*-nightly*` tags deletes the image and leaves its
signature artifacts behind forever.

This script plans deletions by reachability instead of by tag pattern:

  closure(d) = d, every manifest d's index lists (recursively), and for every
               member m the `sha256-<m>` referrer index plus everything it
               lists (recursively)

  delete     = closure(expired nightlies) + closure(orphaned referrer tags)
               - closure(everything that is kept)

An expired nightly is a digest whose tags are ALL `vX.Y.Z-nightly.YYYYMMDD`
with every date older than the cut-off. Any other tag on the digest -
`latest-nightly`, a stable `vX.Y.Z`, `latest` - keeps it. Everything tagged
that is not an expired nightly and not itself a referrer tag is a kept root,
and nothing reachable from a kept root is ever deleted. That is the property
that protects stable releases: their signatures and attestations hang off the
stable digest, so they are in the protected closure even though their tags
look exactly like a nightly's signature tags.

An orphaned referrer tag is a `sha256-<hex>` tag whose subject manifest the
registry no longer has (404). That is what the old cleanup left behind.

Apply mode re-checks every version's tags in the GitHub Packages API right
before deleting it, so a tag added after the registry snapshot (a stable
release retagging a digest mid-run) still protects it. That recheck sees only
TAGGED versions: an untagged manifest that a release pushed after the snapshot
is invisible to it. The workflow therefore runs the cleanup after the tag push
and never concurrently with itself (see trigger-nightly.yml).

Apply mode is bounded. It deletes whole images, at most --max-deletes versions per run (the
first apply has ~2,200 to delete, and GITHUB_TOKEN is limited to roughly 1,000
requests an hour), paces itself, and stops cleanly on a rate limit (403/429,
X-RateLimit-Remaining, Retry-After), a permission 403 or repeated network
errors; the backlog drains over several runs. An image is deleted in
dependency order with its tagged root last, and if any delete in it fails the
rest of it is abandoned and the root is never deleted, so the next run can
still reach every member. Before deleting anything it
refuses to act on a snapshot that looks truncated (see sanity_check). Any
failed delete makes the script exit 1; the old step logged 2,219 failed
deletes and reported success.

Exit codes: 0 done, or stopped early by the delete cap or a rate limit after
making progress; 1 failed deletes, permission or network trouble; 2 refused
to act (unsafe plan, implausible snapshot, no token).

Usage:
  ghcr_nightly_cleanup.py --image ghcr.io/conduitio/conduit --keep-days 7 [--apply] [--max-deletes 400]
  ghcr_nightly_cleanup.py --snapshot-out snap.json   # save the registry state
  ghcr_nightly_cleanup.py --snapshot-in snap.json    # plan from saved state

Without --apply it only prints the plan (dry run). The registry is read
anonymously; --apply needs GH_TOKEN with packages read and delete rights.
Only the Python standard library is used.
"""

import argparse
import concurrent.futures
import datetime
import hashlib
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

NIGHTLY_TAG = re.compile(r"^v\d+\.\d+\.\d+-nightly\.(\d{8})$")
# cosign v3 writes `sha256-<hex>` (OCI referrers tag schema). Legacy cosign
# formats use `sha256-<hex>.sig`, `.att` and `.sbom`; they are matched too so a
# fallback to the legacy format (see release.yml) is cleaned up the same way.
REFERRER_TAG = re.compile(r"^sha256-([0-9a-f]{64})(\.(sig|att|sbom))?$")
REFERRER_SUFFIXES = ("", ".sig", ".att", ".sbom")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
# Tags that must never lose their image. A plan that would delete one is a
# bug in the planner, not a decision, so it aborts instead of applying.
STABLE_TAG = re.compile(r"^(latest|latest-nightly|v\d+(\.\d+){0,2})$")
STABLE_RELEASE_TAG = re.compile(r"^v\d+\.\d+\.\d+$")
# A snapshot with fewer tags than this is treated as truncated. The registry
# holds ~570 today and the stable releases alone are far above this floor.
MIN_TAGS = 50
# Apply mode limits. GITHUB_TOKEN gets ~1,000 requests an hour, and GitHub's
# secondary limit for content-changing requests (DELETE included) is documented
# as 80 a minute and 500 an hour, hence a default cap of 400 and ~0.8 s pacing.
DEFAULT_MAX_DELETES = 400
DEFAULT_DELETE_INTERVAL = 0.8
RATE_LIMIT_FLOOR = 25  # stop when X-RateLimit-Remaining falls to this
MAX_CONSECUTIVE_NET_ERRORS = 3
# HTTP errors with no success in between. A token without the Admin role gets
# 404 on every delete; do not send 2,000 of them.
MAX_CONSECUTIVE_FAILURES = 5
# The live tagged-version count may trail the snapshot a little (a push between
# the two reads) but not by this much.
LIVE_COVERAGE = 0.9

MANIFEST_ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])
INDEX_TYPES = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}


class PlanError(Exception):
    """The plan violates a safety property; nothing may be deleted."""


def next_link(headers):
    """The rel="next" target of an RFC 8288 Link header, or None."""
    m = re.search(r"<([^>]+)>;\s*rel=\"next\"", headers.get("Link", "") or "")
    return m.group(1) if m else None


def sanity_check(snapshot):
    """Refuse a snapshot that looks truncated or from the wrong image.

    A partial tag list is the dangerous input: kept images missing from it are
    not protected. Every real snapshot has `latest`, at least one stable
    vX.Y.Z release and far more than MIN_TAGS tags.
    """
    tags = snapshot.get("tags", {})
    problems = []
    if "latest" not in tags:
        problems.append("no `latest` tag")
    if not any(STABLE_RELEASE_TAG.match(t) for t in tags):
        problems.append("no stable vX.Y.Z tag")
    if len(tags) < MIN_TAGS:
        problems.append(f"only {len(tags)} tags (expected at least {MIN_TAGS})")
    if problems:
        raise PlanError("implausible registry snapshot, not trusting it: " + "; ".join(problems))


def check_against_live(result, live):
    """Cross-check the snapshot against the Packages API version list.

    Every tagged digest in the snapshot is a package version, so the API must
    list about as many tagged versions as the snapshot has tagged digests. A
    snapshot far smaller than that is missing kept images.
    """
    tagged_live = sum(1 for _id, tags in live.values() if tags)
    in_snapshot = len(result["tags_by_digest"])
    if in_snapshot < LIVE_COVERAGE * tagged_live:
        raise PlanError(
            f"snapshot has {in_snapshot} tagged images but the Packages API lists "
            f"{tagged_live} tagged versions; the snapshot looks truncated")


def referrer_tags(digest):
    """Every tag under which cosign may attach artifacts to digest."""
    base = "sha256-" + digest.split(":", 1)[1]
    return [base + s for s in REFERRER_SUFFIXES]


def expired_nightly_tag(tag, cutoff):
    """True for a vX.Y.Z-nightly.YYYYMMDD tag dated before cutoff."""
    m = NIGHTLY_TAG.match(tag)
    if not m:
        return False
    try:
        built = datetime.datetime.strptime(m.group(1), "%Y%m%d").date()
    except ValueError:
        return False  # not a real date: not ours to delete
    return built < cutoff


def plan(snapshot, today, keep_days):
    """Compute what to delete from a registry snapshot. Pure function.

    snapshot: {"tags": {tag: digest},
               "children": {index digest: [child digests]},
               "missing": [digests the registry answered 404 for]}

    Returns a dict with the expired roots, orphaned referrer tags, the delete
    set and the protected set. Raises PlanError if a stable-looking tag would
    lose its image.
    """
    tags = snapshot["tags"]
    children = snapshot.get("children", {})
    missing = set(snapshot.get("missing", []))
    cutoff = today - datetime.timedelta(days=keep_days)

    for digest in list(tags.values()) + [k for ks in children.values() for k in ks]:
        if not DIGEST.match(digest):
            raise PlanError(f"malformed digest in snapshot: {digest!r}")

    tags_by_digest = {}
    for tag, digest in tags.items():
        tags_by_digest.setdefault(digest, []).append(tag)

    def closure(start):
        seen = set()
        stack = [start]
        while stack:
            d = stack.pop()
            if d in seen:
                continue
            seen.add(d)
            stack.extend(children.get(d, []))
            for rt in referrer_tags(d):
                ref = tags.get(rt)
                if ref is not None:
                    stack.append(ref)
        return seen

    expired, kept = [], []
    for digest, digest_tags in tags_by_digest.items():
        if all(REFERRER_TAG.match(t) for t in digest_tags):
            continue  # a referrer index; kept or deleted with its subject
        if all(expired_nightly_tag(t, cutoff) for t in digest_tags):
            expired.append(digest)
        else:
            kept.append(digest)

    protected = set()
    for d in kept:
        protected |= closure(d)

    # A referrer tag is orphaned when its subject is gone from the registry.
    # A subject that is merely untagged (a platform manifest of a kept index)
    # still exists, so it is never in `missing` and never orphaned here.
    orphans = []
    for tag, digest in tags.items():
        m = REFERRER_TAG.match(tag)
        if m and "sha256:" + m.group(1) in missing:
            orphans.append(tag)

    delete = set()
    for d in expired:
        delete |= closure(d)
    for tag in orphans:
        delete |= closure(tags[tag])
    delete -= protected

    for tag, digest in tags.items():
        if STABLE_TAG.match(tag) and digest in delete:
            raise PlanError(f"plan would delete {digest}, which {tag} points at")

    # One group per image (or orphaned referrer tag): the root plus the rest
    # of its closure that is being deleted. Apply deletes a group as a unit.
    # Once an image's tagged root is gone, its untagged children cannot be
    # found from the tag list any more, so a run that stopped between the root
    # and its children would leak them permanently.
    #
    # Members are in deletion order: every manifest comes after everything it
    # lists or has a referrer index for, and the root comes last. A manifest
    # deleted before the manifests it lists would strand them the same way: a
    # signature index gone before its bundle leaves the bundle unreachable.
    def deletion_order(root):
        order, seen = [], set()

        def visit(d):
            if d in seen:
                return
            seen.add(d)
            for k in children.get(d, []):
                visit(k)
            for rt in referrer_tags(d):
                if rt in tags:
                    visit(tags[rt])
            if d in delete:
                order.append(d)

        visit(root)
        return order

    groups = []
    for root in sorted(expired, key=lambda d: sorted(tags_by_digest[d])):
        groups.append({"root": root, "members": deletion_order(root)})
    for tag in sorted(orphans):
        groups.append({"root": tags[tag], "members": deletion_order(tags[tag])})

    return {
        "cutoff": cutoff,
        "expired": sorted(expired, key=lambda d: sorted(tags_by_digest[d])),
        "orphans": sorted(orphans),
        "delete": delete,
        "protected": protected,
        "tags_by_digest": tags_by_digest,
        "groups": groups,
    }


# --- registry (read-only, anonymous) ----------------------------------------


class Registry:
    def __init__(self, image):
        host, _, repo = image.partition("/")
        self.base = f"https://{host}/v2/{repo}"
        url = f"https://{host}/token?scope=repository:{repo}:pull"
        with urllib.request.urlopen(url, timeout=30) as r:
            self.token = json.load(r)["token"]

    def _get(self, path, accept=None, method="GET"):
        req = urllib.request.Request(self.base + path, method=method)
        req.add_header("Authorization", f"Bearer {self.token}")
        if accept:
            req.add_header("Accept", accept)
        return urllib.request.urlopen(req, timeout=60)

    def tags(self):
        out, path = [], "/tags/list?n=1000"
        while path:
            with self._get(path) as r:
                out.extend(json.load(r).get("tags") or [])
                link = r.headers.get("Link", "")
            # Link: </v2/<repo>/tags/list?last=...&n=1000>; rel="next"
            nxt = next_link({"Link": link})
            path = "/tags/list?" + urllib.parse.urlparse(nxt).query if nxt else None
        return out

    def manifest(self, ref):
        """Return (digest, [(child digest, child media type)]) for a tag or digest."""
        with self._get(f"/manifests/{ref}", accept=MANIFEST_ACCEPT) as r:
            body = r.read()
            header = r.headers.get("Docker-Content-Digest", "")
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        if header and header != digest:
            raise RuntimeError(f"{ref}: registry digest {header} != content digest {digest}")
        doc = json.loads(body)
        if doc.get("mediaType", "") not in INDEX_TYPES:
            return digest, []
        return digest, [(m["digest"], m.get("mediaType", "")) for m in doc.get("manifests", [])]

    def exists(self, digest):
        try:
            with self._get(f"/manifests/{digest}", accept=MANIFEST_ACCEPT, method="HEAD"):
                return True
        except urllib.error.HTTPError as e:
            if e.code == 404:
                return False
            raise


def _manifest_or_gone(reg, digest):
    """A nested index that a surviving parent still lists but the registry no
    longer has (half-deleted image) has no children; it must not abort the
    snapshot every night."""
    try:
        return reg.manifest(digest)
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return digest, []
        raise


def snapshot_registry(image):
    reg = Registry(image)
    tag_list = reg.tags()
    tags, children = {}, {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        results = list(pool.map(reg.manifest, tag_list))
    nested = set()
    for tag, (digest, kids) in zip(tag_list, results):
        tags[tag] = digest
        if kids:
            children[digest] = [k for k, _ in kids]
            nested |= {k for k, media in kids if media in INDEX_TYPES}
    # Children of an index are normally image manifests, which have no
    # children. The index entry carries the child's media type, so only a
    # child that is itself an index needs fetching.
    while nested - set(children):
        batch = sorted(nested - set(children))
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            for digest, kids in pool.map(_manifest_or_gone, [reg] * len(batch), batch):
                children[digest] = [k for k, _ in kids]
                nested |= {k for k, media in kids if media in INDEX_TYPES}
    # Subjects of referrer tags that are not reachable from any tag: ask the
    # registry whether they still exist.
    reachable = set(tags.values()) | {k for ks in children.values() for k in ks}
    subjects = ["sha256:" + m.group(1) for t in tags if (m := REFERRER_TAG.match(t))]
    unknown = [s for s in subjects if s not in reachable]
    missing = [s for s in unknown if not reg.exists(s)]
    return {"image": image, "tags": tags, "children": children, "missing": missing}


# --- GitHub Packages API (apply mode only) -----------------------------------


class Packages:
    def __init__(self, org, package, token):
        self.base = f"https://api.github.com/orgs/{org}/packages/container/{package}"
        self.token = token

    def _req(self, url, method="GET"):
        req = urllib.request.Request(url, method=method)
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Accept", "application/vnd.github+json")
        req.add_header("X-GitHub-Api-Version", "2022-11-28")
        return urllib.request.urlopen(req, timeout=60)

    def versions(self):
        """Map digest -> (version id, tags)."""
        out, url = {}, self.base + "/versions?per_page=100"
        while url:
            with self._req(url) as r:
                for v in json.load(r):
                    out[v["name"]] = (v["id"], v["metadata"]["container"]["tags"])
                link = r.headers.get("Link", "")
            url = next_link({"Link": link})
        return out

    def delete(self, version_id):
        """Delete a version; return X-RateLimit-Remaining if the API sent it."""
        with self._req(f"{self.base}/versions/{version_id}", method="DELETE") as r:
            remaining = r.headers.get("X-RateLimit-Remaining")
        return int(remaining) if remaining and remaining.isdigit() else None


def deletable_now(version_tags, result):
    """Re-check a version's live tags against the plan's rules.

    A version may carry only expired nightly tags, or the referrer tag of a
    subject that is itself being deleted (or is already gone). Anything else
    was tagged after the snapshot and keeps the version.
    """
    for t in version_tags:
        if expired_nightly_tag(t, result["cutoff"]):
            continue
        m = REFERRER_TAG.match(t)
        if m and ("sha256:" + m.group(1) in result["delete"]
                  or t in result["orphans"]):
            continue
        return False
    return True


def packages_for(image, token):
    _host, _, repo = image.partition("/")
    org, _, package = repo.partition("/")
    return Packages(org, package, token)


class Outcome:
    """What an apply run did and why it stopped (stopped is None if it finished)."""

    def __init__(self):
        self.deleted = self.skipped = self.failed = self.net_errors = 0
        self.unprocessed = 0
        self.aborted_images = 0
        self.stopped = None  # max-deletes | rate-limit | forbidden | network | errors

    @property
    def exit_code(self):
        if self.failed or self.aborted_images or self.stopped in ("forbidden", "network", "errors"):
            return 1
        if self.stopped == "rate-limit" and not self.deleted:
            return 1  # no progress at all: do not let a green run hide it
        return 0

    def summary(self):
        text = (f"deleted {self.deleted}, skipped {self.skipped}, failed {self.failed}, "
                f"network errors {self.net_errors}, images left half-done {self.aborted_images}")
        if self.stopped:
            text += f"; stopped early ({self.stopped}), {self.unprocessed} planned versions left for the next run"
        return text


def rate_limited(err):
    """True if an HTTPError is GitHub saying 'slow down' rather than 'no'."""
    if err.code == 429:
        return True
    if err.code != 403:
        return False
    h = err.headers or {}
    return h.get("X-RateLimit-Remaining") == "0" or h.get("Retry-After") is not None


def _error_body(err):
    try:
        return err.fp.read()[:200] if err.fp else b""
    except OSError:
        return b""


def apply(result, api, log, max_deletes=DEFAULT_MAX_DELETES,
          interval=0.0, sleep=time.sleep):
    """Delete the plan's manifests through `api`; return an Outcome.

    Stops (without raising) at max_deletes, on a rate limit, on a permission
    403, or after MAX_CONSECUTIVE_NET_ERRORS network errors in a row. The
    remaining versions are picked up by the next run, which re-plans from a
    fresh snapshot. Raises PlanError, before any delete, if the snapshot
    disagrees with the Packages API.
    """
    live = api.versions()
    check_against_live(result, live)
    out = Outcome()
    # Decide which images are off limits before deleting anything. The live
    # tags are the freshest view we have: an image whose root gained a tag
    # since the snapshot (a stable release retagging a nightly) is skipped as
    # a whole, children included, and so is everything it shares with others.
    # This sees tagged versions only; see the module docstring.
    held_back = set()
    for g in result["groups"]:
        if g["root"] in live and not deletable_now(live[g["root"]][1], result):
            log(f"SKIP image {g['root']}: live tags {live[g['root']][1]} no longer match the plan")
            held_back |= set(g["members"])
    for g in result["groups"]:
        for m in g["members"]:
            if m in live and m not in held_back and not deletable_now(live[m][1], result):
                log(f"SKIP {m}: live tags {live[m][1]} no longer match the plan")
                held_back.add(m)
    # Within an image: dependencies first, the tagged root last (see plan()).
    # If the run dies or a delete fails mid-image the root is still tagged, so
    # the next run plans the same image again and finishes it. Hence a failed
    # delete abandons the rest of its image: deleting on, and above all
    # deleting the root, would strand the member that failed.
    planned = set()
    todo = []
    for g in result["groups"]:
        members = [m for m in g["members"] if m not in held_back and m not in planned]
        planned.update(members)
        todo.append([m for m in members if m in live])
    out.skipped = len(planned - set(live))  # already gone
    out.skipped += len({m for g in result["groups"] for m in g["members"]} & held_back & set(live))
    consecutive_net = consecutive_http = 0
    attempts = 0  # failures count against the cap too
    for gi, members in enumerate(todo):
        if not members:
            continue
        left = sum(len(g) for g in todo[gi:])
        # Never split an image across runs; the first image always runs so a
        # small --max-deletes cannot stall the drain.
        if attempts and attempts + len(members) > max_deletes:
            out.stopped, out.unprocessed = "max-deletes", left
            log(f"reached --max-deletes {max_deletes}")
            break
        stop = abandon = False
        for mi, digest in enumerate(members):
            handled = mi + 1
            version_id = live[digest][0]
            attempts += 1
            try:
                remaining = api.delete(version_id)
            except urllib.error.HTTPError as e:
                if rate_limited(e):
                    attempts -= 1
                    out.stopped = "rate-limit"
                    handled = mi  # this version was not deleted
                    h = e.headers or {}
                    log(f"rate limited at version {version_id}: HTTP {e.code}, "
                        f"Retry-After={h.get('Retry-After')}, "
                        f"X-RateLimit-Reset={h.get('X-RateLimit-Reset')}; stopping")
                    stop = True
                else:
                    out.failed += 1
                    consecutive_http += 1
                    abandon = True
                    body = _error_body(e)
                    log(f"FAIL {digest} (version {version_id}): HTTP {e.code} {body!r}")
                    # GitHub refuses to delete a public package version with
                    # more than 5,000 downloads. That is per version, so it
                    # must not stop the run.
                    if e.code in (401, 403) and b"download" not in body.lower():
                        # Every later delete would fail the same way.
                        out.stopped = "forbidden"
                        log("token lacks permission to delete package versions; stopping")
                        stop = True
                    elif consecutive_http >= MAX_CONSECUTIVE_FAILURES:
                        out.stopped = "errors"
                        log(f"{consecutive_http} failed deletes in a row, none succeeded; stopping")
                        stop = True
            except OSError as e:  # URLError, socket timeouts, resets
                out.net_errors += 1
                consecutive_net += 1
                abandon = True
                log(f"NETWORK {digest} (version {version_id}): {e!r}")
                if consecutive_net >= MAX_CONSECUTIVE_NET_ERRORS:
                    out.stopped = "network"
                    log(f"{consecutive_net} network errors in a row; stopping")
                    stop = True
            else:
                consecutive_net = consecutive_http = 0
                out.deleted += 1
                if remaining is not None and remaining <= RATE_LIMIT_FLOOR:
                    out.stopped = "rate-limit"
                    log(f"X-RateLimit-Remaining is {remaining}; stopping before the limit")
                    stop = True
            if interval and not stop:
                sleep(interval)  # after failures too: do not hammer a failing API
            if stop:
                out.unprocessed = left - handled
                break
            if abandon:
                out.aborted_images += 1
                log(f"ABANDON image {members[-1]}: {len(members) - mi - 1} versions left, "
                    "root not deleted; the next run finishes it")
                break
        if stop:
            break
    return out


def _referrer_digests(result):
    for digest, tags in result["tags_by_digest"].items():
        for t in tags:
            if REFERRER_TAG.match(t):
                yield t, digest


def report(result, log):
    tbd = result["tags_by_digest"]
    log(f"expired nightly images: {len(result['expired'])}")
    log(f"orphaned referrer tags: {len(result['orphans'])}")
    log(f"manifests to delete:    {len(result['delete'])}")
    log(f"protected manifests:    {len(result['protected'])}")
    for d in result["expired"]:
        log(f"  expire {','.join(sorted(tbd[d]))} {d}")
    for t in result["orphans"]:
        log(f"  orphan {t}")
    sig = [t for t, d in _referrer_digests(result) if d in result["delete"]]
    for t in sorted(sig):
        log(f"  delete signature/attestation tag {t}")


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    p.add_argument("--image", default="ghcr.io/conduitio/conduit")
    p.add_argument("--keep-days", type=int, default=7)
    p.add_argument("--today", help="YYYY-MM-DD, default: today in UTC")
    p.add_argument("--snapshot-in")
    p.add_argument("--snapshot-out")
    p.add_argument("--apply", action="store_true", help="delete; default is a dry run")
    p.add_argument("--max-deletes", type=int, default=DEFAULT_MAX_DELETES,
                   help="delete at most this many versions per run (default %(default)s)")
    p.add_argument("--delete-interval", type=float, default=DEFAULT_DELETE_INTERVAL,
                   help="seconds between deletes (default %(default)s)")
    args = p.parse_args(argv)
    if args.max_deletes < 1:
        p.error("--max-deletes must be at least 1")

    def error(msg):
        print(f"::error::{msg}", file=sys.stderr)

    try:
        if args.snapshot_in:
            with open(args.snapshot_in) as f:
                snap = json.load(f)
        else:
            snap = snapshot_registry(args.image)
    except (OSError, RuntimeError, ValueError) as e:  # network, HTTP, bad JSON
        error(f"could not read the registry, nothing deleted: {e!r}")
        return 1
    if args.snapshot_out:
        with open(args.snapshot_out, "w") as f:
            json.dump(snap, f, indent=1, sort_keys=True)

    today = (datetime.date.fromisoformat(args.today) if args.today
             else datetime.datetime.now(datetime.timezone.utc).date())
    log = lambda s: print(s, flush=True)  # noqa: E731
    try:
        sanity_check(snap)
        result = plan(snap, today, args.keep_days)
    except PlanError as e:
        error(f"refusing to delete anything: {e}")
        return 2
    report(result, log)

    if not args.apply:
        log("dry run: nothing deleted")
        return 0
    token = os.environ.get("GH_TOKEN")
    if not token:
        error("--apply needs GH_TOKEN")
        return 2
    try:
        out = apply(result, packages_for(args.image, token), log,
                    max_deletes=args.max_deletes, interval=args.delete_interval)
    except PlanError as e:
        error(f"refusing to delete anything: {e}")
        return 2
    except OSError as e:  # listing versions failed; nothing was deleted yet
        error(f"could not list package versions, nothing deleted: {e!r}")
        return 1
    log(out.summary())
    if out.exit_code:
        error(f"cleanup incomplete: {out.summary()}")
    elif out.stopped:
        print(f"::warning::cleanup stopped early ({out.stopped}); "
              f"{out.unprocessed} versions remain for the next run", file=sys.stderr)
    return out.exit_code


if __name__ == "__main__":
    sys.exit(main())
