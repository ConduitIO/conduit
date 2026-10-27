#!/usr/bin/env python3
"""Computes every reported number from the raw run directories, by the rules
PLAN.md pre-registers. Nothing is read from an engine's own metrics.

    analyze.py RESULTS_ROOT [--price USD_PER_HOUR] [--json]

RESULTS_ROOT holds one directory per cell (as run-cell.sh writes them, the
shards merged). It prints one Markdown section per cell and writes
RESULTS_ROOT/analysis.json.

Rules (PLAN.md, "Analysis rules"):
  window       capacity/drain: [first record + warm-up, + window]; ramp step i:
               [step start + settle, step end]. Rates use the real elapsed time
               between the two samples that bound the window.
  tracking     a ramp step is TRACKED by an arm when its sink rate is at least
               97% of the offered rate the loader ACHIEVED in that window, and its
               lag grew by at most 2% of the window's offered events. A step where
               the loader achieved under 97% of its target is LOADER-BOUND: no
               verdict for any arm at that step.
  capacity     capacity cells: the sink rate in the window, valid only if the
               arm's lag grew in the window (it was saturated). An arm whose lag
               did not grow kept up with an unlimited loader: its capacity is
               reported as ">= the loader's achieved rate", unresolved.
               Drain cells: valid only if the backlog was not exhausted before
               the window ended.
  verdict      per pair (Conduit arm vs the other side's arm at the same tier):
               RESOLVED when |median ratio - 1| > 2 x the family's A/A floor, the
               bootstrap 95% CI of the median per-round ratio excludes 1, and at
               least 4 of 5 rounds have the same sign; SUGGESTIVE when the
               median ratio exceeds the A/A floor but not all three hold;
               UNRESOLVED otherwise. The A/A floor is the maximum per-round
               |conduit-s / conduit-s-aa - 1| in the family's base cell.
  correctness  an arm that lost, corrupted or (on ordered sinks) reordered any
               record FAILS the cell regardless of speed; duplicates are
               reported and allowed (at-least-once), counted per million.
"""
import glob
import json
import math
import os
import random
import re
import statistics
import sys

PRICE = 0.424  # c6i.2xlarge on-demand, us-west-1, USD/h (PLAN.md)


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


def sink_count(row):
    """min over the row's sinks (a fan-out lands when every sink has)."""
    vals = [v.get("count") for v in row.get("sinks", {}).values() if isinstance(v, dict)]
    vals = [v for v in vals if isinstance(v, (int, float))]
    return min(vals) if vals else None


def at(samples, t_ms, key):
    """The sample nearest to t_ms that has key (value, its ts)."""
    best = None
    for s in samples:
        v = key(s)
        if v is None:
            continue
        d = abs(s["ts_ms"] - t_ms)
        if best is None or d < best[0]:
            best = (d, v, s["ts_ms"])
    return (best[1], best[2]) if best else (None, None)


def rate_between(samples, t0, t1, key):
    v0, s0 = at(samples, t0, key)
    v1, s1 = at(samples, t1, key)
    if v0 is None or v1 is None or s1 <= s0:
        return None, None
    return (v1 - v0) / ((s1 - s0) / 1000), (v0, v1, s0, s1)


def first_ms(samples):
    for s in samples:
        c = sink_count(s)
        if c and c > 0:
            return s["ts_ms"]
    return None


def src_key(net):
    return (lambda s: s.get("src_net")) if net else (lambda s: s.get("src_events"))


# ---------------------------------------------------------------- per run
def gc_pauses(rd, arm):
    """GC pause times (ms) from the JVM gc.log or Go's gctrace lines."""
    pauses = []
    for p in glob.glob(os.path.join(rd, "prof", "gc.log")):
        with open(p, errors="replace") as f:
            for line in f:
                m = re.search(r"Pause.*? ([0-9.]+)ms", line)
                if m:
                    pauses.append(float(m.group(1)))
    if arm.startswith("conduit"):
        for p in glob.glob(os.path.join(rd, "benchi", "**", "*.log"), recursive=True):
            with open(p, errors="replace") as f:
                for line in f:
                    # gc 12 @3.1s 2%: 0.031+1.2+0.012 ms clock, ...: the two
                    # stop-the-world phases are the first and last terms.
                    m = re.search(r"gc \d+ @[0-9.]+s \d+%: ([0-9.]+)\+[0-9.]+\+([0-9.]+) ms clock", line)
                    if m:
                        pauses.append(float(m.group(1)) + float(m.group(2)))
    if not pauses:
        return None
    pauses.sort()
    return {"n": len(pauses), "p99_ms": pauses[int(0.99 * (len(pauses) - 1))], "max_ms": pauses[-1],
            "total_ms": round(sum(pauses), 1)}


