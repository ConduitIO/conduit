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
calls identical code a change often enough to train people to ignore the comment.

The comment is informative, not a gate: it makes no claim about throughput, only
about whether this runner could tell the two revisions apart. The actual gate is
`--gate` mode, run by `bench-engine.yml` on the same three files: it exits 3
when a benchmark is more than REGRESSION_THRESHOLD slower than base AND that
slowdown exceeds twice the A/A floor, i.e. the measured floor must be
comfortably under the threshold for a fail to be called (a 10% delta only fails
when the floor is under 5%). That is the METHODOLOGY rule: a >10% gate is only
meaningful on a runner whose A/A floor is under 10% for the whole run, and the
floor is cited next to every gate decision. A run that cannot resolve the gate
(base did not build, or no floor is measurable) reports "cannot resolve" and
does not fail: no comparison is not a regression.

THREAT MODEL. The three input files are produced by the PR's own code, so they
are attacker-controlled when the PR is. The comment is posted with a
pull-requests:write token by a separate workflow_run job (bench-engine-comment.yml)
that runs this script and nothing from the PR. So everything read from the files
is data: sizes are capped, a benchmark name must match NAME_RE or the line is
dropped, numbers must be finite and in range, and no string from the files
reaches the comment except an allowlisted name, rendered inside a code span.
Status text is chosen by this script, SHAs and the run URL are validated, and
the PR number is validated by the workflow, never read from the artifact.

