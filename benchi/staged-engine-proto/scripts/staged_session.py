#!/usr/bin/env python3
"""
staged_session.py: a 3-arm throughput session in the method of the archv2-gate
harness (benchi/archv2-gate, PR #2956), for the staged-engine prototype.

Same method as the harness: one fresh container per run, records counted at the
sink (newlines in the byte range written inside the window), warmup discarded,
medians and spread, an A/A pair per arm in every round. Differences, all
forced by comparing THREE arms across TWO images:

  * the harness takes one image and two engines; this takes two binaries
    (main, prototype) and three arms (v1 = main binary, arch-v2 off;
    v2-main = main binary, arch-v2 on; v2-proto = prototype binary, arch-v2 on);
  * each round runs every arm twice in a palindrome (a b c c b a), so a linear
    drift across the round lands equally on both runs of each arm, and the two
    runs of one arm are that arm's A/A pair;
  * the Conduit process runs in a plain alpine container with the binary
    mounted, not in the harness's image (same Linux, same kernel, same sink on
    a docker volume).

LOCAL, INDICATIVE, NOT GATE EVIDENCE: Docker Desktop VM on a shared laptop.

usage: staged_session.py --shape 1x1 --main BIN --proto BIN --out DIR
         [--rounds 5] [--warmup 10] [--window 30] [--env KEY=VAL ...]
"""
import argparse, csv, json, os, re, statistics, subprocess, sys, time

ARMS = [("v1", "main", False), ("v2-main", "main", True), ("v2-proto", "proto", True)]


def run_once(here, binary, v2, shape, warmup, window, out, envs):
    env = dict(os.environ)
    env["EXTRA_ENVS"] = " ".join(f"-e {e}" for e in envs)
    p = subprocess.run(
        [os.path.join(here, "run_docker.sh"), binary, "v2" if v2 else "v1", shape, out, str(warmup), str(window)],
        capture_output=True, text=True, env=env)
    m = re.search(r"^(\d+) rec/s/sink\s+\((\d+) recs over ([\d.]+)s, (\d+) sinks\)\s+cores_used=([\d.]+) vmload=(\S+ \S+ \S+) host_after=(.*)$", p.stdout, re.M)
    if not m:
        return None, p.stdout + p.stderr
    return dict(rate=int(m.group(1)), recs=int(m.group(2)), secs=float(m.group(3)), sinks=int(m.group(4)),
                cores=float(m.group(5)), vmload=m.group(6), hostload=m.group(7).strip()), ""