def resources(rd, t0, t1, broker_needed):
    rows = jl(os.path.join(rd, "res.jsonl"))
    out = {}
    for name in ("cdcparity-sut", "cdcparity-kafka"):
        win = [r for r in rows if name in r.get("c", {}) and t0 <= r["ts_ms"] <= t1]
        alls = [r for r in rows if name in r.get("c", {})]
        if len(win) < 2:
            continue
        c0, c1 = win[0]["c"][name], win[-1]["c"][name]
        cpu_s = ((c1.get("cpu_usec") or 0) - (c0.get("cpu_usec") or 0)) / 1e6
        el = (win[-1]["ts_ms"] - win[0]["ts_ms"]) / 1000
        out[name] = {
            "cpu_s": cpu_s, "cpu_cores": cpu_s / el if el else None,
            "throttled_s": ((c1.get("throttled_usec") or 0) - (c0.get("throttled_usec") or 0)) / 1e6,
            "peak_anon_mb": max((r["c"][name].get("anon") or 0) for r in alls) / 2**20,
            "peak_mem_mb": max((r["c"][name].get("mem_peak") or r["c"][name].get("mem_current") or 0) for r in alls) / 2**20,
            "io_w_mb": ((c1.get("io_wbytes") or 0) - (c0.get("io_wbytes") or 0)) / 2**20,
        }
    tot = dict(out.get("cdcparity-sut", {}))
    if broker_needed and "cdcparity-kafka" in out and tot:
        tot = {k: (tot.get(k) or 0) + (out["cdcparity-kafka"].get(k) or 0) for k in ("cpu_s", "peak_anon_mb", "peak_mem_mb")}
    out["footprint"] = tot  # SUT, plus the broker when the arm cannot run without one
    return out


def latency(samples, t0, t1):
    p50s, p99s, p999s, n = [], [], [], 0
    for s in samples:
        if not (t0 <= s["ts_ms"] <= t1):
            continue
        for v in s.get("sinks", {}).values():
            if isinstance(v, dict) and v.get("lat_n"):
                n += v["lat_n"]
                p50s.append(v["lat_p50_us"])
                p99s.append(v["lat_p99_us"])
                p999s.append(v["lat_p999_us"])
    if not p50s:
        return None
    # Interval percentiles combined conservatively: the median interval p50,
    # the median interval p99, the WORST interval p99.9 (PLAN.md).
    return {"n": n, "p50_ms": statistics.median(p50s) / 1000, "p99_ms": statistics.median(p99s) / 1000,
            "p999_ms": max(p999s) / 1000}


