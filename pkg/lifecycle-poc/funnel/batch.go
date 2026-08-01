// Copyright © 2024 Meroxa, Inc.
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

//go:generate stringer -type=RecordFlag -linecomment

package funnel

import (
	"fmt"
	"slices"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Batch represents a batch of records that are processed together. It keeps
// track of the status of each record in the batch, and provides methods to
// update the status of records.
type Batch struct {
	// records holds all records in the batch, including filtered ones. Use
	// ActiveRecords to get the records that are not filtered.
	records []opencdc.Record
	// recordStatuses holds the status of each record in the batch. The status
	// is used to determine if the record was acked, nacked, filtered or marked
	// for retrial.
	recordStatuses []RecordStatus
	// positions is a slice of positions for the records in the batch. The
	// positions are used to ack or nack records in the source. If a record
	// was split, its position is nil. Split records are always consecutive, so
	// we can use the first record's position to ack or nack the split records.
	positions []opencdc.Position

	// filterCount is updated any time a record is marked as filtered, to make it
	// easier to construct the set of active records.
	filterCount int

	// If a batch is tainted it means that parts need to be either nacked or
	// retried. Such a batch needs to be split into multiple batches, each
	// containing only records with the same status (filtered counts as acked).
	tainted bool

	// splitRecords is a map of split records, where the key is the record
	// position converted to a string. The value is the original record itself.
	// This is used to keep track of the original record when splitting it into
	// multiple records. The position is nil for split records, except the first
	// record, which contains the position of the original record.
	splitRecords map[string]opencdc.Record
}

func NewBatch(records []opencdc.Record) *Batch {
	// Store positions separately, as we need the original positions when acking
	// records in the source, in case a processor tries to change the position.
	positions := make([]opencdc.Position, len(records))
	for i, r := range records {
		positions[i] = r.Position
	}

	return &Batch{
		records:        records,
		recordStatuses: make([]RecordStatus, len(records)),
		positions:      positions,
		filterCount:    0,
		tainted:        false,
	}
}

// Ack marks the record at index i as acked. If a second index is provided, all
// records between i (included) and j (excluded) are marked as acked. If multiple
// indices are provided, the method panics.
// Records are marked as acked by default, so this method is only useful when
// reprocessing records marked to be retried.
//
// Note that the indices i and j point to the slice returned by ActiveRecords.
// If the slice contains filtered records, the filtered records are skipped and
// not marked as acked.
func (b *Batch) Ack(i int, j ...int) {
	b.setFlagNoErr(RecordFlagAck, i, j...)
}

// Nack marks the record at index i as nacked. If multiple errors are provided,
// they are assigned to the records starting at index i.
//
// Note that the indices i and j point to the slice returned by ActiveRecords.
// If the slice contains filtered records, the filtered records are skipped and
// not marked as nacked.
func (b *Batch) Nack(i int, errs ...error) {
	b.setFlagWithErr(RecordFlagNack, i, errs)
	b.tainted = true
}

// Retry marks the record at index i to be retried. If a second index is
// provided, all records between i (included) and j (excluded) are marked to be
// retried. If multiple indices are provided, the method panics.
//
// Note that the indices i and j point to the slice returned by ActiveRecords.
// If the slice contains filtered records, the filtered records are skipped and
// not marked to be retried.
func (b *Batch) Retry(i int, j ...int) {
	b.setFlagNoErr(RecordFlagRetry, i, j...)
	b.tainted = true
}

// Filter marks the record at index i as filtered out. If a second index is
// provided, all records between i (included) and j (excluded) are marked as
// filtered. If multiple indices are provided, the method panics.
// Once a record is filtered, it is kept in the batch as to not split the batch
// into multiple batches. It's not returned in the active records and its status
// can't be changed anymore, however, it is included when acking the batch.
//
// Note that the indices i and j point to the slice returned by ActiveRecords.
// If the slice contains filtered records, the filtered records are skipped and
// not marked to be retried.
func (b *Batch) Filter(i int, j ...int) {
	b.setFlagNoErr(RecordFlagFilter, i, j...)
	end := i + 1
	if len(j) == 1 {
		end = j[0]
	}
	b.filterCount += end - i
}

// SetRecords replaces the records in the batch starting at index i with the
// provided records. If recs contains n records, indices i to i+n-1 are replaced.
func (b *Batch) SetRecords(i int, recs []opencdc.Record) {
	// TODO: we should not have to recalculate the active record indices every time.
	activeIndices := b.activeRecordIndices()
	if activeIndices == nil {
		// No records are filtered, we can use the original indices.
		copy(b.records[i:], recs)
		return
	}

	// We have filtered records, so we need to use the active indices.
	from := i
	for len(recs) > 0 {
		activeFrom := activeIndices[from]
		to := b.findTo(from, from+len(recs), func(idx int) bool {
			return activeIndices[idx]-activeFrom == idx-from
		})
		activeTo := activeIndices[to]
		copy(b.records[activeFrom:activeTo+1], recs[:to-from+1])
		recs = recs[to-from+1:]
		from = to + 1
	}
}

// findTo implements a dichotomic search function. It finds the highest index t
// in the range [l, r) such that check(t) is true. The check function is called
// with the index t, and if it returns true, the index is returned. If check(t)
// returns false, the search continues in the range [l, t). The search stops
// when the range is empty or when check(t) returns true.
func (b *Batch) findTo(l, r int, check func(int) bool) int {
	// Precondition
	if !check(l) {
		panic("precondition failed: check(l) must be true")
	}

	maxIdxTrue := l  // highest known integer which satisfies check(idx) == true
	minIdxFalse := r // lowest known integer which satisfies check(idx) == false

	for maxIdxTrue+1 < minIdxFalse {
		midIdx := (maxIdxTrue + minIdxFalse) / 2

		if check(midIdx) {
			maxIdxTrue = midIdx
		} else {
			minIdxFalse = midIdx
		}
	}

	return maxIdxTrue
}

// SplitRecord splits the record at index i into the provided records. The
// records replace the record at the index i, and the rest of the records are
// shifted to the right.
func (b *Batch) SplitRecord(i int, recs []opencdc.Record) {
	// TODO: we should not have to recalculate the active record indices every time.
	activeIndices := b.activeRecordIndices()
	if activeIndices != nil {
		// We have filtered records, use the active index instead.
		i = activeIndices[i]
	}

	if b.splitRecords == nil {
		b.splitRecords = make(map[string]opencdc.Record)
	}
	if _, ok := b.splitRecords[b.records[i].Position.String()]; b.records[i].Position != nil && !ok {
		// We're splitting an original record, so we need to store it in the
		// split records map. If any of the split records get nacked, we need to
		// send the original to the DLQ.
		b.splitRecords[b.records[i].Position.String()] = b.records[i]
	}

	b.records = append(b.records[:i], append(recs, b.records[i+1:]...)...)

	// Extend the record statuses slice to accommodate the new records. Each
	// new piece REPLICATES the CURRENT status of the record being split
	// (index i) — it is not safe to default them to Ack (the zero value).
	//
	// The old comment here ("the original record must have been acked,
	// otherwise the processor wouldn't receive it") is stale: it assumed the
	// only way a record ever reaches SplitRecord is fresh off the Ack
	// default. That is not a guarantee this type upholds — a record already
	// part of an unresolved split run can be split further with a non-Ack
	// status (e.g. Nack, which still propagates across a run — see
	// normalizeSplitRuns). Defaulting the new pieces to Ack in that case
	// would silently inject wrongly-flagged pieces into an already-Nack'd or
	// already-Retry'd run, corrupting the run's uniformity — see #2723 and
	// the adversarial review of #2725 (finding A) for the shape this caused.
	current := b.recordStatuses[i]
	if current.Flag == RecordFlagFilter {
		// Unreachable through SplitRecord's own calling convention: i has
		// just been translated through activeRecordIndices(), which by
		// construction only ever yields indices whose current flag is NOT
		// Filter (filtered records are excluded from ActiveRecords, and
		// SplitRecord is only ever called — from ProcessorTask.Do's
		// markBatchRecords — with an index derived from ActiveRecords).
		// Panic rather than silently mis-accounting filterCount if some
		// future caller ever violates that contract directly.
		panic("(bug) SplitRecord: cannot split an already-filtered record - " +
			"filtered records are excluded from ActiveRecords and unreachable " +
			"via the active-index calling convention")
	}
	newStatuses := make([]RecordStatus, 0, len(recs)-1)
	for k := 0; k < len(recs)-1; k++ {
		newStatuses = append(newStatuses, current)
	}
	b.recordStatuses = append(
		b.recordStatuses[:i+1],
		append(newStatuses, b.recordStatuses[i+1:]...)...,
	)

	// Extend the positions slice to accommodate the new records. The new records
	// get a nil position, which indicates that they were split from the original
	// record.
	b.positions = append(
		b.positions[:i+1],
		append(make([]opencdc.Position, len(recs)-1), b.positions[i+1:]...)...,
	)
}

func (b *Batch) setFlagNoErr(f RecordFlag, i int, j ...int) {
	// TODO: we should not have to recalculate the active record indices every time.
	activeIndices := b.activeRecordIndices()
	if activeIndices == nil {
		// No records are filtered, we can use the original indices.
		switch len(j) {
		case 0:
			b.recordStatuses[i].Flag = f
		case 1:
			if i >= j[0] {
				panic(fmt.Sprintf("invalid range (%d >= %d)", i, j[0]))
			}
			for k := i; k < j[0]; k++ {
				b.recordStatuses[k].Flag = f
			}
		default:
			panic(fmt.Sprintf("too many arguments (%d)", len(j)))
		}
	} else {
		// We have filtered records, so we need to use the active indices.
		switch len(j) {
		case 0:
			b.recordStatuses[activeIndices[i]].Flag = f
		case 1:
			if i >= j[0] {
				panic(fmt.Sprintf("invalid range (%d >= %d)", i, j[0]))
			}
			for k := i; k < j[0]; k++ {
				b.recordStatuses[activeIndices[k]].Flag = f
			}
		default:
			panic(fmt.Sprintf("too many arguments (%d)", len(j)))
		}
	}
}

// setFlagWithErr sets the flag for the record at index i to f, and sets the
// error for the record to err. If multiple errors are provided, they are
// assigned to the records starting at index i. If an error is assigned to a
// split record, all records in the split record are marked with the same flag
// and error.
//
// Only Nack calls this today, and its split-run propagation below is kept
// exactly as it always was (pre-dating #2723, and confirmed correct by the
// adversarial review of #2725) — a failed piece condemns its whole run
// immediately, not deferred to normalizeSplitRuns. This is deliberately NOT
// generalized to Retry/Filter: those two are written as plain, local,
// single-index ops during Do and reconciled in exactly one place,
// normalizeSplitRuns, after Do returns — see its doc comment for why
// mutating cross-record state DURING Do's own marking pass is the failure
// mode this whole redesign exists to avoid (findings A and B of that
// review). Nack does not carry that risk here because propagating it can
// only ever move a sibling's flag towards Nack — the run's most severe,
// terminal state — never destabilize the active-index mapping in a way
// later, lower-index calls in the same pass depend on: activeRecordIndices
// always re-derives its index list from the records' actual flags, so a
// stale filterCount can only ever make it (safely) skip its own fast path,
// not return a wrong index set.
func (b *Batch) setFlagWithErr(f RecordFlag, i int, errs []error) {
	// TODO: we should not have to recalculate the active record indices every time.
	activeIndices := b.activeRecordIndices()
	for k, err := range errs {
		idx := i + k
		if activeIndices != nil {
			// We have filtered records, so we need to use the active index.
			idx = activeIndices[idx]
		}

		// Set the flag and error for this record.
		b.setRecordFlagWithErr(idx, f, err)

		// Handle split records when nacking.
		if len(b.splitRecords) > 0 && f == RecordFlagNack {
			// If the record was split, we need to set the error for all
			// records in the split record, so they are all marked as nacked.
			if _, ok := b.splitRecords[b.positions[idx].String()]; b.positions[idx] == nil || ok {
				// This is a split record, we need to set the error for all
				// records in the split record.
				from, to := b.findSplitRecord(idx)
				for j := from; j <= to; j++ {
					b.setRecordFlagWithErr(j, f, err)
				}
			}
		}
	}
}

// setRecordFlagWithErr sets the flag and error for the record at idx,
// keeping filterCount in sync with any Filter -> non-Filter transition (see
// setFlagWithErr's Nack-propagation loop, which can overwrite a Filter-
// flagged sibling elsewhere in the same split run) so
// HasActiveRecords/ActiveRecords stay accurate afterwards.
func (b *Batch) setRecordFlagWithErr(idx int, f RecordFlag, err error) {
	if b.recordStatuses[idx].Flag == RecordFlagFilter && f != RecordFlagFilter {
		b.filterCount--
	}
	b.recordStatuses[idx].Flag = f
	b.recordStatuses[idx].Error = err
}

// normalizeSplitRuns walks the batch once, immediately after a task's Do has
// returned and before Worker.doTask partitions a tainted batch into
// sub-batches, and resolves every split run (see SplitRecord) to a flag
// arrangement that Worker.subBatchByFlag can always group as a single unit.
//
// This — not per-write escalation during Do — is the fix for #2723: a split
// run whose pieces disagree (e.g. one piece nacked or marked for retry while
// another already succeeded) can no longer be torn apart by subBatchByFlag
// into a sub-batch that acks the run's head while the rest of the run is
// still outstanding.
//
// It must run exactly once, after Do has fully returned. ProcessorTask.Do's
// own marking loop processes ranges end-to-start specifically so that
// filterCount and the active-index mapping stay stable for the lower-index
// calls still to come; mutating either while that loop is still running —
// the failure mode of an earlier, per-write "escalation" design (see the
// adversarial review of #2725, findings A and B) — can silently corrupt
// which physical record a later, lower-index call actually touches.
// Reconciling once, after Do has committed all of its writes, sidesteps that
// whole class of bug. It also keeps the operation O(n): every record is
// visited a constant number of times total, rather than the O(run²) an
// escalate-on-every-write design pays by re-walking a run's siblings on each
// individual write (finding D).
func (b *Batch) normalizeSplitRuns() {
	if len(b.splitRecords) == 0 {
		return // Fast path: the batch has no split runs at all.
	}

	i := 0
	for i < len(b.recordStatuses) {
		pos := b.positions[i]
		_, isHead := b.splitRecords[pos.String()]
		if pos != nil && !isHead {
			i++ // An ordinary, unsplit record.
			continue
		}

		from, to := b.findSplitRecord(i)
		b.normalizeRun(from, to)
		i = to + 1
	}
}

// normalizeRun resolves the split run occupying records [from,to] (inclusive)
// so Worker.subBatchByFlag will always group it as a single unit, regardless
// of which of its pieces the task that just ran happened to mark first.
//
// Resolution, highest severity first (Nack > Retry > Filter/Ack):
//
//   - Any piece Nack: the entire run failed. Every piece — including any
//     that were individually Filter or Ack — is set to Nack, carrying the
//     first (lowest-index) Nack error found in the run. In practice a run
//     should already be uniformly Nack by the time this runs: Batch.Nack
//     (setFlagWithErr) propagates across a split run immediately, unlike
//     Retry/Filter — see its doc comment for why that is safe to do inline
//     during Do while Retry/Filter are not. This branch is therefore a
//     defensive, idempotent restatement of that same rule, not the primary
//     mechanism — cheap insurance if some future write path ever sets Nack
//     without going through setFlagWithErr. When more than one piece is
//     found individually nacked, picking the first (lowest-index) error as
//     representative is an arbitrary but deterministic tie-break; which
//     exact message reaches the DLQ record is not itself correctness-
//     relevant — that the whole run is uniformly treated as failed is.
//
//   - No Nack, but any piece Retry: the run is not done. Every piece that
//     is NOT Filter is set to Retry, so the whole run is resubmitted
//     together the next time this task runs — without this, an already-
//     Ack'd piece would stay behind and reach the source's ack path while
//     the Retry piece(s) were still outstanding (#2723 itself).
//
//     Filter pieces are deliberately left untouched — this is the
//     resolution for a run that contains BOTH Filter and Retry. Filter is a
//     final decision the processor already made about that specific piece;
//     escalating it to Retry would silently undo that decision. It would
//     also re-feed the processor an unchanged-size input on the very next
//     attempt: for a processor whose output is a deterministic function of
//     its input (e.g. one that drops everything past a size limit), that is
//     a fixpoint that never resolves (the unbounded-recursion crash tracked
//     separately in #2726 — not fixed here, but this choice must not make
//     it more likely, and preserving Filter shrinks the input instead of
//     leaving it the same size). Preserving Filter lets a retry pass
//     actually converge: previously-filtered pieces stay excluded from
//     Batch.ActiveRecords and are never resubmitted, so the processor sees
//     strictly less input each time a piece is dropped for good.
//
//     A run left in this state carries two distinct flags at once (Retry
//     and Filter) — see Batch.groupFlagAt for how Worker.subBatchByFlag
//     still treats the whole run as one group.
//
//   - Otherwise every piece is already Ack or Filter, a combination
//     Worker.subBatchByFlag has always grouped as one bucket. Nothing to do.
func (b *Batch) normalizeRun(from, to int) {
	hasNack, hasRetry := false, false
	var nackErr error
	for k := from; k <= to; k++ {
		switch b.recordStatuses[k].Flag { //nolint:exhaustive // only these two flags matter here.
		case RecordFlagNack:
			if !hasNack {
				nackErr = b.recordStatuses[k].Error
			}
			hasNack = true
		case RecordFlagRetry:
			hasRetry = true
		}
	}

	switch {
	case hasNack:
		for k := from; k <= to; k++ {
			b.overwriteRunMember(k, RecordFlagNack, nackErr)
		}
	case hasRetry:
		for k := from; k <= to; k++ {
			if b.recordStatuses[k].Flag == RecordFlagFilter {
				continue // Preserved — see the doc comment above.
			}
			b.overwriteRunMember(k, RecordFlagRetry, nil)
		}
	}
}

// overwriteRunMember sets the record at idx to flag f (and, for a Nack, error
// err), keeping filterCount in sync with any Filter -> non-Filter transition
// so HasActiveRecords/ActiveRecords stay accurate. f is always
// RecordFlagNack or RecordFlagRetry here — normalizeRun never assigns Filter
// through this path — so there is no non-Filter -> Filter case to handle.
func (b *Batch) overwriteRunMember(idx int, f RecordFlag, err error) {
	if b.recordStatuses[idx].Flag == RecordFlagFilter {
		b.filterCount--
	}
	b.recordStatuses[idx].Flag = f
	b.recordStatuses[idx].Error = err
}

// groupFlagAt returns the flag Worker.subBatchByFlag should treat the record
// at idx as carrying, along with the index one past the span that flag
// applies to.
//
// For an ordinary (non-split) record, that is just its own flag and idx+1.
// For a member of a split run, the span is always the run's full [from,to]
// bounds — never a partial slice of it — because normalizeSplitRuns
// guarantees every run has at most one non-Filter flag (Nack, Retry or Ack)
// among its pieces by the time this is called; that single flag (or Filter,
// if literally every piece in the run was filtered) is what is returned.
//
// It returns an error if the run is NOT actually uniform — i.e. it finds
// two DIFFERENT non-Filter flags among the run's pieces. This is what makes
// the CodeSplitRunPartitioned backstop reachable at all: without checking
// uniformity here, a physically contiguous run would always be swallowed as
// one atomic span regardless of whether its pieces actually agree, silently
// masking exactly the corruption this whole mechanism exists to catch (a
// boundary check alone, run AFTER this function already collapsed the run
// to one — possibly wrong — flag, would never see the inconsistency).
func (b *Batch) groupFlagAt(idx int) (RecordFlag, int, error) {
	if len(b.splitRecords) == 0 {
		return b.recordStatuses[idx].Flag, idx + 1, nil
	}
	pos := b.positions[idx]
	_, isHead := b.splitRecords[pos.String()]
	if pos != nil && !isHead {
		return b.recordStatuses[idx].Flag, idx + 1, nil // Not a run member.
	}

	from, to := b.findSplitRecord(idx)
	flag := RecordFlagFilter
	found := false
	for k := from; k <= to; k++ {
		f := b.recordStatuses[k].Flag
		if f == RecordFlagFilter {
			continue
		}
		if !found {
			flag, found = f, true
			continue
		}
		if f != flag {
			return RecordFlagAck, to + 1, newSplitRunPartitionedError(from, to, -1, -1)
		}
	}
	return flag, to + 1, nil
}

// newSplitRunPartitionedError builds the CodeSplitRunPartitioned error
// shared by groupFlagAt (a run's pieces disagree with each other) and
// validateSplitRunBoundary (a proposed sub-batch boundary would cut a run in
// two) — the same underlying problem from two different vantage points.
// firstIndex/lastIndex may be passed as -1 when the caller has no boundary
// of its own to report (groupFlagAt detects the inconsistency before any
// boundary is even proposed).
func newSplitRunPartitionedError(from, to, firstIndex, lastIndex int) error {
	detail := fmt.Sprintf("split run spanning batch indices [%d,%d] carries inconsistent flags", from, to)
	if firstIndex >= 0 {
		detail += fmt.Sprintf(" and was about to be partitioned by a sub-batch boundary [%d,%d)", firstIndex, lastIndex)
	}
	detail += ": this would ack or nack part of a split record before the rest of it was durably handled"

	ce := conduiterr.New(CodeSplitRunPartitioned, detail)
	ce.Suggestion = "this indicates a bug in Conduit's split-run flag normalization (see " +
		"Batch.normalizeSplitRuns), not a connector or processor bug; please report it with the " +
		"pipeline's processor chain and, if possible, a reproducing config"
	return ce
}

// resetForRetry resets every record currently flagged Retry back to Ack, so
// a task resubmitted via Worker.doTask's RecordFlagRetry case sees them as
// fresh input. Records flagged Filter are left untouched.
//
// Preserving Filter here — rather than the previous unconditional
// Ack(0, len(records)) reset — is what makes a retry pass on a split run
// that mixes Filter and Retry pieces (see normalizeRun) actually shrink: a
// Filter-flagged piece stays excluded from Batch.ActiveRecords, so the
// processor is fed fewer records than on the previous attempt instead of an
// identical-sized batch. That is the difference between a retry that can
// converge and one that is a fixpoint (see #2726).
func (b *Batch) resetForRetry() {
	for i := range b.recordStatuses {
		if b.recordStatuses[i].Flag == RecordFlagRetry {
			b.recordStatuses[i].Flag = RecordFlagAck
			b.recordStatuses[i].Error = nil
		}
	}
}

func (b *Batch) findSplitRecord(i int) (int, int) {
	// Find the first record that has a position (i.e. first split record).
	from := i
	for from > 0 && b.positions[from] == nil {
		from--
	}
	// Find the last record that has a position. (i.e. last split record).
	to := i + 1
	for to < len(b.positions) && b.positions[to] == nil {
		to++
	}
	to-- // Adjust index to point to the last record that was split.

	return from, to
}

// clone returns an independent copy of the batch, suitable for handing to a
// concurrently-running destination branch in a fan-out (see
// Worker.doNextTask's multi-destination case).
//
// records, recordStatuses and splitRecords are fully copied so a branch can
// mutate them (Ack/Nack/Filter/Retry/SplitRecord) without affecting its
// siblings or the original batch.
//
// positions is intentionally NOT deep-copied: the underlying opencdc.Position
// values are shared by reference with the original batch across all branches
// (cheap, and multiAckNacker's per-position tally relies on the exact same
// position bytes to match votes across branches — see
// docs/design-documents/20260731-archv2-multiconnector.md). What IS required
// is that the shared backing ARRAY can never be mutated through one branch's
// slice header and observed by another's.
//
// Before this fix, clone() shared b.positions verbatim (same len AND cap).
// Batch.SplitRecord grows b.positions via
// `append(b.positions[:i+1], newElems...)`, and Go's append reuses the
// backing array whenever spare capacity exists past the slice's length —
// which it always did here, because slicing to a shorter length does not
// reduce capacity. Two branches created from the same clone() call therefore
// shared one growable backing array: if branch A called SplitRecord first, it
// could silently overwrite index i+1 (and beyond) of the SAME array that
// branch B's still-unmodified `positions` slice header pointed to, corrupting
// B's view of its own positions out from under it while B's goroutine was
// concurrently reading them — a data race with a data-loss blast radius
// (wrong positions reaching Source.Ack violates invariant 2). This was only
// reachable once destination fan-out went live (clone() had no caller before
// slice 3a), so nothing shipped was ever exposed to it, but it would have
// been the first thing a multi-destination pipeline with a splitting
// processor hit. slices.Clip caps the capacity to the length (no reallocation
// happens here — it's a zero-cost 3-index reslice), so the FIRST subsequent
// SplitRecord call on either branch is forced to allocate a fresh backing
// array via append, leaving every sibling's slice header (and the original
// batch) untouched. See TestBatch_Clone_PositionsAliasing.
func (b *Batch) clone() *Batch {
	records := make([]opencdc.Record, len(b.records))
	for i, r := range b.records {
		records[i] = r.Clone()
	}

	var splitRecords map[string]opencdc.Record
	if len(b.splitRecords) > 0 {
		// Copy the map (not just the reference): SplitRecord writes new
		// entries into it, and two branches calling SplitRecord concurrently
		// on a shared map is a concurrent map write - a fatal, unrecoverable
		// runtime crash, not just a benign race. The stored opencdc.Record
		// values themselves are never mutated after being stored (only read
		// back by originalBatch), so a shallow copy - reusing the same
		// Record values across branches - is safe.
		splitRecords = make(map[string]opencdc.Record, len(b.splitRecords))
		for k, v := range b.splitRecords {
			splitRecords[k] = v
		}
	}

	return &Batch{
		records:        records,
		recordStatuses: slices.Clone(b.recordStatuses),
		positions:      slices.Clip(b.positions),
		tainted:        b.tainted,
		filterCount:    b.filterCount,
		splitRecords:   splitRecords,
	}
}

func (b *Batch) sub(from, to int) *Batch {
	filterCount := 0
	if b.filterCount > 0 {
		for _, status := range b.recordStatuses[from:to] {
			if status.Flag == RecordFlagFilter {
				filterCount++
			}
		}
	}
	var splitRecords map[string]opencdc.Record
	if len(b.splitRecords) != 0 {
		splitRecords = make(map[string]opencdc.Record)
		for _, pos := range b.positions[from:to] {
			if rec, ok := b.splitRecords[pos.String()]; ok {
				splitRecords[pos.String()] = rec
			}
		}
		if len(splitRecords) == 0 {
			splitRecords = nil
		}
	}

	// Note that we also adjust the capacity of the slices to avoid writing
	// over the original batch when splitting records.
	return &Batch{
		records:        b.records[from:to:to],
		recordStatuses: b.recordStatuses[from:to:to],
		positions:      b.positions[from:to:to],
		filterCount:    filterCount,
		tainted:        false,
		splitRecords:   splitRecords,
	}
}

// HasActiveRecords returns true if the batch has any records that are not
// filtered out.
func (b *Batch) HasActiveRecords() bool {
	return b.filterCount < len(b.records)
}

// ActiveRecords returns the records that are not filtered.
func (b *Batch) ActiveRecords() []opencdc.Record {
	if b.filterCount == 0 {
		return b.records
	}
	if b.filterCount == len(b.records) {
		return nil
	}
	active := make([]opencdc.Record, 0, len(b.records)-b.filterCount)
	for i, r := range b.records {
		if b.recordStatuses[i].Flag != RecordFlagFilter {
			active = append(active, r)
		}
	}
	return active
}

// activeRecordIndices returns the indices of the records that are not filtered.
// If no records are filtered, it returns nil, in which case the caller should
// use the original indices of the records. This prevents the need to
// reallocate the slice if no records are filtered.
func (b *Batch) activeRecordIndices() []int {
	if b.filterCount == 0 {
		return nil
	}
	active := make([]int, 0, len(b.records)-b.filterCount)
	for i, status := range b.recordStatuses {
		if status.Flag != RecordFlagFilter {
			active = append(active, i)
		}
	}
	return active
}

// originalBatch returns the batch in which any split records are replaced with
// the original records. This is needed when acking or nacking records.
// If the batch does not contain any split records, it returns the original batch.
func (b *Batch) originalBatch() *Batch {
	if len(b.splitRecords) == 0 {
		// No records were split, we can return the original batch.
		return b
	}

	records := make([]opencdc.Record, 0, len(b.records))
	recordStatuses := make([]RecordStatus, 0, len(b.records))
	positions := make([]opencdc.Position, 0, len(b.records))
	for i, pos := range b.positions {
		if pos == nil {
			continue
		}

		records = append(records, b.records[i])
		recordStatuses = append(recordStatuses, b.recordStatuses[i])
		positions = append(positions, pos)
		if rec, ok := b.splitRecords[pos.String()]; ok {
			// If the record was split, we need to replace it with the
			// original record.
			records[len(records)-1] = rec
		}
	}

	filterCount := 0
	if b.filterCount > 0 {
		for _, status := range recordStatuses {
			if status.Flag == RecordFlagFilter {
				filterCount++
			}
		}
	}

	return &Batch{
		records:        records,
		recordStatuses: recordStatuses,
		positions:      positions,
		filterCount:    filterCount,
		tainted:        false,
		splitRecords:   nil,
	}
}

// RecordStatus holds the status of a record in a batch. The flag indicates the
// status of the record, and the error is set if the record was nacked.
type RecordStatus struct {
	Flag  RecordFlag
	Error error
}

type RecordFlag int

const (
	RecordFlagAck    RecordFlag = iota // ack
	RecordFlagNack                     // nack
	RecordFlagRetry                    // retry
	RecordFlagFilter                   // filter
)
