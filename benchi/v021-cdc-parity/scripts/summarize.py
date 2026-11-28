#!/usr/bin/env python3
"""Summarise one or more run directories (each holding runs.csv) into medians
with min-max per arm, the A/A spread, WAL pressure, per-round paired ratios and
an ADMISSIBILITY verdict.

Usage: summarize.py RESULTS_DIR [RESULTS_DIR ...]

Rows labelled warmup-* are discarded. Rates use the REAL elapsed window
(ts_after_ms - ts_before_ms), not a nominal 60 s. 'lag' is the source-side
cumulative counter minus the destination ground truth, sampled at the window
start and end; a bounded lag means the arm sustained the offered rate, a lag
that grows means it fell behind, a lag that shrinks means it was draining a
backlog (its rate is then NOT a sustained rate).

ADMISSIBILITY. Two ceilings bound what this harness can measure:

  the LOADER ceiling  — the `loader-ceiling` arm's rate: the loader against the
                        same Postgres with no CDC arm attached, flat out.
  the WAL ceiling     — that same arm's WAL bytes/s: the source database's WAL
                        generation under the maximum offered load.

An arm within CEILING_MARGIN of either was limited by the harness or by the
source database, not by its own capacity, so its number is not a capacity
measurement. When BOTH arms of a pair are limited, the ratio between them is
suppressed: two systems pinned to the same external ceiling look equal whatever
their real capacities are, and reporting that as a tie would be a false
finding. See ../README.md, "The ceiling rule".

Without a `loader-ceiling` arm in the given directories no ceiling is known,
and the verdict is UNQUALIFIED — not admissible.
"""
import csv, statistics, sys, os

PAIRS = {  # arm -> its Kafka Connect counterpart
    "conduit": "connect", "conduit-aa": "connect",
    "conduit-pg": "connect-pg", "conduit-pg-aa": "connect-pg",
    "conduit-s3": "connect-s3", "conduit-s3-aa": "connect-s3",
    "conduit-kafka": "connect-kafka", "conduit-kafka-aa": "connect-kafka",
}

# The ceiling arm, and the margin that makes a result inadmissible. 5% is wide
# relative to the pg_stat_user_tables flush uncertainty (about 1 s, ~2% of a
# 60 s window) and narrow relative to the effects we publish.
CEILING_ARM = "loader-ceiling"
CEILING_MARGIN = 0.05

# /proc/<pid>/stat utime and stime are in USER_HZ, 100 on every mainstream
# Linux (getconf CLK_TCK).
USER_HZ = 100.0


def load(path):
    rows = []
    with open(os.path.join(path, "runs.csv")) as f:
        for r in csv.DictReader(f):
            if r["label"].startswith("warmup") or r["rows_delta"] == "NA":
                continue
            rnd = r["label"].split("-")[0]
            try:
                tsb, tsa = int(r["ts_before_ms"]), int(r["ts_after_ms"])
                el = (tsa - tsb) / 1000.0
            except ValueError:
                tsb = tsa = None
                el = 60.0
            d = int(r["rows_delta"])
            lb = lag(r, "before"); la = lag(r, "after")
            wal, cpu = wal_pressure(os.path.join(path, r["label"]), tsb, tsa)
            rows.append(dict(round=rnd, tool=r["tool"], delta=d, elapsed=el, rate=d / el,
                             lag_before=lb, lag_after=la, wal=wal, walsender_cpu=cpu))
    return rows


def lag(r, side):
    try:
        return int(r["src_" + side]) - int(r["rows_" + side])
    except ValueError:
        return None


