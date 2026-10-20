#!/usr/bin/env python3
"""Summarise one or more run directories (each holding runs.csv) into medians
with min-max per arm, the A/A spread, and per-round paired ratios.

Usage: summarize.py RESULTS_DIR [RESULTS_DIR ...]

Rows labelled warmup-* are discarded. Rates use the REAL elapsed window
(ts_after_ms - ts_before_ms), not a nominal 60 s. 'lag' is the source-side
cumulative counter minus the destination ground truth, sampled at the window
start and end; a bounded lag means the arm sustained the offered rate, a lag
that grows means it fell behind, a lag that shrinks means it was draining a
backlog (its rate is then NOT a sustained rate).
"""
import csv, statistics, sys, os

PAIRS = {  # arm -> its Kafka Connect counterpart
    "conduit": "connect", "conduit-aa": "connect",
    "conduit-pg": "connect-pg", "conduit-s3": "connect-s3", "conduit-kafka": "connect-kafka",
}

def load(path):
    rows = []
    with open(os.path.join(path, "runs.csv")) as f:
        for r in csv.DictReader(f):
            if r["label"].startswith("warmup") or r["rows_delta"] == "NA":
                continue
            rnd = r["label"].split("-")[0]
            try:
                el = (int(r["ts_after_ms"]) - int(r["ts_before_ms"])) / 1000.0
            except ValueError:
                el = 60.0
            d = int(r["rows_delta"])
            lb = lag(r, "before"); la = lag(r, "after")
            rows.append(dict(round=rnd, tool=r["tool"], delta=d, elapsed=el, rate=d / el,
                             lag_before=lb, lag_after=la))
    return rows

def lag(r, side):
    try:
        return int(r["src_" + side]) - int(r["rows_" + side])
    except ValueError:
        return None

def fmt(x):
    return "n/a" if x is None else f"{x:,.0f}"

def main(dirs):
    rows = []
    for d in dirs:
        rows += load(d)
    by = {}
    for r in rows:
        by.setdefault(r["tool"], []).append(r)
    print("| arm | n | median rate/s | min-max rate/s | spread (max-min)/median | median lag start -> end (events) |")
    print("| --- | --- | --- | --- | --- | --- |")
    med = {}
    for tool in sorted(by):
        v = sorted(x["rate"] for x in by[tool])
        m = statistics.median(v); med[tool] = m
        lb = [x["lag_before"] for x in by[tool] if x["lag_before"] is not None]
        la = [x["lag_after"] for x in by[tool] if x["lag_after"] is not None]
        print(f"| {tool} | {len(v)} | {m:,.0f} | {v[0]:,.0f} - {v[-1]:,.0f} | {(v[-1]-v[0])/m*100:.1f}% | "
              f"{fmt(statistics.median(lb) if lb else None)} -> {fmt(statistics.median(la) if la else None)} |")
    print()
    print("| pair | median ratio (conduit / connect) | per-round ratios |")
    print("| --- | --- | --- |")
    for a, b in PAIRS.items():
        if a not in by or b not in by:
            continue
        ra = {x["round"]: x["rate"] for x in by[a]}
        rb = {x["round"]: x["rate"] for x in by[b]}
        rs = [ra[k] / rb[k] for k in sorted(ra) if k in rb]
        if rs:
            print(f"| {a} vs {b} | {statistics.median(rs):.3f} | {', '.join(f'{x:.3f}' for x in rs)} |")
    if "conduit" in by and "conduit-aa" in by:
        ra = {x["round"]: x["rate"] for x in by["conduit"]}
        rb = {x["round"]: x["rate"] for x in by["conduit-aa"]}
        rs = [abs(ra[k] / rb[k] - 1) * 100 for k in ra if k in rb]
        if rs:
            print(f"\nA/A (conduit vs conduit-aa): per-round |delta| median {statistics.median(rs):.2f}%, max {max(rs):.2f}%")

if __name__ == "__main__":
    main(sys.argv[1:])
