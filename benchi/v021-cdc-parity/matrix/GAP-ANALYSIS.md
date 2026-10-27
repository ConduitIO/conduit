# Gap analysis (to be written from the matrix results)

**Status: empty. No matrix data exists yet.** The method is fixed now, before
the data, so the analysis cannot be fitted to what the numbers turn out to
be.

For every cell where a Conduit arm is RESOLVED or SUGGESTIVE behind another
side at the same tier (PLAN.md section 9), one entry:

```
### <cell> / <tier>: Conduit <ratio>x of <side> (verdict, A/A floor, CI)

Bottleneck label: the per-run classifier (SUT CPU-bound / SUT waiting on X /
loader-bound), median over the rounds, with the busiest infrastructure
container's cores.
Where: the stage that saturates, from the diag run's metrics series
(poll-metrics.jsonl: source read, processor, destination write, ack) and its
execution trace (trace-window.out: what each stage's goroutines wait on).
Profile, window only: Conduit's cpu-window.pprof, mutex and block (end minus
start), next to the other side's cpu-window and wall-window collapsed stacks
and its Jolokia series (queue capacity, batch sizes) for the same cell.
Cause: one sentence, backed by the two above. A cause not backed by a profile
or a metric is marked GUESS.
Candidate fix (arch-v2 only): the change, the file and function it touches.
Estimated gain: from the profile share of the removed work, marked as an
estimate.
Experiment: the A/B run (an id, a branch, the cell it reruns) that confirms or
refutes the gain. No gain is claimed without one.
```

Also reported for every cell, win or lose: CPU-seconds per million events
and peak RSS against the other sides (efficiency gaps can exist at equal
throughput), and any correctness or recovery finding.
