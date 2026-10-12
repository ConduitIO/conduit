#!/usr/bin/env python3
"""Tests for ghcr_nightly_cleanup.py. Run: python3 -m unittest -v (from this dir).

The fixture is the real ghcr.io/conduitio/conduit tag list on 2026-10-08,
the morning after the first two signed nightlies (20261007, 20261008). Each
signed nightly has a `sha256-<digest>` referrer tag pointing at an OCI index
that lists one Sigstore bundle manifest.
"""

import contextlib
import copy
import datetime
import hashlib
import io
import json
import os
import unittest
import urllib.error
from unittest import mock

import ghcr_nightly_cleanup as c

HERE = os.path.dirname(os.path.abspath(__file__))

N1007 = "sha256:6f0e60eeb40cf4b505e563d553d400ba3fe028139a1efd7c48dea8a3b4c1b100"
N1008 = "sha256:e23dc585e1c2005007be5f9cb60daa975d5fc905b5201c31f1ef8d741c64c95b"
SIG1007_INDEX = "sha256:a1eb72b3163b79731a2bf387d1c5261352908dcdad92aafc4926372ae329e319"
SIG1007_BUNDLE = "sha256:e9d18df5cf8ad7c29d792cc9de5dd70a3f4fcceaef631346ed4d7e3a70c7a377"
SIG1008_INDEX = "sha256:2746d8a61f7b0f7056366936d2b645e34c0dc30df8e4e351eac0025e71728be2"
SIG1008_BUNDLE = "sha256:acfbd7772e95f64a478d07a7df32f816f7824f40622148aa89d38fc208b32d89"
V019 = "sha256:e8e3cab2e6e0511a398d75d20bbcd90162e56e6c1ecee2c5db8333e36465e35d"


def d(n):
    """A fake digest, distinct from every real one."""
    return "sha256:" + format(n, "064x")


def real():
    with open(os.path.join(HERE, "testdata", "ghcr-snapshot-20261008.json")) as f:
        return json.load(f)


def day(s):
    return datetime.date.fromisoformat(s)


class RealSnapshot(unittest.TestCase):
    def test_fixture_is_what_the_registry_had(self):
        s = real()
        self.assertEqual(s["tags"]["v0.20.0-nightly.20261007"], N1007)
        self.assertEqual(s["tags"]["v0.20.0-nightly.20261008"], N1008)
        self.assertEqual(s["tags"]["latest-nightly"], N1008)
        self.assertEqual(s["tags"]["latest"], V019)
        self.assertEqual(s["tags"]["sha256-" + N1007[7:]], SIG1007_INDEX)
        self.assertEqual(s["children"][SIG1007_INDEX], [SIG1007_BUNDLE])
        refs = sorted(t for t in s["tags"] if c.REFERRER_TAG.match(t))
        self.assertEqual(refs, ["sha256-" + N1007[7:], "sha256-" + N1008[7:]])

    def test_today_keeps_the_last_week_and_both_signatures(self):
        r = c.plan(real(), day("2026-10-08"), 7)
        expired_tags = {t for x in r["expired"] for t in r["tags_by_digest"][x]}
        self.assertEqual(len(r["expired"]), 495)
        self.assertIn("v0.20.0-nightly.20260930", expired_tags)
        self.assertNotIn("v0.20.0-nightly.20261001", expired_tags)
        for x in (N1007, N1008, SIG1007_INDEX, SIG1007_BUNDLE, SIG1008_INDEX, SIG1008_BUNDLE):
            self.assertNotIn(x, r["delete"])
            self.assertIn(x, r["protected"])
        self.assertEqual(r["orphans"], [])

    def test_expired_signed_nightly_takes_its_signature_with_it(self):
        # 2026-10-15: 20261007 is past the 7 days, 20261008 is not.
        r = c.plan(real(), day("2026-10-15"), 7)
        self.assertIn(N1007, r["expired"])
        for x in (N1007, SIG1007_INDEX, SIG1007_BUNDLE):
            self.assertIn(x, r["delete"])
        for child in real()["children"][N1007]:  # platform + provenance manifests
            self.assertIn(child, r["delete"])
        for x in (N1008, SIG1008_INDEX, SIG1008_BUNDLE):
            self.assertNotIn(x, r["delete"])

    def test_latest_nightly_keeps_an_old_nightly(self):
        # If the nightly train stops, latest-nightly keeps pointing at the
        # last one; it must survive however old it gets.
        r = c.plan(real(), day("2027-01-01"), 7)
        for x in (N1008, SIG1008_INDEX, SIG1008_BUNDLE):
            self.assertNotIn(x, r["delete"])
        self.assertIn(N1007, r["delete"])

    def test_no_stable_image_or_child_is_ever_deleted(self):
        s = real()
        r = c.plan(s, day("2030-01-01"), 7)
        for tag, digest in s["tags"].items():
            if c.NIGHTLY_TAG.match(tag) or c.REFERRER_TAG.match(tag):
                continue
            self.assertNotIn(digest, r["delete"], tag)
            for child in s["children"].get(digest, []):
                self.assertNotIn(child, r["delete"], f"child of {tag}")


class StableSignatures(unittest.TestCase):
    """v0.20.0 will be signed exactly like a nightly. Its artifacts must stay."""

    def snap_with_signed_stable(self):
        s = real()
        stable, plat_a, plat_b = d(1), d(2), d(3)
        sig_idx, bundle = d(4), d(5)
        att_idx, att = d(6), d(7)
        s["tags"].update({
            "v0.20.0": stable, "v0.20": stable, "latest": stable,
            "sha256-" + stable[7:]: sig_idx,
            "sha256-" + plat_a[7:]: att_idx,  # a per-platform SBOM attestation
        })
        s["children"].update({stable: [plat_a, plat_b], sig_idx: [bundle], att_idx: [att]})
        return s, [stable, plat_a, plat_b, sig_idx, bundle, att_idx, att]

    def test_stable_signature_and_attestations_are_protected(self):
        s, stable_set = self.snap_with_signed_stable()
        r = c.plan(s, day("2030-01-01"), 7)
        for x in stable_set:
            self.assertNotIn(x, r["delete"])
            self.assertIn(x, r["protected"])

    def test_stable_and_nightly_tag_on_one_digest_keeps_it(self):
        s = real()
        s["tags"]["v0.20.0"] = N1007  # someone promoted a nightly by retagging
        r = c.plan(s, day("2030-01-01"), 7)
        for x in (N1007, SIG1007_INDEX, SIG1007_BUNDLE):
            self.assertNotIn(x, r["delete"])

    def test_child_shared_with_a_stable_image_is_protected(self):
        s = real()
        shared = s["children"][N1007][0]
        s["tags"]["v0.20.0"] = d(10)
        s["children"][d(10)] = [shared, d(11)]
        r = c.plan(s, day("2026-10-15"), 7)
        self.assertIn(N1007, r["delete"])
        self.assertNotIn(shared, r["delete"])


