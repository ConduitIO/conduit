#!/usr/bin/env python3
"""health.py RESULTS_DIR : per-session host health (vmstat steal/iowait, loadavg) over the runs' windows."""
import csv, datetime as dt, glob, os, re, statistics, sys

R = sys.argv[1]


def ts(s):
    return dt.datetime.strptime(s, "%Y-%m-%d %H:%M:%S").replace(tzinfo=dt.timezone.utc).timestamp()


# columns: ... us sy id wa st <date> <time>  => st=f[-3], wa=f[-4], id=f[-5], sy=f[-6], us=f[-7]
vm = []
for line in open(os.path.join(R, "vmstat.log"), errors="replace"):
    f = line.split()
    if len(f) >= 19 and re.match(r"\d{4}-\d\d-\d\d", f[-2]):
        try:
            vm.append((ts(f[-2] + " " + f[-1]), int(f[-3]), int(f[-4]), int(f[-7]), int(f[-6])))
        except ValueError:
            pass
la = []
for line in open(os.path.join(R, "loadavg.log"), errors="replace"):
    f = line.split()
    if len(f) >= 4:
        try:
            la.append((dt.datetime.strptime(f[0], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc).timestamp(), float(f[1])))
        except ValueError:
            pass

print("| session | runs | vmstat samples | steal % mean / p95 / max | iowait % mean / p95 / max | loadavg(1m) mean / max |")
print("| --- | --- | --- | --- | --- | --- |")


def p95(v):
    v = sorted(v)
    return v[min(len(v) - 1, int(len(v) * 0.95))]


tot_st, tot_wa = [], []
for d in sorted(glob.glob(os.path.join(R, "results", "*/"))):
    c = os.path.join(d, "runs.csv")
    if not os.path.exists(c):
        continue
    rows = list(csv.DictReader(open(c)))
    if not rows or "t_start" not in rows[0]:
        continue
    t0 = min(float(r["t_start"]) for r in rows)
    t1 = max(float(r["t_end"]) for r in rows)
    sel = [x for x in vm if t0 <= x[0] <= t1]
    ll = [x[1] for x in la if t0 <= x[0] <= t1]
    if not sel:
        continue
    st = [x[1] for x in sel]; wa = [x[2] for x in sel]
    tot_st += st; tot_wa += wa
    print(f"| {os.path.basename(d.rstrip('/'))} | {len(rows)} | {len(sel)} | {statistics.mean(st):.2f} / {p95(st)} / {max(st)} | "
          f"{statistics.mean(wa):.2f} / {p95(wa)} / {max(wa)} | {statistics.mean(ll):.2f} / {max(ll):.2f} |")
if tot_st:
    print(f"| **all sessions** | | {len(tot_st)} | {statistics.mean(tot_st):.2f} / {p95(tot_st)} / {max(tot_st)} | {statistics.mean(tot_wa):.2f} / {p95(tot_wa)} / {max(tot_wa)} | |")
