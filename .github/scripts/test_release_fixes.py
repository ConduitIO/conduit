import os
import subprocess
import tempfile
import unittest

import release_fixes as r


def sh(cwd, *args):
    return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True,
                          env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                               "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}).stdout.strip()


class Fixture(unittest.TestCase):
    def setUp(self):
        self.d = tempfile.TemporaryDirectory()
        self.addCleanup(self.d.cleanup)
        root = self.d.name
        # A dependency repo: v0.7.1 tag, then a fix commit, then v0.7.2 tag
        # that contains it, then an unreleased commit after the tag.
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
            if name == "fix":
                sh(dep, "git", "tag", "v0.7.2")
        self.unreleased = sh(dep, "git", "rev-parse", "HEAD")
        self.first = sh(dep, "git", "rev-parse", "v0.7.1")
        # This repo, with a fix commit in its history.
        self.me = os.path.join(root, "me")
        os.makedirs(self.me)
        sh(self.me, "git", "init", "-q", "-b", "main")
        open(os.path.join(self.me, "x"), "w").write("x")
        sh(self.me, "git", "add", "-A")
        sh(self.me, "git", "commit", "-q", "-m", "x")
        self.my_fix = sh(self.me, "git", "rev-parse", "HEAD")
        self.cwd = os.getcwd()
        os.chdir(self.me)
        self.addCleanup(os.chdir, self.cwd)

    def run_gate(self, tag, fixes, version="v0.7.2"):
        with open(os.path.join(self.d.name, "fixes.txt"), "w") as f:
            f.write(fixes)
        with open(os.path.join(self.d.name, "go.mod"), "w") as f:
            f.write(f"module x\n\nrequire github.com/conduitio/conduit-commons {version}\n")
        return r.main([tag, "--file", os.path.join(self.d.name, "fixes.txt"),
                       "--gomod", os.path.join(self.d.name, "go.mod"), "--remote-base", self.remote])


class Gate(Fixture):
    def test_fix_in_pinned_tag_passes(self):
        self.assertEqual(self.run_gate("v0.20.6", f"github.com/conduitio/conduit-commons | {self.fix} | the fix\n"), 0)

    def test_fix_merged_but_not_in_pinned_tag_fails(self):
        # The v0.20.3 shape: the fix exists on the dependency's branch but the
        # release pins a version cut before it.
        self.assertEqual(self.run_gate("v0.20.6", f"github.com/conduitio/conduit-commons | {self.fix} | the fix\n",
                                       version="v0.7.1"), 1)

    def test_commit_after_the_tag_fails(self):
        self.assertEqual(self.run_gate("v0.20.6", f"github.com/conduitio/conduit-commons | {self.unreleased}\n"), 1)

    def test_unknown_commit_fails_closed(self):
        self.assertEqual(self.run_gate("v0.20.6", "github.com/conduitio/conduit-commons | deadbeefdead\n"), 1)

    def test_module_not_in_gomod_fails(self):
        self.assertEqual(self.run_gate("v0.20.6", f"github.com/conduitio/conduit-sdk | {self.fix}\n"), 1)

    def test_unreachable_remote_fails_closed(self):
        with open(os.path.join(self.d.name, "fixes.txt"), "w") as f:
            f.write(f"github.com/conduitio/conduit-commons | {self.fix}\n")
        with open(os.path.join(self.d.name, "go.mod"), "w") as f:
            f.write("module x\n\nrequire github.com/conduitio/conduit-commons v0.7.2\n")
        self.assertEqual(r.main(["v0.20.6", "--file", os.path.join(self.d.name, "fixes.txt"),
                                 "--gomod", os.path.join(self.d.name, "go.mod"),
                                 "--remote-base", os.path.join(self.d.name, "nope")]), 1)

    def test_self_commit_in_head_passes_and_unknown_fails(self):
        self.assertEqual(self.run_gate("v0.20.6", f"conduit | {self.my_fix}\n"), 0)
        self.assertEqual(self.run_gate("v0.20.6", f"conduit | {self.fix}\n"), 1)  # a commit from another repo

    def test_patch_without_entries_fails(self):
        self.assertEqual(self.run_gate("v0.20.6", "# nothing\n"), 1)

    def test_patch_without_file_fails(self):
        self.assertEqual(r.main(["v0.20.6", "--file", os.path.join(self.d.name, "absent.txt")]), 1)

    def test_minor_without_file_passes(self):
        self.assertEqual(r.main(["v0.21.0", "--file", os.path.join(self.d.name, "absent.txt")]), 0)

    def test_malformed_line_fails(self):
        self.assertEqual(self.run_gate("v0.21.0", "github.com/x/y | not-a-sha\n"), 1)

    def test_pseudo_version_resolves_to_its_commit(self):
        v = f"v0.0.0-20260101000000-{self.fix[:12]}"
        self.assertEqual(self.run_gate("v0.20.6", f"github.com/conduitio/conduit-commons | {self.fix}\n", version=v), 0)

    def test_bad_tag_fails(self):
        self.assertEqual(r.main(["nightly"]), 1)


if __name__ == "__main__":
    unittest.main()