class PerPlatformAttestations(unittest.TestCase):
    def test_nightly_platform_attestations_go_with_the_nightly(self):
        s = real()
        plat = s["children"][N1007][0]
        s["tags"]["sha256-" + plat[7:]] = d(20)
        s["children"][d(20)] = [d(21), d(22)]  # sbom attestation + its signature
        r = c.plan(s, day("2026-10-15"), 7)
        for x in (plat, d(20), d(21), d(22)):
            self.assertIn(x, r["delete"])

    def test_kept_nightly_platform_attestations_stay(self):
        s = real()
        plat = s["children"][N1008][0]
        s["tags"]["sha256-" + plat[7:]] = d(30)
        s["children"][d(30)] = [d(31)]
        r = c.plan(s, day("2026-10-15"), 7)
        for x in (plat, d(30), d(31)):
            self.assertNotIn(x, r["delete"])


class LegacyCosignTags(unittest.TestCase):
    """cosign's legacy format: `sha256-<hex>.sig` / `.att` image manifests."""

    def test_legacy_tags_follow_their_subject(self):
        s = real()
        s["tags"]["sha256-" + N1007[7:] + ".sig"] = d(70)
        s["tags"]["sha256-" + N1007[7:] + ".att"] = d(71)
        s["tags"]["sha256-" + V019[7:] + ".sig"] = d(72)
        r = c.plan(s, day("2026-10-15"), 7)
        self.assertIn(d(70), r["delete"])
        self.assertIn(d(71), r["delete"])
        self.assertNotIn(d(72), r["delete"])
        self.assertIn(d(72), r["protected"])


class Malformed(unittest.TestCase):
    def test_malformed_digest_refuses_to_plan(self):
        s = real()
        s["tags"]["v0.1.0-nightly.20200101"] = "sha256:nothex"
        with self.assertRaises(c.PlanError):
            c.plan(s, day("2026-10-08"), 7)


class Orphans(unittest.TestCase):
    def test_referrer_tag_of_a_deleted_subject_is_swept(self):
        s = real()
        gone = d(40)
        s["tags"]["sha256-" + gone[7:]] = d(41)
        s["children"][d(41)] = [d(42)]
        s["missing"] = [gone]
        r = c.plan(s, day("2026-10-08"), 7)
        self.assertEqual(r["orphans"], ["sha256-" + gone[7:]])
        self.assertIn(d(41), r["delete"])
        self.assertIn(d(42), r["delete"])

    def test_referrer_tag_of_an_existing_untagged_subject_is_not_an_orphan(self):
        s = real()
        s["tags"]["sha256-" + d(50)[7:]] = d(51)  # subject exists, just untagged
        r = c.plan(s, day("2026-10-08"), 7)
        self.assertEqual(r["orphans"], [])
        self.assertNotIn(d(51), r["delete"])


class Tags(unittest.TestCase):
    def test_tag_patterns(self):
        cutoff = day("2026-10-01")
        self.assertTrue(c.expired_nightly_tag("v0.20.0-nightly.20260930", cutoff))
        self.assertFalse(c.expired_nightly_tag("v0.20.0-nightly.20261001", cutoff))
        self.assertFalse(c.expired_nightly_tag("v0.20.0-nightly.20261399", cutoff))  # no such date
        self.assertFalse(c.expired_nightly_tag("v0.20.0-nightly.2026093", cutoff))
        self.assertFalse(c.expired_nightly_tag("v0.20.0-nightly.20260930-fix", cutoff))
        self.assertFalse(c.expired_nightly_tag("latest-nightly", cutoff))
        self.assertFalse(c.expired_nightly_tag("v0.19.0", cutoff))
        self.assertTrue(c.REFERRER_TAG.match("sha256-" + "a" * 64))
        self.assertFalse(c.REFERRER_TAG.match("sha256-" + "a" * 63))
        self.assertTrue(c.REFERRER_TAG.match("sha256-" + "a" * 64 + ".sig"))
        self.assertTrue(c.REFERRER_TAG.match("sha256-" + "a" * 64 + ".att"))
        self.assertFalse(c.REFERRER_TAG.match("sha256-" + "a" * 64 + ".foo"))

    def test_untagged_or_odd_tags_are_never_roots(self):
        s = {"tags": {"pr-123": d(60), "v0.20.0-rc.1": d(61)}, "children": {}, "missing": []}
        r = c.plan(s, day("2030-01-01"), 7)
        self.assertEqual(r["expired"], [])
        self.assertEqual(r["delete"], set())


class FakePackages:
    """Stands in for ghcr_nightly_cleanup.Packages.

    errors: {version id: exception to raise from delete}.
    remaining: X-RateLimit-Remaining value each successful delete reports.
    """

    def __init__(self, live, errors=None, remaining=None):
        self.live = live
        self.errors = errors or {}
        self.remaining = remaining
        self.deleted = []

    def versions(self):
        return self.live

    def delete(self, version_id):
        if version_id in self.errors:
            raise self.errors[version_id]
        self.deleted.append(version_id)
        return self.remaining


def delete_order(result):
    """Digests in the order apply() deletes them: image by image, dependencies first, root last."""
    seen, out = set(), []
    for g in result["groups"]:
        for m in g["members"]:
            if m not in seen:
                seen.add(m)
                out.append(m)
    return out


def http_error(code, headers=None):
    return urllib.error.HTTPError("u", code, "msg", headers or {}, io.BytesIO(b"{}"))