def analyze_run(rd, meta):
    info = load_json(os.path.join(rd, "run.json"), {})
    arm = info.get("arm") or open(os.path.join(rd, "arm")).read().strip()
    samples = jl(os.path.join(rd, "samples.jsonl"))
    hooks = info.get("hooks", {})
    r = {"run": os.path.basename(rd), "arm": arm, "exit_code": info.get("exit_code")}
    fm = first_ms(samples)
    if hooks.get("TOOL_START_MS") and fm:
        r["startup_s"] = (fm - hooks["TOOL_START_MS"]) / 1000
    mode = meta["mode"]
    net = "pg" in meta["dests"] and meta["source"] == "pg"
    broker = meta["needs_broker"].get(arm, False)
    if mode in ("capacity", "drain") and fm:
        w = meta["window"]
        t0, t1 = fm + w["warm_s"] * 1000, fm + (w["warm_s"] + w["window_s"]) * 1000
        rate, b = rate_between(samples, t0, t1, sink_count)
        r["rate"] = rate
        if mode == "capacity":
            src, sb = rate_between(samples, t0, t1, src_key(net))
            r["offered_achieved"] = src
            if b and sb:
                lag0, lag1 = sb[0] - b[0], sb[1] - b[1]
                r["lag_start"], r["lag_end"] = lag0, lag1
                r["saturated"] = (lag1 - lag0) > 0.01 * (sb[1] - sb[0])
            r["valid"] = bool(r.get("saturated"))
            if not r["valid"]:
                r["note"] = "kept up with the unlimited loader: capacity >= offered_achieved"
        else:
            last, _ = at(samples, t1, sink_count)
            r["backlog_left_at_window_end"] = (meta["backlog"] - last) if last is not None else None
            r["valid"] = last is not None and last < meta["backlog"]
            if not r["valid"]:
                r["note"] = "backlog exhausted inside the window"
        r["resources"] = resources(rd, t0, t1, broker)
        sut = r["resources"].get("cdcparity-sut", {})
        if rate and sut.get("cpu_s"):
            events = rate * (t1 - t0) / 1000
            r["cpu_s_per_m_events"] = sut["cpu_s"] / events * 1e6
            fp = r["resources"]["footprint"]
            r["cpu_s_per_m_events_footprint"] = fp.get("cpu_s", 0) / events * 1e6
        if rate:
            r["usd_per_billion"] = PRICE / 3600 / rate * 1e9
        r["latency"] = latency(samples, t0, t1)
    elif mode == "ramp" and fm:
        rp = meta["ramp"]
        steps = []
        starts = {}
        for s in samples:
            if s.get("step") is not None and s["step"] not in starts:
                starts[s["step"]] = s["ts_ms"]
        for i, target in enumerate(rp["steps"]):
            # Loader step 0 is the warm-up hold; measured step i is loader step i+1.
            st = starts.get(i + 1)
            if st is None:
                break
            t0, t1 = st + rp["settle_s"] * 1000, st + rp["step_s"] * 1000
            rate, b = rate_between(samples, t0, t1, sink_count)
            src, sb = rate_between(samples, t0, t1, src_key(net))
            step = {"target": target, "rate": rate, "offered_achieved": src}
            if rate is not None and src:
                lag_growth = (sb[1] - b[1]) - (sb[0] - b[0])
                # The loader's offered rate is in ops; a Postgres destination
                # counts net rows (inserts - deletes = 60% of ops at 70/20/10).
                expected = target * (0.6 if net else 1.0)
                step["loader_bound"] = src < 0.97 * expected
                step["tracked"] = (rate >= 0.97 * src) and lag_growth <= 0.02 * (sb[1] - sb[0])
                step["latency"] = latency(samples, t0, t1)
            steps.append(step)
        r["steps"] = steps
        ok = [s for s in steps if s.get("tracked") and not s.get("loader_bound")]
        r["sustained"] = max((s["offered_achieved"] for s in ok), default=None)
        fell = next((s for s in steps if s.get("tracked") is False and not s.get("loader_bound")), None)
        r["fell_behind_at"] = fell["target"] if fell else None
        r["delivered_when_behind"] = fell["rate"] if fell else None
        r["resources"] = resources(rd, fm, fm + (rp["warm_s"] + rp["step_s"] * len(rp["steps"])) * 1000, broker)
    elif mode == "snapshot":
        done = hooks.get("DONE_MS")
        if fm and done:
            r["land_s"] = (done - fm) / 1000
            r["rate"] = meta["snapshot_rows"] / r["land_s"] if r["land_s"] > 0 else None
            if hooks.get("TOOL_START_MS"):
                r["total_s"] = (done - hooks["TOOL_START_MS"]) / 1000
        r["valid"] = bool(done)
        r["resources"] = resources(rd, fm or 0, done or 0, broker) if fm and done else None
    elif mode in ("correct", "recover"):
        r["verify"] = {k: {kk: v.get(kk) for kk in ("ok", "missing", "duplicates", "malformed", "reordered", "unknown",
                                                     "unattributed", "extra", "stale", "expected", "expected_rows")}
                       for k, v in info.get("verify", {}).items()}
        if mode == "recover":
            kill, restart = hooks.get("KILL_MS"), hooks.get("RESTART_MS")
            if restart:
                before = None
                resumed = None
                for s in samples:
                    c = sink_count(s)
                    if c is None:
                        continue
                    if s["ts_ms"] <= restart:
                        before = c
                    elif before is not None and c > before:
                        resumed = s["ts_ms"]
                        break
                r["resume_s"] = (resumed - restart) / 1000 if resumed else None
    r["gc"] = gc_pauses(rd, arm)
    return r


# ---------------------------------------------------------------- statistics
def bootstrap_ci(values, stat=statistics.median, n=10000, seed=1):
    """Percentile bootstrap 95% CI. With 5 rounds it is coarse (PLAN.md
    limitations); it is reported, not hidden behind a p-value."""
    if len(values) < 2:
        return None
    rng = random.Random(seed)
    boots = sorted(stat([rng.choice(values) for _ in values]) for _ in range(n))
    return boots[int(0.025 * n)], boots[int(0.975 * n) - 1]


