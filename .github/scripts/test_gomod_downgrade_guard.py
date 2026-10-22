import os
import subprocess
import tempfile
import unittest

import gomod_downgrade_guard as g

COMMONS = "github.com/conduitio/conduit-commons"
BASE = """module x

go 1.26

require (
	github.com/conduitio/conduit-commons v0.12.1
	github.com/conduitio/conduit-connector-postgres v0.17.2
	github.com/some/direct v1.4.0
	github.com/some/pre v1.0.0-rc.2
)

require (
	github.com/some/indirect v1.2.0 // indirect
	github.com/conduitio/conduit-connector-protocol v0.11.1 // indirect
)
"""


def sub(**kw):
    out = BASE
    for k, v in kw.items():
        out = out.replace(k, v)
    return out


ENV = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}


def git(cwd, *a):
    return subprocess.run(["git", *a], cwd=cwd, check=True, capture_output=True, text=True, env=ENV).stdout.strip()


class Guard(unittest.TestCase):
    def run_guard(self, head, body=None, extra_base=None, extra_head=None):
        """Commit BASE then head (a go.mod text) in a scratch repo and compare
        them. extra_* map extra paths (nested go.mod) to text."""
        with tempfile.TemporaryDirectory() as d:
            git(d, "init", "-q", "-b", "main")
            revs = []
            for text, extra in ((BASE, extra_base or {}), (head, extra_head or {})):
                for path, content in {"go.mod": text, **extra}.items():
                    full = os.path.join(d, path)
                    os.makedirs(os.path.dirname(full), exist_ok=True)
                    with open(full, "w") as f:
                        f.write(content)
                git(d, "add", "-A")
                git(d, "commit", "-q", "--allow-empty", "-m", "c")
                revs.append(git(d, "rev-parse", "HEAD"))
            args = ["--base", revs[0], "--head", revs[1]]
            if body is not None:
                bp = os.path.join(d, "body.txt")
                with open(bp, "w") as f:
                    f.write(body)
                args += ["--body-file", bp]
            cwd = os.getcwd()
            os.chdir(d)
            try:
                return g.main(args)
            finally:
                os.chdir(cwd)

    def test_same_passes(self):
        self.assertEqual(self.run_guard(BASE), 0)

    def test_upgrade_passes(self):
        self.assertEqual(self.run_guard(sub(**{"v0.12.1": "v0.12.2"})), 0)

    def test_protected_downgrade_fails(self):
        # The #3192 shape: a stale rebase lowers conduit-commons.
        self.assertEqual(self.run_guard(sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})), 1)

    def test_builtin_connector_downgrade_fails(self):
        self.assertEqual(self.run_guard(sub(**{"postgres v0.17.2": "postgres v0.17.1"})), 1)

    def test_indirect_protected_downgrade_fails(self):
        self.assertEqual(self.run_guard(sub(**{"protocol v0.11.1": "protocol v0.11.0"})), 1)

    def test_direct_third_party_downgrade_fails(self):
        self.assertEqual(self.run_guard(sub(**{"direct v1.4.0": "direct v1.3.9"})), 1)

    def test_indirect_third_party_downgrade_ignored(self):
        self.assertEqual(self.run_guard(sub(**{"indirect v1.2.0": "indirect v1.1.0"})), 0)

    def test_release_candidate_to_older_rc_fails(self):
        self.assertEqual(self.run_guard(sub(**{"rc.2": "rc.1"})), 1)

    def test_rc_numeric_ordering_is_not_lexical(self):
        self.assertEqual(self.run_guard(sub(**{"rc.2": "rc.10"})), 0)

    def test_release_beats_prerelease(self):
        self.assertEqual(self.run_guard(sub(**{"v1.0.0-rc.2": "v1.0.0"})), 0)
        self.assertEqual(self.run_guard(sub(**{"direct v1.4.0": "direct v1.4.0-rc.1"})), 1)

    def test_removed_module_is_not_a_downgrade(self):
        self.assertEqual(self.run_guard(sub(**{"\tgithub.com/some/direct v1.4.0\n": ""})), 0)

    def test_waiver_with_reason_passes(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "text\ndeps-downgrade: v0.12.1 breaks the s3 build, see #1\n"), 0)

    def test_waiver_without_reason_fails(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "deps-downgrade:   \n"), 1)

    def test_waiver_mid_sentence_does_not_count(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "I did not add a deps-downgrade: line, sorry"), 1)

    def test_garbage_go_mod_fails_closed(self):
        self.assertEqual(self.run_guard("not a go.mod"), 2)
        self.assertEqual(self.run_guard("require (\n\tfoo v1.0.0\n"), 2)

    def test_waiver_inside_html_comment_does_not_count(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "<!-- deps-downgrade: because -->"), 1)

    def test_replace_pinning_a_lower_version_is_a_downgrade(self):
        head = BASE + "\nreplace github.com/conduitio/conduit-commons => github.com/conduitio/conduit-commons v0.11.0\n"
        self.assertEqual(self.run_guard(head), 1)

    def test_replace_block_pinning_a_lower_version_is_a_downgrade(self):
        head = BASE + "\nreplace (\n\tgithub.com/some/direct => github.com/some/direct v1.0.0\n)\n"
        self.assertEqual(self.run_guard(head), 1)

    def test_replace_to_a_fork_or_local_path_needs_the_waiver(self):
        head = BASE + "\nreplace github.com/conduitio/conduit-commons => ../commons\n"
        self.assertEqual(self.run_guard(head), 1)
        self.assertEqual(self.run_guard(head, "deps-downgrade: local debugging, removed before merge"), 0)
        fork = BASE + "\nreplace github.com/conduitio/conduit-commons => github.com/evil/commons v0.12.1\n"
        self.assertEqual(self.run_guard(fork), 1)

    def test_replace_unchanged_against_base_is_fine(self):
        base_with = "\nreplace github.com/conduitio/conduit-commons => ../commons\n"
        self.assertEqual(self.run_guard(BASE + base_with, extra_base=None), 1)  # base lacks it: new replace
        # same replace in both: compare two commits where both have it
        with tempfile.TemporaryDirectory() as d:
            git(d, "init", "-q", "-b", "main")
            open(os.path.join(d, "go.mod"), "w").write(BASE + base_with)
            git(d, "add", "-A"); git(d, "commit", "-q", "-m", "a"); a = git(d, "rev-parse", "HEAD")
            git(d, "commit", "-q", "--allow-empty", "-m", "b")
            cwd = os.getcwd(); os.chdir(d)
            try:
                self.assertEqual(g.main(["--base", a, "--head", "HEAD"]), 0)
            finally:
                os.chdir(cwd)

    def test_go_work_replace_is_checked(self):
        work = f"go 1.26\n\nuse .\n\nreplace {COMMONS} => ../commons\n"
        self.assertEqual(self.run_guard(BASE, extra_head={"go.work": work}, extra_base={"README": "x"}), 1)
        lower = f"go 1.26\n\nuse .\n\nreplace {COMMONS} => {COMMONS} v0.11.0\n"
        self.assertEqual(self.run_guard(BASE, extra_head={"go.work": lower}), 1)

    def test_go_work_use_change_needs_the_waiver(self):
        self.assertEqual(self.run_guard(BASE, extra_head={"go.work": "go 1.26\n\nuse (\n\t.\n\t../commons\n)\n"}), 1)
        self.assertEqual(self.run_guard(BASE, "deps-downgrade: local workspace, not committed to main",
                                        extra_head={"go.work": "go 1.26\n\nuse (\n\t.\n\t../commons\n)\n"}), 0)

    def test_nested_and_tools_go_mod_are_checked(self):
        nested = "module y\n\nrequire github.com/some/dep v1.2.0\n"
        lower = "module y\n\nrequire github.com/some/dep v1.1.0\n"
        self.assertEqual(self.run_guard(BASE, extra_base={"tools/go.mod": nested}, extra_head={"tools/go.mod": lower}), 1)
        self.assertEqual(self.run_guard(BASE, extra_base={"a/b/go.mod": nested}, extra_head={"a/b/go.mod": lower}), 1)
        self.assertEqual(self.run_guard(BASE, extra_base={"tools/go.mod": nested}, extra_head={"tools/go.mod": nested}), 0)

    def test_pseudo_versions_order_by_timestamp(self):
        a = "v0.0.0-20260101000000-aaaaaaaaaaaa"
        b = "v0.0.0-20260201000000-bbbbbbbbbbbb"
        self.assertLess(g.version_key(a), g.version_key(b))


if __name__ == "__main__":
    unittest.main()
