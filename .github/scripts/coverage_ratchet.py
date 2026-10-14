#!/usr/bin/env python3
"""Fail when an engine package's statement coverage drops below its baseline.

Reads a Go cover profile and .github/coverage-baseline.txt, computes coverage
per package from the profile, and compares. Writes a Markdown report to stdout
(the workflow appends it to the job summary); exits 1 on a regression.

THE RULE (CLAUDE.md sets an 80% floor on engine packages, "a signal, not a
goal"). Several engine packages are below 80% today, so a hard 80% would fail
every PR for problems unrelated to its diff. The gate is therefore a ratchet,
like govulncheck:

  * a package with a baseline entry fails when it falls more than TOLERANCE
    points below that entry;
  * a package under an engine prefix with NO entry (a new package) must
    reach the floor, so the debt cannot grow by adding packages;
  * a baseline entry whose package has no coverage data fails, so deleting or
    renaming a package forces a deliberate baseline edit rather than silently
    dropping out of the gate;
  * the report lists packages that rose well above their baseline, so the
    entry can be raised. Raising a baseline is the only way the debt shrinks.

FAILS CLOSED on an unreadable, empty or malformed profile or baseline: a
broken measurement must never read as "no regressions".

Coverage is per package, from that package's own tests (no -coverpkg), so a
number means "what this package's tests exercise", not "what any test touches".
"""

import re
import sys
from collections import defaultdict

MODULE = "github.com/conduitio/conduit/"


class GateError(Exception):
    """The measurement or the baseline cannot be trusted."""


def package_coverage(path):
    """Returns {package path relative to the module: percent covered}."""
    try:
        with open(path, encoding="utf-8") as handle:
            lines = handle.read().splitlines()
    except OSError as err:
        raise GateError(f"cannot read the cover profile {path}: {err}") from err
    if not lines or not lines[0].startswith("mode:"):
        raise GateError(f"{path} is not a Go cover profile (no `mode:` header)")

    # A block can appear more than once when a profile is merged from several
    # runs of the same package; a block counts as covered if ANY occurrence hit.
    blocks = {}
    for number, line in enumerate(lines[1:], start=2):
        if not line.strip():
            continue
        match = re.match(r"^(.+?):(\d+\.\d+,\d+\.\d+) (\d+) (\d+)$", line)
        if not match:
            raise GateError(f"{path}:{number}: malformed cover line {line!r}")
        file, span, stmts, count = match.groups()
        key = (file, span)
        covered = int(count) > 0
        prev = blocks.get(key)
        blocks[key] = (int(stmts), covered or (prev[1] if prev else False))

    total = defaultdict(int)
    hit = defaultdict(int)
    for (file, _), (stmts, covered) in blocks.items():
        if file.startswith(MODULE):
            file = file[len(MODULE):]
        pkg = file.rsplit("/", 1)[0] if "/" in file else "."
        total[pkg] += stmts
        if covered:
            hit[pkg] += stmts
    return {pkg: 100.0 * hit[pkg] / n for pkg, n in total.items() if n > 0}


def read_baseline(path):
    """Returns (floor, tolerance, engine_prefixes, ignore_re, {package: baseline})."""
    floor, tolerance = None, None
    prefixes, entries, ignore = [], {}, None
    try:
        with open(path, encoding="utf-8") as handle:
            lines = handle.readlines()
    except OSError as err:
        raise GateError(f"cannot read the baseline {path}: {err}") from err
    for number, line in enumerate(lines, start=1):
        text = line.split("#", 1)[0].strip()
        if not text:
            continue
        parts = text.split()
        try:
            if parts[0] == "floor" and len(parts) == 2:
                floor = float(parts[1])
            elif parts[0] == "tolerance" and len(parts) == 2:
                tolerance = float(parts[1])
            elif parts[0] == "ignore" and len(parts) == 2:
                ignore = re.compile(parts[1])
            elif parts[0] == "engine" and len(parts) == 2:
                prefixes.append(parts[1])
            elif len(parts) == 2 and parts[0].startswith("pkg/"):
                if parts[0] in entries:
                    raise GateError(f"{path}:{number}: duplicate entry {parts[0]}")
                entries[parts[0]] = float(parts[1])
            else:
                raise ValueError
        except ValueError as err:
            raise GateError(f"{path}:{number}: cannot parse {text!r}") from err
    if floor is None or tolerance is None or not prefixes or not entries:
        raise GateError(f"{path} must define floor, tolerance, engine prefixes and entries")
    return floor, tolerance, prefixes, ignore, entries


def is_engine(pkg, prefixes, ignore):
    if ignore is not None and ignore.search(pkg):
        return False
    return any(pkg == p or pkg.startswith(p + "/") for p in prefixes)


def evaluate(coverage, floor, tolerance, prefixes, ignore, entries):
    failures, raise_hints = [], []
    for pkg, base in sorted(entries.items()):
        if pkg not in coverage:
            failures.append(f"`{pkg}`: in the baseline but no coverage data (renamed, deleted, or its tests did not run)")
            continue
        now = coverage[pkg]
        if now < base - tolerance:
            failures.append(f"`{pkg}`: {now:.1f}% is below its baseline {base:.1f}% (tolerance {tolerance:.1f})")
        elif now > base + 2 * tolerance:
            raise_hints.append((pkg, base, now))
    for pkg, now in sorted(coverage.items()):
        if pkg in entries or not is_engine(pkg, prefixes, ignore):
            continue
        if now < floor:
            failures.append(f"`{pkg}`: new engine package at {now:.1f}%, below the {floor:.0f}% floor")
    return failures, raise_hints


def main():
    profile, baseline = sys.argv[1], sys.argv[2]
    try:
        coverage = package_coverage(profile)
        floor, tolerance, prefixes, ignore, entries = read_baseline(baseline)
    except GateError as err:
        print("### coverage")
        print()
        print(f"**FAILED — no usable measurement:** {err}")
        print(f"::error::coverage gate could not verify anything: {err}", file=sys.stderr)
        return 1

    failures, hints = evaluate(coverage, floor, tolerance, prefixes, ignore, entries)
    below = sum(1 for p in entries if coverage.get(p, 0.0) < floor)
    print("### coverage")
    print()
    print(f"{len(entries)} engine packages in the baseline; {below} below the "
          f"{floor:.0f}% floor (held at their baseline until raised).")
    print()
    print("| package | baseline | now |")
    print("| --- | ---: | ---: |")
    for pkg, base in sorted(entries.items()):
        now = coverage.get(pkg)
        print(f"| `{pkg}` | {base:.1f} | {'n/a' if now is None else f'{now:.1f}'} |")
    print()
    if hints:
        print("**Raise these baselines** in `.github/coverage-baseline.txt` (the ratchet only tightens):")
        print()
        for pkg, base, now in hints:
            print(f"- `{pkg}`: {base:.1f} -> {now:.1f}")
        print()
    if not failures:
        print("No engine package regressed.")
        return 0
    print("**FAILED:**")
    print()
    for failure in failures:
        print(f"- {failure}")
    print()
    print("Add tests for what you changed. Do not lower a baseline to make this "
          "pass; a lower entry needs the reason in the PR description.")
    print("::error::engine coverage regressed: " + "; ".join(failures), file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