def wal_pressure(run_dir, ts_before, ts_after):
    """WAL bytes/s and peak per-slot walsender CPU% over [ts_before, ts_after].

    Reads the kind-tagged wal.csv scripts/sample-wal.sh wrote during the run:
      wal,<ts_ms>,<wal_bytes>,,
      slot,<ts_ms>,<flush_lag_bytes>,<slot_name>,<active_pid>
      proc,<ts_ms>,<pid>,<utime_ticks>,<stime_ticks>
    Returns (None, None) when the file is absent or the window holds fewer than
    two samples: "not measured", never zero. Both ends of a run are expected to
    be missing — benchi brings the database up after the sampler starts and
    tears it down before it is killed.
    """
    path = os.path.join(run_dir, "wal.csv")
    if ts_before is None or ts_after is None or not os.path.exists(path):
        return None, None
    wal, slots, proc = [], {}, {}
    try:
        with open(path) as f:
            for r in csv.reader(f):
                if len(r) < 5 or r[0] == "kind":
                    continue
                try:
                    ts = int(r[1])
                except ValueError:
                    continue
                if not ts_before <= ts <= ts_after:
                    continue
                try:
                    if r[0] == "wal":
                        wal.append((ts, int(r[2])))
                    elif r[0] == "slot":
                        pid = int(r[4])
                        if pid:
                            slots.setdefault(r[3], set()).add(pid)
                    elif r[0] == "proc":
                        ticks = int(r[3]) + int(r[4])
                        lo, hi = proc.get(int(r[2]), (None, None))
                        proc[int(r[2])] = (ticks if lo is None else lo, ticks)
                except ValueError:
                    continue
    except OSError:
        return None, None

    bps = None
    if len(wal) >= 2:
        wal.sort()
        dt = (wal[-1][0] - wal[0][0]) / 1000.0
        if dt > 0:
            bps = (wal[-1][1] - wal[0][1]) / dt

    # Peak per-slot walsender CPU%: a slot may be served by more than one pid
    # over a run (a reconnect), so sum the pids attributed to each slot and
    # report the busiest slot.
    cpu = None
    if slots and proc and len(wal) >= 2:
        dt = (wal[-1][0] - wal[0][0]) / 1000.0
        if dt > 0:
            per_slot = []
            for pids in slots.values():
                # max(0, ...) because a pid can be recycled inside one window
                # (a walsender restart), which would otherwise read as negative
                # CPU time.
                ticks = sum(max(0, hi - lo) for p, (lo, hi) in proc.items() if p in pids)
                per_slot.append(ticks / USER_HZ / dt * 100.0)
            if per_slot:
                cpu = max(per_slot)
    return bps, cpu


def fmt(x):
    return "n/a" if x is None else f"{x:,.0f}"


def fmt_mb(x):
    return "n/a" if x is None else f"{x / 1e6:,.1f}"


def median_or_none(values):
    v = [x for x in values if x is not None]
    return statistics.median(v) if v else None


