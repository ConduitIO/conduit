#!/usr/bin/env python3
"""Tests for release_docs_drift.py.

The two that carry the weight: a published release still marked draft must fail
(the v0.22.0 bug), and a release that predates the hand-written-notes
convention must NOT fail (there are 29 of those, and a check that reports 29
problems on its first run is a check nobody reads by its third).
"""

import os
import tempfile
import unittest

import release_docs_drift as d

DRAFTED = "# Conduit v0.22.0 (draft, not yet released)\n\nNotes as changes land.\n"
FINAL = "# Conduit v0.22.0\n\nReleased 2026-11-25.\n\n## Fixed\n\n- something\n"


def notes(dirpath, **files):
    os.makedirs(dirpath, exist_ok=True)
    for name, body in files.items():
        with open(os.path.join(dirpath, name.replace("_", ".") + ".md"), "w") as f:
            f.write(body)
    return dirpath


class StableVersions(unittest.TestCase):
    def test_prereleases_and_junk_are_dropped(self):
        self.assertEqual(
            d.stable_versions(["v0.22.0", "v0.23.0-nightly.20261127", "", "main", "v0.21.2"]),
            ["v0.21.2", "v0.22.0"])

    def test_sorted_by_version_not_by_string(self):
        # v0.9.0 before v0.10.0: a string sort puts them the other way round.
        self.assertEqual(d.stable_versions(["v0.10.0", "v0.9.0"]), ["v0.9.0", "v0.10.0"])


class Drift(unittest.TestCase):
    def test_a_published_release_still_marked_draft_fails(self):
        # The v0.22.0 bug: the finalise commit was tagged and never landed on
        # main, so for two days main called a shipped release unreleased.
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=DRAFTED)
            problems = d.drift(["v0.22.0"], p)
            self.assertEqual(len(problems), 1, problems)
            self.assertIn("draft, not yet released", problems[0])
            self.assertIn("git checkout v0.22.0 --", problems[0])  # the fix is in the message

    def test_a_finalised_note_passes(self):
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=FINAL)
            self.assertEqual(d.drift(["v0.22.0"], p), [])

    def test_a_draft_for_a_release_that_is_not_published_is_fine(self):
        # The normal state between writing notes and tagging.
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_23_0=DRAFTED)
            self.assertEqual(d.drift(["v0.22.0"], p, tagged_notes=["v0.22.0"]), [
                "v0.22.0 is published and has notes at its tag, but "
                + os.path.join(p, "v0.22.0.md")
                + " is missing here. They were probably written on a release branch and never "
                  "landed: `git checkout v0.22.0 -- " + os.path.join(p, "v0.22.0.md") + "`"])
            # ...and with no claim that v0.22.0 has notes at its tag, silence.
            self.assertEqual(d.drift(["v0.22.0"], p), [])

    def test_notes_that_exist_at_the_tag_and_not_here_fail(self):
        # The v0.20.7 case: written on release/v0.20.x, never landed on main.
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=FINAL)
            problems = d.drift(["v0.22.0", "v0.20.7"], p, tagged_notes=["v0.20.7"])
            self.assertEqual(len(problems), 1, problems)
            self.assertIn("v0.20.7", problems[0])
            self.assertIn("never landed", problems[0])

    def test_releases_predating_the_convention_are_not_reported(self):
        # 29 releases from v0.1.1 to v0.20.5 have no notes file at their own
        # tag. Reporting them would drown the one real finding.
        old = ["v0.1.1", "v0.6.1", "v0.9.1", "v0.13.6", "v0.20.5"]
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=FINAL)
            self.assertEqual(d.drift(["v0.22.0"] + old, p, tagged_notes=["v0.22.0"]), [])

    def test_a_missing_notes_dir_fails_closed(self):
        with tempfile.TemporaryDirectory() as t:
            with self.assertRaises(d.DriftError):
                d.drift(["v0.22.0"], os.path.join(t, "nope"))

    def test_an_empty_release_list_fails_closed(self):
        # Not "nothing published, nothing wrong": that would turn a failed API
        # call into a green run with every release unchecked.
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=DRAFTED)
            for empty in ([], [""], ["v0.23.0-nightly.20261127"]):
                with self.assertRaises(d.DriftError, msg=repr(empty)):
                    d.drift(empty, p)

    def test_an_unreadable_notes_file_fails_closed(self):
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=FINAL)
            os.chmod(os.path.join(p, "v0.22.0.md"), 0o000)
            try:
                with self.assertRaises(d.DriftError):
                    d.drift(["v0.22.0"], p)
            finally:
                os.chmod(os.path.join(p, "v0.22.0.md"), 0o644)

    def test_the_draft_marker_comes_from_the_tag_time_gate(self):
        # One definition of "draft", so the two gates cannot drift on it.
        import release_notes_check
        self.assertIs(d.DRAFT, release_notes_check.DRAFT)

    def test_cli_exit_codes(self):
        with tempfile.TemporaryDirectory() as t:
            p = notes(os.path.join(t, "releases"), v0_22_0=DRAFTED)
            self.assertEqual(d.main(["--released", "v0.22.0", "--dir", p]), 1)
            notes(p, v0_22_0=FINAL)
            self.assertEqual(d.main(["--released", "v0.22.0", "--dir", p]), 0)
            self.assertEqual(d.main(["--released", "v0.22.0", "--dir", os.path.join(t, "nope")]), 2)
            # Whitespace-separated input, as `gh release list` produces.
            self.assertEqual(d.main(["--released", "v0.22.0 v0.21.2", "--dir", p]), 0)


class LiveTree(unittest.TestCase):
    def test_this_branch_agrees_with_its_own_notes_files(self):
        # Every notes file present here that is for a patch release must be
        # finalised: a draft on this branch is only legitimate for a version
        # that has not shipped, and this repository's newest notes file is
        # always for something released by the time it lands on main.
        here = os.path.dirname(os.path.abspath(__file__))
        releases = os.path.join(here, "..", "..", "docs", "releases")
        present = sorted(n[: -len(".md")] for n in os.listdir(releases) if n.endswith(".md"))
        self.assertTrue(present)
        drafted = []
        for v in present:
            with open(os.path.join(releases, v + ".md")) as f:
                if d.DRAFT.search(f.read()):
                    drafted.append(v)
        self.assertEqual(drafted, [], f"finalise or remove the draft notes: {drafted}")


if __name__ == "__main__":
    unittest.main()
