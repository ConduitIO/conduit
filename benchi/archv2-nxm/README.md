# benchi: arch-v2 vs v1 on N×M — the shape the parity benchmark could not see

> **⚠ The numbers in this file are RETRACTED.** They were produced with a metric
> that is not comparable between engines, and with an effect smaller than the
> harness's own A/A noise floor. See `benchi/METHODOLOGY.md` for what went wrong
> and what to do instead. The configs remain useful; the results do not.

Companion to `benchi/archv2-parity`. Same harness, same defaults, same image —
but **2 sources × 2 destinations** instead of 1×1.

This shape matters because it is where the two engines actually differ. v1 fans
in and out through `FaninNode`/`FanoutNode`; arch-v2 runs one `Worker` per
source and serializes them into a shared destination subtree behind a
per-shared-root mutex. A single-connector benchmark cannot observe that
contention at all, so `archv2-parity`'s +6.9% result says nothing about it.

## Result: gate cleared, but v2 IS slower here — the opposite direction to 1×1

Seven full runs, 60s steady state, 59 samples per engine per run.

| run | v1 median msg/s | v2 median msg/s | delta | v1 sd | v2 sd |
| --- | --- | --- | --- | --- | --- |
| 1 | 15,780 | 14,361 | −9.0% | 9.4% | 11.5% |
| 2 | 12,058 | 14,043 | **+16.5%** | 18.6% | 21.2% |
| 3 | 15,957 | 14,935 | −6.4% | 13.2% | 12.5% |
| 4 | 15,505 | 15,134 | −2.4% | 10.1% | 10.2% |
| 5 | 15,325 | 15,237 | −0.6% | 11.1% | 12.6% |
| 6 | 15,480 | 14,535 | −6.1% | 12.9% | 10.4% |
| 7 | 15,428 | 15,188 | −1.6% | 10.9% | 10.4% |

- v2 slower in **6 of 7** runs. Per-run delta median **−2.4%**.
- Pooled (n≈412 per engine): v1 15,258 · v2 14,742 · **−3.4%**.
- Two-sided permutation test, 20,000 resamples: **p = 0.0019** — the regression
  is real, not noise.
- Bootstrap 95% CI on the pooled delta: **[−5.9%, −0.1%]**.

**Against the >10% gate: cleared.** The entire confidence interval sits well
inside the threshold. But this is a genuine regression, not parity, and it
should be recorded as such rather than rounded to "no change".

## Why three runs were not enough, and why run 2 is kept

An earlier reading of the first three runs alone gave −9.0%, +16.5%, −6.4% —
median −6.4%, pooled −4.0%, p = 0.08, i.e. **inconclusive**. Reporting run 1's
−9.0% on its own would have been indistinguishable from cherry-picking.

Run 2 is the outlier and it is kept deliberately: v1 came in at 12,058 against
~15,300–16,000 everywhere else, with sd 18.6%, so that run's _baseline_ was
disturbed by something on the host. It is not discarded for being inconvenient —
with 7 runs it no longer drives the conclusion, and its presence is part of the
honest variance story.

N×M is materially noisier than 1×1 (sd 9–21% vs 8–12%), which is expected: two
workers contending on a shared destination subtree is more variable than one
linear path. That noise is exactly why a sub-10% effect needs many runs here.

## The combined picture for the flip

| shape | delta (v2 vs v1) | significance |
| --- | --- | --- |
| 1×1 (`archv2-parity`) | **+6.9%** (v2 faster) | p < 0.0001 |
| 2×2 (this) | **−3.4%** (v2 slower) | p = 0.0019, CI [−5.9%, −0.1%] |

Both inside the gate. Throughput does not block the v0.20 flip. The direction
flip between shapes is the interesting result: arch-v2 wins on the linear path
and gives some of it back under shared-sink contention.

## Honest scope

- Developer laptop under Docker Desktop, not dedicated hardware. Absolute
  numbers are environment-specific; the claim is the v1-vs-v2 ratio, measured
  back to back on the same machine and image.
- `builtin:generator` sources -> `builtin:log` destinations, no external
  infrastructure, so this isolates engine behaviour rather than connector I/O.
- Default config: `sdk.batch.size`/`batch.delay` unset, metrics ON for v2.
- 2×2 only. Wider fan-outs (higher M) would load the shared-root mutex harder
  and are not covered here.

## Reproducing

```bash
docker build -t conduit-bench:archv2-nxm .
docker network create benchi
cd benchi/archv2-nxm
script -q /dev/null benchi -config bench.yml -out ./results/<name>
```

`pull_policy: never` (locally built image) and the `script` TTY wrapper are both
required — see `benchi/archv2-parity/README.md`.
