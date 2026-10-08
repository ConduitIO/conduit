#!/usr/bin/env python3
"""Tests for ghcr_nightly_cleanup.py. Run: python3 -m unittest -v (from this dir).

The fixture is the real ghcr.io/conduitio/conduit tag list on 2026-10-08,
the morning after the first two signed nightlies (20261007, 20261008). Each
signed nightly has a `sha256-<digest>` referrer tag pointing at an OCI index
that lists one Sigstore bundle manifest.
"""

import copy
import datetime
import json
import os
import unittest
import urllib.error

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
    def __init__(self, live, fail=()):
        self.live = live
        self.fail = set(fail)
        self.deleted = []

    def versions(self):
        return self.live

    def delete(self, version_id):
        if version_id in self.fail:
            raise urllib.error.HTTPError("u", 404, "Package not found.", {}, None)
        self.deleted.append(version_id)


class Apply(unittest.TestCase):
    def setUp(self):
        self.r = c.plan(real(), day("2026-10-15"), 7)
        self.live = {x: (i, []) for i, x in enumerate(sorted(self.r["delete"]))}
        self.live[N1007] = (9001, ["v0.20.0-nightly.20261007"])
        self.live[SIG1007_INDEX] = (9002, ["sha256-" + N1007[7:]])
        self.logs = []

    def test_deletes_root_first_then_signature(self):
        api = FakePackages(self.live)
        deleted, skipped, failed = c.apply(self.r, api, self.logs.append)
        self.assertEqual((skipped, failed), (0, 0))
        self.assertEqual(deleted, len(self.r["delete"]))
        roots = {self.live[x][0] for x in self.r["expired"]}
        first = api.deleted[: len(roots)]
        self.assertEqual(set(first), roots)
        self.assertLess(api.deleted.index(9001), api.deleted.index(9002))

    def test_version_retagged_after_snapshot_is_skipped(self):
        live = copy.deepcopy(self.live)
        live[N1007] = (9001, ["v0.20.0-nightly.20261007", "v0.20.0"])
        api = FakePackages(live)
        _deleted, skipped, failed = c.apply(self.r, api, self.logs.append)
        self.assertNotIn(9001, api.deleted)
        self.assertEqual((skipped, failed), (1, 0))

    def test_already_gone_is_skipped_not_failed(self):
        live = copy.deepcopy(self.live)
        del live[SIG1007_BUNDLE]
        _deleted, skipped, failed = c.apply(self.r, FakePackages(live), self.logs.append)
        self.assertEqual((skipped, failed), (1, 0))

    def test_failed_delete_is_counted(self):
        api = FakePackages(self.live, fail=[9001])
        _deleted, _skipped, failed = c.apply(self.r, api, self.logs.append)
        self.assertEqual(failed, 1)
        self.assertTrue(any(line.startswith("FAIL") for line in self.logs))

    def test_deletable_now(self):
        r = self.r
        self.assertTrue(c.deletable_now([], r))
        self.assertTrue(c.deletable_now(["v0.20.0-nightly.20261007"], r))
        self.assertTrue(c.deletable_now(["sha256-" + N1007[7:]], r))
        self.assertFalse(c.deletable_now(["sha256-" + N1008[7:]], r))
        self.assertFalse(c.deletable_now(["v0.20.0-nightly.20261008"], r))
        self.assertFalse(c.deletable_now(["v0.20.0-nightly.20261007", "latest"], r))


if __name__ == "__main__":
    unittest.main()