class Apply(unittest.TestCase):
    def setUp(self):
        self.r = c.plan(real(), day("2026-10-15"), 7)
        self.live = {x: (i, []) for i, x in enumerate(sorted(self.r["delete"]))}
        self.live[N1007] = (9001, ["v0.20.0-nightly.20261007"])
        self.live[SIG1007_INDEX] = (9002, ["sha256-" + N1007[7:]])
        self.logs = []

    def run_apply(self, api, **kw):
        kw.setdefault("max_deletes", 10**6)  # the real default (400) is tested explicitly
        return c.apply(self.r, api, self.logs.append, **kw)

    def test_deletes_each_image_as_a_unit_children_and_signature_before_root(self):
        api = FakePackages(self.live)
        out = self.run_apply(api)
        self.assertEqual((out.skipped, out.failed, out.stopped), (0, 0, None))
        self.assertEqual(out.deleted, len(self.r["delete"]))
        self.assertEqual(out.exit_code, 0)
        group = next(g for g in self.r["groups"] if g["root"] == N1007)
        ids = [self.live[m][0] for m in group["members"]]
        pos = sorted(api.deleted.index(i) for i in ids)
        self.assertEqual(pos, list(range(pos[0], pos[0] + len(ids))))  # contiguous
        self.assertEqual(api.deleted[pos[-1]], 9001)  # the tagged root goes last
        self.assertLess(api.deleted.index(9002), api.deleted.index(9001))  # signature before it

    def test_every_planned_digest_belongs_to_a_group(self):
        grouped = {m for g in self.r["groups"] for m in g["members"]}
        self.assertEqual(grouped, self.r["delete"])

    def test_version_retagged_after_snapshot_keeps_the_whole_image(self):
        live = copy.deepcopy(self.live)
        live[N1007] = (9001, ["v0.20.0-nightly.20261007", "v0.20.0"])
        api = FakePackages(live)
        out = self.run_apply(api)
        group = next(g for g in self.r["groups"] if g["root"] == N1007)
        # Not just the root: its platform manifests, signature and bundle stay too.
        for m in group["members"]:
            self.assertNotIn(live[m][0], api.deleted, m)
        self.assertEqual((out.skipped, out.failed), (len(group["members"]), 0))
        self.assertEqual(out.deleted, len(self.r["delete"]) - len(group["members"]))

    def test_child_shared_with_a_held_back_image_is_not_deleted_via_the_other(self):
        s = real()
        shared = s["children"][N1007][0]
        s["tags"]["v0.20.0-nightly.20261006"] = d(80)  # a second expired image
        s["children"][d(80)] = [shared, d(81)]
        r = c.plan(s, day("2026-10-15"), 7)
        live = {x: (i, []) for i, x in enumerate(sorted(r["delete"]))}
        live[d(80)] = (8000, ["v0.20.0-nightly.20261006"])
        live[N1007] = (9001, ["v0.20.0-nightly.20261007", "v0.20.0"])  # retagged: held back
        api = FakePackages(live)
        c.apply(r, api, self.logs.append, max_deletes=10**6)
        self.assertNotIn(live[shared][0], api.deleted)  # N1007 still needs it
        self.assertIn(8000, api.deleted)
        self.assertIn(live[d(81)][0], api.deleted)

    def test_already_gone_is_skipped_not_failed(self):
        live = copy.deepcopy(self.live)
        del live[SIG1007_BUNDLE]
        out = self.run_apply(FakePackages(live))
        self.assertEqual((out.skipped, out.failed), (1, 0))

    def test_failed_delete_is_counted(self):
        api = FakePackages(self.live, errors={9001: http_error(404)})
        out = self.run_apply(api)
        self.assertEqual(out.failed, 1)
        self.assertIsNone(out.stopped)  # a 404 on one version is not fatal
        self.assertEqual(out.exit_code, 1)
        self.assertTrue(any(line.startswith("FAIL") for line in self.logs))

    def test_deletable_now(self):
        r = self.r
        self.assertTrue(c.deletable_now([], r))
        self.assertTrue(c.deletable_now(["v0.20.0-nightly.20261007"], r))
        self.assertTrue(c.deletable_now(["sha256-" + N1007[7:]], r))
        self.assertFalse(c.deletable_now(["sha256-" + N1008[7:]], r))
        self.assertFalse(c.deletable_now(["v0.20.0-nightly.20261008"], r))
        self.assertFalse(c.deletable_now(["v0.20.0-nightly.20261007", "latest"], r))

    def test_the_recheck_only_sees_tagged_versions(self):
        # Documented limit: an UNTAGGED version in the plan is deleted without a
        # recheck, because there is nothing to recheck it against.
        untagged = next(x for x in self.r["delete"] if self.live[x][1] == [])
        api = FakePackages(self.live)
        self.run_apply(api)
        self.assertIn(self.live[untagged][0], api.deleted)

    # --- caps and rate limits ---

    def test_max_deletes_caps_the_run_without_splitting_an_image(self):
        api = FakePackages(self.live)
        out = self.run_apply(api, max_deletes=20)
        self.assertEqual(out.stopped, "max-deletes")
        self.assertEqual(out.exit_code, 0)  # expected: the next run continues
        self.assertLessEqual(out.deleted, 20)
        self.assertGreater(out.deleted, 0)
        self.assertEqual(out.deleted + out.unprocessed, len(self.r["delete"]))
        # Whatever was deleted is a union of whole images.
        gone = {x for x, (i, _t) in self.live.items() if i in set(api.deleted)}
        for g in self.r["groups"]:
            hit = [m in gone for m in g["members"]]
            self.assertIn(sum(hit), (0, len(hit)), g["root"])

    def test_a_tiny_cap_still_runs_one_whole_image(self):
        n, p1, p2, sig, bundle = d(1), d(2), d(3), d(4), d(5)
        snap = {"tags": {"latest": d(90), "v0.19.0": d(90),
                         "v0.20.0-nightly.20261001": n, "sha256-" + n[7:]: sig,
                         "v0.20.0-nightly.20261002": d(6)},
                "children": {n: [p1, p2], sig: [bundle], d(6): [d(7)]}}
        snap["tags"].update({f"pr-{i}": d(100 + i) for i in range(60)})
        r = c.plan(snap, day("2026-10-15"), 7)
        live = {x: (i, []) for i, x in enumerate(sorted(r["delete"]))}
        api = FakePackages(live)
        out = c.apply(r, api, self.logs.append, max_deletes=1)
        self.assertEqual((out.deleted, out.stopped), (5, "max-deletes"))  # image 1, not image 2
        self.assertEqual(out.unprocessed, 2)

    def test_the_cap_is_never_overshot_by_a_second_image(self):
        snap = {"tags": {"latest": d(90), "v0.19.0": d(90), **{f"pr-{i}": d(100 + i) for i in range(60)}},
                "children": {}}
        for k, day_ in enumerate(("20261001", "20261002")):  # two images of 5 versions each
            root, base = d(10 + 10 * k), 10 + 10 * k
            snap["tags"][f"v0.20.0-nightly.{day_}"] = root
            snap["tags"]["sha256-" + root[7:]] = d(base + 1)
            snap["children"][root] = [d(base + 2), d(base + 3)]
            snap["children"][d(base + 1)] = [d(base + 4)]
        r = c.plan(snap, day("2026-10-15"), 7)
        live = {x: (i, []) for i, x in enumerate(sorted(r["delete"]))}
        out = c.apply(r, FakePackages(live), self.logs.append, max_deletes=7)
        self.assertEqual((out.deleted, out.unprocessed, out.stopped), (5, 5, "max-deletes"))

    def test_capped_runs_drain_the_backlog_and_leak_nothing(self):
        # Replay the nightly cadence: each night re-plans from what is left.
        # The untagged children of a deleted image are only reachable through
        # it, so a run that stopped between an image and its children would
        # strand them forever.
        snap = real()
        everything = c.plan(snap, day("2026-10-15"), 7)["delete"]
        gone, nights = set(), 0
        while True:
            r = c.plan(snap, day("2026-10-15"), 7)
            if not r["delete"]:
                break
            nights += 1
            self.assertLess(nights, 50)
            tbd = r["tags_by_digest"]
            all_digests = set(snap["tags"].values()) | {k for ks in snap["children"].values() for k in ks}
            live = {x: (i, tbd.get(x, [])) for i, x in enumerate(sorted(all_digests))}
            api = FakePackages(live)
            out = c.apply(r, api, self.logs.append, max_deletes=60)
            self.assertGreater(out.deleted, 0)
            ids = set(api.deleted)
            night = {x for x, (i, _t) in live.items() if i in ids}
            self.assertFalse(night & r["protected"])
            gone |= night
            snap["tags"] = {t: x for t, x in snap["tags"].items() if x not in night}
            snap["children"] = {k: v for k, v in snap["children"].items() if k not in night}
        self.assertGreater(nights, 1)
        self.assertEqual(gone, everything)

    def test_default_cap_is_400(self):
        self.assertEqual(c.DEFAULT_MAX_DELETES, 400)
        api = FakePackages(self.live)
        out = c.apply(self.r, api, self.logs.append)
        self.assertEqual((out.deleted, out.stopped), (400, "max-deletes"))

    def test_cap_larger_than_the_plan_finishes(self):
        out = self.run_apply(FakePackages(self.live), max_deletes=10**6)
        self.assertIsNone(out.stopped)

    def test_429_aborts_without_failing_the_deletes_already_done(self):
        third = self.live[delete_order(self.r)[2]][0]
        api = FakePackages(self.live, errors={third: http_error(429, {"Retry-After": "60"})})
        out = self.run_apply(api)
        self.assertEqual(out.stopped, "rate-limit")
        self.assertEqual(out.deleted, 2)
        self.assertEqual(out.failed, 0)
        self.assertEqual(out.exit_code, 0)  # progress made: drain continues tomorrow
        self.assertEqual(out.deleted + out.unprocessed, len(self.r["delete"]))
        self.assertTrue(any("Retry-After=60" in line for line in self.logs))

    def test_403_with_exhausted_quota_is_a_rate_limit_not_a_permission_error(self):
        first = self.live[delete_order(self.r)[0]][0]
        api = FakePackages(self.live, errors={first: http_error(403, {"X-RateLimit-Remaining": "0"})})
        out = self.run_apply(api)
        self.assertEqual(out.stopped, "rate-limit")
        self.assertEqual(out.deleted, 0)
        self.assertEqual(out.exit_code, 1)  # no progress must not look green

    def test_plain_403_is_a_permission_error_and_stops_the_run(self):
        errors = {v[0]: http_error(403) for v in self.live.values()}
        api = FakePackages(self.live, errors=errors)
        out = self.run_apply(api)
        self.assertEqual(out.stopped, "forbidden")
        self.assertEqual(out.failed, 1)  # not one failure per remaining version
        self.assertEqual(out.exit_code, 1)

    def test_download_limit_refusal_is_per_version_and_does_not_stop_the_run(self):
        err = urllib.error.HTTPError("u", 403, "Forbidden", {}, io.BytesIO(
            b'{"message":"You cannot delete a public package version with more than 5,000 downloads"}'))
        api = FakePackages(self.live, errors={9001: err})
        out = self.run_apply(api)
        self.assertIsNone(out.stopped)
        self.assertEqual(out.failed, 1)
        self.assertEqual(out.deleted, len(self.r["delete"]) - 1)

    def test_low_x_ratelimit_remaining_stops_before_the_limit(self):
        api = FakePackages(self.live, remaining=c.RATE_LIMIT_FLOOR)
        out = self.run_apply(api)
        self.assertEqual(out.stopped, "rate-limit")
        self.assertEqual(out.deleted, 1)
        self.assertEqual(out.exit_code, 0)

    def test_healthy_x_ratelimit_remaining_does_not_stop(self):
        out = self.run_apply(FakePackages(self.live, remaining=c.RATE_LIMIT_FLOOR + 1))
        self.assertIsNone(out.stopped)

    def test_isolated_network_errors_are_counted_and_survived(self):
        first = self.live[delete_order(self.r)[0]][0]
        api = FakePackages(self.live, errors={first: urllib.error.URLError("reset")})
        out = self.run_apply(api)
        self.assertEqual(out.net_errors, 1)
        self.assertIsNone(out.stopped)
        self.assertEqual(out.deleted, len(self.r["delete"]) - 1)

    def test_consecutive_timeouts_abort_cleanly(self):
        errors = {v[0]: TimeoutError("timed out") for v in self.live.values()}
        api = FakePackages(self.live, errors=errors)
        out = self.run_apply(api)
        self.assertEqual(out.stopped, "network")
        self.assertEqual(out.net_errors, c.MAX_CONSECUTIVE_NET_ERRORS)
        self.assertEqual(out.exit_code, 1)
        self.assertIn("network errors 3", out.summary())

    def test_pacing_sleeps_between_deletes(self):
        sleeps = []
        self.run_apply(FakePackages(self.live), max_deletes=4, interval=0.5, sleep=sleeps.append)
        self.assertEqual(sleeps, [0.5] * 4)

    # --- snapshot vs Packages API ---

    def test_snapshot_far_smaller_than_the_live_package_aborts_before_any_delete(self):
        live = copy.deepcopy(self.live)
        for i in range(2000):  # the API knows 2000 tagged images the snapshot lacks
            live[d(900_000 + i)] = (500_000 + i, [f"v0.1.0-nightly.{20200101 + i}"])
        api = FakePackages(live)
        with self.assertRaises(c.PlanError):
            self.run_apply(api)
        self.assertEqual(api.deleted, [])


