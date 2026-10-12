#!/usr/bin/env python3
# mkshape.py NAME SRC_BATCH SRC_DELAY DST_BATCH DST_DELAY [NSRC NDST]
import sys,os
name,sb,sd,db,dd=sys.argv[1:6]
ns=int(sys.argv[6]) if len(sys.argv)>6 else 1
nd=int(sys.argv[7]) if len(sys.argv)>7 else 1
out='version: "2.2"\npipelines:\n  - id: archv2-gate\n    status: running\n    name: archv2-gate\n    connectors:\n'
for i in range(1,ns+1):
    out+=f'''      - id: src-{i}
        type: source
        plugin: builtin:generator
        settings:
          format.type: structured
          format.options.id: int
          format.options.name: string
          operations: create
'''
    if sb!='0': out+=f'          sdk.batch.size: "{sb}"\n'
    if sd!='0': out+=f'          sdk.batch.delay: "{sd}"\n'
for i in range(1,nd+1):
    out+=f'''      - id: sink-{i}
        type: destination
        plugin: builtin:file
        settings:
          path: /sink/sink-{i}.jsonl
'''
    if db!='0': out+=f'          sdk.batch.size: "{db}"\n'
    if dd!='0': out+=f'          sdk.batch.delay: "{dd}"\n'
os.makedirs(f'shapes/{name}',exist_ok=True)
open(f'shapes/{name}/pipeline.yml','w').write(out)
