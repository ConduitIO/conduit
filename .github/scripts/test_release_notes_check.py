import os
import tempfile
import unittest

import release_notes_check as n

GOOD = """# Conduit v9.9.1 release notes

This patch fixes two ways a pipeline could lose records after a restart. Upgrade if you run the
Postgres or S3 connectors.

## Fixed

- The Postgres source refuses to start when its replication slot no longer covers the saved position (abc1234).
- S3 resume no longer skips the last object of a batch.
"""


class Notes(unittest.TestCase):
    def run_check(self, tag, text=None, fixes=None):
        with tempfile.TemporaryDirectory() as d:
            if text is not None:
                open(os.path.join(d, f"{tag}.md"), "w").write(text)
            fp = os.path.join(d, "fixes.txt")
            if fixes is not None:
                open(fp, "w").write(fixes)
            return n.main([tag, "--dir", d, "--fixes", fp])

    def test_hand_written_notes_pass(self):
        self.assertEqual(self.run_check("v9.9.1", GOOD), 0)

    def test_missing_file_fails(self):
        self.assertEqual(self.run_check("v9.9.1"), 1)

    def test_generated_changelog_only_fails(self):
        gen = """## Changelog

- fix(postgres): refuse a missing slot by @bot in #12
- feat(s3): exact resume by @bot in #13
- fix: stuff by @bot in #14
"""
        self.assertEqual(self.run_check("v9.9.1", gen), 1)

    def test_fixed_bullets_in_generated_shape_fail_even_with_prose(self):
        text = "Intro paragraph.\n\n## Fixed\n\n- fix(a): x by @u in #1\n- fix(b): y by @u in #2\n"
        self.assertEqual(self.run_check("v9.9.1", text), 1)

    def test_no_fixed_section_fails(self):
        self.assertEqual(self.run_check("v9.9.1", "Some prose.\n\n## Changes\n\n- a thing\n"), 1)

    def test_empty_fixed_section_fails(self):
        self.assertEqual(self.run_check("v9.9.1", "Some prose.\n\n## Fixed\n\nnothing\n"), 1)

    def test_draft_marker_fails(self):
        self.assertEqual(self.run_check("v9.9.1", GOOD.replace("# Conduit v9.9.1 release notes", "# x\n\n*draft, not yet released*")), 1)

    def test_only_lists_and_headings_fail(self):
        self.assertEqual(self.run_check("v9.9.1", "## Fixed\n\n- a real fix\n"), 1)

    def test_declared_fix_missing_from_notes_fails(self):
        fixes = "github.com/conduitio/conduit-commons | 9999999aaaa | clone depth\n"
        self.assertEqual(self.run_check("v9.9.1", GOOD, fixes), 1)

    def test_declared_fix_present_in_notes_passes(self):
        fixes = "github.com/conduitio/conduit-connector-postgres | abc1234ffff | slot\n"
        self.assertEqual(self.run_check("v9.9.1", GOOD, fixes), 0)

    def test_hidden_content_in_html_comments_does_not_count(self):
        hidden = GOOD.replace("## Fixed", "<!--\n## Fixed\n-->\n## Changes")
        self.assertEqual(self.run_check("v9.9.1", hidden), 1)
        fixes = "github.com/conduitio/conduit-commons | 9999999aaaa | n\n"
        named_in_comment = GOOD + "\n<!-- 9999999 -->\n"
        self.assertEqual(self.run_check("v9.9.1", named_in_comment, fixes), 1)
        unclosed = GOOD + "\n<!-- 9999999\n"
        self.assertEqual(self.run_check("v9.9.1", unclosed, fixes), 1)

    def test_short_note_is_not_a_name(self):
        # "slot" appears in GOOD's prose-ish text, but a 4-character note must not count.
        fixes = "github.com/conduitio/conduit-connector-postgres | 9999999aaaa | slot\n"
        self.assertEqual(self.run_check("v9.9.1", GOOD, fixes), 1)

    def test_note_as_substring_of_a_longer_word_is_not_a_name(self):
        fixes = "github.com/conduitio/conduit-commons | 9999999aaaa | evicted object\n"
        text = GOOD.replace("- S3 resume", "- Prevented preevicted objects from vanishing; S3 resume")
        self.assertEqual(self.run_check("v9.9.1", text, fixes), 1)

    def test_whole_phrase_note_of_12_chars_is_a_name(self):
        fixes = "github.com/conduitio/conduit-commons | 9999999aaaa | last object of a batch\n"
        self.assertEqual(self.run_check("v9.9.1", GOOD, fixes), 0)

    def test_commit_prefix_must_be_a_whole_token(self):
        fixes = "github.com/conduitio/conduit-commons | abc1234ffff | n\n"
        embedded = GOOD.replace("(abc1234)", "(xabc1234)")
        self.assertEqual(self.run_check("v9.9.1", embedded, fixes), 1)
        self.assertEqual(self.run_check("v9.9.1", GOOD, fixes), 0)

    def test_minor_and_prerelease_are_not_checked(self):
        self.assertEqual(self.run_check("v0.21.0"), 0)
        self.assertEqual(self.run_check("v9.9.1-rc.1"), 0)

    def test_bad_tag_fails(self):
        self.assertEqual(self.run_check("main"), 1)


if __name__ == "__main__":
    unittest.main()
