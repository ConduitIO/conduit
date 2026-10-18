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
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// defaultMaxBatchSize bounds the buffer when only sdk.batch.delay is set (no
// explicit sdk.batch.size), so a fast source can never grow the pending buffer
// without limit. See docs/design-documents/20261017-funnel-destination-batching.md.
const defaultMaxBatchSize = 1000

// errBuffered is a sentinel returned by DestinationTask.doBuffered to signal
// that the batch was buffered but not yet flushed or acknowledged. The worker
// treats it as "continue to the next source read without acknowledging the
// source" — the source ack is deferred until the flush that durably writes the
// record (Invariant 1).
var errBuffered = cerrors.New("(internal) records buffered, awaiting flush")

// pendingBatch is one source batch whose records are buffered, awaiting flush.
// It carries the source *Batch (to mark per-record nacks and to ack/nack) and
// the ackNacker that would have acked it had it been written immediately.
type pendingBatch struct {
	batch *Batch
	acker ackNacker
}

// flushSpan is one pending batch's contiguous range in the concatenated record
// slice built by flush, so destination acks can be mapped back to the owning
// source batch.
type flushSpan struct {
	p           pendingBatch
	activeStart int // inclusive, into records/positions
	activeEnd   int // exclusive
}

// batchingEnabled reports whether this task buffers writes. It mirrors the
// connector SDK's destination batching predicate: sdk.batch.size > 1 or
// sdk.batch.delay > 0.
func (t *DestinationTask) batchingEnabled() bool {
	return t.batchSize > 1 || t.batchDelay > 0
}

// effectiveBatchSize returns the flush threshold: the configured size when set,
// otherwise the default cap so memory stays bounded even with delay-only
// batching.
func (t *DestinationTask) effectiveBatchSize() int {
	if t.batchSize > 1 {
		return t.batchSize
	}
	return defaultMaxBatchSize
}

// doBuffered is the batching counterpart of Do. It appends the batch's active
// records to the pending buffer and flushes when the buffer reaches the size
// threshold or the flush deadline has passed. It returns:
//
//   - errBuffered: the batch was buffered; the caller must NOT ack it and should
//     read the next source batch.
//   - nil: the batch (and any earlier buffered batches) was flushed and acked.
//   - any other error: the write or ack failed; the pipeline fails.
//
// Invariant 1 (enforcement site): the source is acknowledged only from flush,
// strictly after the destination has written the records and returned their
// acks. No buffered record is ever acked before its flush.
func (t *DestinationTask) doBuffered(ctx context.Context, batch *Batch, acker ackNacker) error {
	records := batch.ActiveRecords()
	if len(records) == 0 {
		// Nothing to write (all records filtered upstream). A filtered record
		// is already handled, so it can be acknowledged immediately: no
		// destination write is owed for it.
		return acker.Ack(ctx, batch)
	}

	t.mu.Lock()
	now := time.Now()
	if len(t.pending) == 0 && t.batchDelay > 0 {
		// First record of a fresh buffer: arm the flush deadline. It is checked
		// synchronously here on every arrival (not on a ticker) so the source
		// ack never runs on a non-worker goroutine; a trailing partial batch is
		// flushed on Close.
		t.flushDeadline = now.Add(t.batchDelay)
	}
	t.pending = append(t.pending, pendingBatch{batch: batch, acker: acker})
	t.bufferedCount += len(records)

	shouldFlush := t.bufferedCount >= t.effectiveBatchSize()
	if !shouldFlush && t.batchDelay > 0 && !t.flushDeadline.IsZero() && now.After(t.flushDeadline) {
		shouldFlush = true
	}
	t.mu.Unlock()

	if shouldFlush {
		return t.flush(ctx)
	}
	return errBuffered
}

// flush writes all buffered records in one Write, drains their acks, marks
// per-record nacks on the owning source batches, and then acks/nacks every
// pending source batch in source order via its stored ackNacker. See doBuffered
// for the invariant comment.
func (t *DestinationTask) flush(ctx context.Context) error {
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.bufferedCount = 0
	t.flushDeadline = time.Time{}
	t.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}

	// Gather active records across all pending batches, remembering each
	// batch's span in the concatenated slices so acks can be mapped back.
	spans := make([]flushSpan, 0, len(pending))
	var records []opencdc.Record
	var positions []opencdc.Position
	for _, p := range pending {
		active := p.batch.ActiveRecords()
		if len(active) == 0 {
			continue // filtered-only batch: acked after the write below
		}
		spans = append(spans, flushSpan{p: p, activeStart: len(records), activeEnd: len(records) + len(active)})
		records = append(records, active...)
		for _, rec := range active {
			positions = append(positions, rec.Position)
		}
	}

	start := time.Now()
	if len(records) > 0 {
		err := t.destination.Write(ctx, records)
		if err != nil {
			t.metrics.ObserveError()
			return cerrors.Errorf("failed to write %d records to destination: %w", len(positions), err)
		}

		ackCount := 0
		for ackCount < len(positions) {
			acks, err := t.destination.Ack(ctx)
			if err != nil {
				t.metrics.ObserveError()
				return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
			}
			if err := t.validateAcks(acks, positions[ackCount:]); err != nil {
				t.metrics.ObserveError()
				return cerrors.Errorf("failed to validate acks: %w", err)
			}
			t.markSpans(spans, acks, ackCount)
			t.metrics.Observe(records[ackCount:ackCount+len(acks)], start)
			ackCount += len(acks)
		}
	}

	// Ack/nack every pending batch in source order, including filtered-only
	// batches (their records were filtered, not written).
	for _, p := range pending {
		if err := t.ackPending(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// markSpans marks per-record nacks on the owning source batches for the acks
// that arrived at global indices [ackCount, ackCount+len(acks)). Each span
// covers a contiguous range of the concatenated positions, so a global index
// maps to a local active-record offset inside exactly one batch.
func (t *DestinationTask) markSpans(spans []flushSpan, acks []connector.DestinationAck, ackCount int) {
	lo, hi := ackCount, ackCount+len(acks)
	for _, s := range spans {
		from := max(lo, s.activeStart)
		to := min(hi, s.activeEnd)
		if from < to {
			t.markBatchRecords(s.p.batch, from-s.activeStart, acks[from-lo:to-lo])
		}
	}
}

// ackPending routes a buffered source batch's records to the parent ackNacker,
// exactly as Worker.doTaskAttempt does for a batch that reached the (last)
// destination task: an untainted batch acks wholesale, a tainted batch splits
// by flag and nacks the failed records.
func (t *DestinationTask) ackPending(ctx context.Context, p pendingBatch) error {
	b := p.batch
	if !b.tainted {
		return p.acker.Ack(ctx, b)
	}

	idx := 0
	for {
		sub := subBatchByFlag(b, idx)
		if sub == nil {
			return nil
		}
		span := len(sub.positions)
		switch sub.recordStatuses[0].Flag {
		case RecordFlagAck, RecordFlagFilter:
			if err := p.acker.Ack(ctx, sub); err != nil {
				return err
			}
		case RecordFlagNack:
			if err := p.acker.Nack(ctx, sub, t.id); err != nil {
				return err
			}
		case RecordFlagRetry:
			// A destination never marks records for retry; reaching here is a
			// bug. Fail loud rather than silently acking or dropping them.
			return cerrors.Errorf("(bug) destination %s buffered a record flagged for retry", t.id)
		}
		idx += span
	}
}

// isBuffered reports whether err is the buffered sentinel. It is used by
// Worker.doTaskAttempt to decide whether to defer the source ack.
func isBuffered(err error) bool {
	return cerrors.Is(err, errBuffered)
}
