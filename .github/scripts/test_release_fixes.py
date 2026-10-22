import os
import subprocess
import tempfile
import unittest

import release_fixes as r

ENV = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t",
       "GIT_COMMITTER_EMAIL": "t@t"}
COMMONS = "github.com/conduitio/conduit-commons"


def sh(cwd, *args):
    return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True, env=ENV).stdout.strip()


def gomod(version, extra=""):
    return f"module x\n\nrequire {COMMONS} {version}\n{extra}"


class Fixture(unittest.TestCase):
    """Dependency repo: a (v0.7.1) -> fix (v0.7.2) -> b (unreleased).
    This repo: old (v0.20.4) -> prev (v0.20.5, pins commons v0.7.1) -> HEAD."""

    def setUp(self):
        self.d = tempfile.TemporaryDirectory()
        self.addCleanup(self.d.cleanup)
        root = self.d.name
        self.remote = os.path.join(root, "remote")
        dep = os.path.join(self.remote, "conduit-commons")
        os.makedirs(dep)
        sh(dep, "git", "init", "-q", "-b", "main")
        for name in ("a", "fix", "b"):
            open(os.path.join(dep, name), "w").write(name)
            sh(dep, "git", "add", "-A")
            sh(dep, "git", "commit", "-q", "-m", name)
            if name == "a":
                sh(dep, "git", "tag", "v0.7.1")
            if name == "fix":
                self.fix = sh(dep, "git", "rev-parse", "HEAD")
                sh(dep, "git", "tag", "v0.7.2")
        self.unreleased = sh(dep, "git", "rev-parse", "HEAD")
        self.first = sh(dep, "git", "rev-parse", "v0.7.1")

        self.me = os.path.join(root, "me")
        os.makedirs(self.me)
        sh(self.me, "git", "init", "-q", "-b", "main")

        def commit(text, tag=None):
            open(os.path.join(self.me, "go.mod"), "w").write(text)
            open(os.path.join(self.me, "n"), "a").write("x")
            sh(self.me, "git", "add", "-A")
            sh(self.me, "git", "commit", "-q", "-m", "c")
            if tag:
                sh(self.me, "git", "tag", tag)
            return sh(self.me, "git", "rev-parse", "HEAD")

        self.old_fix = commit(gomod("v0.7.1"), "v0.20.4")      # already shipped in v0.20.4 and v0.20.5
        commit(gomod("v0.7.1"), "v0.20.5")
        self.my_fix = commit(gomod("v0.7.2"))                   # new since v0.20.5
        self.cwd = os.getcwd()
        os.chdir(self.me)
        self.addCleanup(os.chdir, self.cwd)

    def gate(self, tag, fixes, version="v0.7.2", extra="", remote=None):
        path, mod = os.path.join(self.d.name, "fixes.txt"), os.path.join(self.d.name, "go.mod")
        open(path, "w").write(fixes)
        open(mod, "w").write(gomod(version, extra))
        return r.main([tag, "--file", path, "--gomod", mod, "--remote-base", remote or self.remote])