Without `--gate` this script never exits non-zero for a benchmark result; it
exits 2 only for bad usage, so a CI wrapper can tell "no comment produced" from
"comment produced". With `--gate` it exits 3 when the regression gate fails and
0 otherwise (a "cannot resolve" result is 0).
"""

import argparse
import math
import os
import re
import statistics
import sys

MARKER = "<!-- bench-engine-comment -->"
METRIC = "records/s"
MIN_DELTA = 0.02
FLOOR_FACTOR = 2
# The regression gate (CLAUDE.md): a benchmark that is more than this much
# slower than base, beyond twice the A/A floor, fails the build.
REGRESSION_THRESHOLD = 0.10

NAME_RE = re.compile(r"[A-Za-z0-9_/.-]{1,100}")
SHA_RE = re.compile(r"[0-9a-f]{7,64}")
URL_RE = re.compile(r"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/[0-9]+")
MAX_BYTES = 1 << 20          # per input file
MAX_LINES = 5000             # per input file
MAX_NAMES = 100              # distinct benchmarks
MAX_SAMPLES = 50             # per benchmark per file
MAX_RATE = 1e15              # records/s; anything above is not a measurement

# All validation regexes are used with fullmatch(): `$` would accept a trailing newline.
# Strict ASCII decimal as `go test` prints it. float() would also accept
# "1_000", Unicode digits, "nan", "inf" and surrounding junk.
NUM_RE = re.compile(r"[0-9]{1,20}(?:\.[0-9]{1,20})?(?:[eE][+-]?[0-9]{1,3})?")

LINE = re.compile(r"^Benchmark(?P<name>\S+?)(?:-\d+)?\s+\d+\s+(?P<rest>.*)$")


def parse(path):
    """Return {benchmark name: [records/s samples]} from go test -bench output.

    Untrusted input: see the threat model above. Lines that do not fit are
    skipped, never repaired.
    """
    out = {}
    if not path:
        return out
    try:
        if os.path.islink(path) or not os.path.isfile(path):
            return out
        with open(path, "rb") as fh:
            raw = fh.read(MAX_BYTES + 1)
    except OSError:
        return out
    if len(raw) > MAX_BYTES:
        return out
    text = raw.decode("utf-8", errors="replace")
    for n, line in enumerate(text.splitlines()):
        if n >= MAX_LINES:
            break
        m = LINE.match(line.strip())
        if not m:
            continue
        name = m.group("name")
        if not NAME_RE.fullmatch(name):
            continue
        fields = m.group("rest").split()
        for value, unit in zip(fields[0::2], fields[1::2]):
            if unit != METRIC:
                continue
            if not NUM_RE.fullmatch(value):
                continue
            v = float(value)
            if not math.isfinite(v) or v <= 0 or v > MAX_RATE:
                continue
            if name not in out and len(out) >= MAX_NAMES:
                continue
            samples = out.setdefault(name, [])
            if len(samples) < MAX_SAMPLES:
                samples.append(v)
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


def natural_key(name):
    """Sort 1x1, 2x2, 4x4, 10x10 numerically and the rest alphabetically."""
    return [int(t) if t.isdigit() else t for t in re.split(r"([0-9]+)", name)]


def compare(base_a, base_b, head):
    """Return one row dict per benchmark present in head or base_a."""
    rows = []
    for name in sorted(set(base_a) | set(head), key=natural_key):
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


def is_regression(row):
    """True when this benchmark is a >threshold slowdown beyond the A/A floor."""
    d, f = row["delta"], row["floor"]
    return (
        d is not None
        and f is not None
        and d < -REGRESSION_THRESHOLD
        and -d > FLOOR_FACTOR * f
    )


def gate_verdict(rows, base_ok):
    """Return (verdict, failing_rows) for the >10% regression gate.

    "fail" when any benchmark is more than REGRESSION_THRESHOLD slower than base
    AND that slowdown exceeds twice the A/A floor. "cannot-resolve" when no
    comparison could be made (the base did not build, or no benchmark has both a
    base median and a measurable floor). "pass" otherwise. Only "fail" should
    block a PR; the other two never do.
    """
    failing = [r for r in rows if is_regression(r)]
    if failing:
        return "fail", failing
    comparable = any(
        r["base"] is not None and r["head"] is not None and r["floor"] is not None
        for r in rows
    )
    if base_ok != "true" or not comparable:
        return "cannot-resolve", []
    return "pass", []


def gate_summary(rows, base_ok):
    """A short, human-readable summary of the gate decision, citing the floor."""
    verdict, failing = gate_verdict(rows, base_ok)
    lines = ["### Engine benchmark gate"]
    if verdict == "fail":
        lines.append("")
        lines.append(f"**Gate: FAIL** — {len(failing)} benchmark(s) regressed more than "
                     f"{fmt_pct(REGRESSION_THRESHOLD)} beyond twice the A/A floor:")
        for r in failing:
            lines.append(f"- `{r['name']}`: head {fmt_rate(r['head'])} vs base {fmt_rate(r['base'])} "
                         f"({fmt_pct(r['delta'], sign=True)}), floor {fmt_pct(r['floor'])}")
    elif verdict == "cannot-resolve":
        reason = ("the base revision did not build with this PR's benchmark harness"
                  if base_ok != "true" else
                  "no benchmark has both a base median and a measurable A/A floor")
        lines.append("")
        lines.append(f"**Gate: cannot resolve** — {reason}. No regression is asserted; the check passes.")
    else:
        max_floor = max((r["floor"] for r in rows if r["floor"] is not None), default=None)
        if max_floor is not None and FLOOR_FACTOR * max_floor >= REGRESSION_THRESHOLD:
            lines.append("")
            lines.append(f"**Gate: pass** — but the A/A floor ({fmt_pct(max_floor)}) is too high to "
                         f"resolve a {fmt_pct(REGRESSION_THRESHOLD)} regression on this runner "
                         f"(a {fmt_pct(REGRESSION_THRESHOLD)} delta would need the floor under "
                         f"{fmt_pct(REGRESSION_THRESHOLD / FLOOR_FACTOR)}).")
        else:
            lines.append("")
            lines.append(f"**Gate: pass** — no benchmark regressed more than "
                         f"{fmt_pct(REGRESSION_THRESHOLD)} beyond twice the A/A floor.")
    return "\n".join(lines) + "\n"


def code_span(text):
    """Render text as a Markdown code span that cannot be broken out of."""
    safe = re.sub(r"[^A-Za-z0-9_/.\- ]", "?", str(text))
    return f"`{safe}`"


def fmt_rate(v):
    return "-" if v is None else f"{v:,.0f}"


def fmt_pct(v, sign=False):
    if v is None:
        return "-"
    return f"{v * 100:+.1f}%" if sign else f"{v * 100:.1f}%"


def render(rows, args):
    verdict, failing = gate_verdict(rows, args.base_ok)
    lines = [
        MARKER,
        "### Engine benchmark (base vs head, with an A/A control)",
        "",
        "In-process fake connectors on both pipeline engines (`make bench-engine`), base and head",
        "run alternately on the same runner. Medians of records/s, higher is better. The noise floor is",
        "measured on the base revision against itself in the same job. This reports whether this runner",
        "could tell the two revisions apart (a change is called only beyond twice the floor); it is not a",
        "throughput claim.",
        "",
    ]
    if args.base_ok != "true":
        lines += [
            "> The base revision did not build with this PR's benchmark harness, so there is no comparison. "
            "No table is shown.",
            "",
        ]
    if args.base_ok != "true":
        rows = []  # no comparison, so no table: head-only numbers invite misreading
    if rows:
        lines += [
            "| benchmark | base | head | delta | noise floor | read |",
            "| --- | ---: | ---: | ---: | ---: | --- |",
        ]
        for r in rows:
            lines.append(
                f"| {code_span(r['name'])} | {fmt_rate(r['base'])} | {fmt_rate(r['head'])} | "
                f"{fmt_pct(r['delta'], sign=True)} | {fmt_pct(r['floor'])} | {r['verdict']} |"
            )
        lines.append("")
    elif args.base_ok == "true":
        lines += ["No benchmark results were produced.", ""]
    if verdict == "fail":
        names = ", ".join(code_span(r["name"]) for r in failing)
        lines += [
            f"**Gate: FAIL** — {names} regressed more than {fmt_pct(REGRESSION_THRESHOLD)} beyond twice "
            "the A/A floor. The `bench-engine (gate)` check fails this PR.",
            "",
        ]
    elif verdict == "cannot-resolve":
        reason = ("the base revision did not build with this PR's benchmark harness"
                  if args.base_ok != "true" else
                  "no benchmark has both a base median and a measurable A/A floor")
        lines += [f"**Gate: cannot resolve** — {reason}. No regression is asserted.", ""]
    else:
        lines += [
            f"**Gate: pass** — no benchmark regressed more than {fmt_pct(REGRESSION_THRESHOLD)} beyond "
            "twice the A/A floor.",
            "",
        ]
    foot = [
        f"base `{args.base_sha[:10]}`" if SHA_RE.fullmatch(args.base_sha) else "",
        f"PR head `{args.head_sha[:10]}`" if SHA_RE.fullmatch(args.head_sha) else "",
    ]
    foot = ", ".join(x for x in foot if x)
    if SHA_RE.fullmatch(args.merge_sha):
        foot += f"; benchmarked on merge commit `{args.merge_sha[:10]}`"
    if URL_RE.fullmatch(args.run_url):
        foot += f" - [run]({args.run_url})"
    lines.append(f"<sub>{foot}. Wall-clock shapes (`acklatency`) depend on runner timer granularity.</sub>")
    return "\n".join(lines) + "\n"


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--base-a", required=True)
    p.add_argument("--base-b", default="")
    p.add_argument("--head", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--base-ok", default="false", help='"true" if the base built; read strictly')
    p.add_argument("--base-sha", default="")
    p.add_argument("--head-sha", default="")
    p.add_argument("--merge-sha", default="", help="commit the head binary was built from (pull_request runs the merge ref)")
    p.add_argument("--run-url", default="")
    p.add_argument("--gate", action="store_true",
                   help="run as the regression gate: write the gate summary to --out and exit 3 on a regression")
    args = p.parse_args(argv)

    args.base_ok = args.base_ok.strip()
    rows = compare(parse(args.base_a), parse(args.base_b), parse(args.head))
    verdict, _ = gate_verdict(rows, args.base_ok)
    if args.gate:
        summary = gate_summary(rows, args.base_ok)
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(summary)
        sys.stdout.write(summary)
        return 3 if verdict == "fail" else 0
    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write(render(rows, args))
    return 0


if __name__ == "__main__":
    sys.exit(main())
