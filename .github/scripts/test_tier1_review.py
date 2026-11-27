import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest

import tier1_review as t

HEAD = "abcdef0123456789abcdef0123456789abcdef01"
OLD = "1111111222222233333334444444555555566666"


def pr(body="**Risk tier: Tier 1**", labels=(), head=HEAD, login="devarismeroxa"):
    # Every real PR has an author, and the Tier 1 stamp compares the verdicts'
    # logins against it, so the default fixture carries one.
    return {"body": body, "labels": [{"name": l} for l in labels], "head": {"sha": head},
            "user": {"login": login, "type": "User"}}


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

    def test_signer_and_allowlist_matching_ignores_login_case(self):
        # The signer list and --reviewers are written by hand; the login comes
        # from the API with its own casing. Matching them case-sensitively made
        # a valid sign-off invisible (fail closed, so it blocked rather than
        # passed, but it blocked for no reason).
        cs = [c(f"Review: approve @ {HEAD[:7]}", "r", at(1)),
              c(f"Sign-off @ {HEAD[:7]}", "DeVarisMeroxa", at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "success")
        x = c(f"Review: approve @ {HEAD[:7]}", "BotAccount", at(3), assoc="NONE")
        cs = [x, c(f"Sign-off @ {HEAD[:7]}", at=at(4))]
        self.assertEqual(t.decide(pr(), cs, S, reviewers={"botaccount"})["state"], "success")

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

    def test_unclosed_and_multiline_comments_are_stripped(self):
        self.assertEqual(t.decide(pr(body="Risk tier: Tier 3\n<!--\nRisk tier: Tier 1\n-->"), [], S)["state"], "success")
        # An unclosed comment hides the rest, so the Tier 3 line after it does not count.
        self.assertEqual(t.decide(pr(body="Risk tier: Tier 1\n<!-- Risk tier: Tier 3"), [], S)["state"], "failure")
        self.assertIsNone(t.declared_tier("<!-- Risk tier: Tier 1", set()))
        cs = [c(f"<!--\nReview: approve @ {HEAD[:7]}\n", "r", at(1)), c(f"Sign-off @ {HEAD[:7]}", at=at(2))]
        self.assertEqual(t.decide(pr(), cs, S)["state"], "failure")

    def test_multiple_declarations_take_the_highest_risk(self):
        body = "Risk tier: Tier 3\nRisk tier: Tier 1\nRisk tier: Tier 2"
        self.assertEqual(t.declared_tier(body, set()), 1)
        self.assertEqual(t.decide(pr(body=body), [], S)["state"], "failure")

    def test_rename_out_of_a_tier1_path_counts(self):
        import json as j, os as o, tempfile as tf
        with tf.TemporaryDirectory() as d:
            files, owners, prj, cm = (o.path.join(d, n) for n in ("f", "o", "p", "c"))
            j.dump([{"filename": "docs/moved.go", "previous_filename": "pkg/connector/x.go"}], open(files, "w"))
            open(owners, "w").write("/pkg/connector/ @devarismeroxa\n")
            j.dump(pr(body="Risk tier: Tier 3"), open(prj, "w"))
            j.dump([], open(cm, "w"))
            self.assertEqual(t.main([prj, cm, "--files", files, "--codeowners", owners]), 1)
            j.dump([{"filename": "docs/moved.go"}], open(files, "w"))
            self.assertEqual(t.main([prj, cm, "--files", files, "--codeowners", owners]), 0)

    def test_cli_fails_closed_on_bad_input(self):
        with tempfile.TemporaryDirectory() as d:
            p, q = os.path.join(d, "p"), os.path.join(d, "q")
            open(p, "w").write("{not json")
            open(q, "w").write("[]")
            self.assertEqual(t.main([p, q]), 1)
            json.dump({"body": "Risk tier: Tier 3"}, open(p, "w"))  # no head sha
            self.assertEqual(t.main([p, q]), 1)

    def test_cli_prints_the_verdict_when_it_fails(self):
        # The workflow reads the verdict from stdout even though the exit
        # status is 1.
        with tempfile.TemporaryDirectory() as d:
            prj, cm = os.path.join(d, "pr.json"), os.path.join(d, "c.json")
            json.dump(pr(body="## Tier and roadmap\n\nTier 2"), open(prj, "w"))
            json.dump([], open(cm, "w"))
            r = subprocess.run(["python3", os.path.join(HERE, "tier1_review.py"), prj, cm],
                               capture_output=True, text=True)
            self.assertEqual(r.returncode, 1)
            v = json.loads(r.stdout)
            self.assertEqual(v["state"], "failure")
            self.assertIn("Risk tier", v["description"])


def bot_pr(body="", login="dependabot[bot]", type="Bot", labels=(), head=HEAD):
    p = pr(body=body, labels=labels, head=head)
    p["user"] = {"login": login, "type": type}
    return p


class BotDeclaration(unittest.TestCase):
    """A GitHub App writes its body from a fixed template and cannot add a
    `Risk tier:` line, so the declaration was a gate no bot PR could ever pass
    (#3389). An App author with no declaration is read as Tier 3 — and the path
    and module floors still apply, so exempting the declaration does not exempt
    the tier."""

    def tier(self, p, files=(), patches=None):
        paths = t.tier1_patterns(OWNERS, S)
        return t.decide(p, [], S, files=files, tier1_paths=paths, patches=patches, tier1_mods=MODS)

    def test_a_bot_pr_with_no_declaration_is_tier3(self):
        r = self.tier(bot_pr())
        self.assertEqual(r["state"], "success")
        # Named in the description, so the history separates a tier a bot was
        # given from one a human declared.
        self.assertIn("bot default", r["description"])

    def test_a_human_pr_with_no_declaration_still_fails(self):
        r = self.tier(pr(body="fixes a bug"))
        self.assertEqual(r["state"], "failure")
        self.assertIn("Risk tier", r["description"])

    def test_the_api_type_and_the_login_suffix_both_identify_a_bot(self):
        self.assertTrue(t.is_bot({"login": "dependabot[bot]", "type": "Bot"}))
        self.assertTrue(t.is_bot({"login": "github-actions[bot]", "type": "User"}))  # suffix alone
        self.assertTrue(t.is_bot({"login": "renovate", "type": "Bot"}))  # type alone
        self.assertFalse(t.is_bot({"login": "notabot", "type": "User"}))
        self.assertFalse(t.is_bot({"login": "devarismeroxa", "type": "User"}))
        # A missing or empty user object is not a bot: the declaration stays
        # required rather than being waived by malformed input.
        self.assertFalse(t.is_bot({}))
        self.assertFalse(t.is_bot(None))
        self.assertEqual(self.tier(pr(body="no tier here"))["state"], "failure")

    def test_the_path_floor_still_reaches_a_bot_pr(self):
        r = self.tier(bot_pr(), files=["tools/release.go"])
        self.assertEqual(r["state"], "failure")
        self.assertIn("touches a Tier 1 path", r["description"])

    def test_the_module_floor_still_reaches_a_bot_pr(self):
        # The #3338 shape: a bot bump of a module that serializes store entries.
        r = self.tier(bot_pr(), files=["go.mod"], patches=bump())
        self.assertEqual(r["state"], "failure")
        self.assertIn("module github.com/conduitio/conduit-commons", r["description"])
        # And the #3337 shape: a bump of a module that is not on the list.
        p = bump(module="github.com/prometheus/client_golang", old="v1.23.0", new="v1.24.0")
        self.assertEqual(self.tier(bot_pr(), files=["go.mod"], patches=p)["state"], "success")

    def test_a_bot_pr_that_moves_the_go_directive_is_tier1(self):
        # #3329 arrived as a sigstore-go bump that also moved `go 1.26.11` to
        # `go 1.27.0`.
        p = {"go.mod": hunk("-go 1.26.11", "+go 1.27.0")}
        r = self.tier(bot_pr(), files=["go.mod"], patches=p)
        self.assertEqual(r["state"], "failure")
        self.assertIn("`go` directive", r["description"])

    def test_a_declaration_in_a_bot_body_still_counts(self):
        # The exemption is a default, not an override: a body or label that
        # raises the tier is still read.
        self.assertEqual(self.tier(bot_pr(body="Risk tier: Tier 1"))["state"], "failure")
        self.assertEqual(self.tier(bot_pr(labels=("tier-1",)))["state"], "failure")

    def test_do_not_merge_still_blocks_a_bot_pr(self):
        self.assertEqual(self.tier(bot_pr(labels=("do-not-merge",)))["state"], "failure")

    def test_a_signed_off_bot_pr_passes(self):
        # A Tier 1 bot bump is not unmergeable, it needs the verdict at its
        # current head — which a Dependabot rebase moves.
        paths = t.tier1_patterns(OWNERS, S)
        cs = [c(f"Review: approve @ {HEAD[:7]}", "r", at(1)), c(f"Sign-off @ {HEAD[:7]}", at=at(2))]
        r = t.decide(bot_pr(), cs, S, files=["go.mod"], tier1_paths=paths,
                     patches=bump(), tier1_mods=MODS)
        self.assertEqual(r["state"], "success")
        self.assertIn("signed off", r["description"])
        # After a rebase the verdict is stale again.
        r = t.decide(bot_pr(head=OLD), cs, S, files=["go.mod"], tier1_paths=paths,
                     patches=bump(), tier1_mods=MODS)
        self.assertEqual(r["state"], "failure")

    def test_cli_reads_the_author_from_the_pr_json(self):
        with tempfile.TemporaryDirectory() as d:
            prj, cm = os.path.join(d, "p"), os.path.join(d, "c")
            json.dump([], open(cm, "w"))
            json.dump(bot_pr(), open(prj, "w"))
            self.assertEqual(t.main([prj, cm]), 0)
            json.dump(pr(body="no tier"), open(prj, "w"))
            self.assertEqual(t.main([prj, cm]), 1)


class Certification(unittest.TestCase):
    """A Tier 1 success names how independent its two verdicts were (#3389).

    With one write account the author, the reviewing session and the signer are
    the same login. The gate cannot manufacture independence, so the status says
    which of the verdicts were the author's own instead of reading as peer
    review that never happened. Pass/fail is unchanged by any of this."""

    AUTHOR = "devarismeroxa"

    def verdicts(self, approver, signer, head=HEAD, **kw):
        cs = [c(f"Review: approve @ {head[:7]}", approver, at(1)),
              c(f"Sign-off @ {head[:7]}", signer, at(2))]
        return t.decide(pr(head=head, **kw), cs, {self.AUTHOR, "other-signer"})

    def test_both_verdicts_from_the_author_are_stamped_self_certified(self):
        r = self.verdicts(self.AUTHOR, self.AUTHOR)
        self.assertEqual(r["state"], "success")
        self.assertEqual(r["description"],
                         f"Tier 1: self-certified by author {self.AUTHOR} @ {HEAD[:7]}")

    def test_two_independent_verdicts_are_stamped_as_review(self):
        r = self.verdicts("reviewer", "other-signer")
        self.assertEqual(r["state"], "success")
        self.assertEqual(r["description"], f"Tier 1: review approved and signed off @ {HEAD[:7]}")
        self.assertNotIn("self", r["description"])

    def test_an_independent_approve_with_the_authors_signoff_names_both(self):
        r = self.verdicts("reviewer", self.AUTHOR)
        self.assertEqual(r["state"], "success")
        self.assertEqual(r["description"],
                         f"Tier 1: approved by reviewer, self-signed by author @ {HEAD[:7]}")

    def test_the_authors_approve_with_an_independent_signoff_names_both(self):
        r = self.verdicts(self.AUTHOR, "other-signer")
        self.assertEqual(r["state"], "success")
        self.assertEqual(r["description"],
                         f"Tier 1: self-approved by author, signed off by other-signer @ {HEAD[:7]}")

    def test_one_independent_approver_among_several_is_independence(self):
        # The author approving too does not cancel the other reviewer's approve.
        cs = [c(f"Review: approve @ {HEAD[:7]}", self.AUTHOR, at(1)),
              c(f"Review: approve @ {HEAD[:7]}", "reviewer", at(2)),
              c(f"Sign-off @ {HEAD[:7]}", "other-signer", at(3))]
        r = t.decide(pr(), cs, {self.AUTHOR, "other-signer"})
        self.assertEqual(r["description"], f"Tier 1: review approved and signed off @ {HEAD[:7]}")

    def test_the_author_match_ignores_login_case(self):
        # GitHub logins are case-insensitive, so a differently-cased login is
        # the same account and must not read as an independent certifier.
        r = self.verdicts("DeVarisMeroxa", "DEVARISMEROXA")
        self.assertEqual(r["state"], "success")
        self.assertEqual(r["description"],
                         f"Tier 1: self-certified by author {self.AUTHOR} @ {HEAD[:7]}")

    def test_an_unknown_author_does_not_claim_independence(self):
        # Fail closed on the wording: with no author object the gate cannot show
        # the verdicts came from anyone else, so it does not say they did.
        p = pr()
        del p["user"]
        cs = [c(f"Review: approve @ {HEAD[:7]}", "reviewer", at(1)),
              c(f"Sign-off @ {HEAD[:7]}", "devarismeroxa", at(2))]
        r = t.decide(p, cs, S)
        self.assertEqual(r["state"], "success")
        self.assertIn("independence unverified", r["description"])

    def test_a_stale_verdict_is_not_counted_as_a_certifier(self):
        # The sign-off is at the old head, so it is missing, not a certifier.
        cs = [c(f"Review: approve @ {HEAD[:7]}", self.AUTHOR, at(1)),
              c(f"Sign-off @ {OLD[:7]}", self.AUTHOR, at(2))]
        r = t.decide(pr(), cs, S)
        self.assertEqual(r["state"], "failure")
        self.assertIn("Sign-off", r["description"])

    def test_the_stamp_does_not_change_who_passes(self):
        # Self-certification is still a success: the gate checks the ritual, and
        # making it fail would block every PR while there is one account.
        paths = t.tier1_patterns(OWNERS, S)
        cs = [c(f"Review: approve @ {HEAD[:7]}", self.AUTHOR, at(1)),
              c(f"Sign-off @ {HEAD[:7]}", self.AUTHOR, at(2))]
        r = t.decide(pr(body="Risk tier: Tier 3"), cs, S, files=["tools/release.go"],
                     tier1_paths=paths)
        self.assertEqual(r["state"], "success")
        self.assertIn("self-certified by author", r["description"])

    def test_the_stamp_fits_a_github_status_description(self):
        # GitHub truncates at 140 characters; `detail` carries the whole line.
        for approver, signer in (("reviewer", "other-signer"), (self.AUTHOR, self.AUTHOR),
                                 ("reviewer", self.AUTHOR), (self.AUTHOR, "other-signer")):
            r = self.verdicts(approver, signer)
            self.assertLessEqual(len(r["description"]), 140)
            self.assertEqual(r["description"], r["detail"])

    def test_cli_stamps_a_self_certified_pr(self):
        with tempfile.TemporaryDirectory() as d:
            prj, cm = os.path.join(d, "p"), os.path.join(d, "c")
            json.dump(pr(), open(prj, "w"))
            json.dump([c(f"Review: approve @ {HEAD[:7]}", "devarismeroxa", at(1)),
                       c(f"Sign-off @ {HEAD[:7]}", "devarismeroxa", at(2))], open(cm, "w"))
            r = subprocess.run(["python3", os.path.join(HERE, "tier1_review.py"), prj, cm],
                               capture_output=True, text=True)
            self.assertEqual(r.returncode, 0)
            self.assertIn("self-certified by author devarismeroxa",
                          json.loads(r.stdout)["description"])


MODS = ["github.com/conduitio/conduit-commons", "github.com/conduitio/conduit-connector-sdk",
        "github.com/conduitio/conduit-commons/paramgen"]
OWNERS = ("*  @org/team @devarismeroxa\n/tools/ @devarismeroxa\n/tests/upgrade/ @devarismeroxa\n"
          "/go.mod @devarismeroxa\n/go.sum @devarismeroxa\n")


def hunk(*lines):
    """A GitHub `patch` body: a hunk header and the diff lines under it."""
    return "@@ -10,7 +10,7 @@\n" + "\n".join(lines)


def bump(name="go.mod", module="github.com/conduitio/conduit-commons", old="v0.13.1", new="v0.14.0"):
    return {name: hunk(f"-\t{module} {old}", f"+\t{module} {new}")}


class Modules(unittest.TestCase):
    """A dependency manifest takes its tier from the MODULES it moves, not from
    the directory it sits in (#3389). Every Go bump touches `go.mod`, so the
    file was the wrong granularity: owning it made every bump Tier 1."""

    def tier(self, files, patches=None, mods=MODS, body="Risk tier: Tier 3"):
        paths = t.tier1_patterns(OWNERS, S)
        return t.decide(pr(body=body), [], S, files=files, tier1_paths=paths,
                        patches=patches, tier1_mods=mods)

    # --- the list file -----------------------------------------------------

    def test_list_parsing_drops_comments_and_blanks(self):
        text = "# a comment\n\ngithub.com/a/b   # trailing\ngithub.com/c/d/\n   \n"
        self.assertEqual(t.tier1_modules(text), ["github.com/a/b", "github.com/c/d"])

    def test_a_list_entry_covers_everything_under_it(self):
        hit = lambda m: t.module_hit(m, MODS)
        self.assertEqual(hit("github.com/conduitio/conduit-commons"), MODS[0])
        self.assertEqual(hit("github.com/conduitio/conduit-commons/v2"), MODS[0])
        self.assertEqual(hit("GitHub.com/ConduitIO/Conduit-Commons"), MODS[0])
        # A longer NAME is a different module, not a child of this one.
        self.assertIsNone(hit("github.com/conduitio/conduit-commons-extra"))
        self.assertIsNone(hit("github.com/cohere-ai/cohere-go/v2"))

    def test_the_shipped_list_is_well_formed(self):
        with open(os.path.join(HERE, "..", "tier1-modules.txt")) as f:
            mods = t.tier1_modules(f.read())
        self.assertGreater(len(mods), 10)
        self.assertEqual(len(mods), len(set(mods)), "duplicate entry")
        for m in mods:
            self.assertRegex(m, t.MODULE_PATH, f"{m} is not a module path")
            covered = [o for o in mods if o != m and t.module_hit(m, [o])]
            self.assertEqual(covered, [], f"{m} is already covered by {covered}")
        # #3208 was a conduit-connector-sdk bump that fixed a source `Run`
        # `tomb.Go` race: a bump genuinely can be a data-path change.
        self.assertIn("github.com/conduitio/conduit-connector-sdk", mods)

    # --- the floor itself --------------------------------------------------

    def test_bumping_a_listed_module_is_tier1(self):
        r = self.tier(["go.mod"], bump())
        self.assertEqual(r["state"], "failure")
        self.assertIn("module github.com/conduitio/conduit-commons", r["description"])
        # The 140-character status cap can cut a long module path out of the
        # description, so the whole reason travels in `detail` as well.
        self.assertIn("module github.com/conduitio/conduit-commons", r["detail"])
        self.assertLessEqual(len(r["description"]), 140)

    def test_bumping_an_unlisted_module_is_not(self):
        # The #3389 case: a routine Dependabot bump stops being Tier 1.
        p = bump(module="github.com/prometheus/client_golang", old="v1.23.0", new="v1.24.0")
        self.assertEqual(self.tier(["go.mod"], p)["state"], "success")

    def test_go_sum_alone_is_enough(self):
        p = {"go.sum": hunk(
            "-github.com/conduitio/conduit-commons v0.13.1 h1:aaa=",
            "-github.com/conduitio/conduit-commons v0.13.1/go.mod h1:bbb=",
            "+github.com/conduitio/conduit-commons v0.14.0 h1:ccc=",
            "+github.com/conduitio/conduit-commons v0.14.0/go.mod h1:ddd=")}
        self.assertEqual(self.tier(["go.sum"], p)["state"], "failure")
        # The `v1.2.3/go.mod` in the version column is not a module path.
        self.assertEqual(t.manifest_modules("go.sum", p["go.sum"]),
                         {"github.com/conduitio/conduit-commons"})

    def test_both_sides_of_a_replace_count(self):
        p = {"go.mod": hunk("+replace github.com/x/y v1.0.0 => github.com/conduitio/conduit-commons v0.14.0")}
        self.assertEqual(self.tier(["go.mod"], p)["state"], "failure")

    def test_indirect_comments_and_block_openers_are_not_lines_to_classify(self):
        p = {"go.mod": hunk("+require (", "+\tgithub.com/goccy/go-json v0.11.2 // indirect", "+)")}
        self.assertEqual(t.manifest_modules("go.mod", p["go.mod"]), {"github.com/goccy/go-json"})

    # --- the floors that need no list -------------------------------------

    def test_the_go_directive_is_tier1_on_its_own(self):
        # The runtime under the whole data path; #2914 cleared 20 stdlib
        # vulnerabilities with one of these.
        for line, want in (("go 1.26.11", "`go` directive"), ("toolchain go1.26.11", "`toolchain` directive"),
                           ("module github.com/conduitio/conduit", "`module` directive")):
            p = {"go.mod": hunk(f"-{line}", f"+{line}x")}
            r = self.tier(["go.mod"], p)
            self.assertEqual(r["state"], "failure", line)
            self.assertIn(want, r["description"])

    def test_a_manifest_with_no_diff_is_tier1(self):
        # The API omits `patch` for a diff it will not render. Fail closed.
        r = self.tier(["go.mod"], {})
        self.assertEqual(r["state"], "failure")
        self.assertIn("no diff for go.mod", r["description"])

    def test_an_unclassifiable_line_is_tier1(self):
        for line in ("+retract v0.1.0", "+godebug default=go1.25", "+something entirely new"):
            r = self.tier(["go.mod"], {"go.mod": hunk(line)})
            self.assertEqual(r["state"], "failure", line)
            self.assertIn("unclassifiable line in go.mod", r["description"])

    def test_an_empty_or_unwired_list_makes_every_bump_tier1(self):
        r = self.tier(["go.mod"], bump(), mods=[])
        self.assertEqual(r["state"], "failure")
        self.assertIn("no Tier 1 module list", r["description"])

    # --- the path floor no longer reaches manifests ------------------------

    def test_a_manifest_does_not_inherit_its_directory(self):
        # `/tools/` and `/tests/upgrade/` are Tier 1 paths, but the manifests
        # inside them are judged by their modules like any other.
        p = bump("tools/go.mod", "github.com/golangci/golangci-lint/v2", "v2.6.0", "v2.7.0")
        self.assertEqual(self.tier(["tools/go.mod"], p)["state"], "success")
        p = bump("tests/upgrade/storecompat/downgrade/go.sum", "github.com/kr/pretty", "v0.3.0", "v0.3.1")
        self.assertEqual(self.tier(["tests/upgrade/storecompat/downgrade/go.sum"], p)["state"], "success")
        # A listed module in a nested manifest is still Tier 1.
        p = bump("tools/go.mod", "github.com/conduitio/conduit-commons/paramgen")
        self.assertEqual(self.tier(["tools/go.mod"], p)["state"], "failure")

    def test_a_non_manifest_file_still_takes_its_tier_from_the_path(self):
        r = self.tier(["tools/release.go"])
        self.assertEqual(r["state"], "failure")
        self.assertIn("touches a Tier 1 path", r["description"])

    def test_the_path_floor_wins_when_both_apply(self):
        r = self.tier(["pkg/connector/x.go", "go.mod"], bump(),
                      mods=MODS)
        self.assertIn("Tier 1", r["description"])
        self.assertEqual(r["state"], "failure")

    def test_a_golden_testdata_manifest_is_judged_by_its_path(self):
        # pkg/scaffold's golden connector carries its own go.mod naming the SDK.
        # It is fixture content, not a dependency of this build, so it stays on
        # the path rule rather than becoming Tier 1 on every regeneration.
        f = "pkg/scaffold/template/testdata/golden/connector/go.mod"
        p = bump(f, "github.com/conduitio/conduit-connector-sdk", "v0.18.1", "v0.19.0")
        self.assertFalse(t.is_dep_manifest(f))
        self.assertEqual(self.tier([f], p)["state"], "success")
        # Under a Tier 1 path it is Tier 1 again, by that path.
        f = "tools/testdata/go.mod"
        r = self.tier([f], bump(f, "github.com/kr/pretty"))
        self.assertEqual(r["state"], "failure")
        self.assertIn("touches a Tier 1 path", r["description"])

    def test_is_manifest(self):
        for p in ("go.mod", "go.sum", "tools/go.mod", "a/b/c/go.sum"):
            self.assertTrue(t.is_manifest(p), p)
        for p in ("go.mod.bak", "pkg/gomod.go", "docs/go.mod.md", "tools/tools.go"):
            self.assertFalse(t.is_manifest(p), p)

    # --- end to end --------------------------------------------------------

    def test_cli_reads_the_patch_and_the_module_list(self):
        with tempfile.TemporaryDirectory() as d:
            files, owners, prj, cm, ml = (os.path.join(d, n) for n in ("f", "o", "p", "c", "m"))
            json.dump(pr(body="Risk tier: Tier 3"), open(prj, "w"))
            json.dump([], open(cm, "w"))
            with open(owners, "w") as f:
                f.write(OWNERS)
            with open(ml, "w") as f:
                f.write("# the list\ngithub.com/conduitio/conduit-commons\n")
            argv = [prj, cm, "--files", files, "--codeowners", owners, "--tier1-modules", ml]

            def changed(module):
                return [{"filename": "go.mod", "patch": hunk(f"-\t{module} v1.0.0", f"+\t{module} v1.1.0")}]

            json.dump(changed("github.com/cohere-ai/cohere-go/v2"), open(files, "w"))
            self.assertEqual(t.main(argv), 0)
            json.dump(changed("github.com/conduitio/conduit-commons"), open(files, "w"))
            self.assertEqual(t.main(argv), 1)
            # No `patch` key at all: fail closed.
            json.dump([{"filename": "go.mod"}], open(files, "w"))
            self.assertEqual(t.main(argv), 1)
            # Without the flag the list is empty, which is also fail closed.
            json.dump(changed("github.com/cohere-ai/cohere-go/v2"), open(files, "w"))
            self.assertEqual(t.main([prj, cm, "--files", files, "--codeowners", owners]), 1)


HERE = os.path.dirname(os.path.abspath(__file__))
DOTGITHUB = os.path.abspath(os.path.join(HERE, ".."))
WORKFLOW = os.path.join(DOTGITHUB, "workflows", "tier1-review.yml")

# A fake `gh`: it answers the reads the shell scripts make from files in a
# directory, and records the writes. Nothing in the gate's shell is exercised by
# mocking Python, so this is where the posting path gets its coverage.
GH_FAKE = r"""#!/bin/bash
data="__DATA__"; log="__LOG__"
url=""
for a in "$@"; do case "$a" in repos/*|*/repos/*) url="$a" ;; esac; done
# `#` as the delimiter: the alternation needs the pipe.
num=$(printf '%s' "$url" | sed -nE 's#.*/(pulls|issues)/([0-9]+).*#\2#p')
sha=$(printf '%s' "$url" | sed -nE 's#.*/(commits|statuses)/([0-9a-f]+).*#\2#p')
case "$url" in
  *"/pulls?state=open"*)
      # No list file means the API call failed, not that there are no PRs.
      [ -f "$data/list" ] || { echo "gh: cannot list pulls" >&2; exit 1; }
      cat "$data/list"; exit 0 ;;
  *"/pulls/"*"/files"*)   cat "$data/$num.files" 2>/dev/null; exit 0 ;;
  *"/issues/"*"/comments"*) cat "$data/$num.comments" 2>/dev/null; exit 0 ;;
  *"/commits/"*"/statuses"*) cat "$data/poststatuses-$sha" 2>/dev/null; exit 0 ;;
  *"/commits/"*"/status"*) cat "$data/status-$sha" 2>/dev/null; exit 0 ;;
  *"/actions/runs/"*)
      id=$(printf '%s' "$url" | sed -nE 's#.*/actions/runs/([0-9]+).*#\1#p')
      [ -f "$data/run-$id" ] || { echo "gh: no run $id" >&2; exit 1; }
      cat "$data/run-$id"; exit 0 ;;
  *"/pulls/"*)
      [ -f "$data/$num.json" ] || { echo "gh: no such PR $num" >&2; exit 1; }
      cat "$data/$num.json"; exit 0 ;;
  *"/statuses/"*)
      printf '%s\n' "$*" >> "$log"
      state=""; desc=""
      for a in "$@"; do
        case "$a" in
          state=*) state="${a#state=}" ;;
          description=*) desc="${a#description=}" ;;
        esac
      done
      printf '%s\037%s\n' "$state" "$desc" > "$data/status-$sha"
      # The status history, newest first, as the statuses endpoint returns it.
      url_arg=""
      for a in "$@"; do case "$a" in target_url=*) url_arg="${a#target_url=}" ;; esac; done
      printf '%s\037%s\n' "$state" "$url_arg" | cat - "$data/poststatuses-$sha" 2>/dev/null \
        > "$data/poststatuses-$sha.new"
      mv "$data/poststatuses-$sha.new" "$data/poststatuses-$sha"
      exit 0 ;;
esac
printf 'CMD %s\n' "$*" >> "$log"   # gh label create, gh pr edit
exit 0
"""


class Shell:
    """A throwaway checkout holding the gate's files, a fake `gh` on PATH, and
    the data that fake answers from."""

    def __init__(self, d, omit=()):
        self.d = d
        self.data = os.path.join(d, "data")
        self.log = os.path.join(d, "posted.txt")
        self.scripts = os.path.join(d, ".github", "scripts")
        os.makedirs(self.scripts)
        os.makedirs(self.data)
        os.makedirs(os.path.join(d, "bin"))
        os.makedirs(os.path.join(d, "tmp"))
        for n in ("tier1_status.sh", "tier1_backfill.sh", "tier1_provenance.sh", "tier1_review.py"):
            if n not in omit:
                shutil.copy(os.path.join(HERE, n), os.path.join(self.scripts, n))
        for n in ("CODEOWNERS", "tier1-modules.txt"):
            shutil.copy(os.path.join(DOTGITHUB, n), os.path.join(d, ".github", n))
        # GITHUB_RUN_ID=1 below, so a status this fixture posts names run 1;
        # provenance has to find it and see the gate's own workflow.
        self.add_run(1, ".github/workflows/tier1-review.yml")
        gh = os.path.join(d, "bin", "gh")
        with open(gh, "w") as f:
            f.write(GH_FAKE.replace("__DATA__", self.data).replace("__LOG__", self.log))
        os.chmod(gh, 0o755)

    def add_pr(self, n, body="Risk tier: Tier 3", head=None, files=(), comments=()):
        head = head or (f"{n:07d}" + "abcdef0123456789" * 3)[:40]  # 40 hex chars, as GitHub's are
        with open(os.path.join(self.data, f"{n}.json"), "w") as f:
            json.dump({"body": body, "labels": [], "head": {"sha": head},
                       "user": {"login": "devarismeroxa", "type": "User"}}, f)
        # The scripts pipe these through `--jq '.[]'`, so the fake answers with
        # one object per line, as that flag would print.
        for name, items in (("files", files), ("comments", comments)):
            if items:
                with open(os.path.join(self.data, f"{n}.{name}"), "w") as f:
                    for x in items:
                        f.write(json.dumps(x) + "\n")
        return head

    def add_run(self, run_id, path):
        """An Actions run the fake `gh` will answer for; absent means a 404."""
        with open(os.path.join(self.data, f"run-{run_id}"), "w") as f:
            f.write(path + "\n")

    def add_status_history(self, head, *posts):
        """The `tier1-review` posts already at a head, newest first, as
        (state, target_url) pairs."""
        with open(os.path.join(self.data, f"poststatuses-{head}"), "w") as f:
            for state, url in posts:
                f.write(f"{state}\x1f{url}\n")

    def open_prs(self, *numbers):
        with open(os.path.join(self.data, "list"), "w") as f:
            f.write("".join(f"{n}\n" for n in numbers))

    def run(self, script, *args):
        env = dict(os.environ, PATH=os.path.join(self.d, "bin") + os.pathsep + os.environ["PATH"],
                   RUNNER_TEMP=os.path.join(self.d, "tmp"),
                   GITHUB_STEP_SUMMARY=os.path.join(self.d, "summary"),
                   GITHUB_SERVER_URL="https://github.com", GITHUB_RUN_ID="1",
                   REPO="o/r", SIGNERS="devarismeroxa")
        return subprocess.run(["bash", os.path.join(self.scripts, script), *args],
                              cwd=self.d, env=env, capture_output=True, text=True)

    def posted(self):
        return open(self.log).read() if os.path.exists(self.log) else ""

    def summary(self):
        p = os.path.join(self.d, "summary")
        return open(p).read() if os.path.exists(p) else ""


@unittest.skipUnless(shutil.which("jq") and shutil.which("bash"), "needs jq and bash")
class StatusScript(unittest.TestCase):
    """`tier1_status.sh`: decide ONE PR and post the commit status. The per-PR
    job and the backfill both call it, so this is the posting path for both."""

    def test_failing_verdict_still_posts_a_status_with_a_description(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(7, body="## Tier and roadmap\n\nTier 2")  # no `Risk tier:` line
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("state=failure", sh.posted())
            self.assertIn("context=tier1-review", sh.posted())
            self.assertIn("Risk tier", sh.posted())  # the description says why

    def test_passing_verdict_posts_success(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(7)
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("state=success", sh.posted())

    def test_the_script_passes_the_module_list_and_the_default_branch_codeowners(self):
        # Unwired, every dependency bump is Tier 1 (fail closed) instead of
        # being judged by its modules (#3389); and CODEOWNERS must come from the
        # checkout, never from the PR.
        s = open(os.path.join(HERE, "tier1_status.sh")).read()
        self.assertIn("--tier1-modules", s)
        self.assertIn("$root/.github/tier1-modules.txt", s)
        self.assertIn("$root/.github/CODEOWNERS", s)

    def test_a_tier1_path_is_read_from_the_file_list(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(7, body="Risk tier: Tier 3", files=[{"filename": "pkg/connector/x.go"}])
            sh.run("tier1_status.sh", "7")
            self.assertIn("state=failure", sh.posted())
            self.assertIn("Tier+1+path", sh.posted().replace(" ", "+"))

    def test_an_identical_verdict_is_not_reposted(self):
        # The nightly re-decides every open PR; a duplicate row a day would bury
        # the entries that mean something.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(7)
            sh.run("tier1_status.sh", "7")
            first = sh.posted()
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(sh.posted(), first)  # nothing new was posted
            self.assertIn("unchanged", r.stdout)

    def test_a_changed_verdict_is_reposted(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            head = sh.add_pr(7)
            sh.run("tier1_status.sh", "7")
            sh.add_pr(7, body="Risk tier: Tier 1", head=head)  # now needs a review
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("state=failure", sh.posted())
            # Reported as a change, not as a PR the gate had never reached.
            self.assertIn("changed (was success)", r.stdout)

    def test_a_failed_read_posts_nothing_and_fails(self):
        # Judging a PR on the half of it that arrived is worse than not judging
        # it: the backfill counts this as undecided and goes red.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)  # PR 7 has no data file, so the fake `gh` fails
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 1)
            self.assertEqual(sh.posted(), "")

    def test_a_pr_with_no_head_sha_fails_without_posting(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            with open(os.path.join(sh.data, "7.json"), "w") as f:
                json.dump({"body": "Risk tier: Tier 3", "labels": [], "head": {}}, f)
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 1)
            self.assertEqual(sh.posted(), "")
            self.assertIn("no head sha", r.stderr + sh.summary())

    def test_a_pr_number_that_is_not_a_number_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            for bad in ("", "7; rm -rf /", "../../etc/passwd", "7a"):
                r = sh.run("tier1_status.sh", bad)
                self.assertEqual(r.returncode, 1, bad)
                self.assertEqual(sh.posted(), "")

    def test_script_crash_posts_an_error_status(self):
        # A gate that prints nothing must not leave the PR without a status.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d, omit=("tier1_review.py",))
            sh.add_pr(7)
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("state=error", sh.posted())
            self.assertIn("produced+no+verdict", sh.posted().replace(" ", "+"))

    def test_a_changes_needed_verdict_applies_the_label_before_the_repost_check(self):
        # So a label taken off by hand comes back on the next run even when the
        # verdict itself has not moved.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(7, body="Risk tier: Tier 1", head=HEAD, comments=[
                {"id": 1, "body": f"Review: changes-needed @ {HEAD[:7]}",
                 "created_at": "2026-10-21T00:00:01Z", "updated_at": "2026-10-21T00:00:01Z",
                 "author_association": "OWNER", "user": {"login": "devarismeroxa"}}])
            r = sh.run("tier1_status.sh", "7")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("state=failure", sh.posted())
            self.assertIn("add-label do-not-merge", sh.posted())
            sh.run("tier1_status.sh", "7")  # unchanged verdict
            self.assertEqual(sh.posted().count("add-label do-not-merge"), 2)
            self.assertEqual(sh.posted().count("state=failure"), 1)


@unittest.skipUnless(shutil.which("jq") and shutil.which("bash"), "needs jq and bash")
class Backfill(unittest.TestCase):
    """`tier1_backfill.sh`: every open PR gets a verdict, so none sits with no
    `tier1-review` status at all — the prerequisite for requiring the context
    (#3389). It also re-decides a rebased head without waiting for a comment."""

    def test_every_open_pr_gets_a_status(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            sh.add_pr(12, body="fixes a thing")  # no declaration: a failing verdict
            sh.add_pr(13)
            sh.open_prs(11, 12, 13)
            r = sh.run("tier1_backfill.sh")
            self.assertEqual(r.returncode, 0, r.stderr + r.stdout)
            self.assertEqual(sh.posted().count("context=tier1-review"), 3)
            self.assertIn("3 open PR(s), 3 new, 0 changed, 0 unchanged, 0 undecided, 0 suspect", r.stdout)

    def test_a_second_run_posts_nothing_new(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            sh.open_prs(11)
            sh.run("tier1_backfill.sh")
            r = sh.run("tier1_backfill.sh")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(sh.posted().count("context=tier1-review"), 1)
            self.assertIn("1 open PR(s), 0 new, 0 changed, 1 unchanged, 0 undecided, 0 suspect", r.stdout)

    def test_one_undecided_pr_does_not_stop_the_others_and_fails_the_job(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            sh.add_pr(13)
            sh.open_prs(11, 12, 13)  # 12 has no data: the fake `gh` fails on it
            r = sh.run("tier1_backfill.sh")
            self.assertEqual(r.returncode, 1)
            self.assertEqual(sh.posted().count("context=tier1-review"), 2)
            self.assertIn("COULD NOT DECIDE", r.stdout + r.stderr)
            self.assertIn("3 open PR(s), 2 new, 0 changed, 0 unchanged, 1 undecided, 0 suspect", r.stdout)

    def test_a_failed_listing_fails_closed(self):
        # Not "no open PRs, nothing to do": that would be a silent green nightly
        # while every PR went unjudged.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            r = sh.run("tier1_backfill.sh")  # no list file
            self.assertEqual(r.returncode, 1)
            self.assertEqual(sh.posted(), "")
            self.assertIn("cannot list the open PRs", r.stderr + sh.summary())

    def test_an_explicit_pr_number_decides_only_that_one(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            sh.add_pr(12)
            sh.open_prs(11, 12)
            r = sh.run("tier1_backfill.sh", "12")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("#12", sh.summary())
            self.assertNotIn("#11", sh.summary())
            self.assertIn("1 open PR(s), 1 new", r.stdout)

    def test_a_dispatch_input_that_is_not_a_number_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            sh.open_prs(11)
            for bad in ("12; touch /tmp/pwned", "--repo other/repo", "all"):
                r = sh.run("tier1_backfill.sh", bad)
                self.assertEqual(r.returncode, 1, bad)
                self.assertEqual(sh.posted(), "")

    def test_a_rebased_head_gets_a_fresh_verdict(self):
        # The verdict is pinned to the head sha, so a rebase stales it. Before
        # the backfill, the new head carried no status until something commented.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            old = sh.add_pr(11, body="Risk tier: Tier 1")
            sh.open_prs(11)
            sh.run("tier1_backfill.sh")
            self.assertIn(f"statuses/{old}", sh.posted())
            new = "b" * 40
            sh.add_pr(11, body="Risk tier: Tier 1", head=new)
            sh.run("tier1_backfill.sh")
            self.assertIn(f"statuses/{new}", sh.posted())


@unittest.skipUnless(shutil.which("jq") and shutil.which("bash"), "needs jq and bash")
class Provenance(unittest.TestCase):
    """`tier1_provenance.sh`: every `tier1-review` status at a head must have
    been written by a run of the gate's own workflow (#3400).

    `target_url` is set by whoever posts the status, so it is a claim, not
    proof — but it is a checkable claim, and the checks below are what a forged
    status has to survive."""

    GATE = ".github/workflows/tier1-review.yml"
    RUN = "https://github.com/o/r/actions/runs/"

    def check(self, posts, computed="success", runs=((7001, GATE),), head=None):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            head = head or sh.add_pr(11)
            for rid, path in runs:
                sh.add_run(rid, path)
            sh.add_status_history(head, *posts)
            r = sh.run("tier1_provenance.sh", "11", head, computed)
            return r, sh.summary()

    def test_a_status_from_a_run_of_the_gate_passes(self):
        r, _ = self.check([("success", self.RUN + "7001")])
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn("SUSPECT", r.stdout + r.stderr)

    def test_no_statuses_at_all_is_not_a_finding(self):
        # A PR the gate has not reached yet is the backfill's job, not a forgery.
        r, _ = self.check([])
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_a_target_url_that_is_not_a_run_of_this_repo_is_suspect(self):
        for url in ("", "https://example.com/pwned", "https://github.com/o/r/actions/runs/",
                    "https://github.com/other/repo/actions/runs/7001",
                    "https://github.com/o/r/actions/runs/abc"):
            r, summary = self.check([("success", url)])
            self.assertEqual(r.returncode, 2, f"{url!r}: {r.stdout}{r.stderr}")
            self.assertIn("SUSPECT", r.stderr + summary)

    def test_a_status_written_by_another_workflow_is_suspect(self):
        # The shape of the attack: a workflow the PR added posts the context.
        r, summary = self.check([("success", self.RUN + "9999")],
                                runs=((9999, ".github/workflows/sneaky.yml"),))
        self.assertEqual(r.returncode, 2, r.stdout)
        self.assertIn("sneaky.yml", r.stderr + summary)

    def test_one_run_claiming_two_different_verdicts_is_suspect(self):
        # The gate posts once per run, so a borrowed run id shows up twice.
        r, summary = self.check([("success", self.RUN + "7001"), ("failure", self.RUN + "7001")])
        self.assertEqual(r.returncode, 2, r.stdout)
        self.assertIn("claimed by both", r.stderr + summary)

    def test_one_run_posting_the_same_verdict_twice_is_not_suspect(self):
        # A re-run of the same run posts again; identical verdicts are benign.
        r, _ = self.check([("success", self.RUN + "7001"), ("success", self.RUN + "7001")])
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_a_missing_run_that_agrees_with_the_verdict_is_unverifiable(self):
        # Actions deletes old run metadata, so a 404 on an old PR is not
        # evidence of anything when the posted verdict still matches.
        r, _ = self.check([("success", self.RUN + "4242")], computed="success", runs=())
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("no longer has", r.stdout)

    def test_a_missing_run_that_disagrees_with_the_verdict_is_suspect(self):
        # A stale run id attached to a verdict the gate does not compute is the
        # shape of the attack, not an old PR.
        r, summary = self.check([("success", self.RUN + "4242")], computed="failure", runs=())
        self.assertEqual(r.returncode, 2, r.stdout)
        self.assertIn("does not have", r.stderr + summary)

    def test_a_pr_number_that_is_not_a_number_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            head = sh.add_pr(12)
            for bad in ("", "11; id", "all"):
                self.assertEqual(sh.run("tier1_provenance.sh", bad, head, "success").returncode, 1, bad)
            # And the sha is checked the same way: it reaches an API path.
            for bad in ("", "deadbee; id", "../../etc", "zz" * 20):
                self.assertEqual(sh.run("tier1_provenance.sh", "12", bad, "success").returncode, 1, bad)

    def test_a_status_list_the_api_will_not_answer_for_cannot_be_checked(self):
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            head = sh.add_pr(11)
            # The fake `gh` fails the statuses call when told to.
            os.remove(os.path.join(sh.d, "bin", "gh"))
            self.assertEqual(sh.run("tier1_provenance.sh", "11", head, "success").returncode, 1)

    def test_the_status_line_carries_the_full_sha(self):
        # The backfill reads the sha off that line and looks the status history
        # up by it. A short sha matched no history and verified nothing, which
        # is the worst failure available to a detection check: silently green.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            head = sh.add_pr(11)
            r = sh.run("tier1_status.sh", "11")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(len(head), 40)
            self.assertIn(f"#11 {head} ", r.stdout)

    def test_the_backfill_fails_the_job_on_a_suspect_status(self):
        # Detection that does not turn the nightly red is not detection.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            head = sh.add_pr(11)
            sh.add_run(9999, ".github/workflows/sneaky.yml")
            sh.add_status_history(head, ("success", "https://github.com/o/r/actions/runs/9999"))
            sh.open_prs(11)
            r = sh.run("tier1_backfill.sh")
            self.assertEqual(r.returncode, 1)
            self.assertIn("SUSPECT", r.stderr + sh.summary())
            self.assertIn("1 suspect", r.stdout)

    def test_the_backfill_is_green_when_its_own_posts_are_the_history(self):
        # End to end: the fixture's POST records the status with the run id the
        # job is running as, and provenance then verifies it.
        with tempfile.TemporaryDirectory() as d:
            sh = Shell(d)
            sh.add_pr(11)
            sh.open_prs(11)
            r = sh.run("tier1_backfill.sh")
            self.assertEqual(r.returncode, 0, r.stderr + r.stdout)
            self.assertNotIn("SUSPECT", r.stderr + r.stdout)
            self.assertIn("0 suspect", r.stdout)


class Workflow(unittest.TestCase):
    """The wiring: the two jobs, their triggers, and what they run."""

    def setUp(self):
        self.text = open(WORKFLOW).read()

    def test_the_backfill_checks_provenance(self):
        # The backfill is the only place that would notice a status this gate
        # did not write (#3400).
        b = open(os.path.join(HERE, "tier1_backfill.sh")).read()
        self.assertIn("tier1_provenance.sh", b)

    def test_both_jobs_call_the_shared_scripts(self):
        # One script for both paths, so the nightly and the event path cannot
        # drift into judging the same PR by different rules.
        self.assertIn('run: .github/scripts/tier1_status.sh "$PR"', self.text)
        self.assertIn('run: .github/scripts/tier1_backfill.sh "$ONLY_PR"', self.text)

    def test_the_backfill_is_scheduled_and_dispatchable(self):
        self.assertRegex(self.text, r"schedule:\n(\s*#.*\n)*\s*- cron: '[\d*/ ,-]+'")
        self.assertIn("workflow_dispatch:", self.text)

    def test_the_backfill_never_runs_on_a_pr_event(self):
        # Otherwise every push would re-decide every open PR.
        m = re.search(r"\n  backfill:\n(.*?)(?=\n  \w|\Z)", self.text, re.S)
        self.assertIsNotNone(m)
        self.assertIn("github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'",
                      m.group(1))
        # And the per-PR job does not run on the schedule.
        per = re.search(r"\n  tier1-review:\n(.*?)(?=\n  backfill:)", self.text, re.S)
        self.assertIn("github.event_name == 'pull_request_target' || github.event.issue.pull_request",
                      per.group(1))

    def test_both_jobs_check_out_the_default_branch(self):
        # The rules that judge a PR are the merged ones, never a branch's
        # proposal — including on a dispatch from another ref.
        self.assertEqual(self.text.count("ref: ${{ github.event.repository.default_branch }}"), 2)

    def test_the_dispatch_input_does_not_reach_a_command_line(self):
        # It is outside input: it goes through the environment, and the script
        # checks it is a number.
        self.assertIn("ONLY_PR: ${{ inputs.pr }}", self.text)
        self.assertNotIn("${{ inputs.pr }}\"", self.text)
        self.assertNotIn("tier1_backfill.sh ${{", self.text)

    def test_both_jobs_run_the_gate_tests_first(self):
        self.assertEqual(self.text.count("python3 -m unittest -v test_tier1_review"), 2)


if __name__ == "__main__":
    unittest.main()