class SimRegistry:
    """A registry that apply() can delete from and snapshot_registry() can re-read.

    docs: {digest: [(child digest, media type)]}; tags: {tag: digest}. Deleting
    a digest removes its document and its tags, but leaves any surviving
    parent's list of it alone, as a real registry does.
    """

    IDX = "application/vnd.oci.image.index.v1+json"
    IMG = "application/vnd.oci.image.manifest.v1+json"

    def __init__(self, tags, children):
        self.tags = dict(tags)
        self.docs = {}
        for t in set(tags.values()) | {k for ks in children.values() for k in ks}:
            self.docs[t] = []
        for parent, kids in children.items():
            self.docs[parent] = [(k, self.IDX if k in children else self.IMG) for k in kids]

    # snapshot_registry() side
    def tag_names(self):
        return list(self.tags)

    def manifest(self, ref):
        digest = self.tags[ref] if ref in self.tags else ref
        if digest not in self.docs:
            raise http_error(404)
        return digest, list(self.docs[digest])

    def exists(self, digest):
        return digest in self.docs

    def snapshot(self):
        sim = self

        class R:
            def tags(self):
                return sim.tag_names()

            manifest = staticmethod(sim.manifest)
            exists = staticmethod(sim.exists)

        with mock.patch.object(c, "Registry", lambda image: R()):
            return c.snapshot_registry("ghcr.io/conduitio/conduit")

    # Packages API side
    def delete_digest(self, digest):
        self.docs.pop(digest, None)
        self.tags = {t: x for t, x in self.tags.items() if x != digest}

    def packages(self, fail_at=None, error=None):
        sim = self
        by_id = {}
        tagged = {}
        for t, x in self.tags.items():
            tagged.setdefault(x, []).append(t)
        live = {}
        for i, x in enumerate(sorted(self.docs)):
            live[x] = (i, tagged.get(x, []))
            by_id[i] = x

        class P:
            calls = 0
            deleted = []

            def versions(self):
                return live

            def delete(self, vid):
                n = P.calls
                P.calls += 1
                if fail_at is not None and n == fail_at:
                    raise error
                sim.delete_digest(by_id[vid])
                P.deleted.append(by_id[vid])

        return P()


