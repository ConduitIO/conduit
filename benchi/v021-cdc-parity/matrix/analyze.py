#!/usr/bin/env python3
"""Computes every reported number from the raw run directories, by the rules
PLAN.md pre-registers (section 9). Nothing is read from an engine's metrics.

    analyze.py RESULTS_ROOT [--gate GATE_JSON] [--allow-harness-mismatch]

RESULTS_ROOT holds one directory per cell (run-cell.sh layout, shards merged;
a cell's other instances are CELL-rep1, CELL-rep2). Prints Markdown, writes
RESULTS_ROOT/analysis.json, and with --gate writes the smoke gate.

It refuses results produced by a different harness (cell.json harness_hash
vs this tree's), unless --allow-harness-mismatch, which is then printed on
every table.
"""
import glob
import json
import math
import os
import re
import statistics
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import cells  # noqa: E402

ANALYSIS_VERSION = "2"
MIN_FLOOR = 0.05          # pre-registered minimum effect floor (5%)
LAG_CAP_S = 5             # a tracked step's end lag must be under 5 s of offered load
TRACK_RATE = 0.97
TRACK_LAG_GROWTH = 0.02
LOADER_OK = 0.97
SATURATED_LAG_GROWTH = 0.01
STATIONARY_HALVES = 0.05  # first vs second half of a window within 5%
SUT_BUSY_CORES = 3.6      # of 4: SUT CPU-bound
DISK_BUSY = 0.90          # a data volume busy >= 90% of the window: disk-bound
DEST_BUSY_CORES = 6.0     # Postgres using >= 6 of the 12 infrastructure cores: destination-bound (pilot revises)
SHORT_WINDOW_S = 120      # a drain whose backlog ran out still counts with >= 120 s of window


# ---------------------------------------------------------------- loading
def jl(path):
    out = []
    try:
        with open(path) as f:
            for line in f:
                try:
                    out.append(json.loads(line))
                except ValueError:
                    pass
    except OSError:
        pass
    return out


