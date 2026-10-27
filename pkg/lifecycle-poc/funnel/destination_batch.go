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

// defaultMaxBatchSize is the flush threshold used when a destination enables
// batching via sdk.batch.delay but leaves sdk.batch.size unset (the SDK's
// default). It bounds the pending buffer by record count, so a fast source can
// never grow it without limit. 3000 matches Kafka Connect's JDBC sink default
// (batch.size=3000), so a Conduit Postgres destination that does not configure
// batching flushes the same number of rows per round-trip as the sink it is
// benchmarked against in the v0.21 CDC-parity suite.
//
// It is a fallback, NOT a cap: effectiveBatchSize returns a larger explicit
// sdk.batch.size verbatim, so a connector that asks for more than 3000 gets it.
// See docs/design-documents/20261017-funnel-destination-batching.md.
const defaultMaxBatchSize = 3000

// maxInFlightFlushes is the read-ahead credit: how many destination flushes may
// be enqueued to the flusher goroutine (and in flight) at once, beyond the one
// the flusher is currently writing and the batch doBuffered is accumulating.
// 2 is enough to keep the destination saturated while the source reads ahead,
// and bounds buffered-but-unacked memory to (maxInFlightFlushes+2)×
// effectiveBatchSize records. See
// docs/design-documents/20261019-funnel-read-ahead-pipelining.md.
const maxInFlightFlushes = 2

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
//   - errBuffered: the batch was buffered or handed to the flusher; the caller
//     must NOT ack it and should read the next source batch.
//   - nil: the batch (and any earlier buffered batches) was flushed and acked.
//     On the pipelined path this is only reachable for an all-filtered batch,
//     which is acked immediately; on the synchronous (fan-out) path it is the
//     pre-pipelining "flushed and acked" result.
//   - any other error: a prior flusher error, or the write/ack failed; the
//     pipeline fails.
//
// Invariant 1 (enforcement site): the source is acknowledged only from
// flushPending, strictly after the destination has written the records and
// returned their acks. No buffered record is ever acked before its flush.
func (t *DestinationTask) doBuffered(ctx context.Context, batch *Batch, acker ackNacker) error {
	// A prior flusher error is surfaced here so it propagates through the
	// worker exactly like a synchronous flush error would (and poisons a shared
	// destination on the way out).
	if err := t.flushError(); err != nil {
		return err
	}

	records := batch.ActiveRecords()
	if len(records) == 0 {
		// Nothing to write (all records filtered upstream). A filtered record
		// is already handled, so it can be acknowledged immediately: no
		// destination write is owed for it.
		return acker.Ack(ctx, batch)
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return cerrors.Errorf("destination %s is closed", t.id)
	}
	now := time.Now()
	if len(t.pending) == 0 && t.batchDelay > 0 {
		// First record of a fresh buffer: arm the flush deadline. It is checked
		// synchronously here on every arrival (not on a ticker); a trailing
		// partial batch is flushed on Close.
		t.flushDeadline = now.Add(t.batchDelay)
	}
	t.pending = append(t.pending, pendingBatch{batch: batch, acker: acker})
	t.bufferedCount += len(records)

	shouldFlush := t.bufferedCount >= t.effectiveBatchSize()
	if !shouldFlush && t.batchDelay > 0 && !t.flushDeadline.IsZero() && now.After(t.flushDeadline) {
		shouldFlush = true
	}
	var toFlush []pendingBatch
	if shouldFlush {
		toFlush = t.pending
		t.pending = nil
		t.bufferedCount = 0
		t.flushDeadline = time.Time{}
	}
	t.mu.Unlock()

	if !shouldFlush {
		return errBuffered
	}
	if t.flushCh == nil {
		// Pipelining not running (fan-out, or Open not called): flush
		// synchronously on the worker, the pre-pipelining behavior.
		return t.flushPending(ctx, toFlush)
	}
	return t.enqueueFlush(ctx, toFlush)
}

// flush writes all buffered records (taken from t.pending) in one Write. It is
// the synchronous path used when pipelining is not running (fan-out) and by
// Close; see flushPending for the write/ack work itself.
func (t *DestinationTask) flush(ctx context.Context) error {
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.bufferedCount = 0
	t.flushDeadline = time.Time{}
	t.mu.Unlock()
	return t.flushPending(ctx, pending)
}

