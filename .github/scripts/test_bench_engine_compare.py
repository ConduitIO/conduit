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
    base = dict(base_ok="true", run_url="", base_sha="aaaaaaaaaaaa", head_sha="bbbbbbbbbbbb", merge_sha="")
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

    def test_base_not_built_is_reported_and_no_table_is_shown(self):
        rows = c.compare({}, {}, {"X": [5.0]})
        out = c.render(rows, args(base_ok="false"))
        self.assertIn("did not build", out)
        self.assertNotIn("| benchmark |", out)
        self.assertNotIn("`X`", out)

    def test_merge_sha_is_shown_when_valid_only(self):
        self.assertIn("merge commit `cccccccccc`", c.render([], args(merge_sha="c" * 40)))
        self.assertNotIn("merge commit", c.render([], args(merge_sha="zz`x")))

    def test_rows_sort_naturally(self):
        names = ["Engine/10x10/v1", "Engine/2x2/v1", "Engine/1x1/v1", "Engine/4x4/v2"]
        rows = c.compare({n: [1.0] for n in names}, {}, {})
        self.assertEqual([r["name"] for r in rows],
                         ["Engine/1x1/v1", "Engine/2x2/v1", "Engine/4x4/v2", "Engine/10x10/v1"])

    def test_empty_results_say_so(self):
        self.assertIn("No benchmark results were produced.", c.render([], args()))


class HostileInputTest(unittest.TestCase):
    def write(self, text):
        fd, path = tempfile.mkstemp()
        self.addCleanup(os.unlink, path)
        with os.fdopen(fd, "w") as fh:
            fh.write(text)
        return path

    def test_hostile_benchmark_names_never_reach_the_comment(self):
        names = ["Engine/x`|[click](http://evil)<script>@channel", "A|B", "A`B", "<img src=x>",
                 "@channel", "[l](http://x)", "a b", "x" * 101]
        for name in names:
            path = self.write(f"Benchmark{name}-16   3   1 ns/op   100 records/s\n")
            out = c.render(c.compare(c.parse(path), {}, c.parse(path)), args())
            self.assertNotIn("evil", out, name)
            self.assertNotIn("<script", out, name)
            self.assertNotIn("<img", out, name)
            self.assertNotIn("@channel", out, name)
            self.assertIn("No benchmark results", out, name)

    def test_code_span_cannot_be_broken_out_of(self):
        inner = c.code_span("a`|<b>@c[d](e)")[1:-1]
        for ch in "`|<>@[]()":
            self.assertNotIn(ch, inner)

    def test_non_finite_and_out_of_range_numbers_are_dropped(self):
        for v in ["NaN", "Inf", "-Inf", "+Inf", "0", "-5", "1e999", "1e16", "1_000", "١٢٣", "1e+", "0x10", "1e5000", "٣.٥"]:
            path = self.write(f"BenchmarkEngine/ok/v1-16   3   1 ns/op   {v} records/s\n")
            self.assertEqual(c.parse(path), {}, v)

    def test_caps_on_size_names_and_samples(self):
        many = "".join(f"BenchmarkEngine/n{i}-16 3 1 ns/op 10 records/s\n" for i in range(500))
        self.assertEqual(len(c.parse(self.write(many))), c.MAX_NAMES)
        same = "BenchmarkEngine/a-16 3 1 ns/op 10 records/s\n" * 500
        self.assertEqual(len(c.parse(self.write(same))["Engine/a"]), c.MAX_SAMPLES)
        self.assertEqual(c.parse(self.write("x" * (c.MAX_BYTES + 10))), {})

    def test_symlink_input_is_ignored(self):
        target = self.write("BenchmarkEngine/a-16 3 1 ns/op 10 records/s\n")
        link = target + ".lnk"
        os.symlink(target, link)
        self.addCleanup(os.unlink, link)
        self.assertEqual(c.parse(link), {})

    def test_untrusted_sha_and_url_are_validated(self):
        out = c.render([], args(base_sha="x](http://evil)", head_sha="`", run_url="http://evil/x"))
        self.assertNotIn("evil", out)
        out = c.render([], args(run_url="https://github.com/ConduitIO/conduit/actions/runs/123"))
        self.assertIn("[run](https://github.com/ConduitIO/conduit/actions/runs/123)", out)

    def test_base_ok_is_read_strictly(self):
        self.assertIn("did not build", c.render([], args(base_ok="true; rm -rf /")))
        self.assertNotIn("did not build", c.render([], args(base_ok="true")))


if __name__ == "__main__":
    unittest.main()