def synthetic_signed_image():
    """A kept stable image, plus one expired nightly with 9 versions in its group:
    root R lists platforms A, B and attestation manifests; R, A have referrer indexes."""
    R, A, B, AT1, AT2 = d(1), d(2), d(3), d(4), d(5)
    SIG, BUNDLE, ASIG, ABUNDLE = d(6), d(7), d(8), d(9)
    tags = {"latest": d(90), "v0.19.0": d(90), "v0.20.0-nightly.20261001": R,
            "sha256-" + R[7:]: SIG, "sha256-" + A[7:]: ASIG}
    tags.update({f"pr-{i}": d(100 + i) for i in range(60)})
    children = {R: [A, B, AT1, AT2], SIG: [BUNDLE], ASIG: [ABUNDLE]}
    return tags, children, {R, A, B, AT1, AT2, SIG, BUNDLE, ASIG, ABUNDLE}


class DeletionOrder(unittest.TestCase):
    def test_every_manifest_comes_after_everything_it_lists_and_root_last(self):
        tags, children, group = synthetic_signed_image()
        r = c.plan({"tags": tags, "children": children, "missing": []}, day("2026-10-15"), 7)
        (g,) = r["groups"]
        order = g["members"]
        self.assertEqual(set(order), group)
        self.assertEqual(order[-1], d(1))
        pos = {m: i for i, m in enumerate(order)}
        for parent, kids in children.items():
            for k in kids:
                self.assertLess(pos[k], pos[parent], f"{k} must go before {parent}")
        # Referrer indexes are deleted before the manifest they are about.
        self.assertLess(pos[d(6)], pos[d(1)])
        self.assertLess(pos[d(8)], pos[d(2)])

    def test_real_signed_nightlies_delete_the_bundle_before_its_index(self):
        r = c.plan(real(), day("2026-10-15"), 7)
        order = next(g for g in r["groups"] if g["root"] == N1007)["members"]
        self.assertLess(order.index(SIG1007_BUNDLE), order.index(SIG1007_INDEX))
        self.assertLess(order.index(SIG1007_INDEX), order.index(N1007))
        self.assertEqual(order[-1], N1007)


class InterruptedImages(unittest.TestCase):
    """Whatever stops a run at whatever position, the next runs strand nothing."""

    def finish(self, sim, group, kept):
        """Replan from the (half-deleted) registry and delete with no faults."""
        for night in range(5):
            snap = sim.snapshot()
            r = c.plan(snap, day("2026-10-15"), 7)
            if not r["delete"]:
                break
            api = sim.packages()
            out = c.apply(r, api, lambda m: None, max_deletes=10**6)
            self.assertEqual((out.failed, out.stopped), (0, None))
        self.assertFalse(set(sim.docs) & group, f"stranded: {set(sim.docs) & group}")
        self.assertTrue(kept <= set(sim.docs), "a kept manifest was deleted")

    def run_with_fault_at(self, k, error):
        tags, children, group = synthetic_signed_image()
        sim = SimRegistry(tags, children)
        kept = set(sim.docs) - group
        r = c.plan(sim.snapshot(), day("2026-10-15"), 7)
        api = sim.packages(fail_at=k, error=error)
        out = c.apply(r, api, lambda m: None, max_deletes=10**6)
        return sim, group, kept, api, out

    def test_stop_for_a_rate_limit_at_every_position_strands_nothing(self):
        for k in range(len(synthetic_signed_image()[2]) + 1):
            with self.subTest(position=k):
                sim, group, kept, api, out = self.run_with_fault_at(k, http_error(429, {"Retry-After": "1"}))
                if k < len(group):
                    self.assertEqual(out.stopped, "rate-limit")
                    self.assertEqual(len(api.deleted), k)
                    self.assertIn(d(1), sim.docs)  # the tagged root is still there to re-plan from
                self.finish(sim, group, kept)

    def test_a_failed_delete_at_every_position_abandons_the_image_and_keeps_its_root(self):
        for kind, error in (("http 500", http_error(500)), ("http 404", http_error(404)),
                            ("network", urllib.error.URLError("reset")), ("timeout", TimeoutError("t"))):
            for k in range(len(synthetic_signed_image()[2])):
                with self.subTest(error=kind, position=k):
                    sim, group, kept, api, out = self.run_with_fault_at(k, error)
                    self.assertEqual(len(api.deleted), k)  # nothing after the failure
                    self.assertIn(d(1), sim.docs)  # never the root: it is the only way back
                    self.assertEqual(out.aborted_images, 1)
                    self.assertEqual(out.exit_code, 1)
                    self.finish(sim, group, kept)

    def test_old_order_would_have_stranded_a_child(self):
        # The failure the abandon rule exists for: delete on past a failed child,
        # then the root, and the child can never be found again.
        tags, children, group = synthetic_signed_image()
        sim = SimRegistry(tags, children)
        for m in group - {d(3)}:  # everything but platform B, as a run that skipped B would
            sim.delete_digest(m)
        snap = sim.snapshot()
        self.assertEqual(c.plan(snap, day("2026-10-15"), 7)["delete"], set())
        self.assertIn(d(3), sim.docs)  # B is stranded

    def test_half_deleted_nested_index_does_not_break_the_snapshot(self):
        top, inner, leaf = d(1), d(2), d(3)
        tags = {"v0.20.0-nightly.20261001": top, "latest": d(90), "v0.19.0": d(90)}
        tags.update({f"pr-{i}": d(100 + i) for i in range(60)})
        sim = SimRegistry(tags, {top: [inner], inner: [leaf]})
        sim.delete_digest(inner)  # parent still lists the nested index
        snap = sim.snapshot()
        self.assertEqual(snap["children"][inner], [])


