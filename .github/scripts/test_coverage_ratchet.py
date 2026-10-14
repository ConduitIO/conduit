import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import coverage_ratchet as cr  # noqa: E402

M = cr.MODULE


def profile(*blocks):
    """blocks: (file, stmts, count) -> a cover profile text."""
    lines = ["mode: set"]
    for i, (file, stmts, count) in enumerate(blocks):
        lines.append(f"{M}{file}:{i + 1}.1,{i + 1}.9 {stmts} {count}")
    return "\n".join(lines) + "\n"


BASELINE = """
floor 80.0
tolerance 1.0
engine pkg/pipeline
engine pkg/connector
ignore (^|/)mock(/|$)
pkg/pipeline 60.0
pkg/connector 90.0
"""


class Tmp(unittest.TestCase):
    def write(self, text):
        handle = tempfile.NamedTemporaryFile("w", delete=False, suffix=".txt")
        handle.write(text)
        handle.close()
        self.addCleanup(os.unlink, handle.name)
        return handle.name


class PackageCoverage(Tmp):
    def test_percent_per_package(self):
        path = self.write(profile(
            ("pkg/pipeline/a.go", 3, 1), ("pkg/pipeline/a.go", 1, 0),
            ("pkg/connector/b.go", 2, 1)))
        cov = cr.package_coverage(path)
        self.assertEqual(cov["pkg/pipeline"], 75.0)
        self.assertEqual(cov["pkg/connector"], 100.0)

    def test_block_hit_in_any_run_counts_as_covered(self):
        text = profile(("pkg/pipeline/a.go", 4, 0)) + \
            f"{M}pkg/pipeline/a.go:1.1,1.9 4 2\n"
        cov = cr.package_coverage(self.write(text))
        self.assertEqual(cov["pkg/pipeline"], 100.0)

    def test_fails_closed_on_garbage(self):
        for text in ["", "not a profile\n", "mode: set\nbroken line\n"]:
            with self.assertRaises(cr.GateError):
                cr.package_coverage(self.write(text))
        with self.assertRaises(cr.GateError):
            cr.package_coverage("/nonexistent/cover.out")


class Baseline(Tmp):
    def test_parse(self):
        floor, tol, prefixes, ignore, entries = cr.read_baseline(self.write(BASELINE))
        self.assertEqual((floor, tol), (80.0, 1.0))
        self.assertEqual(prefixes, ["pkg/pipeline", "pkg/connector"])
        self.assertEqual(entries["pkg/pipeline"], 60.0)
        self.assertTrue(ignore.search("pkg/connector/mock"))

    def test_rejects_malformed_and_duplicate(self):
        for bad in [BASELINE + "pkg/pipeline 61.0\n", BASELINE + "nonsense\n",
                    "floor 80\n", BASELINE + "pkg/x notanumber\n"]:
            with self.assertRaises(cr.GateError):
                cr.read_baseline(self.write(bad))

    def test_checked_in_baseline_parses(self):
        here = os.path.dirname(os.path.abspath(__file__))
        cr.read_baseline(os.path.join(here, "..", "coverage-baseline.txt"))


class Evaluate(Tmp):
    def run_eval(self, cov):
        floor, tol, prefixes, ignore, entries = cr.read_baseline(self.write(BASELINE))
        return cr.evaluate(cov, floor, tol, prefixes, ignore, entries)

    def test_clean(self):
        fails, hints = self.run_eval({"pkg/pipeline": 60.0, "pkg/connector": 90.0})
        self.assertEqual((fails, hints), ([], []))

    def test_within_tolerance_passes_beyond_fails(self):
        fails, _ = self.run_eval({"pkg/pipeline": 59.1, "pkg/connector": 90.0})
        self.assertEqual(fails, [])
        fails, _ = self.run_eval({"pkg/pipeline": 58.9, "pkg/connector": 90.0})
        self.assertEqual(len(fails), 1)

    def test_package_above_floor_is_held_at_its_baseline(self):
        # 90 -> 80 is "still at the floor" but is a regression for the ratchet.
        fails, _ = self.run_eval({"pkg/pipeline": 60.0, "pkg/connector": 80.0})
        self.assertEqual(len(fails), 1)

    def test_missing_package_fails(self):
        fails, _ = self.run_eval({"pkg/connector": 90.0})
        self.assertIn("pkg/pipeline", fails[0])

    def test_new_engine_package_must_reach_floor(self):
        base = {"pkg/pipeline": 60.0, "pkg/connector": 90.0}
        fails, _ = self.run_eval({**base, "pkg/pipeline/new": 79.9})
        self.assertEqual(len(fails), 1)
        fails, _ = self.run_eval({**base, "pkg/pipeline/new": 80.0})
        self.assertEqual(fails, [])

    def test_non_engine_and_ignored_packages_are_not_gated(self):
        base = {"pkg/pipeline": 60.0, "pkg/connector": 90.0}
        fails, _ = self.run_eval({**base, "pkg/http/api": 1.0, "pkg/connector/mock": 0.0})
        self.assertEqual(fails, [])

    def test_improvement_is_reported_for_raising(self):
        _, hints = self.run_eval({"pkg/pipeline": 70.0, "pkg/connector": 90.0})
        self.assertEqual(hints, [("pkg/pipeline", 60.0, 70.0)])


if __name__ == "__main__":
    unittest.main()
