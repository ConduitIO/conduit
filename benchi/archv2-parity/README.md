# benchi: arch-v2 vs v1 throughput — the v0.20 default-flip gate

Reference-pipeline benchmark comparing the classic v1 engine (`pkg/lifecycle`,
today's default) against arch-v2 (`pkg/lifecycle-poc`, behind
`--preview.pipeline-arch-v2`).

**The gate, agreed before any number existed:** a >10% regression on _default_
config means the flip does not ship in v0.20. Pre-committing it was deliberate,
so the number arrived with a meaning already attached instead of being argued
about afterwards.

## Result: gate cleared — v2 is faster, by single-digit percent

Three full runs, 60s steady state each, 59 samples per engine per run.

| run | v1 median msg/s | v2 median msg/s | delta | v1 sd | v2 sd |
| --- | --- | --- | --- | --- | --- |
| 1 | 23,547 | 25,589 | **+8.7%** | 11.3% | 8.6% |
| 2 | 24,628 | 25,244 | **+2.5%** | 11.7% | 8.7% |
| 3 | 23,697 | 25,425 | **+7.3%** | 10.3% | 8.1% |

- Delta across runs: median **+7.3%**, range +2.5% to +8.7%. v2 faster in 3/3.
- Pooled (n=177 per engine): v1 median 23,758 · v2 median 25,402 · **+6.9%**.
- Permutation test on pooled samples, one-sided, 20,000 resamples: **p < 0.0001**.
- CPU: v2 ~133% vs v1 ~151% (v2 lower). Memory: v2 ~128 MB vs v1 ~122 MB (v2 higher).

Single-run variance is high (sd 8–12% of median), which is why one run was not
enough: in run 1 alone the distributions overlapped so heavily that v1's best
sample beat v2's median. The repeats and the permutation test are what make the
direction trustworthy; the _magnitude_ should still be read as "single-digit
percent," not as +6.9% precisely.

## Two things this result corrects

**The predicted regression did not happen.** The concern going in was that
`sdk.batch.size`/`batch.delay` default to 0, so the SDK calls `readFn(ctx, 1)`
and arch-v2 reads batches of ONE — while the fan-out ADR's ~6.3x figure was
measured on a mocked 1000-record batch. Batch-of-one is real, but on this
pipeline it does not translate into a throughput penalty.

**It is nowhere near 6.3x.** Whatever that ADR measured, a real pipeline on
default config shows single-digit percent. The 6.3x figure should not be quoted
as what users get.

## Honest scope

- One source, one destination, `builtin:generator` -> `builtin:log`, no external
  infrastructure — isolates the engine hot path, and is the _default-config_
  gate. It is **not** the N×M shape, which remains unmeasured.
- Run on a developer laptop under Docker Desktop, not dedicated hardware.
  Absolute numbers are environment-specific; the v1-vs-v2 _ratio_ is the claim,
  and both tools ran on the same machine, same image, back to back.
- Metrics are ON for the v2 run (no `--preview.pipeline-arch-v2-disable-metrics`).
  The flip ships with metrics on, so measuring with them off would describe
  something users do not get.

## Reproducing

```bash
docker build -t conduit-bench:archv2-parity .
docker network create benchi
cd benchi/archv2-parity
benchi -config bench.yml -out ./results/<name>
```

Notes for anyone re-running:

- benchi runs `docker compose pull` before each tool, which fails on a
  locally-built image — hence `pull_policy: never` in both compose files.
- benchi requires a TTY. Under a non-interactive shell, wrap it:
  `script -q /dev/null benchi -config bench.yml -out ./results/<name>`.
- Both tools use the SAME image and the SAME `pipeline.yml`. The only
  difference is the `--preview.pipeline-arch-v2` flag in the v2 compose file,
  which is what makes this an engine comparison rather than a pipeline one.