// flushPending writes all buffered records in one Write, drains their acks,
// marks per-record nacks on the owning source batches, and then acks/nacks
// every pending source batch in source order via its stored ackNacker. It is
// the single place the destination is written and the source is acked, and the
// only caller of Destination.Write/Ack — the flusher goroutine serializes entry
// so the destination's single gRPC stream stays single-writer, position-ordered
// FIFO (Invariant 1/4). See doBuffered for the invariant comment.
func (t *DestinationTask) flushPending(ctx context.Context, pending []pendingBatch) error {
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
		empty := 0
		for ackCount < len(positions) {
			acks, err := t.destination.Ack(ctx)
			if err != nil {
				t.metrics.ObserveError()
				return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
			}
			// Engine policy, see maxConsecutiveEmptyAcks.
			if len(acks) == 0 {
				if empty++; empty >= maxConsecutiveEmptyAcks {
					t.metrics.ObserveError()
					return t.errEmptyAckFlood(len(positions) - ackCount)
				}
				continue
			}
			empty = 0
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

// startFlusher starts the flusher goroutine that owns Destination.Write/Ack.
// It is called from Open, once, when pipelining and batching are both enabled.
// ctx is the run's context (the tomb context): it is alive during normal
// processing and graceful stop, and cancelled on a fatal error or force-stop,
// which is what unblocks a flusher wedged in a destination Ack.
func (t *DestinationTask) startFlusher(ctx context.Context) {
	t.flushCh = make(chan []pendingBatch, maxInFlightFlushes)
	t.flusherDone = make(chan struct{})
	go t.runFlusher(ctx)
}

// runFlusher is the flusher goroutine. It is the ONLY caller of
// Destination.Write/Ack, so the destination's single gRPC stream stays
// single-writer and position-ordered (Invariant 4). It drains flushes in
// enqueue order; when the channel is closed (Close) it finishes the trailing
// flush and exits.
//
// Invariant 1 (enforcement site): a source batch is acked (inside
// flushPending) only after the destination has written its records and returned
// their acks. On a flush error the flusher records it, exits, and leaves any
// still-queued flushes un-acked so they replay on restart (Invariant 3).
func (t *DestinationTask) runFlusher(ctx context.Context) {
	defer close(t.flusherDone)
	for {
		select {
		case <-ctx.Done():
			// Run context cancelled (fatal error / force-stop): stop flushing.
			// In-flight and queued records are un-acked and replay on restart.
			return
		case pending, ok := <-t.flushCh:
			if !ok {
				return // channel closed by Close after the trailing flush
			}
			if err := t.flushPending(ctx, pending); err != nil {
				t.setFlushErr(err)
				return
			}
		}
	}
}

// stopFlusher drains and stops the flusher goroutine before teardown. It is
// called from Close, after every worker has exited, so no doBuffered can race
// it. It enqueues any trailing partial batch, closes the channel so the flusher
// drains, waits for it to exit, and returns any flush error (which Close logs,
// not returns, so teardown still runs — the records replay on restart).
func (t *DestinationTask) stopFlusher(ctx context.Context) error {
	t.mu.Lock()
	t.closed = true
	var trailing []pendingBatch
	if len(t.pending) > 0 {
		trailing = t.pending
		t.pending = nil
		t.bufferedCount = 0
		t.flushDeadline = time.Time{}
	}
	t.mu.Unlock()

	if len(trailing) > 0 {
		select {
		case t.flushCh <- trailing:
		case <-t.flusherDone:
			// The flusher already exited (a prior error, or ctx cancellation);
			// the trailing records replay on restart.
		}
	}
	close(t.flushCh)
	<-t.flusherDone
	return t.flushError()
}

// enqueueFlush hands a full buffer to the flusher goroutine and returns
// errBuffered so the worker reads the next source batch instead of blocking on
// the flush. A full channel blocks here — that is the read-ahead credit's
// backpressure: the source stalls when the destination is slower than it. A
// dead/errored flusher (flusherDone closed) or a cancelled run unblocks the
// enqueuer so it fails instead of hanging.
func (t *DestinationTask) enqueueFlush(ctx context.Context, pending []pendingBatch) error {
	select {
	case t.flushCh <- pending:
		return errBuffered
	case <-t.flusherDone:
		if err := t.flushError(); err != nil {
			return err
		}
		return cerrors.Errorf("destination %s flusher stopped before flushing %d buffered batches", t.id, len(pending))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setFlushErr records the first flush error. Called only by the flusher
// goroutine; read by doBuffered/Close under mu.
func (t *DestinationTask) setFlushErr(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.flushErr == nil {
		t.flushErr = err
	}
}

// flushError returns the first flush error recorded by the flusher, if any.
func (t *DestinationTask) flushError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.flushErr
}
