import csv, glob, os, statistics as st, sys
R=sys.argv[1]
def load(d):
    by={}
    for r in csv.DictReader(open(d+'/runs.csv')):
        by.setdefault(r['arm'],{'a':{},'b':{}})[r['rep']][int(r['round'])]=int(r['rate'])
    return by
def fl(by,n):
    ds=[abs(by[n]['b'][r]-by[n]['a'][r])/((by[n]['a'][r]+by[n]['b'][r])/2)*100 for r in by[n]['a'] if by[n]['a'][r] and by[n]['b'].get(r)]
    return max(ds)
def med(by,n): return st.median([x for k in 'ab' for x in by[n][k].values()])
def per(by,x,y):
    o=[]
    for r in sorted(by[x]['a']):
        mx=(by[x]['a'][r]+by[x]['b'][r])/2; my=(by[y]['a'][r]+by[y]['b'][r])/2
        o.append((mx-my)/my*100)
    return o
PAIRS=[('v2-main','v1'),('v2-proto','v1'),('v2-proto','v2-main'),('v2-altA','v1'),('v2-altA','v2-main'),('v2-proto','v2-altA')]
print('| session | comparison | delta (median to median) | per-round range | A/A floors (x / y) | exceeds both floors |')
print('|---|---|---|---|---|---|')
for d in sorted(glob.glob(R+'/*/')):
    by=load(d); name=os.path.basename(d.rstrip('/'))
    for x,y in PAIRS:
        if x not in by or y not in by: continue
        dl=(med(by,x)-med(by,y))/med(by,y)*100; p=per(by,x,y)
        fx,fy=fl(by,x),fl(by,y)
        ex=abs(dl)>max(fx,fy)
        sign=all(v>0 for v in p) or all(v<0 for v in p)
        print(f'| {name} | {x} vs {y} | {dl:+.1f}% | {min(p):+.1f}% to {max(p):+.1f}% | {fx:.1f}% / {fy:.1f}% | {"yes" if ex else "NO"}{"" if sign else " (per-round sign flips)"} |')
