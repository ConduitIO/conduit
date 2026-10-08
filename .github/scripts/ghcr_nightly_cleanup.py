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
release retagging a digest mid-run) still protects it. Any failed delete
makes the script exit 1; the old step logged 2,219 failed deletes and
reported success.

Usage:
  ghcr_nightly_cleanup.py --image ghcr.io/conduitio/conduit --keep-days 7 [--apply]
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

    return {
        "cutoff": cutoff,
        "expired": sorted(expired, key=lambda d: sorted(tags_by_digest[d])),
        "orphans": sorted(orphans),
        "delete": delete,
        "protected": protected,
        "tags_by_digest": tags_by_digest,
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
            m = re.search(r"<([^>]+)>;\s*rel=\"next\"", link)
            path = "/tags/list?" + urllib.parse.urlparse(m.group(1)).query if m else None
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
            for digest, kids in pool.map(reg.manifest, batch):
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
            m = re.search(r"<([^>]+)>;\s*rel=\"next\"", link)
            url = m.group(1) if m else None
        return out

    def delete(self, version_id):
        with self._req(f"{self.base}/versions/{version_id}", method="DELETE"):
            pass


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


def apply(result, api, log):
    """Delete the plan's manifests through `api`; return (deleted, skipped, failed)."""
    live = api.versions()
    # Roots first, then referrer indexes, then the rest. If a run dies part
    # way, a root deleted without its signature leaves an orphaned referrer
    # tag, which the next run's orphan sweep deletes.
    roots = set(result["expired"])
    ref_indexes = {d for _tag, d in _referrer_digests(result)}
    order = sorted(result["delete"], key=lambda d: (d not in roots, d not in ref_indexes, d))
    deleted, skipped, failed = 0, 0, 0
    for digest in order:
        if digest not in live:
            skipped += 1  # already gone
            continue
        version_id, version_tags = live[digest]
        if not deletable_now(version_tags, result):
            log(f"SKIP {digest}: live tags {version_tags} no longer match the plan")
            skipped += 1
            continue
        try:
            api.delete(version_id)
            deleted += 1
        except urllib.error.HTTPError as e:
            failed += 1
            body = e.fp.read()[:200] if e.fp else b""
            log(f"FAIL {digest} (version {version_id}): HTTP {e.code} {body!r}")
    return deleted, skipped, failed


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


def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    p.add_argument("--image", default="ghcr.io/conduitio/conduit")
    p.add_argument("--keep-days", type=int, default=7)
    p.add_argument("--today", help="YYYY-MM-DD, default: today in UTC")
    p.add_argument("--snapshot-in")
    p.add_argument("--snapshot-out")
    p.add_argument("--apply", action="store_true", help="delete; default is a dry run")
    args = p.parse_args()

    if args.snapshot_in:
        with open(args.snapshot_in) as f:
            snap = json.load(f)
    else:
        snap = snapshot_registry(args.image)
    if args.snapshot_out:
        with open(args.snapshot_out, "w") as f:
            json.dump(snap, f, indent=1, sort_keys=True)

    today = (datetime.date.fromisoformat(args.today) if args.today
             else datetime.datetime.now(datetime.timezone.utc).date())
    log = lambda s: print(s, flush=True)  # noqa: E731
    try:
        result = plan(snap, today, args.keep_days)
    except PlanError as e:
        print(f"::error::refusing to delete anything: {e}", file=sys.stderr)
        return 2
    report(result, log)

    if not args.apply:
        log("dry run: nothing deleted")
        return 0
    token = os.environ.get("GH_TOKEN")
    if not token:
        print("::error::--apply needs GH_TOKEN", file=sys.stderr)
        return 2
    deleted, skipped, failed = apply(result, packages_for(args.image, token), log)
    log(f"deleted {deleted}, skipped {skipped}, failed {failed}")
    if failed:
        print(f"::error::{failed} package version deletes failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
