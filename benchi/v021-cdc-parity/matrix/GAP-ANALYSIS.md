# Gap analysis (to be written from the matrix results)

**Status: empty. No matrix data exists yet.** The method is fixed now, before
the data, so the analysis cannot be fitted to what the numbers turn out to
be.

For every cell where a Conduit arm is RESOLVED or SUGGESTIVE behind another
side at the same tier (PLAN.md section 9), one entry:

```
### <cell> / <tier>: Conduit <ratio>x of <side> (verdict, A/A floor, CI)

Where: the stage that saturates, from the window's Conduit metrics (source
read, processor, destination write, ack histograms: metrics-window-*.txt) and
the SUT's cgroup CPU (saturated at 4 cores, or not: then it is waiting).
Profile: the top frames of Conduit's CPU and block profiles (diag-conduit-*),
next to the other side's async-profiler stacks for the same cell.
Cause: one sentence, backed by the two above. A cause not backed by a profile
or a metric is marked GUESS.
Candidate fix (arch-v2 only): the change, the file and function it touches.
Estimated gain: from the profile share of the removed work, marked as an
estimate, with the experiment that would confirm it.
```

Also reported for every cell, win or lose: CPU-seconds per million events
and peak RSS against the other sides (efficiency gaps can exist at equal
throughput), and any correctness or recovery finding.