def resummarize(d):
    rows = list(csv.DictReader(open(os.path.join(d, "runs.csv"))))
    for r in rows:
        r["round"] = int(r["round"]); r["rate"] = int(r["rate"]); r["cores"] = float(r["cores"])
    env = json.load(open(os.path.join(d, "env.json")))
    class A: pass
    a = A(); a.shape = env["shape"]; a.rounds = env["rounds"]; a.warmup = env["warmup"]; a.window = env["window"]; a.env = env["env"]
    open(os.path.join(d, "summary.md"), "w").write(summarize(rows, a))


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "--resummarize":
        resummarize(sys.argv[2]); return
    ap = argparse.ArgumentParser()
    ap.add_argument("--shape", required=True)
    ap.add_argument("--main", required=True)
    ap.add_argument("--proto", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--rounds", type=int, default=5)
    ap.add_argument("--warmup", type=int, default=10)
    ap.add_argument("--window", type=int, default=30)
    ap.add_argument("--env", action="append", default=[])
    ap.add_argument("--note", default="")
    a = ap.parse_args()
    here = os.path.dirname(os.path.abspath(__file__))
    os.makedirs(a.out, exist_ok=True)
    bins = {"main": os.path.abspath(a.main), "proto": os.path.abspath(a.proto)}
    rows = []
    csvp = os.path.join(a.out, "runs.csv")
    for r in range(1, a.rounds + 1):
        # rotate which arm leads, palindrome within the round
        order = ARMS[(r - 1) % 3:] + ARMS[:(r - 1) % 3]
        seq = [(arm, "a") for arm in order] + [(arm, "b") for arm in reversed(order)]
        for (name, img, v2), ab in seq:
            t0 = time.time()
            res, err = run_once(here, bins[img], v2, a.shape, a.warmup, a.window, os.path.join(a.out, "tmp"), a.env)
            if res is None:
                print(f"round {r} {name}-{ab}: FAILED\n{err[-400:]}", file=sys.stderr)
                res = dict(rate=0, recs=0, secs=0, sinks=0, cores=0, vmload="", hostload="")
            res.update(round=r, arm=name, rep=ab)
            rows.append(res)
            print(f"round {r} {name}-{ab}: {res['rate']} rec/s/sink cores={res['cores']} host={res['hostload']}", file=sys.stderr, flush=True)
            with open(csvp, "w", newline="") as f:
                w = csv.DictWriter(f, fieldnames=["round", "arm", "rep", "rate", "recs", "secs", "sinks", "cores", "vmload", "hostload"])
                w.writeheader(); w.writerows(rows)
    json.dump(dict(shape=a.shape, rounds=a.rounds, warmup=a.warmup, window=a.window, env=a.env, note=a.note,
                   main=a.main, proto=a.proto, started=time.strftime("%FT%TZ", time.gmtime())),
              open(os.path.join(a.out, "env.json"), "w"), indent=2)
    open(os.path.join(a.out, "summary.md"), "w").write(summarize(rows, a))
    print(open(os.path.join(a.out, "summary.md")).read())


def summarize(rows, a):
    names = [n for n, _, _ in ARMS]
    by = {n: {"a": {}, "b": {}} for n in names}
    for r in rows:
        by[r["arm"]][r["rep"]][r["round"]] = r["rate"]
    out = [f"# staged-engine local session: shape {a.shape}\n",
           f"{a.rounds} rounds, {a.warmup}s warmup discarded, {a.window}s window, records counted at the sink,\n"
           f"every arm twice per round (palindrome).\n\n"
           f"**LOCAL, INDICATIVE, NOT GATE EVIDENCE** (Docker Desktop VM, shared laptop).\n"]
    if a.env:
        out.append("Env: " + ", ".join(f"`{e}`" for e in a.env) + "\n")
    out.append("| arm | n | median rec/s | sd % | min | max | cores used (median) | A/A floor (max abs delta) | A/A median abs delta | A/A per-round deltas (b vs a) |")
    out.append("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
    med = {}
    for n in names:
        vals = list(by[n]["a"].values()) + list(by[n]["b"].values())
        vals = [v for v in vals if v > 0]
        if not vals:
            continue
        m = statistics.median(vals)
        med[n] = m
        sd = statistics.pstdev(vals) / statistics.mean(vals) * 100
        signed = [(by[n]["b"][r] - by[n]["a"][r]) / ((by[n]["a"][r] + by[n]["b"][r]) / 2) * 100
                  for r in sorted(by[n]["a"]) if r in by[n]["b"] and by[n]["a"][r] and by[n]["b"][r]]
        deltas = [abs(d) for d in signed]
        cores = statistics.median([r["cores"] for r in rows if r["arm"] == n])
        out.append(f"| {n} | {len(vals)} | {m:.0f} | {sd:.1f} | {min(vals)} | {max(vals)} | {cores:.2f} | +/-{max(deltas) if deltas else float('nan'):.1f}% | {statistics.median(deltas) if deltas else float('nan'):.1f}% | " + ", ".join(f"{d:+.1f}%" for d in signed) + " |")
    out.append("")
    def pair(x, y):
        if x not in med or y not in med:
            return
        per = []
        for r in sorted(by[x]["a"]):
            try:
                mx = (by[x]["a"][r] + by[x]["b"][r]) / 2
                my = (by[y]["a"][r] + by[y]["b"][r]) / 2
                per.append((mx - my) / my * 100)
            except KeyError:
                pass
        out.append(f"- **{x} vs {y}**: median-to-median {((med[x]-med[y])/med[y]*100):+.1f}%; per-round "
                   + ", ".join(f"{d:+.1f}%" for d in per))
    out.append("## Comparisons\n")
    pair("v2-main", "v1"); pair("v2-proto", "v1"); pair("v2-proto", "v2-main")
    out.append("\nRead every delta against the arms' A/A floors above; a delta inside the floor is not\n"
               "resolved. Rates from different sessions are not comparable.\n")
    return "\n".join(out)


main()