class FailureBackoff(unittest.TestCase):
    def setUp(self):
        self.r = c.plan(real(), day("2026-10-15"), 7)
        self.live = {x: (i, []) for i, x in enumerate(sorted(self.r["delete"]))}
        self.live[N1007] = (9001, ["v0.20.0-nightly.20261007"])
        self.live[SIG1007_INDEX] = (9002, ["sha256-" + N1007[7:]])

    def test_every_delete_404s_stops_after_five(self):
        # A token without the Admin role on the package: 404 for every delete.
        errors = {v[0]: http_error(404) for v in self.live.values()}
        api = FakePackages(self.live, errors=errors)
        calls = []
        orig = api.delete
        api.delete = lambda vid: (calls.append(vid), orig(vid))[1]
        out = c.apply(self.r, api, lambda m: None, max_deletes=10**6)
        self.assertEqual(len(calls), c.MAX_CONSECUTIVE_FAILURES)
        self.assertEqual((out.deleted, out.failed, out.stopped), (0, 5, "errors"))
        self.assertEqual(out.exit_code, 1)

    def test_a_success_resets_the_failure_streak(self):
        order = delete_order(self.r)
        errors = {self.live[x][0]: http_error(500) for x in order[:4] + order[5:9]}
        api = FakePackages(self.live, errors=errors)
        out = c.apply(self.r, api, lambda m: None, max_deletes=10**6)
        self.assertIsNone(out.stopped)  # 4 failures, a success, 4 failures: never 5 in a row
        self.assertEqual(out.failed, 8)

    def test_failures_are_paced_like_successes(self):
        errors = {v[0]: http_error(500) for v in self.live.values()}
        sleeps = []
        c.apply(self.r, FakePackages(self.live, errors=errors), lambda m: None,
                max_deletes=10**6, interval=0.5, sleep=sleeps.append)
        self.assertEqual(sleeps, [0.5] * (c.MAX_CONSECUTIVE_FAILURES - 1))  # not after the final, stopping one

    def test_failures_count_against_the_cap(self):
        errors = {self.live[x][0]: http_error(500) for x in delete_order(self.r)[:3]}
        api = FakePackages(self.live, errors=errors)
        calls = []
        orig = api.delete
        api.delete = lambda vid: (calls.append(vid), orig(vid))[1]
        out = c.apply(self.r, api, lambda m: None, max_deletes=10)
        self.assertEqual(len(calls), 10)
        self.assertEqual(out.stopped, "max-deletes")

    def test_a_failed_image_does_not_block_the_next_one(self):
        tags, children, group = synthetic_signed_image()
        tags["v0.20.0-nightly.20261002"] = d(50)
        children[d(50)] = [d(51)]
        r = c.plan({"tags": tags, "children": children, "missing": []}, day("2026-10-15"), 7)
        live = {x: (i, []) for i, x in enumerate(sorted(r["delete"]))}
        first = r["groups"][0]["members"][0]
        api = FakePackages(live, errors={live[first][0]: http_error(500)})
        out = c.apply(r, api, lambda m: None, max_deletes=10**6)
        self.assertEqual(out.aborted_images, 1)
        self.assertIn(live[d(51)][0], api.deleted)  # the second image went through
        self.assertIn(live[d(50)][0], api.deleted)
        self.assertNotIn(live[d(1)][0], api.deleted)  # the first image's root did not


class SanityCheck(unittest.TestCase):
    def test_real_snapshot_passes(self):
        c.sanity_check(real())

    def test_missing_latest_aborts(self):
        s = real()
        del s["tags"]["latest"]
        with self.assertRaisesRegex(c.PlanError, "latest"):
            c.sanity_check(s)

    def test_no_stable_release_tag_aborts(self):
        s = real()
        s["tags"] = {t: x for t, x in s["tags"].items() if not c.STABLE_RELEASE_TAG.match(t)}
        with self.assertRaisesRegex(c.PlanError, "stable"):
            c.sanity_check(s)

    def test_minor_only_alias_is_not_a_stable_release(self):
        s = {"tags": {"latest": d(1), "v0.20": d(1), **{f"x{i}": d(2) for i in range(60)}}}
        with self.assertRaisesRegex(c.PlanError, "stable"):
            c.sanity_check(s)

    def test_implausibly_few_tags_aborts(self):
        s = {"tags": {"latest": d(1), "v0.19.0": d(1)}}
        with self.assertRaisesRegex(c.PlanError, "tags"):
            c.sanity_check(s)

    def test_floor_is_inclusive(self):
        tags = {"latest": d(1), "v0.19.0": d(1)}
        tags.update({f"pr-{i}": d(2) for i in range(c.MIN_TAGS - 2)})
        c.sanity_check({"tags": tags})
        del tags["pr-0"]
        with self.assertRaises(c.PlanError):
            c.sanity_check({"tags": tags})

    def test_empty_snapshot_aborts(self):
        with self.assertRaises(c.PlanError):
            c.sanity_check({"tags": {}})


class StableTagGuard(unittest.TestCase):
    """plan() must abort if a planner bug would delete what a stable tag points at."""

    def test_guard_fires_when_a_stable_digest_lands_in_the_delete_set(self):
        # Simulate the bug the guard exists for: every tag looks expired, so
        # `latest` and the stable releases are planned for deletion.
        with mock.patch.object(c, "expired_nightly_tag", lambda tag, cutoff: True):
            with self.assertRaisesRegex(c.PlanError, "plan would delete"):
                c.plan(real(), day("2026-10-08"), 7)

    def test_guard_names_the_offending_tag(self):
        s = {"tags": {"latest": d(1)}, "children": {}, "missing": []}
        with mock.patch.object(c, "expired_nightly_tag", lambda tag, cutoff: True):
            with self.assertRaises(c.PlanError) as cm:
                c.plan(s, day("2026-10-08"), 7)
        self.assertIn("latest", str(cm.exception))

    def test_guard_is_quiet_on_a_correct_plan(self):
        c.plan(real(), day("2030-01-01"), 7)


class FakeResponse:
    def __init__(self, body=b"", headers=None, status=200):
        self.body = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.headers = headers or {}
        self.status = status

    def read(self):
        return self.body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class FakeNet:
    """A urlopen replacement: routes (method, url) to a response or exception."""

    def __init__(self, routes):
        self.routes = routes
        self.requests = []

    def __call__(self, req, timeout=None):
        url = req if isinstance(req, str) else req.full_url
        method = "GET" if isinstance(req, str) else req.get_method()
        self.requests.append((method, url))
        r = self.routes[(method, url)]
        if isinstance(r, Exception):
            raise r
        return r


BASE = "https://ghcr.io/v2/conduitio/conduit"
TOKEN_URL = "https://ghcr.io/token?scope=repository:conduitio/conduit:pull"


def registry_with(routes):
    net = FakeNet({("GET", TOKEN_URL): FakeResponse({"token": "t"}), **routes})
    return net, mock.patch("urllib.request.urlopen", net)


