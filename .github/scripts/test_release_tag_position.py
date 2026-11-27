#!/usr/bin/env python3
"""Tests for release_tag_position.py."""

import os
import tempfile
import unittest

from release_tag_position import decide, main, version

# The repository's tags as of the 2026-11-27 incident, plus a nightly.
REAL = [
    "v0.17.0", "v0.18.0", "v0.19.0",
    "v0.20.0", "v0.20.1", "v0.20.2", "v0.20.3", "v0.20.4", "v0.20.5", "v0.20.6", "v0.20.7",
    "v0.21.0", "v0.21.1", "v0.21.2",
    "v0.22.0",
    "v0.23.0-nightly.20261126",
]


class TestVersion(unittest.TestCase):
    def test_stable(self):
        self.assertEqual(version("v0.20.7"), (0, 20, 7))
        self.assertEqual(version("v1.0.0"), (1, 0, 0))
        self.assertEqual(version("v10.20.30"), (10, 20, 30))

    def test_whitespace_tolerated(self):
        self.assertEqual(version("  v0.20.7\n"), (0, 20, 7))

    def test_not_stable(self):
        for tag in [
            "v0.23.0-nightly.20261126",
            "v1.0.0-rc.1",
            "v0.20",
            "v0.20.7.1",
            "0.20.7",
            "latest",
            "",
            "v0.20.x",
        ]:
            self.assertIsNone(version(tag), tag)


class TestDecide(unittest.TestCase):
    def test_back_line_patch_is_not_highest(self):
        """The 2026-11-27 incident: v0.20.7 tagged after v0.22.0 shipped."""
        got = decide("v0.20.7", REAL)
        self.assertFalse(got["is_highest"])
        self.assertEqual(got["previous_tag"], "v0.20.6")

    def test_newest_minor_is_highest(self):
        got = decide("v0.22.0", REAL)
        self.assertTrue(got["is_highest"])
        self.assertEqual(got["previous_tag"], "v0.21.2")

    def test_previous_tag_is_not_the_global_runner_up(self):
        """What the old shell did: sort -r | head -2 | tail -1 gave v0.21.2."""
        self.assertNotEqual(decide("v0.20.7", REAL)["previous_tag"], "v0.21.2")

    def test_patch_on_the_newest_line_is_highest(self):
        tags = REAL + ["v0.22.1"]
        got = decide("v0.22.1", tags)
        self.assertTrue(got["is_highest"])
        self.assertEqual(got["previous_tag"], "v0.22.0")

    def test_nightly_is_never_highest(self):
        got = decide("v0.23.0-nightly.20261126", REAL)
        self.assertFalse(got["is_highest"])
        self.assertEqual(got["previous_tag"], "")

    def test_unparseable_fails_closed(self):
        for tag in ["latest", "v0.20", "garbage", ""]:
            got = decide(tag, REAL)
            self.assertFalse(got["is_highest"], tag)
            self.assertEqual(got["previous_tag"], "", tag)

    def test_first_ever_release_is_highest_with_no_previous(self):
        got = decide("v0.1.0", ["v0.1.0"])
        self.assertTrue(got["is_highest"])
        self.assertEqual(got["previous_tag"], "")

    def test_current_tag_absent_from_the_list(self):
        """The tag may not be in the list yet depending on fetch timing."""
        got = decide("v0.22.0", [t for t in REAL if t != "v0.22.0"])
        self.assertTrue(got["is_highest"])
        self.assertEqual(got["previous_tag"], "v0.21.2")

    def test_numeric_not_lexical_ordering(self):
        tags = ["v0.9.0", "v0.10.0", "v0.2.0"]
        self.assertTrue(decide("v0.10.0", tags)["is_highest"])
        self.assertFalse(decide("v0.9.0", tags)["is_highest"])
        self.assertEqual(decide("v0.10.0", tags)["previous_tag"], "v0.9.0")

    def test_patch_ordering_past_nine(self):
        tags = ["v0.20.9", "v0.20.10", "v0.20.7"]
        self.assertTrue(decide("v0.20.10", tags)["is_highest"])
        self.assertEqual(decide("v0.20.10", tags)["previous_tag"], "v0.20.9")

    def test_equal_version_does_not_block_highest(self):
        """Re-running on an existing tag must still report is_highest."""
        got = decide("v0.22.0", REAL + ["v0.22.0"])
        self.assertTrue(got["is_highest"])

    def test_blank_lines_ignored(self):
        got = decide("v0.22.0", ["", "  ", "v0.21.2", "v0.22.0"])
        self.assertTrue(got["is_highest"])
        self.assertEqual(got["previous_tag"], "v0.21.2")


class TestMain(unittest.TestCase):
    def run_main(self, current, tags):
        with tempfile.TemporaryDirectory() as d:
            tags_path = os.path.join(d, "tags")
            out_path = os.path.join(d, "out")
            with open(tags_path, "w") as f:
                f.write("\n".join(tags) + "\n")
            os.environ["GITHUB_OUTPUT"] = out_path
            try:
                rc = main(["release_tag_position.py", current, tags_path])
            finally:
                del os.environ["GITHUB_OUTPUT"]
            with open(out_path) as f:
                return rc, f.read()

    def test_writes_github_output(self):
        rc, out = self.run_main("v0.20.7", REAL)
        self.assertEqual(rc, 0)
        self.assertIn("is_highest=false", out)
        self.assertIn("previous_tag=v0.20.6", out)

    def test_writes_true_for_highest(self):
        rc, out = self.run_main("v0.22.0", REAL)
        self.assertEqual(rc, 0)
        self.assertIn("is_highest=true", out)
        self.assertIn("previous_tag=v0.21.2", out)

    def test_bad_arity_is_an_error(self):
        self.assertEqual(main(["release_tag_position.py"]), 2)


if __name__ == "__main__":
    unittest.main()


class TestFailsClosedOnShallowCheckout(unittest.TestCase):
    """A shallow checkout makes `git tag --list` empty. That must not read as
    "this is the first release", because it would hand `latest` to whatever
    tag is being built — the exact bug this script exists to prevent."""

    def test_empty_tag_list(self):
        got = decide("v0.20.7", [])
        self.assertFalse(got["is_highest"])
        self.assertEqual(got["previous_tag"], "")

    def test_only_nightlies_in_list(self):
        got = decide("v0.20.7", ["v0.23.0-nightly.20261126", "v0.23.0-nightly.20261125"])
        self.assertFalse(got["is_highest"])

    def test_only_blank_lines(self):
        self.assertFalse(decide("v0.22.0", ["", "   "])["is_highest"])
