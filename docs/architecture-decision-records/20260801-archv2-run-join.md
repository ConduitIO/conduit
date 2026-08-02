# arch-v2 fan-out: defer dispatch of a straddling split run instead of joining it after the fact

## Summary

A split run whose pieces straddle `funnel.Worker`'s multi-destination fan-out point is refused today
with `CodeSplitRunStraddlesFanOut` — correct, but fatal for the pipeline on an ordinary, recoverable
condition (a pre-fan-out processor returning fewer records than it received, e.g. a rate-limited
embedder). `#2731`'s merged review considered and explicitly rejected joining a run's tally across
fan-out branches, for lack of a concrete use case (`gh pr view 2731`, "Not adopted"). This ADR
**reverses that call**: a concrete flagship shape (a dual-write RAG pipeline: chunk → embed → two
vector destinations) now exists, and joining the run is worth building. Among the three designs
considered — a per-pass registry, extending `multiAckNacker` to arbitrate across branches, and
deferring dispatch until a straddling run is whole — **defer-the-fan-out** is adopted:
`Worker.doNextTask` buffers a fan-out-bound group that belongs to an incomplete run, inside the same
single-goroutine pass that already produces it, and dispatches once the run is whole. No cross-call
registry and no new shared mutable state visible to concurrently-running branches. **Tier 1** — data
path.

Two corrections to this ADR's first draft, both found by adversarial review before merge and both
material enough to state here rather than bury: `validateRunsWholeBeforeFanOut`
(`pkg/lifecycle-poc/funnel/run_ledger.go:341-369`) does **not** require zero changes — it must be
relaxed, or it rejects the very batch this design produces — and the design needs an end-of-pass
reconciliation step, without which members that go terminal by a path that bypasses `doNextTask`
strand their siblings and silently skip a source position. Both are specified in the companion
design doc.

## Context

`docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md` established `multiAckNacker`'s
per-position unanimity/nack-wins tally for `funnel.Worker`'s M-destination fan-out.
`docs/design-documents/20260801-archv2-split-run-ack-ledger.md` (merged as #2731, after two rejected
prior attempts, #2725 and #2727) then closed a related but distinct bug — a split run's head could be
acked before its tail was delivered — by inserting `runAckNacker`, a per-run completion ledger, between
`Worker.doTask` and whatever it would otherwise ack/nack through. That design deliberately left one
shape unfixed: a run whose pieces are split across the fan-out boundary itself, where `Batch.clone`
(necessarily, since branches diverge and must not share state) gives each destination branch its own
copy of the run's completion ledger, carrying the run's whole-member count but not necessarily all of
its members. Two disjoint tallies result, with no join point, so the run's position would be withheld
forever while later positions ack past it — silently skipping the record on restart. `#2731` closes
this by refusing the shape loudly instead: `validateRunsWholeBeforeFanOut` fails fast with
`CodeSplitRunStraddlesFanOut`, restoring the loud-failure behavior that predates the ledger.

`#2731`'s adversarial review confirmed this refusal was the right call for that PR and explicitly
declined two richer alternatives offered during review — a per-pass registry and extending
`multiAckNacker` to be the join point — calling joining a run's tally across branches "speculative
generality" without a use case for the shape (`gh pr view 2731`, review comment, "Not adopted"
section).

### What changed since #2731

Nothing about the code changed the calculus. What changed is that a concrete, reachable, flagship
pipeline shape is now **anticipated** — deliberately not "already shipping", which an earlier draft
of this ADR claimed. The checked-in
`cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/pipeline.yaml` has exactly **one**
destination and no fan-out, so the shape below is the extension this design targets, not an observed
production failure. `cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/pipeline.yaml`
chunks each row's text (`ai.chunk`) and embeds each chunk (`ai.embed`, rate-limited against a real
embedding provider) before writing to a vector destination. Extend that template to write to a second
destination (a backup vector store, or an analytics sink alongside pgvector — a dual-write shape, not
a hypothetical one) and the embedder's ordinary rate-limit backoff — returning fewer chunks than it
received on a given call — now takes the whole pipeline down via `CodeSplitRunStraddlesFanOut`,
every time.