def load_json(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return default


def series(rd, meta):
    """Per sink: [(t_ms, count)], each reading at its own read midpoint;
    S3 rebuilt from the post-run object index; plus the source series."""
    rows = jl(os.path.join(rd, "samples.jsonl"))
    sinks = {}
    src = []
    for r in rows:
        s = r.get("src") or {}
        if "events" in s and s.get("t_ms"):
            src.append((s["t_ms"], s["events"], s.get("net"), s.get("step"), s.get("target")))
        for name, v in (r.get("sinks") or {}).items():
            if isinstance(v, dict) and "count" in v and v.get("t_ms"):
                sinks.setdefault(name, []).append((v["t_ms"], v["count"]))
    idx = jl(os.path.join(rd, "s3-index.jsonl"))
    if idx:
        pts, n = [], 0
        for o in sorted(idx, key=lambda o: o["lastmod_ms"]):
            n += o["lines"]
            pts.append((o["lastmod_ms"], n))
        sinks["s3:bench"] = pts
    return sinks, src


def at(points, t):
    """Value at time t, linearly interpolated between the two readings around
    t (readings are point measurements at their own midpoints)."""
    if not points:
        return None
    if t <= points[0][0]:
        return points[0][1]
    for (t0, v0), (t1, v1) in zip(points, points[1:]):
        if t0 <= t <= t1:
            return v0 + (v1 - v0) * ((t - t0) / (t1 - t0)) if t1 > t0 else v1
    return points[-1][1]


def delivered(sinks, t):
    """min over the destinations (a fan-out has landed when every sink has)."""
    vals = [at(p, t) for p in sinks.values() if p]
    vals = [v for v in vals if v is not None]
    return min(vals) if vals else None


def first_ms(sinks):
    ts = []
    for p in sinks.values():
        t = next((t for t, v in p if v > 0), None)
        if t is None:
            return None
        ts.append(t)
    return max(ts) if ts else None


def src_points(src, net):
    return [(t, (n if net else e)) for t, e, n, _, _ in src if (n if net else e) is not None]


def rate(points_fn, t0, t1):
    a, b = points_fn(t0), points_fn(t1)
    if a is None or b is None or t1 <= t0:
        return None
    return (b - a) / ((t1 - t0) / 1000)


def stationarity(fn, t0, t1):
    mid = (t0 + t1) / 2
    r1, r2 = rate(fn, t0, mid), rate(fn, mid, t1)
    sub = []
    step = 30_000
    t = t0
    while t + step <= t1:
        x = rate(fn, t, t + step)
        if x is not None:
            sub.append(x)
        t += step
    cv = statistics.pstdev(sub) / statistics.mean(sub) if len(sub) > 1 and statistics.mean(sub) else None
    ok = r1 is not None and r2 is not None and max(r1, r2) > 0 and abs(r1 - r2) / max(r1, r2) <= STATIONARY_HALVES
    return {"first_half": r1, "second_half": r2, "cv_30s": cv, "stationary": ok}


# ---------------------------------------------------------------- latency
def merge_hist(rows, t0, t1, key="sec"):
    h = {}
    for r in rows:
        s = r.get(key)
        if s is None or not (t0 // 1000 <= int(s) < t1 // 1000):
            continue
        for b, n in (r.get("lat") or {}).items():
            h[int(b)] = h.get(int(b), 0) + n
    return h


def hist_pct(h, q):
    n = sum(h.values())
    if not n:
        return None
    k, acc = q * n, 0
    for b in sorted(h):
        acc += h[b]
        if acc >= k:
            return 1.02 ** b / 1000  # ms
    return None


def latency(rd, t0, t1):
    rows = jl(os.path.join(rd, "kafka-hist.jsonl")) + jl(os.path.join(rd, "pg-hist.jsonl"))
    hh = load_json(os.path.join(rd, "http-hist.json"))
    if hh:
        rows += [{"sec": int(k), **v} for k, v in (hh.get("secs") or {}).items()]
    for o in jl(os.path.join(rd, "s3-index.jsonl")):
        rows.append({"sec": o["lastmod_ms"] // 1000, "lat": o.get("lat") or {}})
    h = merge_hist(rows, t0, t1)
    if not h:
        return None
    return {"n": sum(h.values()), "p50_ms": hist_pct(h, 0.5), "p99_ms": hist_pct(h, 0.99), "p999_ms": hist_pct(h, 0.999)}


def bytes_per_record(rd):
    rows = jl(os.path.join(rd, "kafka-hist.jsonl"))
    n = sum(r.get("n", 0) for r in rows)
    if n:
        return sum(r.get("bytes", 0) for r in rows) / n
    idx = jl(os.path.join(rd, "s3-index.jsonl"))
    lines = sum(o["lines"] for o in idx)
    return sum(o["size"] for o in idx) / lines if lines else None


# ---------------------------------------------------------------- resources
INFRA = ("cdcparity-postgres", "cdcparity-kafka", "cdcparity-minio", "cdcparity-registry", "cdcparity-receiver")


def resources(rd, t0, t1, broker_needed):
    rows = jl(os.path.join(rd, "res.jsonl"))
    out = {}
    for name in ("cdcparity-sut",) + INFRA:
        win = [r for r in rows if name in r.get("c", {}) and t0 <= r["ts_ms"] <= t1]
        alls = [r for r in rows if name in r.get("c", {})]
        if len(win) < 2:
            continue
        c0, c1 = win[0]["c"][name], win[-1]["c"][name]
        el = (win[-1]["ts_ms"] - win[0]["ts_ms"]) / 1000
        cpu_s = ((c1.get("cpu_usec") or 0) - (c0.get("cpu_usec") or 0)) / 1e6
        out[name] = {"cpu_s": cpu_s, "cores": cpu_s / el if el else None,
                     "throttled_s": ((c1.get("throttled_usec") or 0) - (c0.get("throttled_usec") or 0)) / 1e6,
                     "peak_anon_mb": max((r["c"][name].get("anon") or 0) for r in alls) / 2**20,
                     "io_w_mb": ((c1.get("io_wbytes") or 0) - (c0.get("io_wbytes") or 0)) / 2**20}
    sut = out.get("cdcparity-sut", {})
    fp = {"cpu_s": sut.get("cpu_s", 0), "peak_anon_mb": sut.get("peak_anon_mb", 0)}
    if broker_needed and "cdcparity-kafka" in out:
        fp = {k: fp[k] + out["cdcparity-kafka"][k] for k in fp}
    out["footprint"] = fp  # SUT, plus the broker when the arm cannot run without one
    dw = [r for r in rows if t0 <= r["ts_ms"] <= t1 and r.get("disks")]
    if len(dw) >= 2:
        el_ms = dw[-1]["ts_ms"] - dw[0]["ts_ms"]
        out["disks"] = {}
        for role, d1 in dw[-1]["disks"].items():
            d0 = dw[0]["disks"].get(role)
            if not d0 or el_ms <= 0:
                continue
            ops = (d1["reads"] - d0["reads"]) + (d1["writes"] - d0["writes"])
            out["disks"][role] = {"util": (d1["io_ticks_ms"] - d0["io_ticks_ms"]) / el_ms,
                                  "await_ms": ((d1["read_ms"] - d0["read_ms"]) + (d1["write_ms"] - d0["write_ms"])) / ops if ops else 0.0,
                                  "write_mb_s": (d1["sectors_w"] - d0["sectors_w"]) * 512 / 2**20 / (el_ms / 1000)}
    win = [r for r in rows if t0 <= r["ts_ms"] <= t1 and r.get("host")]
    if len(win) >= 2:
        s0, s1 = win[0]["host"].get("steal_ticks", {}), win[-1]["host"].get("steal_ticks", {})
        out["host_steal_ticks"] = sum(int(s1.get(k, 0)) - int(s0.get(k, 0)) for k in s1)
    return out


def classify(res, saturated, loader_bound):
    """First-order cause next to every rate (review M8)."""
    if loader_bound:
        return "loader-bound"
    sut = (res or {}).get("cdcparity-sut") or {}
    cores = sut.get("cores") or 0
    if cores >= SUT_BUSY_CORES or (sut.get("throttled_s") or 0) > 1:
        return f"SUT CPU-bound ({cores:.1f} cores)"
    hot = sorted(((v["util"], k) for k, v in ((res or {}).get("disks") or {}).items() if v.get("util", 0) >= DISK_BUSY), reverse=True)
    if hot:
        return f"disk-bound ({hot[0][1]} {hot[0][0]:.0%} busy; SUT {cores:.1f} cores)"
    dest = ((res or {}).get("cdcparity-postgres") or {}).get("cores") or 0
    if saturated and dest >= DEST_BUSY_CORES:
        return f"destination-bound (postgres {dest:.1f} cores; SUT {cores:.1f} cores)"
    busy = sorted(((v.get("cores") or 0, k) for k, v in (res or {}).items() if k in INFRA), reverse=True)
    top = f"{busy[0][1].replace('cdcparity-', '')} {busy[0][0]:.1f} cores" if busy else "no destination data"
    if saturated:
        return f"SUT waiting ({cores:.1f} cores; busiest infra: {top})"
    return f"not saturated ({cores:.1f} cores)"


def gc_pauses(rd, arm):
    pauses = []
    for p in glob.glob(os.path.join(rd, "prof", "gc*.log")):
        with open(p, errors="replace") as f:
            for line in f:
                m = re.search(r"Pause.*? ([0-9.]+)ms", line)
                if m:
                    pauses.append(float(m.group(1)))
    if arm.startswith("conduit"):
        for p in glob.glob(os.path.join(rd, "benchi", "**", "*.log"), recursive=True):
            with open(p, errors="replace") as f:
                for line in f:
                    m = re.search(r"gc \d+ @[0-9.]+s \d+%: ([0-9.]+)\+[0-9.]+\+([0-9.]+) ms clock", line)
                    if m:
                        pauses.append(float(m.group(1)) + float(m.group(2)))
    if not pauses:
        return None
    pauses.sort()
    return {"n": len(pauses), "p99_ms": pauses[int(0.99 * (len(pauses) - 1))], "max_ms": pauses[-1]}


# ---------------------------------------------------------------- per run
def analyze_run(rd, meta):
    info = load_json(os.path.join(rd, "run.json"), {})
    arm = info.get("arm") or open(os.path.join(rd, "arm")).read().strip()
    hooks = info.get("hooks", {})
    sinks, src = series(rd, meta)
    r = {"run": os.path.basename(rd), "arm": arm, "exit_code": info.get("exit_code"), "hooks": hooks}
    fm = first_ms(sinks)
    r["first_record"] = fm is not None
    if hooks.get("TOOL_START_MS") and fm:
        r["startup_s"] = (fm - hooks["TOOL_START_MS"]) / 1000
    mode = meta["mode"]
    net = False  # Postgres destinations count source position, like every other sink (review B6)
    broker = meta["needs_broker"].get(arm, False)
    dfn = lambda t: delivered(sinks, t)
    sfn = lambda t: at(src_points(src, net), t)
    if hooks.get("BROKER_BATCH_COUNT"):
        r["broker"] = {"batches": hooks["BROKER_BATCH_COUNT"], "records": hooks.get("BROKER_RECORD_COUNT"),
                       "records_per_batch": hooks.get("BROKER_RECORD_COUNT", 0) / max(1, hooks["BROKER_BATCH_COUNT"]),
                       "codecs": {k[len("BROKER_CODEC_"):-len("_COUNT")]: v for k, v in hooks.items() if k.startswith("BROKER_CODEC_")}}
    r["bytes_per_record"] = bytes_per_record(rd)
    if mode in ("capacity", "drain", "idle") and fm:
        w = meta["window"]
        t0, t1 = fm + w["warm_s"] * 1000, fm + (w["warm_s"] + w["window_s"]) * 1000
        r["window"] = [t0, t1]
        if mode == "drain":
            # The backlog may run out inside the window: measure up to the
            # last reading before the count stopped, if at least 120 s remain.
            total = meta["backlog_records"]
            end = next((t for t, v in min(sinks.values(), key=len) if v >= total), None) if sinks else None
            if end and end < t1:
                t1 = end - 1000
                r["short_window_s"] = (t1 - t0) / 1000
        r["rate"] = rate(dfn, t0, t1)
        r["stationarity"] = stationarity(dfn, t0, t1)
        if mode == "capacity":
            r["offered_achieved"] = rate(sfn, t0, t1)
            if r["rate"] is not None and r["offered_achieved"]:
                lag0, lag1 = (sfn(t0) or 0) - (dfn(t0) or 0), (sfn(t1) or 0) - (dfn(t1) or 0)
                r["lag_start"], r["lag_end"] = lag0, lag1
                r["saturated"] = (lag1 - lag0) > SATURATED_LAG_GROWTH * ((sfn(t1) or 0) - (sfn(t0) or 0))
            r["valid"] = bool(r.get("saturated"))
            if not r["valid"]:
                # Kept up with an unlimited loader: a LOWER BOUND, reported as such.
                r["lower_bound"] = r.get("offered_achieved")
        elif mode == "drain":
            r["valid"] = r.get("short_window_s", 1e9) >= SHORT_WINDOW_S
        else:
            r["valid"] = True
        r["resources"] = resources(rd, t0, t1, broker)
        sut = r["resources"].get("cdcparity-sut", {})
        if r["rate"] and sut.get("cpu_s"):
            ev = r["rate"] * (t1 - t0) / 1000
            r["cpu_s_per_m_events"] = sut["cpu_s"] / ev * 1e6
            r["cpu_s_per_m_events_footprint"] = r["resources"]["footprint"]["cpu_s"] / ev * 1e6
            dest = sum((r["resources"].get(k) or {}).get("cpu_s", 0) for k in INFRA if k != "cdcparity-kafka" or not broker)
            r["destination_cpu_s_per_m_events"] = dest / ev * 1e6
        r["bottleneck"] = classify(r["resources"], r.get("saturated", mode == "drain"), False)
    elif mode == "ramp" and fm:
        rp = meta["ramp"]
        starts = {}
        for t, _, _, step, _ in src:
            if step is not None and step not in starts:
                starts[step] = t
        steps = []
        for i, target in enumerate(rp["steps"]):
            st = starts.get(i + 1)  # loader step 0 is the warm-up hold
            if st is None:
                break
            # The sampler sees a step change up to one interval late, so the
            # window ends one interval before the NEXT step was first seen:
            # it never includes the next step's higher offered load.
            t0, t1 = st + rp["settle_s"] * 1000, st + rp["step_s"] * 1000
            nxt = starts.get(i + 2)
            if nxt is not None:
                t1 = min(t1, nxt - 6000)
            dr, sr = rate(dfn, t0, t1), rate(sfn, t0, t1)
            step = {"target": target, "rate": dr, "offered_achieved": sr, "window": [t0, t1]}
            if dr is not None and sr:
                lag0, lag1 = (sfn(t0) or 0) - (dfn(t0) or 0), (sfn(t1) or 0) - (dfn(t1) or 0)
                step["lag_end"] = lag1
                step["loader_bound"] = sr < LOADER_OK * target
                step["tracked"] = (dr >= TRACK_RATE * sr and (lag1 - lag0) <= TRACK_LAG_GROWTH * sr * (t1 - t0) / 1000
                                   and lag1 <= LAG_CAP_S * sr)
                if step["tracked"]:
                    step["latency"] = latency(rd, t0, t1)  # tracked steps only (review M6)
            steps.append(step)
        r["steps"] = steps
        idx = [k for k, s in enumerate(steps) if s.get("tracked") and not s.get("loader_bound")]
        # Ordinal: the index of the highest consecutive tracked step from the bottom.
        hi = -1
        for k, s in enumerate(steps):
            if s.get("loader_bound"):
                break
            if not s.get("tracked"):
                break
            hi = k
        r["highest_tracked_step"] = hi
        r["highest_tracked_rate"] = steps[hi]["target"] if hi >= 0 else None
        nxt = steps[hi + 1] if hi + 1 < len(steps) else None
        r["first_untracked"] = {"target": nxt["target"], "delivered": nxt["rate"], "loader_bound": nxt.get("loader_bound")} if nxt else None
        r["tracked_steps"] = [steps[k]["target"] for k in idx]
        r["resources"] = resources(rd, fm, fm + (rp["warm_s"] + rp["step_s"] * len(rp["steps"])) * 1000, broker)
    elif mode == "snapshot":
        done = hooks.get("DONE_MS")
        if fm and done:
            r["land_s"] = (done - fm) / 1000
            r["rate"] = meta["snapshot_rows"] / r["land_s"] if r["land_s"] > 0 else None
            if hooks.get("TOOL_START_MS"):
                r["total_s"] = (done - hooks["TOOL_START_MS"]) / 1000
            r["resources"] = resources(rd, fm, done, broker)
        r["valid"] = bool(done)
    if mode in ("correct", "recover", "recover-snapshot"):
        r["verify"] = info.get("verify", {})
        if mode.startswith("recover") and hooks.get("RESTART_MS"):
            restart = hooks["RESTART_MS"]
            before = dfn(restart)
            resumed = None
            for p in sinks.values():
                t = next((t for t, v in p if t > restart and before is not None and v > before), None)
                if t:
                    resumed = max(resumed or 0, t)
            r["resume_s"] = (resumed - restart) / 1000 if resumed else None
            r["kill_at_delivered"] = dfn(hooks.get("KILL_MS", restart))
    r["gc"] = gc_pauses(rd, arm)
    return r


# ---------------------------------------------------------------- statistics
def metric_of(meta):
    return {"capacity": "rate", "drain": "rate", "snapshot": "land_s", "ramp": "highest_tracked_step", "idle": None}.get(meta["mode"])


def round_of(run):
    m = re.match(r"r(\d+)-", run)
    return m.group(1) if m else None


def by_round(runs, arm, key):
    out = {}
    for x in runs:
        if x.get("arm") == arm and round_of(x.get("run", "")):
            out[round_of(x["run"])] = x
    return out


def half_range(vals):
    vals = [v for v in vals if v]
    if len(vals) < 2:
        return 0.0
    m = statistics.median(vals)
    return (max(vals) - min(vals)) / m / 2 if m else 0.0


def verdict(runs, a, b, aa_floor, floor_borrowed, metric, lower_is_better=False):
    """The pre-registered rule (PLAN.md section 9). A consistency label, NOT a
    significance test: with 5 rounds the smallest two-sided sign-test p is
    0.0625. Reports the median per-round ratio, its range and the sign count."""
    if b.startswith("best-of:"):
        arms_b = b[len("best-of:"):].split(",")
        rb = {}
        for arm in arms_b:
            for k, x in by_round(runs, arm, metric).items():
                if x.get("v") is not None and (k not in rb or x["v"] > rb[k]["v"]):
                    rb[k] = x
    else:
        rb = by_round(runs, b, metric)
    ra = by_round(runs, a, metric)
    ratios, lower = [], []
    for k in sorted(set(ra) & set(rb)):
        x, y = ra[k], rb[k]
        if x.get("v") is not None and y.get("v") is not None and y["v"]:
            q = x["v"] / y["v"]
            ratios.append(1 / q if lower_is_better and q else q)
        elif x.get("lower_bound") and y.get("v") and x["lower_bound"] > y["v"]:
            lower.append(x["lower_bound"] / y["v"])  # a kept up above b's capacity
        elif y.get("lower_bound") and x.get("v") and y["lower_bound"] > x["v"]:
            lower.append(-y["lower_bound"] / x["v"])  # b kept up above a's capacity
    res = {"pair": f"{a} vs {b}", "ratios": [round(q, 4) for q in ratios]}
    if lower and len(lower) >= 4 and not ratios:
        up = [q for q in lower if q > 0]
        res.update(verdict=("LOWER-BOUND: a >= " if len(up) == len(lower) else ("LOWER-BOUND: b >= " if not up else "UNRESOLVED")),
                   lower_bound_ratios=lower)
        if res["verdict"].startswith("LOWER-BOUND"):
            res["verdict"] += f"{min(abs(q) for q in lower):.2f}x the other"
        return res
    if len(ratios) < 3:
        res.update(verdict="UNRESOLVED", why=f"only {len(ratios)} paired rounds with both valid")
        return res
    med = statistics.median(ratios)
    noise = max(half_range([ra[k].get("v") for k in ra]), half_range([rb[k].get("v") for k in rb]))
    floor = max(MIN_FLOOR, aa_floor or 0.0, noise)
    if floor_borrowed:
        floor = max(floor * 2, 2 * MIN_FLOOR)
    up, down = sum(q > 1 for q in ratios), sum(q < 1 for q in ratios)
    res.update(median_ratio=med, range=[min(ratios), max(ratios)], signs=f"{max(up, down)}/{len(ratios)}",
               floor=floor, floor_borrowed=floor_borrowed)
    eff = abs(med - 1)
    if eff > floor and max(up, down) == len(ratios):
        res["verdict"] = "RESOLVED"
    elif eff > floor and max(up, down) >= len(ratios) - 1:
        res["verdict"] = "SUGGESTIVE"
    else:
        res["verdict"] = "UNRESOLVED"
    return res


def ramp_verdict(runs, a, b):
    ra, rb = by_round(runs, a, None), by_round(runs, b, None)
    ks = sorted(set(ra) & set(rb))
    da = [ra[k].get("highest_tracked_step", -1) for k in ks]
    db = [rb[k].get("highest_tracked_step", -1) for k in ks]
    higher = sum(x > y for x, y in zip(da, db))
    lower = sum(x < y for x, y in zip(da, db))
    res = {"pair": f"{a} vs {b}", "a_steps": da, "b_steps": db, "a_higher": higher, "b_higher": lower, "rounds": len(ks)}
    if len(ks) >= 3 and (higher == len(ks) or lower == len(ks)):
        res["verdict"] = "RESOLVED (ordinal)"
    elif len(ks) >= 3 and max(higher, lower) >= len(ks) - 1:
        res["verdict"] = "SUGGESTIVE (ordinal)"
    else:
        res["verdict"] = "UNRESOLVED"
    return res


# ---------------------------------------------------------------- cells
def analyze_cell(cdir):
    meta = load_json(os.path.join(cdir, "cell", "cell.json"))
    if not meta:
        return None
    runs = []
    for rd in sorted(glob.glob(os.path.join(cdir, "*"))):
        b = os.path.basename(rd)
        if not os.path.isdir(rd) or not re.match(r"(warmup|r\d+|diag|kill\d+)-", b):
            continue
        try:
            runs.append(analyze_run(rd, meta))
        except Exception as e:  # a broken run is reported, never silently dropped
            runs.append({"run": b, "error": repr(e)})
    m = metric_of(meta)
    for r in runs:
        if re.match(r"r\d+-", r.get("run", "")):
            r["v"] = r.get(m) if (r.get("valid", True) and m) else None
    return {"meta": meta, "runs": runs, "dir": os.path.basename(cdir)}


def aa_floors(cell):
    """Per side's A/A twin in this cell: max per-round |arm / arm-aa - 1|."""
    runs = cell["runs"]
    floors = {}
    for aa in [a for a in cell["meta"]["arms"] if cells.parse_arm(a)[2]]:
        side, tier, _, var = cells.parse_arm(aa)
        twin = f"{side}-{tier}" + (f"@{var}" if var else "")
        a, b = by_round(runs, twin, None), by_round(runs, aa, None)
        d = [abs(a[k]["v"] / b[k]["v"] - 1) for k in set(a) & set(b) if a[k].get("v") and b[k].get("v")]
        if d:
            floors[side] = max(d)
    return floors


def family_key(meta):
    return (meta["family"], tuple(meta["dests"]), meta["source"], meta["mode"])


def report(root, allow_mismatch=False):
    here_hash = cells.harness_hash()
    cs = {}
    for cdir in sorted(glob.glob(os.path.join(root, "*"))):
        if os.path.isdir(os.path.join(cdir, "cell")):
            c = analyze_cell(cdir)
            if c:
                cs[c["dir"]] = c
    mismatched = sorted({c["meta"].get("harness_hash") for c in cs.values()} - {here_hash})
    if mismatched and not allow_mismatch:
        sys.exit(f"results come from harness {mismatched}, this analysis is {here_hash}: refusing (--allow-harness-mismatch)")
    # A/A floors: per cell where it has twins, else borrowed from the family's base cell.
    own = {cid: aa_floors(c) for cid, c in cs.items()}
    family = {}
    for cid, c in cs.items():
        if own[cid]:
            k = family_key(c["meta"])
            family[k] = max(family.get(k, 0), max(own[cid].values()))
    primary = {(cid, a, b) for cid, a, b in cells.PRIMARY}
    md = [f"analysis v{ANALYSIS_VERSION}, harness {here_hash}" + (f" (RESULTS FROM {mismatched})" if mismatched else ""),
          f"{len(cells.PRIMARY)} pre-registered primary comparisons; every other pair is exploratory. "
          "Labels are consistency labels over the rounds, not significance tests (5 rounds: minimum two-sided sign-test p = 0.0625).", ""]
    by_cell = {}
    for cid, c in cs.items():
        base = re.sub(r"-rep\d+$", "", cid)
        by_cell.setdefault(base, []).append(c)
    out = {"analysis_version": ANALYSIS_VERSION, "harness_hash": here_hash, "cells": {}}
    for base, insts in sorted(by_cell.items()):
        meta = insts[0]["meta"]
        appendix = meta.get("reference") or meta.get("plugin")
        md.append(f"## {base}{' (APPENDIX: ' + ('reference connector' if meta.get('reference') else 'labs plugin') + ')' if appendix else ''}\n")
        md.append(f"{meta['mode']}, {meta['source']} -> {'+'.join(meta['dests'])}, parallelism {meta['par']}, payload {meta['payload']} B, "
                  f"workload {json.dumps(meta['workload'])}, instances {len(insts)}")
        cell_out = {"instances": [], "verdicts": {}}
        pairs = []
        for a in meta["arms"]:
            side, tier, aa, var = cells.parse_arm(a)
            if not side.startswith("conduit") or aa or side == "conduit-classic":
                continue
            for b in meta["arms"]:
                s2, t2, aa2, _ = cells.parse_arm(b)
                if s2.startswith("conduit") or aa2 or t2 != tier:
                    continue
                if (side == "conduit-avro") != (cells.serialization(b) == "avro") and "kafka" in meta["dests"]:
                    continue  # compare like serialization with like on Kafka
                pairs.append((a, b))
        for cid, a, b in cells.PRIMARY:
            if cid == base and b.startswith("best-of:"):
                pairs.append((a, b))
        for inst in insts:
            floors = own[inst["dir"]]
            fam = family.get(family_key(meta))
            rows = per_arm_table(inst, meta)
            md += rows
            vs = {}
            for a, b in pairs:
                if meta["mode"] == "ramp":
                    vs[f"{a} vs {b}"] = ramp_verdict(inst["runs"], a, b)
                    continue
                sa, sb = cells.arm_side(a), cells.arm_side(b.split(",")[0].replace("best-of:", ""))
                aa_floor = max([floors.get(sa, 0), floors.get(sb, 0)]) if floors else fam
                borrowed = not floors
                vs[f"{a} vs {b}"] = verdict(inst["runs"], a, b, aa_floor, borrowed, metric_of(meta),
                                            lower_is_better=meta["mode"] == "snapshot")
            cell_out["instances"].append({"dir": inst["dir"], "verdicts": vs, "aa_floors": floors})
        # cross-instance rule: a verdict stands only if every instance agrees.
        for key in (cell_out["instances"][0]["verdicts"] if cell_out["instances"] else {}):
            vals = [i["verdicts"][key] for i in cell_out["instances"] if key in i["verdicts"]]
            labels = {v["verdict"] for v in vals}
            signs = {(v.get("median_ratio", 1) > 1) for v in vals if "median_ratio" in v}
            final = vals[0]["verdict"] if len(labels) == 1 and len(signs) <= 1 else "UNRESOLVED (instances disagree)"
            a, b = key.split(" vs ")
            cell_out["verdicts"][key] = {"final": final, "per_instance": vals, "primary": (base, a, b) in primary,
                                         "appendix": bool(appendix)}
        if cell_out["verdicts"]:
            md.append("\n| pair | primary | median ratio | range | signs | floor | verdict |\n| --- | --- | --- | --- | --- | --- | --- |")
            for key, v in cell_out["verdicts"].items():
                p = v["per_instance"][0]
                rng = p.get("range")
                md.append(f"| {key} | {'PRIMARY' if v['primary'] else 'exploratory'} | {p.get('median_ratio', float('nan')):.3f} | "
                          f"{'%.3f - %.3f' % tuple(rng) if rng else '-'} | {p.get('signs', '-')} | "
                          f"{'%.1f%%%s' % (p['floor'] * 100, ' (borrowed x2)' if p.get('floor_borrowed') else '') if 'floor' in p else '-'} | "
                          f"{v['final']} |")
        if meta["mode"] in ("correct", "recover", "recover-snapshot"):
            md.append("\n| run | sink | ok | summary | resume s |\n| --- | --- | --- | --- | --- |")
            for inst in insts:
                for r in inst["runs"]:
                    for sink, v in (r.get("verify") or {}).items():
                        md.append(f"| {r['run']} | {sink} | {v.get('ok')} | {v.get('summary', '-')} | {r.get('resume_s', '-')} |")
        md.append("")
        out["cells"][base] = {"meta": meta, "result": cell_out, "runs": [i["runs"] for i in insts]}
    with open(os.path.join(root, "analysis.json"), "w") as f:
        json.dump(out, f, indent=1, default=str)
    return "\n".join(md), out


def per_arm_table(inst, meta):
    runs = [r for r in inst["runs"] if re.match(r"r\d+-", r.get("run", ""))]
    fmt = lambda v, p=0: "-" if v is None else f"{v:,.{p}f}"
    md = [f"\n{inst['dir']}:\n",
          "| arm | metric median | min - max | valid | CPU-s/M ev (SUT / footprint / dest) | peak RSS MB | startup s | bytes/rec | bottleneck (median run) | stationary |",
          "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"]
    for arm in meta["arms"]:
        rs = [r for r in runs if r.get("arm") == arm]
        vals = [r.get("v") for r in rs if r.get("v") is not None]
        lb = [r.get("lower_bound") for r in rs if r.get("lower_bound")]
        med = statistics.median(vals) if vals else None
        g = lambda k: statistics.median([r[k] for r in rs if r.get(k) is not None]) if any(r.get(k) is not None for r in rs) else None
        rss = [((r.get("resources") or {}).get("cdcparity-sut") or {}).get("peak_anon_mb") for r in rs]
        rss = [x for x in rss if x]
        bn = [r.get("bottleneck") for r in rs if r.get("bottleneck")]
        st = [r.get("stationarity", {}).get("stationary") for r in rs if r.get("stationarity")]
        val = f"{len(vals)}/{len(rs)}" + (f", >= {fmt(min(lb))} (kept up)" if lb else "")
        md.append(f"| {arm} | {fmt(med, 2 if meta['mode'] == 'snapshot' else 0)} | {fmt(min(vals) if vals else None)} - {fmt(max(vals) if vals else None)} | "
                  f"{val} | {fmt(g('cpu_s_per_m_events'), 1)} / {fmt(g('cpu_s_per_m_events_footprint'), 1)} / {fmt(g('destination_cpu_s_per_m_events'), 1)} | "
                  f"{fmt(statistics.median(rss) if rss else None)} | {fmt(g('startup_s'), 1)} | {fmt(g('bytes_per_record'))} | "
                  f"{bn[len(bn)//2] if bn else '-'} | {sum(1 for x in st if x)}/{len(st)} |")
    return md


# ---------------------------------------------------------------- smoke gate
def gate(out):
    """PASS only if every correctness and recovery run verified clean, every
    run exited 0 and delivered a record, and the run directories exist for
    every smoke cell's arms (review B2, M9)."""
    failures = []
    for base, c in out["cells"].items():
        meta = c["meta"]
        for runs in c["runs"]:
            seen = {r.get("arm") for r in runs}
            for a in meta["arms"]:
                if a not in seen:
                    failures.append(f"{base}: no run for {a}")
            for r in runs:
                if r.get("error"):
                    failures.append(f"{base} {r['run']}: analysis error {r['error']}")
                    continue
                if r.get("exit_code") not in (0, None):
                    failures.append(f"{base} {r['run']}: exit {r['exit_code']}")
                if not r.get("first_record"):
                    failures.append(f"{base} {r['run']}: no record delivered")
                if meta["mode"] in ("correct", "recover", "recover-snapshot"):
                    v = r.get("verify") or {}
                    if not v:
                        failures.append(f"{base} {r['run']}: no verify report")
                    for sink, rep in v.items():
                        if rep.get("ok") is not True:
                            failures.append(f"{base} {r['run']} {sink}: {rep.get('summary', 'verify failed')}")
    return {"pass": not failures, "failures": failures, "harness_hash": out["harness_hash"]}


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    md, out = report(sys.argv[1], "--allow-harness-mismatch" in sys.argv)
    print(md)
    if "--gate" in sys.argv:
        g = gate(out)
        with open(sys.argv[sys.argv.index("--gate") + 1], "w") as f:
            json.dump(g, f, indent=1)
        print(f"\nsmoke gate: {'PASS' if g['pass'] else 'FAIL'} ({len(g['failures'])} failures)")
