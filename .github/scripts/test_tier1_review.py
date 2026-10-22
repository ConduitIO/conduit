import json
import os
import tempfile
import unittest

import tier1_review as t

HEAD = "abcdef0123456789abcdef0123456789abcdef01"
OLD = "1111111222222233333334444444555555566666"


def pr(body="**Risk tier: Tier 1**", labels=(), head=HEAD):
    return {"body": body, "labels": [{"name": l} for l in labels], "head": {"sha": head}}


def c(body, who="devarismeroxa", at="2026-10-21T00:00:00Z", n=[0], assoc="MEMBER", updated=None):
    n[0] += 1
    return {"id": n[0], "body": body, "created_at": at, "updated_at": updated or at,
            "author_association": assoc, "user": {"login": who}}


def at(i):
    return f"2026-10-21T00:00:{i:02d}Z"


S = {"devarismeroxa"}


class Tier1(unittest.TestCase):
    def test_undeclared_tier_fails(self):
        self.assertEqual(t.decide(pr(body="fixes a bug"), [], S)["state"], "failure")

    def test_template_placeholder_does_not_declare(self):
        body = "**Risk tier:** <!-- Tier 1 (data path) | Tier 2 | Tier 3 -->"
        self.assertEqual(t.decide(pr(body=body), [], S)["state"], "failure")

    def test_tier3_passes_without_review(self):
        self.assertEqual(t.decide(pr(body="Risk tier: Tier 3"), [], S)["state"], "success")

    def test_tier1_label_overrides_a_lower_declaration(self):
        r = t.decide(pr(body="Risk tier: Tier 3", labels=["tier-1"]), [], S)
        self.assertEqual(r["state"], "failure")

    def test_do_not_merge_always_fails(self):
        self.assertEqual(t.decide(pr(body="Risk tier: Tier 3", labels=["do-not-merge"]), [], S)["state"], "failure")

    def test_tier1_without_comments_fails(self):
        self.assertEqual(t.decide(pr(), [], S)["state"], "failure")

    def test_tier1_review_and_signoff_at_head_pass(self):
        cs = [c(f"Review: approve @ {HEAD[:7]}", "reviewer", at(1)), c(f"Sign-off @ {HEAD}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "success")

    def test_review_without_signoff_fails(self):
        cs = [c(f"Review: approve @ {HEAD[:7]}", "reviewer", at(1))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_signoff_from_a_non_signer_fails(self):
        cs = [c(f"Review: approve @ {HEAD[:7]}", "r", at(1)), c(f"Sign-off @ {HEAD[:7]}", "someone", at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_a_push_makes_both_stale(self):
        cs = [c(f"Review: approve @ {OLD[:7]}", "r", at(1)), c(f"Sign-off @ {OLD[:7]}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_changes_needed_blocks_even_when_current_head_is_approved_earlier(self):
        cs = [
            c(f"Review: approve @ {HEAD[:7]}", "r", at(1)),
            c(f"Sign-off @ {HEAD[:7]}", at=at(2)),
            c(f"Review: changes-needed @ {HEAD[:7]}", "r", at(3)),
        ]
        r = t.decide(pr(), cs, S)
        self.assertEqual(r["state"], "failure")
        self.assertTrue(r["label"])

    def test_changes_needed_at_old_head_blocks_until_a_later_approve(self):
        cs = [c(f"Review: changes-needed @ {OLD[:7]}", "r", at(1))]
        self.assertEqual(t.decide(pr(), cs + [c(f"Sign-off @ {HEAD[:7]}", at=at(2))], S)["state"], "failure")
        cs.append(c(f"Review: approve @ {HEAD[:7]}", "r", at(3)))
        cs.append(c(f"Sign-off @ {HEAD[:7]}", at=at(4)))
        self.assertEqual(t.decide(pr(), cs, S)["state"], "success")

    def test_verdict_inside_html_comment_or_mid_sentence_is_ignored(self):
        cs = [
            c(f"<!-- Review: approve @ {HEAD[:7]} -->", "r", at(1)),
            c(f"I will not write Review: approve @ {HEAD[:7]} yet", "r", at(2)),
            c(f"Sign-off @ {HEAD[:7]}", at=at(3)),
        ]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_short_sha_below_seven_chars_is_not_accepted(self):
        cs = [c(f"Review: approve @ {HEAD[:6]}", "r", at(1)), c(f"Sign-off @ {HEAD[:7]}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_comments_from_untrusted_authors_are_ignored(self):
        cs = [c(f"Review: approve @ {HEAD[:7]}", "stranger", at(1), assoc="NONE"),
              c(f"Sign-off @ {HEAD[:7]}", "devarismeroxa", at(2), assoc="CONTRIBUTOR")]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_comment_without_association_is_not_trusted(self):
        x = c(f"Review: approve @ {HEAD[:7]}", "r", at(1))
        del x["author_association"]
        cs = [x, c(f"Sign-off @ {HEAD[:7]}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_allowlisted_login_counts_without_association(self):
        x = c(f"Review: approve @ {HEAD[:7]}", "bot", at(1), assoc="NONE")
        cs = [x, c(f"Sign-off @ {HEAD[:7]}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S, reviewers={"bot"})["state"], "success")

    def test_untrusted_changes_needed_cannot_block(self):
        cs = [c(f"Review: approve @ {HEAD[:7]}", "r", at(1)), c(f"Sign-off @ {HEAD[:7]}", at=at(2)),
              c(f"Review: changes-needed @ {HEAD[:7]}", "drive-by", at(3), assoc="NONE")]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "success")

    def test_touching_a_tier1_path_floors_the_tier(self):
        body = "Risk tier: Tier 3"
        paths = t.tier1_patterns("*  @org/team @devarismeroxa\n/pkg/connector/  @devarismeroxa\n/go.mod @devarismeroxa\n", S)
        self.assertEqual(paths, ["/pkg/connector/", "/go.mod"])
        self.assertEqual(t.decide(pr(body=body), [], S, files=["docs/x.md"], tier1_paths=paths)["state"], "success")
        r = t.decide(pr(body=body), [], S, files=["pkg/connector/source.go"], tier1_paths=paths)
        self.assertEqual(r["state"], "failure")
        self.assertIn("Tier 1 path", r["description"])
        self.assertEqual(t.decide(pr(body=body), [], S, files=["go.mod"], tier1_paths=paths)["state"], "failure")

    def test_path_matching(self):
        self.assertTrue(t.touches("/pkg/lifecycle/", "pkg/lifecycle/a/b.go"))
        self.assertFalse(t.touches("/pkg/lifecycle/", "pkg/lifecycle-poc/a.go"))
        self.assertTrue(t.touches("/.github/", ".github/workflows/x.yml"))
        self.assertFalse(t.touches("/go.mod", "tools/go.mod"))

    def test_only_the_newest_verdict_per_author_counts(self):
        # approve, then the same author edits it into changes-needed later.
        a = c(f"Review: approve @ {HEAD[:7]}", "r", at(1), updated=at(9))
        a["body"] = f"Review: changes-needed @ {HEAD[:7]}"
        cs = [a, c(f"Sign-off @ {HEAD[:7]}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_edit_moves_a_comment_to_its_edit_time(self):
        # changes-needed written first but EDITED after the approve: it is the newer verdict.
        old = c(f"Review: changes-needed @ {HEAD[:7]}", "r", at(1), updated=at(8))
        new = c(f"Review: approve @ {HEAD[:7]}", "r", at(5))
        cs = [old, new, c(f"Sign-off @ {HEAD[:7]}", at=at(6))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_a_second_reviewers_approve_does_not_clear_the_first_ones_finding(self):
        cs = [c(f"Review: changes-needed @ {HEAD[:7]}", "r1", at(1)), c(f"Review: approve @ {HEAD[:7]}", "r2", at(2)),
              c(f"Sign-off @ {HEAD[:7]}", at=at(3))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_deleted_comment_is_simply_absent(self):
        cs = [c(f"Review: changes-needed @ {HEAD[:7]}", "r", at(1))]
        self.assertEqual(t.decide(pr(), cs + [c(f"Sign-off @ {HEAD[:7]}", at=at(2))], S)["state"], "failure")
        cs = []  # the finding was deleted and nothing else exists
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")  # still no approve or sign-off

    def test_cli_fails_closed_on_bad_input(self):
        with tempfile.TemporaryDirectory() as d:
            p, q = os.path.join(d, "p"), os.path.join(d, "q")
            open(p, "w").write("{not json")
            open(q, "w").write("[]")
            self.assertEqual(t.main([p, q]), 1)
            json.dump({"body": "Risk tier: Tier 3"}, open(p, "w"))  # no head sha
            self.assertEqual(t.main([p, q]), 1)


if __name__ == "__main__":
    unittest.main()