Stated plainly, because the distinction is what justifies reversing #2731: the _mechanism_ is
demonstrated (the refusal is real, deterministic, and reachable the moment a second destination is
added), while the _pipeline_ is anticipated. That is a weaker premise than "it is happening today",
and it is the honest one. Landing the two-destination RAG template would convert it to observed;
until then this ADR rests on an anticipated shape, and a future reader should weigh it accordingly.
DeVaris reviewed this concrete case and approved building the join, choosing among the options
below.

## Decision

Adopt **defer-the-fan-out**: `Worker.doNextTask`'s multi-destination branch buffers a group destined
for the fan-out when it belongs to a currently-incomplete split run, instead of dispatching it and
relying on a later join. The buffer — this ADR calls it the fan-out stage, to keep it visibly distinct
from `runAckNacker`'s existing, differently-scoped completion ledger — accumulates a run's pieces
across however many `Ack`/`Filter` groups it takes, and `doNextTask`'s existing clone/`multiAckNacker`
path runs, unmodified, the moment the run is whole. See the companion design doc,
`docs/design-documents/20260801-archv2-run-join-defer-fanout.md`, for the full mechanism, the
prefix-scan discipline that keeps this correct under an adversarial (malformed-plugin) input shape,
and the failure-mode analysis.

## Alternatives considered

### A cross-branch registry shared by concurrent branches (offered, rejected in #2731; reconsidered and rejected again here)

Hoist run-completion tracking out of the per-branch `Batch.runs` ledger into a registry keyed by
run identity, shared across all branches of a fan-out and consulted by each branch's `Ack`/`Nack`
calls to decide when a run's original position is actually releasable.