class RegistryTags(unittest.TestCase):
    def test_follows_link_header_across_pages(self):
        net, patch = registry_with({
            ("GET", BASE + "/tags/list?n=1000"): FakeResponse(
                {"tags": ["a", "b"]},
                {"Link": '</v2/conduitio/conduit/tags/list?last=b&n=1000>; rel="next"'}),
            ("GET", BASE + "/tags/list?last=b&n=1000"): FakeResponse(
                {"tags": ["c"]},
                {"Link": '</v2/conduitio/conduit/tags/list?last=c&n=1000>; rel="next"'}),
            ("GET", BASE + "/tags/list?last=c&n=1000"): FakeResponse({"tags": ["d"]}),
        })
        with patch:
            tags = c.Registry("ghcr.io/conduitio/conduit").tags()
        self.assertEqual(tags, ["a", "b", "c", "d"])
        self.assertEqual(len(net.requests), 4)  # token + 3 pages

    def test_single_page_and_null_tags(self):
        _net, patch = registry_with({("GET", BASE + "/tags/list?n=1000"): FakeResponse({"tags": None})})
        with patch:
            self.assertEqual(c.Registry("ghcr.io/conduitio/conduit").tags(), [])

    def test_link_with_prev_and_next_picks_next(self):
        h = {"Link": '</p?last=1>; rel="prev", </p?last=2&n=5>; rel="next"'}
        self.assertEqual(c.next_link(h), "/p?last=2&n=5")

    def test_link_without_next_or_without_header(self):
        self.assertIsNone(c.next_link({"Link": '</p?last=1>; rel="prev"'}))
        self.assertIsNone(c.next_link({}))
        self.assertEqual(c.next_link({"Link": '<https://x/y?page=2>;rel="next"'}), "https://x/y?page=2")

    def test_http_error_on_a_page_propagates(self):
        _net, patch = registry_with({("GET", BASE + "/tags/list?n=1000"): http_error(500)})
        with patch, self.assertRaises(urllib.error.HTTPError):
            c.Registry("ghcr.io/conduitio/conduit").tags()


class RegistryExists(unittest.TestCase):
    def exists(self, outcome):
        url = BASE + "/manifests/" + d(1)
        _net, patch = registry_with({("HEAD", url): outcome})
        with patch:
            return c.Registry("ghcr.io/conduitio/conduit").exists(d(1))

    def test_200_exists(self):
        self.assertTrue(self.exists(FakeResponse()))

    def test_404_is_missing(self):
        self.assertFalse(self.exists(http_error(404)))

    def test_other_statuses_are_errors_not_orphans(self):
        for code in (401, 403, 429, 500, 503):
            with self.subTest(code=code), self.assertRaises(urllib.error.HTTPError):
                self.exists(http_error(code))

    def test_timeouts_are_errors_not_orphans(self):
        with self.assertRaises(TimeoutError):
            self.exists(TimeoutError("timed out"))


def manifest_body(children=(), index=True):
    if not index:
        return json.dumps({"mediaType": "application/vnd.oci.image.manifest.v1+json"}).encode()
    return json.dumps({
        "mediaType": "application/vnd.oci.image.index.v1+json",
        "manifests": [{"digest": k, "mediaType": m} for k, m in children],
    }).encode()


class FakeRegistry:
    """Registry stand-in for snapshot_registry: manifests by ref, tag pages, existing digests."""

    def __init__(self, tag_list, manifests, existing=()):
        self.tag_list, self.manifests, self.existing = tag_list, manifests, set(existing)

    def tags(self):
        return self.tag_list

    def manifest(self, ref):
        return self.manifests[ref]

    def exists(self, digest):
        return digest in self.existing


class SnapshotRegistry(unittest.TestCase):
    IMG = "application/vnd.oci.image.manifest.v1+json"
    IDX = "application/vnd.oci.image.index.v1+json"

    def snap(self, reg):
        with mock.patch.object(c, "Registry", lambda image: reg):
            return c.snapshot_registry("ghcr.io/conduitio/conduit")

    def test_tags_children_and_nested_indexes(self):
        top, inner, leaf, plat = d(1), d(2), d(3), d(4)
        reg = FakeRegistry(["latest"], {
            "latest": (top, [(inner, self.IDX), (plat, self.IMG)]),
            inner: (inner, [(leaf, self.IMG)]),
        })
        s = self.snap(reg)
        self.assertEqual(s["tags"], {"latest": top})
        self.assertEqual(s["children"][top], [inner, plat])
        self.assertEqual(s["children"][inner], [leaf])  # nested index was fetched
        self.assertNotIn(plat, s["children"])  # a plain image manifest is not fetched
        self.assertEqual(s["missing"], [])

    def test_missing_subjects_are_only_those_the_registry_404s(self):
        gone, present_untagged, reachable = d(10), d(11), d(12)
        top = d(1)
        tags = {
            "latest": (top, [(reachable, self.IMG)]),
            "sha256-" + gone[7:]: (d(20), []),
            "sha256-" + present_untagged[7:]: (d(21), []),
            "sha256-" + reachable[7:]: (d(22), []),
        }
        reg = FakeRegistry(list(tags), tags, existing=[present_untagged])
        s = self.snap(reg)
        self.assertEqual(s["missing"], [gone])  # reachable needs no HEAD at all

    def test_errors_from_exists_abort_the_snapshot(self):
        reg = FakeRegistry(["sha256-" + d(10)[7:]], {"sha256-" + d(10)[7:]: (d(20), [])})
        reg.exists = mock.Mock(side_effect=http_error(500))
        with self.assertRaises(urllib.error.HTTPError):
            self.snap(reg)


class RegistryManifest(unittest.TestCase):
    def fetch(self, body, header=None):
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        h = {} if header is None else {"Docker-Content-Digest": header or digest}
        _net, patch = registry_with({("GET", BASE + "/manifests/ref"): FakeResponse(body, h)})
        with patch:
            return digest, c.Registry("ghcr.io/conduitio/conduit").manifest("ref")

    def test_index_children_and_content_digest(self):
        body = manifest_body([(d(1), "application/vnd.oci.image.manifest.v1+json")])
        digest, got = self.fetch(body)
        self.assertEqual(got, (digest, [(d(1), "application/vnd.oci.image.manifest.v1+json")]))

    def test_image_manifest_has_no_children(self):
        digest, got = self.fetch(manifest_body(index=False))
        self.assertEqual(got, (digest, []))

    def test_digest_header_mismatch_aborts(self):
        with self.assertRaises(RuntimeError):
            self.fetch(manifest_body(), header=d(99))


class PackagesApi(unittest.TestCase):
    API = "https://api.github.com/orgs/conduitio/packages/container/conduit"

    def test_versions_paginate_and_map_digest_to_id_and_tags(self):
        p2 = self.API + "/versions?page=2"
        _net, patch = registry_with({
            ("GET", self.API + "/versions?per_page=100"): FakeResponse(
                [{"name": d(1), "id": 11, "metadata": {"container": {"tags": ["latest"]}}}],
                {"Link": f'<{p2}>; rel="next"'}),
            ("GET", p2): FakeResponse(
                [{"name": d(2), "id": 22, "metadata": {"container": {"tags": []}}}]),
        })
        with patch:
            got = c.Packages("conduitio", "conduit", "tok").versions()
        self.assertEqual(got, {d(1): (11, ["latest"]), d(2): (22, [])})

    def test_delete_reports_remaining_quota(self):
        url = self.API + "/versions/7"
        for header, want in (("812", 812), (None, None), ("abc", None)):
            h = {} if header is None else {"X-RateLimit-Remaining": header}
            _net, patch = registry_with({("DELETE", url): FakeResponse(b"", h, 204)})
            with patch:
                self.assertEqual(c.Packages("conduitio", "conduit", "tok").delete(7), want)

    def test_packages_for_splits_org_and_package(self):
        p = c.packages_for("ghcr.io/conduitio/conduit", "tok")
        self.assertEqual(p.base, self.API)