class Gate(Fixture):
    def test_new_fix_in_pinned_tag_passes(self):
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix} | the fix\n"), 0)

    def test_fix_merged_but_not_in_pinned_tag_fails(self):
        # The v0.20.3 shape: the fix exists upstream but the release pins a version cut before it.
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix} | the fix\n", version="v0.7.1"), 1)

    def test_commit_after_the_tag_fails(self):
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.unreleased}\n"), 1)

    def test_unknown_commit_fails_closed(self):
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | deadbeefdead\n"), 1)

    def test_module_not_in_gomod_fails(self):
        self.assertEqual(self.gate("v0.20.6", f"github.com/conduitio/conduit-sdk | {self.fix}\n"), 1)

    def test_unreachable_remote_fails_closed(self):
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix}\n", remote=os.path.join(self.d.name, "nope")), 1)

    def test_self_commit_new_since_previous_tag_passes(self):
        self.assertEqual(self.gate("v0.20.6", f"conduit | {self.my_fix}\n"), 0)

    def test_self_commit_from_another_repo_fails(self):
        self.assertEqual(self.gate("v0.20.6", f"conduit | {self.fix}\n"), 1)

    def test_patch_with_only_old_entries_fails(self):
        # Trivial padding: a commit that was already in v0.20.5 is not a fix of this release.
        self.assertEqual(self.gate("v0.20.6", f"conduit | {self.old_fix}\n"), 1)

    def test_old_module_fix_already_pinned_at_previous_tag_fails(self):
        # commons `a` is in v0.7.1, which v0.20.5 already pinned.
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.first}\n", version="v0.7.2"), 1)

    def test_one_new_entry_among_old_ones_passes(self):
        self.assertEqual(self.gate("v0.20.6", f"conduit | {self.old_fix}\n{COMMONS} | {self.fix}\n"), 0)

    def test_patch_without_entries_fails(self):
        self.assertEqual(self.gate("v0.20.6", "# nothing\n"), 1)

    def test_patch_without_file_fails(self):
        self.assertEqual(r.main(["v0.20.6", "--file", os.path.join(self.d.name, "absent.txt")]), 1)

    def test_minor_without_file_passes(self):
        self.assertEqual(r.main(["v0.21.0", "--file", os.path.join(self.d.name, "absent.txt")]), 0)

    def test_malformed_line_fails(self):
        self.assertEqual(self.gate("v0.21.0", "github.com/x/y | not-a-sha\n"), 1)

    def test_pseudo_version_resolves_to_its_commit(self):
        v = f"v0.0.0-20260101000000-{self.fix[:12]}"
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix}\n", version=v), 0)

    def test_bad_tag_fails(self):
        self.assertEqual(r.main(["nightly"]), 1)

    def test_patch_without_a_previous_tag_fails_closed(self):
        self.assertEqual(self.gate("v0.1.1", f"conduit | {self.my_fix}\n"), 1)


class Replace(Fixture):
    def test_replace_to_a_fork_fails(self):
        extra = f"\nreplace {COMMONS} => github.com/evil/commons v0.7.2\n"
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix}\n", extra=extra), 1)

    def test_replace_to_a_local_path_fails(self):
        extra = f"\nreplace {COMMONS} => ../commons\n"
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix}\n", extra=extra), 1)

    def test_replace_block_form_to_a_fork_fails(self):
        extra = f"\nreplace (\n\t{COMMONS} v0.7.2 => github.com/evil/commons v0.7.2\n)\n"
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix}\n", extra=extra), 1)

    def test_waiver_allows_a_replace(self):
        extra = f"\nreplace {COMMONS} => ../commons\n"
        fixes = f"waive-replace | {COMMONS} | testing a local build\n{COMMONS} | {self.fix}\n"
        self.assertEqual(self.gate("v0.20.6", fixes, extra=extra), 0)

    def test_waiver_needs_a_reason(self):
        self.assertEqual(self.gate("v0.20.6", f"waive-replace | {COMMONS} |\n{COMMONS} | {self.fix}\n"), 1)

    def test_same_path_replace_pins_the_version_that_ships(self):
        # Replace pins commons back to v0.7.1: the fix is not in what ships.
        extra = f"\nreplace {COMMONS} => {COMMONS} v0.7.1\n"
        self.assertEqual(self.gate("v0.20.6", f"{COMMONS} | {self.fix}\n", extra=extra), 1)

    def test_replace_block_line_does_not_overwrite_the_require_version(self):
        text = (f"module x\n\nrequire {COMMONS} v0.7.2\n\nreplace (\n\t{COMMONS} v0.7.1 => "
                f"{COMMONS} v0.7.2\n)\n")
        reqs, repls = r.gomod_versions(text)
        self.assertEqual(reqs[COMMONS], "v0.7.2")
        # an unrelated block must not set versions either
        reqs, _ = r.gomod_versions("module x\nrequire a v1.0.0\nexclude (\n\ta v0.0.1\n)\nretract (\n\tv0.1.0\n)\n")
        self.assertEqual(reqs, {"a": "v1.0.0"})


if __name__ == "__main__":
    unittest.main()