**Rejected.** This is precisely the shape of shared mutable state visible to concurrently-running
branches that `#2731`'s review flagged as reintroducing the class of race that PR train was working to
close. (A related concurrency defect in the fan-out path, H2 — `worker.go`'s
`sharedBoundary`/`poisoned` mechanism — was found by the adversarial review of **#2734**, not #2731;
`worker.go` names #2734 at each of its three enforcement sites, and #2734 merged four hours later.)
A registry consulted from M concurrently-running goroutines needs its own locking discipline, its
own proof that a vote
from one branch can never be misattributed to another branch's clone of the same run, and its own
crash-safety argument for state that now spans the fan-out boundary instead of staying within one
branch's already-reviewed `runAckNacker`. None of that risk buys anything defer-the-fan-out doesn't
already get for free: by construction, the run is never split across branches in the first place, so
there is nothing to arbitrate concurrently. Paying for concurrent-safe joins to solve a problem that
can be avoided by not creating the concurrency in the first place is exactly the kind of complexity
CLAUDE.md's "no speculative generality" and "simplicity is the review criterion" call out.

### Extend `multiAckNacker` to arbitrate across branches (offered, rejected in #2731; reconsidered and rejected again here)

Make `multiAckNacker` (`pkg/lifecycle-poc/funnel/worker.go:1121-1444`) aware of split-run structure
directly, so its existing per-position tally could also resolve a run whose pieces are split
unevenly across branches — e.g. by tracking sub-run membership per branch and only counting a
position "voted" once a branch's own partial run-share is internally consistent.

**Rejected**, for a reason distinct from (and additional to) the registry option's: `multiAckNacker`'s
entire design, per its own ADR (`docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md`,
"Consequences") and its type doc comment (`worker.go:1121-1177`), is deliberately scoped to per-position
unanimity **across already-collapsed original positions** — it never sees split-run structure at all,
by design, because `originalBatch()` collapses a run to one position before `multiAckNacker` ever looks
at it. Teaching it about run structure would break that separation of concerns for every fan-out
pipeline, including the overwhelming majority that never split a record before reaching it, in service
of a case (straddling runs) that defer-the-fan-out can eliminate entirely upstream.

(An earlier draft added a second reason — that `multiAckNacker` "would need essentially the same
buffering/prefix-scan logic defer-the-fan-out already needs". That is inaccurate: it _already has_
that logic, in `releaseLocked` (`worker.go:1390-1420`), which the companion design doc explicitly
cites as the prior art the prefix-scan rule is copied from. The reason above — that it never sees
split-run structure at all, by design, because `originalBatch()` collapses a run before it looks —
is the one that actually holds.)

### Per-branch recount: complete against members actually received (recorded previously, evaluated here for the first time)

`20260801-archv2-split-run-ack-ledger.md`'s Limits section named **two** join options, not one:
hoist the ledger into a per-pass registry, _or_ "have each branch complete against the member count
it actually received rather than `run.total`". An earlier draft of this ADR claimed "three designs
considered" while silently substituting "extend `multiAckNacker`" for the second — so the option
with arguably the smallest diff was never written down or refuted. Correcting that here.

The idea: at fan-out, set each branch clone's effective total to the number of members that branch
was actually handed, so a branch completes on its own share. The parent separately accounts for the
members that never reached a branch, and the original position is released once every share is in.

**Rejected**, but on the merits rather than by omission. It needs a rendezvous anyway — something has
to know when _all_ shares are accounted for, which is the same join this ADR is choosing how to
build, only now spread across M concurrently-running branches plus a parent, instead of confined to
one pre-fan-out goroutine. That rendezvous carries every hazard the registry option does (vote
attribution, locking discipline, a crash-safety argument spanning the fan-out boundary) while also
mutating `splitRun.total` — a field whose "live counter that only grows while a member is still
active" contract (`run_ledger.go:71-82`) is load-bearing for the ack path and is currently
maintained in exactly one place, `SplitRecord`. Rewriting it per branch would make that contract
conditional on which side of a fan-out you are reading it from.

It is worth recording that this option does sidestep the buffering hazards the adopted design has to
handle explicitly — memory footprint, and the end-of-pass reconciliation. If defer-the-fan-out's
buffer bound turns out to be a real problem at production batch sizes (an open question in the
companion design doc), this is the alternative to revisit first.

### Defer-the-fan-out (adopted)

Buffer a straddling group **before** it is ever dispatched to a branch, in the single goroutine that
already owns the batch up to that point, and dispatch once. Argued in full in the companion design
doc; summarized here as the deciding factors against the two alternatives above:

- **No cross-branch concurrency to arbitrate, ever.** A run is never split across branches under this
  design, so there is no vote-attribution problem, no new locking discipline, and no new
  crash-safety argument beyond "buffered, in-memory, per-goroutine state disappears on crash exactly
  like every other not-yet-acked thing already does."
- **`validateRunsWholeBeforeFanOut` survives as a backstop, relaxed rather than replaced.** The
  alternatives above would need to replace it or teach it about partial-branch state. Defer needs a
  one-term change — `present + terminalCount >= total` instead of `present >= total` — after which it
  keeps doing its real job of catching a genuinely fragmented run. (An earlier draft claimed it needed
  _zero_ changes and stayed "trivially true by construction". That was wrong in the worst direction:
  unrelaxed, it rejects exactly the batch this design produces.)
- **Cost is honestly ack-latency-neutral, not merely "acceptable."** Within one run, a fast-resolving
  piece now waits behind a slower sibling before reaching a destination, losing some destination-write
  overlap. But the run's original source position could never be acked until every member is terminal
  regardless — that is `runAckNacker`'s existing, unmodified guarantee (`run_ledger.go:34-44`,
  `197-215`) — so the source-visible ack latency for this record is unchanged. What is lost is
  intra-run write parallelism for the delayed piece, not correctness or overall pipeline throughput
  outside of split-run pipelines (which are unaffected, since the fan-out stage only ever engages when
  `Batch.runs` is non-nil for the group in question).