def summarize(vals):
    vals = [v for v in vals if v is not None]
    if not vals:
        return None
    med = statistics.median(vals)
    return {"n": len(vals), "median": med, "min": min(vals), "max": max(vals),
            "spread_pct": (max(vals) - min(vals)) / med * 100 if med else None, "ci95": bootstrap_ci(vals)}


def metric_of(meta):
    return {"capacity": "rate", "drain": "rate", "snapshot": "rate", "ramp": "sustained"}.get(meta["mode"])


def verdict(conduit_runs, other_runs, aa_floor):
    """Per-round paired ratios (same round index), then the pre-registered rule."""
    by_round = lambda runs: {re.match(r"r(\d+)-", x["run"]).group(1): x for x in runs if re.match(r"r\d+-", x["run"])}
    a, b = by_round(conduit_runs), by_round(other_runs)
    ratios = []
    for k in sorted(set(a) & set(b)):
        if a[k].get("v") and b[k].get("v"):
            ratios.append(a[k]["v"] / b[k]["v"])
    if len(ratios) < 3:
        return {"verdict": "UNRESOLVED", "why": f"only {len(ratios)} paired rounds", "ratios": ratios}
    med = statistics.median(ratios)
    ci = bootstrap_ci(ratios)
    same_sign = max(sum(r > 1 for r in ratios), sum(r < 1 for r in ratios))
    floor = aa_floor if aa_floor is not None else float("nan")
    eff = abs(med - 1)
    res = {"median_ratio": med, "ci95": ci, "ratios": ratios, "aa_floor": aa_floor, "same_sign_rounds": same_sign}
    if aa_floor is None:
        res.update(verdict="UNRESOLVED", why="no A/A floor for this family")
    elif eff > 2 * floor and ci and not (ci[0] <= 1 <= ci[1]) and same_sign >= len(ratios) - 1:
        res.update(verdict="RESOLVED")
    elif eff > floor:
        res.update(verdict="SUGGESTIVE")
    else:
        res.update(verdict="UNRESOLVED", why="inside the A/A floor")
    return res


# ---------------------------------------------------------------- cells
def analyze_cell(cdir):
    meta = load_json(os.path.join(cdir, "cell", "cell.json"))
    if not meta:
        return None
    runs = []
    for rd in sorted(glob.glob(os.path.join(cdir, "*"))):
        b = os.path.basename(rd)
        if not os.path.isdir(rd) or b == "cell" or not re.match(r"(warmup|r\d+|diag|kill\d+)-", b):
            continue
        try:
            runs.append(analyze_run(rd, meta))
        except Exception as e:  # a broken run is reported, never silently dropped
            runs.append({"run": b, "error": repr(e)})
    m = metric_of(meta)
    measured = [r for r in runs if re.match(r"r\d+-", r.get("run", ""))]
    for r in measured:
        r["v"] = r.get(m) if (r.get("valid", True) and m) else None
    per_arm = {}
    for arm in meta["arms"]:
        rs = [r for r in measured if r.get("arm") == arm]
        per_arm[arm] = {
            "metric": m,
            "summary": summarize([r.get("v") for r in rs]),
            "invalid_runs": [r["run"] for r in rs if r.get("valid") is False],
            "startup_s": summarize([r.get("startup_s") for r in rs]),
            "cpu_s_per_m_events": summarize([r.get("cpu_s_per_m_events") for r in rs]),
            "cpu_s_per_m_events_footprint": summarize([r.get("cpu_s_per_m_events_footprint") for r in rs]),
            "peak_rss_mb": summarize([((r.get("resources") or {}).get("cdcparity-sut") or {}).get("peak_anon_mb") for r in rs]),
            "usd_per_billion": summarize([r.get("usd_per_billion") for r in rs]),
            "gc_max_ms": summarize([(r.get("gc") or {}).get("max_ms") for r in rs]),
            "latency_p99_ms": summarize([(r.get("latency") or {}).get("p99_ms") for r in rs]),
        }
    return {"meta": meta, "runs": runs, "arms": per_arm}


