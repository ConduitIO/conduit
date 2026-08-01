// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package funnel

import (
	"context"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// splitRun tracks the completion state of one split run: a set of batch
// entries that all trace back, via Batch.SplitRecord, to a single original
// source record (see Batch.runs's doc comment for how membership is tracked
// across sub-batches). Every currently-live member of the run shares a
// pointer to the SAME splitRun value, so the tally survives across however
// many times the batch is subsequently sliced (Batch.sub), retried
// (Worker.doTask's RecordFlagRetry branch), or further split by a later task.
//
// # What "terminal" means (fixes #2723)
//
// A run member is terminal once it has reached a disposition that Worker.doTask
// will never revisit with another Task.Do call: it was acked (reached the end
// of the pipeline, including a destination write), filtered, or nacked
// (routed to the DLQ). A member flagged RecordFlagRetry is explicitly NOT
// terminal - it is still in flight, and (per ProcessorTask.Do's documented
// behavior: a processor returning fewer records than it received produces a
// Retry) may yet be fed to the SAME task again, which can split it further.
// That is exactly why total below is a live counter, not a value fixed at the
// first SplitRecord call - see the field doc.
//
// # Why this does not touch RecordFlag semantics
//
// Both rejected fixes for #2723 (PRs #2725, #2727) tried to make a run's
// RecordFlag values uniform - either by escalating every write across the
// run, or by normalizing them in one pass after Task.Do. Escalating Retry
// across a run that also contains a Filter'd member starves the retry input
// of the shrink a Filter provides, which is what makes a deterministic
// output-capped processor converge; escalating flags at all reached into
// territory (re-feeding already-transformed records to a processor) with no
// existing idempotency contract. This design does neither. RecordFlagRetry,
// RecordFlagFilter, RecordFlagNack all behave on this branch exactly as they
// do on main; splitRun only changes WHEN a disposition that Worker.doTask
// already decided is forwarded to the parent ackNacker, never WHAT that
// disposition is. See docs/design-documents/20260801-archv2-split-run-ack-ledger.md.
type splitRun struct {
	// origPos and origRecord are captured once, at the run's creation (the
	// first Batch.SplitRecord call for this original record), and never
	// change. They are what gets handed to the parent ackNacker once the run
	// completes - see ackBatch/nackBatch below. Both must be captured here,
	// rather than looked up later via Batch.splitRecords, because a sub-batch
	// holding only nil-position tail members (no head, see Batch.runs's doc
	// comment) cannot resolve that map lookup by the time the run completes.
	origPos    opencdc.Position
	origRecord opencdc.Record

	// total is the number of CURRENTLY LIVE members of the run - i.e. it can
	// grow: Batch.SplitRecord increments it every time it splits an existing
	// member further, whether that happens before or after some OTHER member
	// of the same run has already gone terminal. It can only grow while some
	// member is still active (fed to a Task.Do call) - once every currently
	// known member is terminal, by definition nothing is left that Task.Do
	// could still split, so growth stops exactly when completion is checked.
	total int
	// terminalCount is how many of the run's CURRENT total members have
	// reached a terminal disposition so far, credited by runAckNacker.vote
	// the moment each one arrives at an Ack or Nack call.
	terminalCount int

	// nacked, once true, is sticky: the run's eventual disposition to the
	// parent is Nack if ANY member ever nacked, regardless of how many
	// siblings acked and regardless of arrival order - the same nack-wins
	// rule multiAckNacker applies across branches (invariant 3), applied here
	// across a run's members instead.
	nacked     bool
	nackErr    error
	nackTaskID string

	// released guards against ever forwarding the same run to the parent
	// twice. Structurally this should be unreachable (see run_ledger.go's
	// package doc), but a double-forward would double-ack a source position -
	// an invariant-1 violation - so it is asserted rather than assumed.
	released bool
}

// complete reports whether every currently-known member of the run has
// reached a terminal disposition - see the total/terminalCount field docs for
// why this correctly implies no further growth is possible.
func (r *splitRun) complete() bool {
	return r.terminalCount >= r.total
}

// ackBatch builds the single-record *Batch representing this run's original
// position, ready to hand to the parent ackNacker's Ack. It carries no runs
// or splitRecords (both nil/zero) - by construction the run is finished, so
// there's nothing left for the parent (or anything further downstream, like
// an outer runAckNacker or multiAckNacker) to track for it.
func (r *splitRun) ackBatch() *Batch {
	return &Batch{
		records:        []opencdc.Record{r.origRecord},
		recordStatuses: []RecordStatus{{Flag: RecordFlagAck}},
		positions:      []opencdc.Position{r.origPos},
	}
}

// nackBatch builds the single-record *Batch representing this run's original
// position for the parent ackNacker's Nack, carrying the error and task ID of
// whichever member nacked first (see the nacked field doc).
func (r *splitRun) nackBatch() *Batch {
	return &Batch{
		records:        []opencdc.Record{r.origRecord},
		recordStatuses: []RecordStatus{{Flag: RecordFlagNack, Error: r.nackErr}},
		positions:      []opencdc.Position{r.origPos},
		tainted:        true,
	}
}

// runAckNacker sits between Worker.doTask and the acker it would otherwise
// call directly - the Worker itself for a single-destination pipeline, or a
// multiAckNacker for one branch of a destination fan-out. See Worker.Do and
// Worker.doNextTask for where it's inserted, and Batch.runs's doc comment for
// how run membership survives across the calls it intercepts.
//
// It withholds a split run's original source position from the parent until
// every member of the run is terminal (see splitRun), which is the fix for
// #2723: a sub-batch covering only PART of a run (e.g. the head, while the
// tail is still off being retried in a separate doTask recursion) no longer
// reaches the parent's Ack/Nack on its own.
//
// # Ordering (invariant 4) needs no explicit bookkeeping here
//
// multiAckNacker needs an explicit released-prefix scan (see its doc comment
// and releaseLocked) because it arbitrates M concurrently-running branches
// that can each finish ANY position first. runAckNacker has no such problem:
// within one branch, Worker.doTask's tainted-batch loop and its
// RecordFlagRetry recursion are both fully sequential and synchronous - no
// goroutines. subBatchByFlag advances strictly left to right, and a
// RecordFlagRetry sub-batch's recursive doTask call (including everything IT
// does: further splits, nested retries, and its own Ack/Nack calls) completes
// before the loop advances past it. A split run always occupies a contiguous
// span of the original batch (SplitRecord only ever grows a run in place), so
// by the time the loop's cursor reaches any position after the run's span,
// every member of the run has already been voted on by this type. That is
// what makes "buffer until terminalCount==total, then forward immediately"
// sufficient on its own: nothing positioned after a pending run can reach
// vote() before the run resolves, so release order along the original
// sequence is preserved by construction rather than by an explicit counter.
// See docs/design-documents/20260801-archv2-split-run-ack-ledger.md for the
// full argument, including the one scenario (a run's disjoint pieces each
// independently reaching a REPEATED destination fan-out point) this argument
// does not cover, and why that gap is pre-existing and unaffected by this fix.
type runAckNacker struct {
	parent ackNacker
}

// newRunAckNacker wraps parent with a fresh, empty run ledger. A fresh
// instance is required per scope that needs independent run tracking: once
// per top-level batch-processing pass for a non-fanned-out pipeline (see
// Worker.Do), and once per destination branch at a fan-out point (see
// Worker.doNextTask) since branches diverge independently after cloning and
// must not share (or race on) each other's run completion state.
func newRunAckNacker(parent ackNacker) *runAckNacker {
	return &runAckNacker{parent: parent}
}

// Ack records a vote that every record in batch reached the end of the
// pipeline (or was filtered). Standalone records (not part of any split run)
// are forwarded to the parent immediately, unchanged. Records that belong to
// an incomplete run are held; the run's original position is forwarded to the
// parent only once every one of its currently-known members has voted
// (Ack, Nack, or Filter - see splitRun's doc comment for what counts as
// terminal).
func (r *runAckNacker) Ack(ctx context.Context, batch *Batch) error {
	return r.vote(ctx, batch, true, "")
}

// Nack records a vote that every record in batch failed at taskID and was
// routed to the DLQ. Same withholding rule as Ack; if any member of a run
// ever nacks, the run's eventual disposition to the parent is Nack regardless
// of how its other members voted (invariant 3, applied within a run - see
// splitRun.nacked).
func (r *runAckNacker) Nack(ctx context.Context, batch *Batch, taskID string) error {
	return r.vote(ctx, batch, false, taskID)
}

// vote walks batch left to right. Contiguous standalone records (Batch.runs[i]
// == nil) are coalesced into one pass-through call to the parent. Contiguous
// records belonging to the same run are credited toward that run's
// completion; the run itself is only forwarded to the parent once (see
// splitRun.released) at the moment its last currently-known member votes.
//
// A batch built without run tracking (batch.runs == nil - e.g. a hand-built
// single-record Batch like splitRun's own ackBatch/nackBatch, or
// multiAckNacker's) is treated the same as all-standalone: there is nothing
// to withhold, so every record forwards immediately. This is what makes
// runAckNacker composable with itself and with multiAckNacker without special
// cases at the boundary.
func (r *runAckNacker) vote(ctx context.Context, batch *Batch, isAck bool, taskID string) error {
	i := 0
	for i < len(batch.records) {
		var run *splitRun
		if batch.runs != nil {
			run = batch.runs[i]
		}

		j := i + 1
		for j < len(batch.records) {
			var next *splitRun
			if batch.runs != nil {
				next = batch.runs[j]
			}
			if next != run {
				break
			}
			j++
		}

		if run == nil {
			if err := r.forward(ctx, batch.sub(i, j), isAck, taskID); err != nil {
				return err
			}
			i = j
			continue
		}

		if run.released {
			// Should be unreachable: a run's members are never handed to
			// another Ack/Nack call once the run has already been forwarded
			// (see the type doc comment's ordering argument). Fail loud
			// rather than silently double-crediting (or, worse, double-
			// forwarding) a position already released to the source.
			return cerrors.Errorf(
				"(bug) runAckNacker: split run at source position %q voted on again after already being released",
				run.origPos,
			)
		}

		run.terminalCount += j - i
		if !isAck && !run.nacked {
			run.nacked = true
			run.nackErr = firstRunError(batch.recordStatuses[i:j])
			run.nackTaskID = taskID
		}

		if run.complete() {
			run.released = true
			if run.nacked {
				if err := r.parent.Nack(ctx, run.nackBatch(), run.nackTaskID); err != nil {
					return err
				}
			} else if err := r.parent.Ack(ctx, run.ackBatch()); err != nil {
				return err
			}
		}

		i = j
	}
	return nil
}

func (r *runAckNacker) forward(ctx context.Context, batch *Batch, isAck bool, taskID string) error {
	if isAck {
		return r.parent.Ack(ctx, batch)
	}
	return r.parent.Nack(ctx, batch, taskID)
}

// firstRunError returns the first non-nil error among statuses, used to
// attribute a run's DLQ entry to whichever member nacked first (see
// splitRun.nacked).
func firstRunError(statuses []RecordStatus) error {
	for _, s := range statuses {
		if s.Error != nil {
			return s.Error
		}
	}
	return nil
}
