#!/usr/bin/env python3
"""Tests for bench_engine_compare.py. Run: python3 -m unittest -v (from this dir)."""

import argparse
import os
import tempfile
import unittest

import bench_engine_compare as c


def bench(name, rates):
    return "".join(f"Benchmark{name}-16   3   1000 ns/op   {r} records/s   9 B/op\n" for r in rates)


def args(**kw):
    base = dict(status="", base_sha="aaaaaaaaaaaa", head_sha="bbbbbbbbbbbb", run_url="")
    base.update(kw)
    return argparse.Namespace(**base)


class ParseTest(unittest.TestCase):
    def write(self, text):
        fd, path = tempfile.mkstemp()
        self.addCleanup(os.unlink, path)
        with os.fdopen(fd, "w") as fh:
            fh.write(text)
        return path

    def test_parses_samples_and_ignores_other_lines(self):
        path = self.write("goos: linux\n" + bench("Engine/1x1/v1", [100, 110]) + "PASS\nok  pkg 1s\n")
        self.assertEqual(c.parse(path), {"Engine/1x1/v1": [100.0, 110.0]})

    def test_missing_file_is_empty(self):
        self.assertEqual(c.parse("/nonexistent/file"), {})


class CompareTest(unittest.TestCase):
    def row(self, a, b, h):
        rows = c.compare({"X": a}, {"X": b}, {"X": h})
        self.assertEqual(len(rows), 1)
        return rows[0]

    def test_delta_inside_the_noise_floor_is_not_called_a_change(self):
        # A/A differs by 8% on unchanged code, so a 5% head delta says nothing.
        r = self.row([100, 100, 100], [108, 108, 108], [105, 105, 105])
        self.assertEqual(r["verdict"], "within noise")

    def test_delta_between_one_and_two_times_the_floor_is_still_noise(self):
        # Floor is 10% (A/A), head is 15% off: more than the floor, less than twice it.
        r = self.row([100, 100, 100], [110, 110, 110], [115, 115, 115])
        self.assertEqual(r["verdict"], "within noise")

    def test_clear_regression(self):
        r = self.row([100, 101, 99], [100, 100, 100], [70, 71, 69])
        self.assertEqual(r["verdict"], "slower")
        self.assertAlmostEqual(r["delta"], -0.30, places=2)

    def test_clear_improvement(self):
        r = self.row([100, 101, 99], [100, 100, 100], [150, 151, 149])
        self.assertEqual(r["verdict"], "faster")

    def test_spread_of_reference_samples_widens_the_floor(self):
        # A/A medians agree, but the reference samples span 40%.
        r = self.row([80, 100, 120], [100, 100, 100], [90, 90, 90])
        self.assertEqual(r["verdict"], "within noise")

    def test_small_delta_under_two_percent_is_never_a_change(self):
        r = self.row([100, 100, 100], [100, 100, 100], [101, 101, 101])
        self.assertEqual(r["verdict"], "within noise")

    def test_missing_base_reports_head_only(self):
        rows = c.compare({}, {}, {"X": [5.0]})
        self.assertEqual(rows[0]["verdict"], "n/a")
        self.assertEqual(rows[0]["head"], 5.0)


class RenderTest(unittest.TestCase):
    def test_comment_is_labelled_indicative_and_carries_the_marker(self):
        out = c.render(c.compare({"X": [100]}, {"X": [100]}, {"X": [100]}), args())
        self.assertIn(c.MARKER, out)
        self.assertIn("indicative, not a gate", out)
        self.assertIn("`X`", out)

    def test_status_line_and_empty_results(self):
        out = c.render([], args(status="base did not build"))
        self.assertIn("> base did not build", out)
        self.assertIn("No benchmark results were produced.", out)


if __name__ == "__main__":
    unittest.main()