def main(dirs):
    rows = []
    for d in dirs:
        rows += load(d)
    by = {}
    for r in rows:
        by.setdefault(r["tool"], []).append(r)

    med, wal_med, cpu_med = {}, {}, {}
    for tool, v in by.items():
        med[tool] = statistics.median(sorted(x["rate"] for x in v))
        wal_med[tool] = median_or_none([x["wal"] for x in v])
        cpu_med[tool] = median_or_none([x["walsender_cpu"] for x in v])

    ceiling = med.get(CEILING_ARM)
    wal_ceiling = wal_med.get(CEILING_ARM)

    print("| arm | n | median rate/s | min-max rate/s | spread (max-min)/median | median lag start -> end (events) | WAL MB/s | walsender CPU% | admissibility |")
    print("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
    for tool in sorted(by):
        v = sorted(x["rate"] for x in by[tool])
        m = med[tool]
        lb = [x["lag_before"] for x in by[tool] if x["lag_before"] is not None]
        la = [x["lag_after"] for x in by[tool] if x["lag_after"] is not None]
        print(f"| {tool} | {len(v)} | {m:,.0f} | {v[0]:,.0f} - {v[-1]:,.0f} | {(v[-1]-v[0])/m*100:.1f}% | "
              f"{fmt(median_or_none(lb))} -> {fmt(median_or_none(la))} | "
              f"{fmt_mb(wal_med[tool])} | {'n/a' if cpu_med[tool] is None else f'{cpu_med[tool]:.0f}'} | "
              f"{verdict_short(tool, med, wal_med, ceiling, wal_ceiling)} |")
    print()

    print("## Admissibility")
    print()
    if ceiling is None:
        print(f"**UNQUALIFIED: no `{CEILING_ARM}` arm in these directories.** The loader's own")
        print("ceiling was not measured in this session, so no arm's number is known to be its")
        print("own capacity rather than the harness's. Run the ceiling arm on the same box, in")
        print("the same session, with the same LOADER_WRITERS (see ../README.md, \"The ceiling")
        print("rule\") before publishing any of these rates.")
    else:
        print(f"Measured loader ceiling (`{CEILING_ARM}`, no CDC arm attached, flat out): "
              f"**{ceiling:,.0f} ops/s**"
              + (f", WAL **{wal_ceiling / 1e6:,.1f} MB/s**" if wal_ceiling else ", WAL not measured")
              + f". Margin {CEILING_MARGIN * 100:.0f}%.")
        print()
        for tool in sorted(by):
            if tool == CEILING_ARM:
                continue
            reasons = limits(tool, med, wal_med, ceiling, wal_ceiling)
            if reasons:
                print(f"- **{tool}: INADMISSIBLE** — {'; '.join(reasons)}. "
                      "The harness was the limiter, so this is not a capacity measurement.")
            else:
                print(f"- {tool}: admissible — {med[tool]:,.0f} ops/s is "
                      f"{med[tool] / ceiling * 100:.0f}% of the loader ceiling.")
    print()

    print("| pair | median ratio (conduit / connect) | per-round ratios |")
    print("| --- | --- | --- |")
    for a, b in PAIRS.items():
        if a not in by or b not in by:
            continue
        la = limits(a, med, wal_med, ceiling, wal_ceiling)
        lb = limits(b, med, wal_med, ceiling, wal_ceiling)
        if ceiling is not None and la and lb:
            # Two systems pinned to the same external ceiling look equal
            # whatever their capacities are, so the ratio is an artifact of the
            # harness and is SUPPRESSED, not labelled: printing 1.00 here would
            # manufacture a tie out of a measurement that resolved nothing.
            kinds = " and ".join(sorted(limit_kinds(a, med, wal_med, ceiling, wal_ceiling) |
                                        limit_kinds(b, med, wal_med, ceiling, wal_ceiling)))
            print(f"| {a} vs {b} | **suppressed: both arms are at the {kinds}** — the harness, "
                  f"not either engine, set this rate, so no \"N% faster\" claim is possible in "
                  f"this regime | suppressed |")
            continue
        ra = {x["round"]: x["rate"] for x in by[a]}
        rb = {x["round"]: x["rate"] for x in by[b]}
        rs = [ra[k] / rb[k] for k in sorted(ra) if k in rb]
        if rs:
            # An unqualified ratio is still shown, loudly labelled: "nobody
            # measured the ceiling" is unknown, not known-to-be-an-artifact,
            # and hiding a number that is in the committed results would make
            # the published tables unreproducible.
            note = ""
            if ceiling is None:
                note = f" (**UNQUALIFIED**: no `{CEILING_ARM}` run in this session)"
            elif la or lb:
                note = (f" (**{a if la else b} is at the "
                        f"{' and '.join(sorted(limit_kinds(a if la else b, med, wal_med, ceiling, wal_ceiling)))}**: "
                        f"{'; '.join(la or lb)})")
            print(f"| {a} vs {b} | {statistics.median(rs):.3f}{note} | {', '.join(f'{x:.3f}' for x in rs)} |")
    print()

    for a in ("conduit", "conduit-pg", "conduit-s3", "conduit-kafka"):
        b = a + "-aa"
        if a in by and b in by:
            ra = {x["round"]: x["rate"] for x in by[a]}
            rb = {x["round"]: x["rate"] for x in by[b]}
            rs = [abs(ra[k] / rb[k] - 1) * 100 for k in ra if k in rb]
            if rs:
                print(f"A/A ({a} vs {b}): per-round |delta| median {statistics.median(rs):.2f}%, max {max(rs):.2f}%")


def limits(tool, med, wal_med, ceiling, wal_ceiling):
    """Why `tool`'s number is not its own capacity, as a list of reasons."""
    out = []
    if ceiling is None:
        return ["no measured loader ceiling (UNQUALIFIED)"]
    if med[tool] >= (1 - CEILING_MARGIN) * ceiling:
        out.append(f"{med[tool]:,.0f} ops/s is {med[tool] / ceiling * 100:.1f}% of the "
                   f"{ceiling:,.0f} ops/s loader ceiling")
    w = wal_med.get(tool)
    if w is not None and wal_ceiling and w >= (1 - CEILING_MARGIN) * wal_ceiling:
        out.append(f"WAL {w / 1e6:,.1f} MB/s is {w / wal_ceiling * 100:.1f}% of the "
                   f"{wal_ceiling / 1e6:,.1f} MB/s WAL ceiling")
    return out


def limit_kinds(tool, med, wal_med, ceiling, wal_ceiling):
    """Which ceiling(s) `tool` is pinned to, as short names for one-line prose."""
    kinds = set()
    if ceiling is None:
        return kinds
    if med[tool] >= (1 - CEILING_MARGIN) * ceiling:
        kinds.add("loader ceiling")
    w = wal_med.get(tool)
    if w is not None and wal_ceiling and w >= (1 - CEILING_MARGIN) * wal_ceiling:
        kinds.add("WAL ceiling")
    return kinds


def verdict_short(tool, med, wal_med, ceiling, wal_ceiling):
    if tool == CEILING_ARM:
        return "ceiling (not an arm)"
    if ceiling is None:
        return "UNQUALIFIED"
    return "INADMISSIBLE" if limits(tool, med, wal_med, ceiling, wal_ceiling) else "ok"


if __name__ == "__main__":
    main(sys.argv[1:])
