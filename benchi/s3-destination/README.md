# S3 destination baseline

Roadmap item: v0.21: S3 sink parity (#3009). The certification bar asks for a committed benchi baseline config
for every certified connector, and forbids performance claims without one. This is that config. **It has not
been run. There are no numbers here, and none may be quoted from it.**

## What it measures

`builtin:generator` (structured records) to `builtin:s3` (JSON lines, no partitioning, one object per 1000
records) to MinIO, 60 seconds. The destination's encoding and `PutObject` path are in the loop; network and disk
are those of the host running Docker.

## How to run it (and what a result must include)

Follow [`../METHODOLOGY.md`](../METHODOLOGY.md):

1. Ground truth: the `post-test` step lists the objects and bytes in the bucket. Records = objects × 1000 (the
   last object may be smaller). Do not use Conduit's `msg-rate-per-second` as the result.
2. 60s runs, alternated single runs when comparing two builds, and an A/A control (the same build against itself)
   in the same session. Report medians with the spread.
3. Record the host, Docker resources, and the image digests.

```sh
benchi -config benchi/s3-destination/bench.yml
```

To compare a connector branch, build Conduit with that connector version as the built-in, tag the image, and add
it as a second tool (see `../ack-hot-path/bench.yml` for the two-tool layout).

## Not covered

- Partitioned layouts (`partitioner: time` etc., `ConduitIO/conduit-connector-s3#1054`): a follow-up config once released.
- The S3 source.
- Real AWS S3: MinIO on the same host says nothing about S3 latency or throughput limits.
