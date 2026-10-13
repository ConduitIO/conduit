#!/usr/bin/env python3
"""Turn interleaved `make bench-engine` runs into a PR comment.

Inputs are three files of plain `go test -bench` output, produced by running the
base and head test binaries alternately on one runner:

  --base-a  the base revision (the reference)
  --base-b  the base revision again (an A/A control: same code, same runner)
  --head    the PR head

For each benchmark the comment shows the median records/s of base and head, the
delta, and a noise floor. The floor is the larger of two things measured on code
that did not change: how far the A/A control's median is from the reference's,
and the half-range of the reference samples. A change is only called when the
delta exceeds twice that floor (and 2%); anything smaller is "within noise". The
factor of two is deliberate: with five rounds on a shared runner, a bare floor
calls identical code a change often enough to train people to ignore the comment. The comment is labelled indicative and is
never a gate: it makes no claim about throughput, only about whether this
runner could tell the two revisions apart.

This script never exits non-zero for a benchmark result. It exits 2 only for
bad usage, so a CI wrapper can tell "no comment produced" from "comment
produced".
"""

import argparse
import re
import statistics
import sys

MARKER = "<!-- bench-engine-comment -->"
METRIC = "records/s"
MIN_DELTA = 0.02
FLOOR_FACTOR = 2

LINE = re.compile(r"^Benchmark(?P<name>\S+?)(?:-\d+)?\s+\d+\s+(?P<rest>.*)$")


def parse(path):
    """Return {benchmark name: [records/s samples]} from go test -bench output."""
    out = {}
    if not path:
        return out
    try:
        fh = open(path, encoding="utf-8")
    except OSError:
        return out
    with fh:
        for raw in fh:
            m = LINE.match(raw.strip())
            if not m:
                continue
            fields = m.group("rest").split()
            for value, unit in zip(fields[0::2], fields[1::2]):
                if unit == METRIC:
                    try:
                        out.setdefault(m.group("name"), []).append(float(value))
                    except ValueError:
                        pass
    return out


def noise_floor(base_a, base_b):
    """Relative noise measured on unchanged code, or None if it cannot be."""
    if not base_a:
        return None
    med_a = statistics.median(base_a)
    if med_a <= 0:
        return None
    spread = (max(base_a) - min(base_a)) / (2 * med_a)
    if not base_b:
        return spread
    aa = abs(statistics.median(base_b) / med_a - 1)
    return max(aa, spread)


def compare(base_a, base_b, head):
    """Return one row dict per benchmark present in head or base_a."""
    rows = []
    for name in sorted(set(base_a) | set(head)):
        a, b, h = base_a.get(name, []), base_b.get(name, []), head.get(name, [])
        row = {"name": name, "base": None, "head": None, "delta": None, "floor": None, "verdict": "n/a"}
        if a:
            row["base"] = statistics.median(a)
        if h:
            row["head"] = statistics.median(h)
        floor = noise_floor(a, b)
        row["floor"] = floor
        if row["base"] and row["head"] is not None and floor is not None:
            delta = row["head"] / row["base"] - 1
            row["delta"] = delta
            if abs(delta) <= max(FLOOR_FACTOR * floor, MIN_DELTA):
                row["verdict"] = "within noise"
            else:
                row["verdict"] = "faster" if delta > 0 else "slower"
        rows.append(row)
    return rows


def fmt_rate(v):
    return "-" if v is None else f"{v:,.0f}"


def fmt_pct(v, sign=False):
    if v is None:
        return "-"
    return f"{v * 100:+.1f}%" if sign else f"{v * 100:.1f}%"


def render(rows, args):
    lines = [
        MARKER,
        "### Engine benchmark (indicative, not a gate)",
        "",
        "In-process fake connectors on both pipeline engines (`make bench-engine`), base and head",
        "run alternately on the same runner. Medians of records/s, higher is better. The noise floor is",
        "measured on the base revision against itself in the same job. This reports whether this runner",
        "could tell the two revisions apart (a change is called only beyond twice the floor); it is not a",
        "throughput claim and blocks nothing.",
        "",
    ]
    if args.status:
        lines += [f"> {args.status}", ""]
    if rows:
        lines += [
            "| benchmark | base | head | delta | noise floor | read |",
            "| --- | ---: | ---: | ---: | ---: | --- |",
        ]
        for r in rows:
            lines.append(
                f"| `{r['name']}` | {fmt_rate(r['base'])} | {fmt_rate(r['head'])} | "
                f"{fmt_pct(r['delta'], sign=True)} | {fmt_pct(r['floor'])} | {r['verdict']} |"
            )
        lines.append("")
    else:
        lines += ["No benchmark results were produced.", ""]
    foot = [f"base `{args.base_sha[:10]}`" if args.base_sha else "", f"head `{args.head_sha[:10]}`" if args.head_sha else ""]
    foot = ", ".join(x for x in foot if x)
    if args.run_url:
        foot += f" - [run]({args.run_url})"
    lines.append(f"<sub>{foot}. Wall-clock shapes (`acklatency`) depend on runner timer granularity.</sub>")
    return "\n".join(lines) + "\n"


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--base-a", required=True)
    p.add_argument("--base-b", default="")
    p.add_argument("--head", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--status", default="")
    p.add_argument("--base-sha", default="")
    p.add_argument("--head-sha", default="")
    p.add_argument("--run-url", default="")
    args = p.parse_args(argv)

    rows = compare(parse(args.base_a), parse(args.base_b), parse(args.head))
    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write(render(rows, args))
    return 0


if __name__ == "__main__":
    sys.exit(main())
