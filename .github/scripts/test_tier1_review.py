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
WORKFLOW = os.path.join(HERE, "..", "workflows", "tier1-review.yml")


@unittest.skipUnless(shutil.which("jq") and shutil.which("bash"), "needs jq and bash")
class WorkflowStep(unittest.TestCase):
    """Runs the workflow's `Decide and post the status` script under `bash -e`
    with a fake `gh`, as the runner does."""

    def step_script(self):
        text = open(WORKFLOW).read()
        m = re.search(r"- name: Decide and post the status\n\s+run: \|\n((?:(?:          .*)?\n)+)", text)
        self.assertIsNotNone(m, "step not found")
        return "\n".join(l[10:] for l in m.group(1).splitlines())

    def run_step(self, body, gh_script=None):
        with tempfile.TemporaryDirectory() as d:
            bindir, tmp = os.path.join(d, "bin"), os.path.join(d, "tmp")
            os.makedirs(bindir)
            os.makedirs(tmp)
            log = os.path.join(d, "posted.txt")
            fake = gh_script or f"""#!/bin/bash
case "$*" in
  *"/pulls/7/files"*) ;;      # --jq '.[]' of an empty list prints nothing
  *"/issues/7/comments"*) ;;
  *"/pulls/7"*) echo '{json.dumps({"body": body, "labels": [], "head": {"sha": HEAD}})}' ;;
  *"/statuses/"*) echo "$*" >> {log} ;;
esac
"""
            open(os.path.join(bindir, "gh"), "w").write(fake)
            os.chmod(os.path.join(bindir, "gh"), 0o755)
            env = dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"], RUNNER_TEMP=tmp,
                       GITHUB_STEP_SUMMARY=os.path.join(d, "summary"), GITHUB_SERVER_URL="https://github.com",
                       GITHUB_RUN_ID="1", REPO="o/r", PR="7", SIGNERS="devarismeroxa")
            r = subprocess.run(["bash", "-e", "-c", self.step_script()], cwd=os.path.join(HERE, "..", ".."),
                               env=env, capture_output=True, text=True)
            posted = open(log).read() if os.path.exists(log) else ""
            return r, posted

    def test_failing_verdict_still_posts_a_status_with_a_description(self):
        r, posted = self.run_step("## Tier and roadmap\n\nTier 2")  # no `Risk tier:` line
        self.assertIn("state=failure", posted, r.stderr)
        self.assertIn("context=tier1-review", posted)
        self.assertIn("Risk tier", posted)  # the description says why

    def test_the_step_passes_the_module_list(self):
        # Unwired, every dependency bump is Tier 1 (fail closed) instead of
        # being judged by its modules (#3389).
        self.assertIn("--tier1-modules .github/tier1-modules.txt", self.step_script())

    def test_passing_verdict_posts_success(self):
        r, posted = self.run_step("Risk tier: Tier 3")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("state=success", posted)

    def test_script_crash_posts_an_error_status(self):
        # A script that prints nothing must not leave the PR without a status.
        with tempfile.TemporaryDirectory() as d:
            script = self.step_script().replace("tier1_review.py", "does_not_exist.py")
            bindir = os.path.join(d, "bin")
            os.makedirs(bindir)
            log = os.path.join(d, "posted.txt")
            open(os.path.join(bindir, "gh"), "w").write(f"""#!/bin/bash
case "$*" in
  *"/pulls/7/files"*) ;;      # --jq '.[]' of an empty list prints nothing
  *"/issues/7/comments"*) ;;
  *"/pulls/7"*) echo '{json.dumps({"body": "x", "labels": [], "head": {"sha": HEAD}})}' ;;
  *"/statuses/"*) echo "$*" >> {log} ;;
esac
""")
            os.chmod(os.path.join(bindir, "gh"), 0o755)
            os.makedirs(os.path.join(d, "tmp"))
            env = dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"], RUNNER_TEMP=os.path.join(d, "tmp"),
                       GITHUB_STEP_SUMMARY=os.path.join(d, "s"), GITHUB_SERVER_URL="https://github.com",
                       GITHUB_RUN_ID="1", REPO="o/r", PR="7", SIGNERS="devarismeroxa")
            subprocess.run(["bash", "-e", "-c", script], cwd=os.path.join(HERE, "..", ".."), env=env,
                           capture_output=True, text=True)
            posted = open(log).read()
            self.assertIn("state=error", posted)
            self.assertIn("produced no verdict", posted)


if __name__ == "__main__":
    unittest.main()
