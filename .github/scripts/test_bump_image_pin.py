import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bump_image_pin as bp  # noqa: E402

IMG = bp.IMAGE


def tree(files):
    root = tempfile.mkdtemp()
    for rel, text in files.items():
        path = os.path.join(root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(text)
    return root


def read(root, rel):
    with open(os.path.join(root, rel), encoding="utf-8") as handle:
        return handle.read()


FILES = {
    "deploy/docker-compose/compose.yaml": f"image: ${{CONDUIT_IMAGE:-{IMG}:v0.20.0}}\n",
    "deploy/nomad/conduit.nomad.hcl": f'image = "{IMG}:v0.20.0"\n',
    "deploy/ecs/task-definition.json": f'{{"image": "{IMG}:v0.20.0"}}\n',
    "deploy/README.md": f"pinned `{IMG}:v0.20.0`, never `latest`; also {IMG}:latest\n",
    "README.md": f"docker run {IMG}:latest\n",
    "docs/releases.md": f"cosign verify {IMG}:v0.20.0\n",
}


class ParseTag(unittest.TestCase):
    def test_stable_accepted(self):
        self.assertEqual(bp.parse_tag("v0.21.0"), (0, 21, 0))
        self.assertEqual(bp.parse_tag("v1.10.3"), (1, 10, 3))

    def test_everything_else_refused(self):
        for tag in ["v0.21.0-nightly.20261014", "v0.21.0-rc.1", "0.21.0", "v0.21", "v0.21.0.1", "latest", ""]:
            with self.assertRaises(bp.BumpError, msg=tag):
                bp.parse_tag(tag)


class Bump(unittest.TestCase):
    def test_rewrites_every_pin_and_only_pins(self):
        root = tree(FILES)
        changed = bp.run(root, "v0.21.0", dry_run=False)
        self.assertEqual(sorted(changed), [
            "deploy/README.md", "deploy/docker-compose/compose.yaml",
            "deploy/ecs/task-definition.json", "deploy/nomad/conduit.nomad.hcl"])
        for rel in changed:
            self.assertIn(f"{IMG}:v0.21.0", read(root, rel))
            self.assertNotIn("v0.20.0", read(root, rel))
        # :latest untouched, README without a version pin untouched, releases.md out of scope.
        self.assertIn(f"{IMG}:latest", read(root, "deploy/README.md"))
        self.assertIn("v0.20.0", read(root, "docs/releases.md"))

    def test_dry_run_writes_nothing(self):
        root = tree(FILES)
        changed = bp.run(root, "v0.21.0", dry_run=True)
        self.assertEqual(len(changed), 4)
        for rel, text in FILES.items():
            self.assertEqual(read(root, rel), text)

    def test_never_downgrades_or_repeats(self):
        root = tree(FILES)
        self.assertEqual(bp.run(root, "v0.20.0", dry_run=False), [])
        self.assertEqual(bp.run(root, "v0.19.9", dry_run=False), [])
        bp.run(root, "v0.21.0", dry_run=False)
        self.assertEqual(bp.run(root, "v0.20.5", dry_run=False), [])
        self.assertEqual(bp.run(root, "v0.21.0", dry_run=False), [])

    def test_patch_on_same_line_bumps(self):
        root = tree(FILES)
        bp.run(root, "v0.20.1", dry_run=False)
        self.assertIn(f"{IMG}:v0.20.1", read(root, "deploy/nomad/conduit.nomad.hcl"))

    def test_version_comparison_is_numeric(self):
        root = tree({"deploy/a.yaml": f"{IMG}:v0.9.0\n"})
        self.assertEqual(bp.run(root, "v0.10.0", dry_run=False), ["deploy/a.yaml"])

    def test_pin_prefix_of_longer_version_not_matched(self):
        root = tree({"deploy/a.yaml": f"{IMG}:v0.20.0\n{IMG}:v0.20.0-nightly.1\n"})
        bp.run(root, "v0.21.0", dry_run=False)
        text = read(root, "deploy/a.yaml")
        self.assertIn(f"{IMG}:v0.21.0\n", text)
        self.assertIn(f"{IMG}:v0.20.0-nightly.1", text)

    def test_no_pins_is_an_error_not_a_clean_result(self):
        root = tree({"deploy/a.yaml": "nothing here\n"})
        with self.assertRaises(bp.BumpError):
            bp.run(root, "v0.21.0", dry_run=False)

    def test_other_images_untouched(self):
        root = tree({"deploy/a.yaml": f"{IMG}:v0.20.0\nghcr.io/other/conduit:v0.20.0\n"})
        bp.run(root, "v0.21.0", dry_run=False)
        self.assertIn("ghcr.io/other/conduit:v0.20.0", read(root, "deploy/a.yaml"))


if __name__ == "__main__":
    unittest.main()
