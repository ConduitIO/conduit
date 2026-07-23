# benchi: Source.Ack persist-ordering fix — ack hot-path throughput

Reference-pipeline benchmark for the sev-0 fix
(`fix(engine): persist position before plugin ack — Approach A`,
`docs/design-documents/20260723-source-ack-persist-ordering-fix.md`). Compares
record throughput between `origin/main` (ack-before-persist, the bug) and this
branch (ack-follows-durable-flush, the fix) on a generator-source ->
log-destination pipeline with no external infrastructure, isolating the
engine's own record/ack hot path (`Source.Read` -> `Source.Ack` ->
`Persister.Persist` -> the deferred plugin-ack) from any downstream I/O.

## Status: config committed, not run in this sandbox

Per `CLAUDE.md`'s performance discipline ("claims require benchi runs,
committed configs, reproducible results" / "never assert numbers without a
benchmark in the repo"), stating this plainly: **this benchi config was
authored and committed, but the actual Docker-orchestrated run has not been
executed** — the sandbox this fix was developed in has the `benchi` binary
installable (`go install github.com/conduitio/benchi/cmd/benchi@latest`
succeeds) but no reachable Docker daemon (`docker info` fails with "no such
file or directory" on the daemon socket). Do not read the numbers below as a
benchi result; they are not.

## What was actually measured: a same-sandbox Go microbenchmark

As a substitute, immediately-reproducible signal — not a replacement for the
benchi run above, which is still required before this claim is considered
benchi-backed — `pkg/connector/source_bench_test.go`'s
`BenchmarkSource_Ack` measures `Source.Ack` in isolation (real
`connector.Source`/`connector.Persister`, real in-memory store, production
default thresholds), run three times at `-benchtime=20000x` on both this
branch and a temporary revert of just the ordering change (`Source.Ack`
sending the ack before `persister.Persist`, restored immediately after
measuring — see the fix PR description's "fails-without-fix" section, which
reuses the same revert):

```text
$ go test -run '^$' -bench '^BenchmarkSource_Ack$' -benchmem -benchtime=20000x -count=3 ./pkg/connector/...

# this branch (fix: ack deferred to post-flush callback)
BenchmarkSource_Ack-16    20000    642.0 ns/op    707 B/op    6 allocs/op
BenchmarkSource_Ack-16    20000    637.0 ns/op    707 B/op    6 allocs/op
BenchmarkSource_Ack-16    20000    739.0 ns/op    707 B/op    6 allocs/op

# temporarily reverted (pre-fix: synchronous stream.Send before persist)
BenchmarkSource_Ack-16    20000   1501.0 ns/op    743 B/op    8 allocs/op
BenchmarkSource_Ack-16    20000   1184.0 ns/op    742 B/op    8 allocs/op
BenchmarkSource_Ack-16    20000   1214.0 ns/op    742 B/op    8 allocs/op
```

**Direction, not magnitude, is the claim worth stating from this data:** the
fix's `Ack` call is faster per call, not slower, in this microbenchmark —
because the pre-fix code pays a synchronous stream `Send` (a channel
rendezvous with the plugin-side reader) on every single call, while the fix
only enqueues and registers with the already-existing debounced persister,
deferring the actual `Send` to the batched flush callback. This is
consistent with the design doc's own framing ("hot path unchanged, only ack
timing moves") but is a microbenchmark of one function, on one machine
(Apple M3 Max, `darwin/arm64`), against an in-memory store and an in-memory
stream with no plugin-side processing cost — it does not establish an
end-to-end pipeline throughput or latency number, and does not exercise the
cross-connector shared-batch blast radius the design doc names as inherited,
not introduced, by this fix. That end-to-end number is exactly what the
benchi run above is for, and it is what a >10% regression check would
actually gate on per `CLAUDE.md`'s benchmark-regression-gate discipline.

## Reproducing the benchi run

From the repo root, on a machine with a running Docker daemon:

```sh
# 1. Build the "main" (pre-fix) image from a clean checkout of origin/main.
git fetch origin main
git worktree add /tmp/conduit-bench-main origin/main
docker build -t conduit-bench:main /tmp/conduit-bench-main
git worktree remove /tmp/conduit-bench-main

# 2. Build the "fix" (this branch) image from the current checkout.
docker build -t conduit-bench:fix-source-ack .

# 3. Install benchi if not already installed.
go install github.com/conduitio/benchi/cmd/benchi@latest

# 4. Run both tool variants against the same pipeline/test definition.
benchi -config benchi/ack-hot-path/bench.yml

# 5. Inspect results/<timestamp>/aggregated-results.csv for the
#    conduit-main vs conduit-fix throughput comparison.
```

A >10% regression on `conduit-fix` vs `conduit-main` without a stated
justification would fail the benchmark-regression-gate discipline in
`CLAUDE.md` — given the microbenchmark direction above, none is expected, but
this run has not confirmed that end-to-end, and this doc does not claim
otherwise.