def aa_floors(cells):
    floors = {}
    for cid, c in cells.items():
        runs = [r for r in c["runs"] if re.match(r"r\d+-", r.get("run", ""))]
        a = {re.match(r"r(\d+)-", r["run"]).group(1): r.get("v") for r in runs if r.get("arm") == "conduit-s"}
        b = {re.match(r"r(\d+)-", r["run"]).group(1): r.get("v") for r in runs if r.get("arm") == "conduit-s-aa"}
        deltas = [abs(a[k] / b[k] - 1) for k in set(a) & set(b) if a.get(k) and b.get(k)]
        if deltas:
            key = (c["meta"]["family"], tuple(c["meta"]["dests"]), c["meta"]["source"], c["meta"]["mode"])
            floors[key] = max(floors.get(key, 0), max(deltas))
    return floors


def report(root):
    cells = {}
    for cdir in sorted(glob.glob(os.path.join(root, "*"))):
        if os.path.isdir(os.path.join(cdir, "cell")):
            c = analyze_cell(cdir)
            if c:
                cells[os.path.basename(cdir)] = c
    floors = aa_floors(cells)
    md = []
    for cid, c in cells.items():
        meta = c["meta"]
        key = (meta["family"], tuple(meta["dests"]), meta["source"], meta["mode"])
        floor = floors.get(key)
        md.append(f"## {cid}\n\n{meta['mode']}, source {meta['source']}, dest {'+'.join(meta['dests'])}, "
                  f"parallelism {meta['par']}, payload {meta['payload']} B, A/A floor "
                  f"{'%.2f%%' % (floor * 100) if floor is not None else 'none'}\n")
        md.append("| arm | metric | median | min - max | spread | 95% CI | CPU-s/M ev | peak RSS MB | startup s | $/B ev |")
        md.append("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
        for arm, a in c["arms"].items():
            s = a["summary"]
            g = lambda k: (a.get(k) or {}).get("median")
            fmt = lambda v, p=0: "-" if v is None else f"{v:,.{p}f}"
            md.append(f"| {arm} | {a['metric']} | {fmt(s and s['median'])} | "
                      f"{fmt(s and s['min'])} - {fmt(s and s['max'])} | {fmt(s and s['spread_pct'], 1)}% | "
                      f"{('%s - %s' % (fmt(s['ci95'][0]), fmt(s['ci95'][1]))) if s and s['ci95'] else '-'} | "
                      f"{fmt(g('cpu_s_per_m_events'), 1)} | {fmt(g('peak_rss_mb'))} | {fmt(g('startup_s'), 1)} | {fmt(g('usd_per_billion'), 2)} |")
            if a["invalid_runs"]:
                md.append(f"|  | invalid runs: {', '.join(a['invalid_runs'])} | | | | | | | | |")
        c["verdicts"] = {}
        for arm in c["arms"]:
            if not arm.startswith("conduit-") or arm.endswith("-aa") or arm.startswith("conduit-classic"):
                continue
            tier = arm.split("-")[-1]
            for other in c["arms"]:
                if other.startswith("conduit") or not other.endswith("-" + tier):
                    continue
                v = verdict([r for r in c["runs"] if r.get("arm") == arm], [r for r in c["runs"] if r.get("arm") == other], floor)
                c["verdicts"][f"{arm} vs {other}"] = v
        if c["verdicts"]:
            md.append("\n| pair | median ratio | 95% CI | rounds same sign | verdict |\n| --- | --- | --- | --- | --- |")
            for pair, v in c["verdicts"].items():
                ci = v.get("ci95")
                md.append(f"| {pair} | {v.get('median_ratio', float('nan')):.3f} | "
                          f"{'%.3f - %.3f' % ci if ci else '-'} | {v.get('same_sign_rounds', '-')} | {v['verdict']} |")
        if meta["mode"] in ("correct", "recover"):
            md.append("\n| run | sink | ok | missing | duplicates | malformed | reordered | resume s |\n| --- | --- | --- | --- | --- | --- | --- | --- |")
            for r in c["runs"]:
                for sink, v in (r.get("verify") or {}).items():
                    md.append(f"| {r['run']} | {sink} | {v.get('ok')} | {json.dumps(v.get('missing'))} | "
                              f"{json.dumps(v.get('duplicates'))} | {v.get('malformed')} | {json.dumps(v.get('reordered'))} | "
                              f"{r.get('resume_s', '-')} |")
        md.append("")
    with open(os.path.join(root, "analysis.json"), "w") as f:
        json.dump(cells, f, indent=1, default=str)
    return "\n".join(md)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    if "--price" in sys.argv:
        PRICE = float(sys.argv[sys.argv.index("--price") + 1])
    print(report(sys.argv[1]))
