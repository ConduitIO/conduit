# arch-v2 fan-out: destination-only branches share records instead of deep-copying them

## Summary

`funnel.Worker.doNextTask` deep-copied every record of a batch once per destination branch. In
`BenchmarkEngineFanOut` that made two destinations cost 5.2x the per-record cost of one, and four
cost 8.9x (#2754). The [graduation gate ADR](../architecture-decision-records/20261006-archv2-graduation-gate.md)
requires that cost to grow roughly in proportion to destination count before the 2x2 shape is
measured (#2910).

A branch made only of destination tasks now shares the batch's records with its siblings and gets
its own copy of everything else (statuses, run ledger, split-record map). A branch that contains
any other task, in practice a destination-scoped processor, still gets a full deep copy. The ack
model ([20260731-archv2-fanout-ack-model](../architecture-decision-records/20260731-archv2-fanout-ack-model.md))
is untouched.

## Problem

Profiles of `BenchmarkEngineFanOut/dest2` before the change: `opencdc.Record.Clone` and
`StructuredData.Clone` were 56% of allocated bytes, and the clone path accounted for about 1,150
of the 1,655 allocations per 100-record pass. Each extra destination added a deep copy of every
record, which cost more than the whole single-destination pipeline did per record.

That copy protects one thing: a branch that edits records in place must not be seen by a sibling.
Only processors edit records. A destination branch reads them:

- `DestinationTask.Do` reads `Position`, passes the slice to `Destination.Write`, and records
  per-record outcomes on its own `Batch` (statuses only).
- `connector.Destination.Write` never hands the caller's records to plugin code. A builtin plugin's
  in-memory stream clones the request before sending it (`pkg/plugin/connector/builtin/stream.go`);
  a standalone plugin gets a protobuf-serialized copy. The inspector clones before fanning out to
  sessions.
- Connector metrics compute record sizes without writing.
- After a vote, the parent path reads only: `Worker.Ack` reads positions and metadata,
  `DLQ.dlqRecord` builds a new record from `r.Map()` and never writes to `r`.

## Decision

`branchMutatesRecords(node)` walks a branch and returns false only if every task in it is a
`*DestinationTask`. It is an allow-list: any other task type, including one added later, is
treated as mutating and gets `Batch.clone()`. Read-only branches get `Batch.cloneSharingRecords()`:
a new `Batch` whose `records` slice is the same (clipped) slice, with `recordStatuses`, `runs` and
`splitRecords` copied exactly as `clone()` copies them.

Measured with `-count=10`, interleaved against `origin/main` (numbers in #2910's PR): allocations
per 100-record pass went from 510 / 1,655 / 2,683 (1 / 2 / 4 destinations) to 510 / 645 / 668.

## Alternatives considered

**One branch takes the original batch, M-1 branches deep-copy.** Measured: two destinations went
from 1,278 to 864 ns/record, still 3.1x one destination. It halves the copies at M=2 but keeps the
per-destination copy, so cost still grows faster than destination count. It also hands one branch
the caller's batch, which is only safe if no sibling shares its records, so it does not combine
with sharing.

**Copy on write.** Copy a record the first time a branch writes to it. Processors write to
`Metadata` maps and payloads directly, including inside plugin code the engine does not control,
so there is no write barrier to hook. Rejected as not implementable without changing the processor
contract.

**Run the last branch on the calling goroutine.** Saves one goroutine start and one park per pass.
Measured after the change above: 4 fewer allocations per pass, no time difference distinguishable
from noise. Rejected; it adds a panic-propagation path for no measured gain.

## Failure modes

- **A future change makes the destination path write to a record** (for example, stamping
  destination metadata in `DestinationTask.Do`, or removing the clone in the builtin in-memory
  stream). Siblings would then race on the same maps: a data race and cross-destination corruption.
  Detection: `go test -race` on the fan-out tests (the existing fan-out suite now runs on the shared
  path), `TestDoNextTask_FanOut_SharedRecords_MutatingBranchGetsOwnCopy`, and a comment at the
  builtin stream's clone pointing here. Mitigation: such a change has to either keep the write off
  the shared record or take `*DestinationTask` off the allow-list.
- **A nacked record leaves the fan-out while a sibling still reads it.** `multiAckNacker` releases a
  nack as soon as it heads the queue. The DLQ only reads the record and builds a new one, so this is
  read-read. `TestDoNextTask_FanOut_SharedRecords_NackToDLQLeavesSharedRecordIntact` holds a sibling
  inside `Write` while the DLQ runs and checks the sibling's record afterwards.
- **A branch is misclassified as read-only.** Only possible if a non-destination task is a
  `*DestinationTask`, which the type check rules out. A misclassification the other way (a read-only
  branch deep-copied) costs time, not correctness.
- **Ack accounting.** Unchanged by construction: `multiAckNacker`, its construction, and the
  per-branch `runAckNacker` are not modified, and every branch still has its own statuses and run
  ledger. The existing fan-out ack tests, the chaos suite, and `-race -count=20` on
  `pkg/lifecycle-poc/...` were run against the change.

Crash behaviour is unchanged: nothing here is persisted, and a SIGKILL mid-fan-out replays the
batch from the last acked source position exactly as before.

## Upgrade and rollback

No serialized format, config, or API changes. Rollback is a revert.

## Observability

No new metrics. A regression here shows up as race-detector failures in CI, or in production as a
destination receiving a sibling branch's processor edits. Fan-out cost is tracked by
`BenchmarkEngineFanOut` and, deterministically, by
`TestDoNextTask_FanOut_DestinationBranchesDoNotCopyRecords`, which fails if an added
destination-only branch costs allocations proportional to the batch size again.

## Related

- [20261006-archv2-graduation-gate](../architecture-decision-records/20261006-archv2-graduation-gate.md), decision 2
- [20260731-archv2-fanout-ack-model](../architecture-decision-records/20260731-archv2-fanout-ack-model.md)
- [20260731-archv2-multiconnector](20260731-archv2-multiconnector.md)
- #2754 (the in-process benchmarks), #2910
