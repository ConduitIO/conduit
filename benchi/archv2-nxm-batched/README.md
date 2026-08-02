# benchi: N×M with batching — the arch-v2 regression is a batch-size artifact

Same 2×2 pipeline as `benchi/archv2-nxm`, with one change: the sources set
`sdk.batch.size: "100"` and `sdk.batch.delay: "10ms"` instead of leaving them at
their zero defaults.

It exists to test a specific hypothesis about why arch-v2 was ~3% slower than v1
on N×M while being ~7% faster on 1×1: **arch-v2 serializes N workers into a
shared destination subtree behind a per-shared-root mutex, and at the shipped
defaults the SDK reads batches of ONE record — so that mutex is taken and
released once per record per destination.** v1 has no equivalent; it moves
records through channels between fan-in/fan-out nodes. If the hypothesis holds,
batching should amortize the lock and the regression should disappear.

## Result: it disappears, and then some

| N×M 2×2 | v1 median msg/s | v2 median msg/s | delta |
| --- | --- | --- | --- |
| default (batch of 1) — `benchi/archv2-nxm` | 15,258 | 14,742 | **−3.4%** |
| batched (size 100) — this config | 15,191 | 24,624 | **+62.1%** |

Per-run, batched: +60.3%, +74.6%, +60.9%. Pooled permutation test over 20,000
resamples: **p < 0.0001**.

**The decisive detail is that v1 does not move.** 15,258 unbatched vs 15,191
batched — statistically flat. v1 gets nothing from batching. arch-v2 goes from
14,742 to 24,624, **+67% on the same pipeline**.

So the N×M "regression" is not a property of arch-v2's design. It is the cost of
running arch-v2 in the one configuration that maximally penalises it — per-record
lock traffic with no amortisation — and that configuration is the current
default.

## What this changes about the flip decision

The honest summary across all three configs:

| shape | config | delta (v2 vs v1) |
| --- | --- | --- |
| 1×1 | default | **+6.9%** |
| 2×2 | default | **−3.4%** |
| 2×2 | batched | **+62.1%** |

arch-v2 is never meaningfully slower, and is dramatically faster the moment
records arrive in batches. The `−3.4%` is real but is a worst-case artifact, not
a ceiling.

## Options for closing the default-config gap

Not decided here — recorded so the choice is explicit:

1. **Amortise the shared-root lock inside the worker.** arch-v2 already holds a
   whole `Batch`; the mutex is acquired per `doTask` pass. If a pass can cover
   more records, the per-record cost falls without any user-visible change. This
   is the fix that helps users who never touch batch settings.
2. **Change the shipped `sdk.batch.size` default.** Highest leverage and lowest
   code risk, but it is an SDK-wide behavioural change affecting every connector
   and every engine, so it needs its own decision and its own compatibility
   analysis.
3. **Document it.** Tell arch-v2 users to set `sdk.batch.size`. Weakest option —
   defaults are what most pipelines actually run.

Option 1 is the one that makes the default path fast without asking anything of
users, and is the natural follow-up to this measurement.

## Honest scope

- Same environment caveats as `benchi/archv2-nxm`: laptop under Docker Desktop,
  `builtin:generator` -> `builtin:log`, no external infrastructure, metrics ON
  for v2. The claim is the v1-vs-v2 ratio, measured back to back.
- `sdk.batch.size: 100` was picked as a plainly-batched value, not tuned. The
  shape of the curve between 1 and 100 is not measured here.
- 2×2 only. Wider fan-outs load the shared-root mutex harder.
- Three runs. The effect is large enough (+60%) that three suffice; the −3.4%
  default-config result needed seven precisely because it was small.

## Reproducing

```bash
docker build -t conduit-bench:archv2-nxm-batched .
docker network create benchi
cd benchi/archv2-nxm-batched
script -q /dev/null benchi -config bench.yml -out ./results/<name>
```