## Consequences

- **No new durable state.** Like `multiAckNacker` and `runAckNacker` before it, the fan-out stage is
  entirely in-memory, rebuilt fresh (empty) on every batch read and every restart. This decision has no
  serialization format of its own and no upgrade/migration obligation.
- **A new fatal error code, `CodeRunAbandonedAtFanOut`.** The end-of-pass reconciliation fails
  fatally if anything is still buffered when a pass ends. Fatal is deliberate: a non-fatal worker
  error goes to the recovery arm, whose default `MaxRetries` is infinite, so a deterministic stranded
  buffer would restart-loop forever and re-write the DLQ every iteration. This is the same reasoning
  `DLQ.Nack` already applies to its own write failure.
- **`CodeSplitRunStraddlesFanOut` remains defined and reachable**, now exclusively as a defense-in-depth
  backstop for a bug in the fan-out stage's own bookkeeping, not as an expected operator-facing failure
  mode for rate-limited or otherwise partial-output pre-fan-out processors. Its doc comment
  (`pkg/lifecycle-poc/funnel/codes.go:62-82`) should be revisited when this ships to reflect that the
  shape it describes is no longer the common trigger. Note it is **not** currently a `FatalError`
  (a bare `conduiterr.New`, never wrapped), so today it drives a recovery crash-loop rather than the
  tomb an earlier draft described; once it is a bug backstop rather than an operator-facing
  condition, making it fatal should be decided at the same time.
- **Termination is bounded by the existing, unmodified `#2726`/`#2732` mechanism**
  (`maxRetryStall`/`maxRetryAttempts`/`CodeRetryNotConverging`), not by anything new this decision
  introduces. A genuinely non-convergent processor still fails the pipeline; a merely rate-limited one
  no longer does.
- **Ordering discipline is copied, not reinvented.** The fan-out stage's prefix-scan rule is the same
  "never release past the first thing that isn't ready" discipline `multiAckNacker.releaseLocked`
  already implements and has already been reviewed for (`worker.go:1390-1420`). Applying a second,
  independently-argued instance of the same rule one layer earlier is a deliberate, boring choice over
  inventing new ordering machinery.
- **Extending this to N sources, or to a second (nested) fan-out point, is out of scope.** Like the
  ADR this one is a companion to, this decision covers one source's single fan-out axis. A future
  N-source or nested-fan-out slice would need its own decision about whether/how the fan-out stage's
  per-scope creation generalizes — flagged as an open question in the companion design doc, not
  resolved here.

## Related

- `docs/design-documents/20260801-archv2-run-join-defer-fanout.md` — the full mechanism, constraint
  tracing, failure modes, and test plan for this decision.
- `docs/design-documents/20260801-archv2-split-run-ack-ledger.md` — the run-completion ledger
  (`runAckNacker`/`splitRun`) this decision builds alongside, and whose "Limits" section first named
  the gap this ADR closes. Not edited in place, per repo convention (ADRs and the design docs they
  reference are immutable once merged) — this ADR supersedes only its "Not adopted" framing of the
  fan-out-straddle case, not any other part of that design.
- `docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md` — the per-position
  unanimity/nack-wins model `multiAckNacker` implements, unmodified by this decision and what
  everything downstream of a reconstructed complete run continues to rely on.
- `pkg/lifecycle-poc/funnel/worker.go`, `run_ledger.go` — `doNextTask`, `validateRunsWholeBeforeFanOut`,
  `multiAckNacker`, `runAckNacker`.
- `cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/` — the flagship shape motivating this
  reversal.
- #2723, #2725, #2726, #2727, #2730, #2731 — the issue/PR history this decision continues.
