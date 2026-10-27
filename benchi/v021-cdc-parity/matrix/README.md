# Parity matrix: Conduit vs Kafka Connect vs Debezium Server

`PLAN.md` is the pre-registration: every cell, metric, rule and hypothesis,
fixed before any data. This file says how to run it.

Nothing here runs on a laptop. Every run happens on a dedicated AWS instance;
locally you only compile, generate configs and run the unit tests.

## Pieces

| file | what |
| --- | --- |
| `cells.py` | the cell registry (the pre-registration in code) and the generator: `gen CELL DIR` writes a cell's benchi config, compose files, Conduit pipelines, Connect connector configs and Debezium Server properties. `list`, `table`, `estimate`, `shard` |
| `test_cells.py` | the fairness rules of PLAN.md checked against every generated config (resource parity, no tuning on the shipped tier, every side tuned on the tuned tier, acks=all and no exactly-once, equal parallelism, hooks that point at real containers) |
| `../loader/` | one static binary for the workload and the ground truth: `load` (deterministic Postgres workload with payload markers), `fill-kafka`, `fill-file`, `receive` (HTTP sink receiver), `sample` (time series), `verify` (record-by-record check), hook helpers |
| `run-cell.sh` | runs one cell on the current instance: warmup round, 5 rotated rounds, diag runs; recovery cells through compose directly |
| `run-shard.sh` | one instance's share of the matrix: builds and pulls the pinned images, computes cpusets, runs its cells, syncs results to S3 after each |
| `res_sampler.py`, `diag.py`, `runinfo.py`, `cpusets.py`, `envspec.py`, `meta.py` | per-run helpers: cgroup resources, profiles and metrics scrapes, hook timestamps, cpusets, environment record |
| `analyze.py` | every reported number, by PLAN.md's rules; `test_analyze.py` |
| `aws/` | Terraform plus `launch.sh`, `collect.sh`, `destroy.sh` |
| `images/` | the Connect worker (Debezium 3.7 + sinks) and the Conduit HTTP-plugin image |

## Run

```sh
# local, no Docker: tests and the estimate
(cd ../loader && go test -race ./...)
python3 -m unittest -v test_cells test_analyze
python3 cells.py estimate --phase 1 --price 0.848

# commit the kit (launch refuses uncommitted changes), then on AWS:
aws/launch.sh smoke-$(date +%Y%m%d) <conduit-sha> 2 1 c6i.4xlarge --smoke   # first: one short run per family
aws/launch.sh matrix-$(date +%Y%m%d) <conduit-sha> 2 1 c6i.4xlarge          # phase 1
aws/collect.sh <run-id> results-aws/<run-id>                                  # sync, merge, analyze
aws/destroy.sh <run-id>                                                       # after collecting
```

Progress: `s3://<bucket>/results/shard-<i>/status.txt` and `shard.log`. Each
instance terminates itself when done or at its cap (estimate + 25%).

One cell by hand, on an instance that ran `run-shard.sh`'s setup:

```sh
benchi/v021-cdc-parity/matrix/run-cell.sh A2-pg-kafka-p1-b64 /opt/matrix/results
```