class RateLimitedClassifier(unittest.TestCase):
    def test_classification(self):
        self.assertTrue(c.rate_limited(http_error(429)))
        self.assertTrue(c.rate_limited(http_error(403, {"Retry-After": "30"})))
        self.assertTrue(c.rate_limited(http_error(403, {"X-RateLimit-Remaining": "0"})))
        self.assertFalse(c.rate_limited(http_error(403, {"X-RateLimit-Remaining": "40"})))
        self.assertFalse(c.rate_limited(http_error(403)))
        self.assertFalse(c.rate_limited(http_error(404)))
        self.assertFalse(c.rate_limited(http_error(500, {"Retry-After": "5"})))


class MainExitCodes(unittest.TestCase):
    """main() is what the workflow's exit status comes from."""

    def run_main(self, argv, snap=None, api=None, token="tok", snap_error=None):
        snap = real() if snap is None else snap
        env = {"GH_TOKEN": token} if token else {}
        out, err = io.StringIO(), io.StringIO()
        sr = mock.Mock(return_value=snap, side_effect=snap_error)
        with mock.patch.object(c, "snapshot_registry", sr), \
                mock.patch.object(c, "packages_for", lambda image, tok: api), \
                mock.patch.dict(os.environ, env, clear=True), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = c.main(["--today", "2026-10-15", "--delete-interval", "0"] + argv)
        return code, out.getvalue(), err.getvalue()

    def live_for(self, snap=None):
        r = c.plan(snap or real(), day("2026-10-15"), 7)
        return {x: (i, []) for i, x in enumerate(sorted(r["delete"]))}

    def test_dry_run_exits_0_and_never_touches_the_api(self):
        api = mock.Mock()
        code, out, _ = self.run_main([], api=api)
        self.assertEqual(code, 0)
        self.assertIn("dry run: nothing deleted", out)
        api.versions.assert_not_called()

    def test_apply_success_exits_0(self):
        api = FakePackages(self.live_for())
        code, out, _ = self.run_main(["--apply", "--max-deletes", "100000"], api=api)
        self.assertEqual(code, 0)
        self.assertIn("failed 0", out)

    def test_apply_stopped_by_the_cap_exits_0_with_a_warning(self):
        api = FakePackages(self.live_for())
        code, out, err = self.run_main(["--apply", "--max-deletes", "5"], api=api)
        self.assertEqual(code, 0)
        self.assertEqual(len(api.deleted), 5)
        self.assertIn("::warning::", err)
        self.assertIn("stopped early (max-deletes)", out)

    def test_apply_with_a_failed_delete_exits_1(self):
        live = self.live_for()
        first = live[sorted(live)[0]][0]
        api = FakePackages(live, errors={first: http_error(404)})
        code, _, err = self.run_main(["--apply", "--max-deletes", "100000"], api=api)
        self.assertEqual(code, 1)
        self.assertIn("::error::", err)

    def test_apply_permission_error_exits_1(self):
        live = self.live_for()
        api = FakePackages(live, errors={v[0]: http_error(403) for v in live.values()})
        code, out, _ = self.run_main(["--apply"], api=api)
        self.assertEqual(code, 1)
        self.assertIn("stopped early (forbidden)", out)

    def test_apply_rate_limited_after_progress_exits_0(self):
        live = self.live_for()
        r = c.plan(real(), day("2026-10-15"), 7)
        ids = [live[x][0] for x in delete_order(r)]
        api = FakePackages(live, errors={i: http_error(429) for i in ids[1:]})
        code, out, _ = self.run_main(["--apply"], api=api)
        self.assertEqual(code, 0)
        self.assertIn("stopped early (rate-limit)", out)

    def test_apply_network_failure_exits_1(self):
        live = self.live_for()
        api = FakePackages(live, errors={v[0]: urllib.error.URLError("down") for v in live.values()})
        code, _, _ = self.run_main(["--apply"], api=api)
        self.assertEqual(code, 1)

    def test_listing_versions_fails_exits_1_and_deletes_nothing(self):
        api = mock.Mock()
        api.versions.side_effect = urllib.error.URLError("down")
        code, _, err = self.run_main(["--apply"], api=api)
        self.assertEqual(code, 1)
        self.assertIn("nothing deleted", err)
        api.delete.assert_not_called()

    def test_apply_without_token_exits_2(self):
        code, _, err = self.run_main(["--apply"], api=mock.Mock(), token=None)
        self.assertEqual(code, 2)
        self.assertIn("GH_TOKEN", err)

    def test_unsafe_plan_exits_2_and_deletes_nothing(self):
        s = real()
        s["tags"]["v0.1.0-nightly.20200101"] = "sha256:nothex"
        api = mock.Mock()
        code, _, err = self.run_main(["--apply"], snap=s, api=api)
        self.assertEqual(code, 2)
        self.assertIn("refusing to delete anything", err)
        api.versions.assert_not_called()

    def test_implausible_snapshot_exits_2_before_touching_the_api(self):
        s = real()
        del s["tags"]["latest"]
        api = mock.Mock()
        code, _, err = self.run_main(["--apply"], snap=s, api=api)
        self.assertEqual(code, 2)
        self.assertIn("implausible", err)
        api.versions.assert_not_called()

    def test_snapshot_far_smaller_than_the_package_exits_2(self):
        live = self.live_for()
        for i in range(2000):
            live[d(900_000 + i)] = (500_000 + i, [f"v0.1.0-nightly.{20200101 + i}"])
        api = FakePackages(live)
        code, _, _ = self.run_main(["--apply"], api=api)
        self.assertEqual(code, 2)
        self.assertEqual(api.deleted, [])

    def test_registry_read_failure_exits_1(self):
        for err in (urllib.error.URLError("dns"), TimeoutError("t"), RuntimeError("digest")):
            with self.subTest(err=err):
                code, _, stderr = self.run_main(["--apply"], api=mock.Mock(), snap_error=err)
                self.assertEqual(code, 1)
                self.assertIn("nothing deleted", stderr)

    def test_max_deletes_must_be_positive(self):
        with self.assertRaises(SystemExit) as cm, contextlib.redirect_stderr(io.StringIO()):
            c.main(["--max-deletes", "0"])
        self.assertEqual(cm.exception.code, 2)

    def test_snapshot_in_file_is_used(self):
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".json") as f:
            json.dump(real(), f)
            f.flush()
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(c.main(["--snapshot-in", f.name, "--today", "2026-10-15"]), 0)


if __name__ == "__main__":
    unittest.main()
