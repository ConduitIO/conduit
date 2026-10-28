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

// deferredAckError marks an error from a queued source ack/nack (see
// deferOrdered), e.g. a processor nack the DLQ refused. Close returns it, unlike
// other flush errors it only logs, because it is a pipeline failure the worker
// would have returned immediately before the ack was queued.
type deferredAckError struct{ err error }

func (e *deferredAckError) Error() string { return "deferred source ack failed: " + e.err.Error() }
func (e *deferredAckError) Unwrap() error { return e.err }

// isDeferredAckError reports whether err comes from a queued source ack/nack.
func isDeferredAckError(err error) bool {
	var d *deferredAckError
	return cerrors.As(err, &d)
}

// pendingBatch is one source batch whose records are buffered, awaiting flush.
// It carries the source *Batch (to mark per-record nacks and to ack/nack) and
// the ackNacker that would have acked it had it been written immediately.
//
// A pendingBatch with a non-nil action carries no records: it is an ordered
// source ack/nack (see deferOrdered) that must run only after every earlier
// pendingBatch has been written and acked.
type pendingBatch struct {
	batch *Batch
	acker ackNacker
	// action, when set, replaces the batch/acker pair: it is the source
	// ack/nack of records that bypassed the destination (filtered or nacked
	// upstream), queued so it is released in source order behind the records
	// buffered ahead of it.
	action func(context.Context) error
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
	if t.testAfterBufferErrCheck != nil {
		t.testAfterBufferErrCheck()
	}

	records := batch.ActiveRecords()
	if len(records) == 0 {
		// Nothing to write (all records filtered upstream): no destination
		// write is owed for it. It may only be acknowledged once every earlier
		// batch is acknowledged, though: Source.Ack persists the LAST position
		// it is given, so acking this batch ahead of records still buffered or
		// in flight would persist a position past records that are not durable
		// (Invariant 1/2/3). deferOrdered acks immediately when nothing is
		// ahead of it, and queues the ack behind the earlier batches otherwise.
		return t.deferOrdered(ctx, func(ctx context.Context) error {
			return acker.Ack(ctx, batch)
		})
	}

	if t.flushCh != nil {
		// Invariant 1: "take pending + hand it to the flusher" is one unit with
		// respect to deferOrdered (another source's worker, outside any shared
		// lock). Without it that worker could queue its source ack on flushCh
		// between our take and our send, and the flusher would release it ahead
		// of the batch we took.
		t.enqueueMu.Lock()
		defer t.enqueueMu.Unlock()
	}

	t.mu.Lock()
	// Invariant 3: re-check under the lock that decides. The early check above
	// is only a fast path; a flush can fail between it and here, and buffering
	// (then flushing and acking) this batch would move the source past the
	// batches that flush dropped.
	if err := t.flushErr; err != nil {
		t.mu.Unlock()
		return err
	}
	if t.closed {
		t.mu.Unlock()
		return cerrors.Errorf("destination %s is closed", t.id)
	}
	now := time.Now()
	if t.bufferedCount == 0 && t.batchDelay > 0 {
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
		// Counted on the synchronous path too: another source's filter or nack
		// runs outside the shared lock and must see this write as ahead of it.
		// Released by runFlusher (pipelined) or below (synchronous) once the
		// flush is fully acked.
		t.outstanding++
	}
	t.mu.Unlock()

	if !shouldFlush {
		return errBuffered
	}
	if t.testAfterTake != nil {
		t.testAfterTake()
	}
	if t.flushCh == nil {
		// Pipelining not running (fan-out, or Open not called): flush
		// synchronously on the worker, the pre-pipelining behavior.
		err := t.flushPending(ctx, toFlush)
		// Invariant 3: the flush may have taken a sibling's batches with it.
		// finishFlush records the failure in the same step that releases the
		// slot, so deferOrdered (reached from the sibling's own processors,
		// outside the shared lock) and doBuffered fail closed instead of acking
		// a later batch past the gap.
		t.finishFlush(err)
		return err
	}
	return t.enqueueFlush(ctx, toFlush)
}

// deferOrdered runs ack (a source ack or nack for records that bypass this
// destination: all filtered, or nacked by a processor) in source order relative
// to the batches already handed to this task.
//
// Invariant 1/2/3 (enforcement site): connector.Source.Ack persists the last
// position it is given and has no monotonic guard, so an ack issued while an
// earlier batch is buffered or in flight would persist a position past records
// that are not yet durable, and a crash then skips them. If nothing is buffered,
// queued or in flight the ack runs inline; otherwise it is appended to the
// pending buffer, behind the earlier batches, and runs on the flusher (or on the
// synchronous flush) strictly after they have been written and acked.
//
// An action-only buffer (nothing active ahead of it) is handed to the flusher
// at once so the position is not held back until the next record arrives.
func (t *DestinationTask) deferOrdered(ctx context.Context, ack func(context.Context) error) error {
	if err := t.flushError(); err != nil {
		return err
	}
	if t.testAfterDeferErrCheck != nil {
		t.testAfterDeferErrCheck()
	}
	if t.flushCh != nil {
		t.enqueueMu.Lock() // see doBuffered
		defer t.enqueueMu.Unlock()
	}

	t.mu.Lock()
	// Invariant 3: read the flush error in the same critical section that decides
	// inline-vs-queue. The early check above is only a fast path: a flush that
	// fails after it releases its slot and records its error atomically
	// (finishFlush), and this section would otherwise see an idle task and ack
	// inline past the batch that flush dropped.
	if err := t.flushErr; err != nil {
		t.mu.Unlock()
		return err
	}
	if t.closed {
		// Close can overlap a Stop that is still draining, so "closed" does not
		// prove nothing is ahead: refuse rather than ack inline.
		t.mu.Unlock()
		return cerrors.Errorf("destination %s is closed", t.id)
	}
	// guard: funnel-ordered-source-ack tests=^(TestWorker_FilteredBatch_AckOrderedBehindInflightFlush|TestWorker_ProcessorNack_AckOrderedBehindBufferedRecords)$
	if len(t.pending) > 0 || t.outstanding > 0 {
		t.pending = append(t.pending, pendingBatch{action: ack})
		var toFlush []pendingBatch
		// Hand the buffer to the flusher now when no record is waiting in it,
		// when the queue (actions count: each pins a *Batch and the source
		// position) holds a full batch worth of entries, or when the flush
		// deadline has passed. Only the pipelined path can: the synchronous
		// path flushes on the worker under the shared lock, which this caller
		// does not hold, so there the ack waits for the next record or Close.
		if t.flushCh != nil &&
			(t.bufferedCount == 0 ||
				len(t.pending) >= t.effectiveBatchSize() ||
				(t.batchDelay > 0 && !t.flushDeadline.IsZero() && time.Now().After(t.flushDeadline))) {
			toFlush = t.pending
			t.pending = nil
			t.bufferedCount = 0
			t.flushDeadline = time.Time{}
			t.outstanding++
		}
		t.mu.Unlock()
		if toFlush == nil {
			return nil
		}
		if err := t.enqueueFlush(ctx, toFlush); !isBuffered(err) {
			return err
		}
		return nil
	}
	t.mu.Unlock()
	return ack(ctx)
}

// flush writes all buffered records (taken from t.pending) in one Write. It is
// the synchronous path used when pipelining is not running (fan-out) and by
// Close; see flushPending for the write/ack work itself.
func (t *DestinationTask) flush(ctx context.Context) error {
	t.mu.Lock()
	// Invariant 3: after a failed flush what is still pending (records and source
	// acks queued behind the batch that failed) must not run: those acks would
	// move the source past the batch the failed flush dropped. It stays
	// unacknowledged and replays on restart.
	if err := t.flushErr; err != nil {
		t.mu.Unlock()
		return err
	}
	pending := t.pending
	t.pending = nil
	t.bufferedCount = 0
	t.flushDeadline = time.Time{}
	t.outstanding++
	t.mu.Unlock()
	err := t.flushPending(ctx, pending)
	t.finishFlush(err)
	return err
}

// finishFlush releases a flush's slot in outstanding and, if the flush failed,
// records the flush error in the SAME critical section.
//
// Invariant 3 (enforcement site): deferOrdered treats "nothing pending and
// outstanding == 0" as "nothing ahead of me" and acks inline. If the slot were
// released first and the error recorded afterwards, a sibling arriving between
// the two would see an idle task with no error and ack past the batch the failed
// flush took.
func (t *DestinationTask) finishFlush(err error) {
	t.mu.Lock()
	t.outstanding--
	if err != nil && t.flushErr == nil {
		t.flushErr = err
	}
	t.mu.Unlock()
	if t.testAfterFlushRelease != nil {
		t.testAfterFlushRelease()
	}
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
		if p.action != nil {
			continue // ordered source ack, run with the acks below
		}
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
	if p.action != nil {
		if err := p.action(ctx); err != nil {
			return &deferredAckError{err: err}
		}
		return nil
	}
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
	// On every exit that leaves work behind (cause != ""), close flusherDone FIRST
	// and drain flushCh AFTER it. A sender that wins the send select just as we
	// exit then either lands before the drain (we drop and count it) or after
	// flusherDone is closed (enqueueFlush sees that and reclaims it itself), so
	// no batch is left in the channel with nobody to complete or count it.
	cause := ""
	defer func() {
		close(t.flusherDone)
		if cause != "" {
			t.dropQueued(ctx, cause)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			// Run context cancelled (fatal error / force-stop): stop flushing.
			// In-flight and queued records are un-acked and replay on restart.
			// Fail closed so nothing queues behind a flusher that is gone.
			t.setFlushErr(cerrors.Errorf("destination %s flusher stopped: %w", t.id, ctx.Err()))
			cause = "run context ended"
			return
		case pending, ok := <-t.flushCh:
			if !ok {
				return // channel closed by Close after the trailing flush
			}
			// Invariant 3: the flush error can be set by paths other than this
			// goroutine's own failure (doTask poisoning the shared node, a failed
			// drain restoring batches to the front of pending). Once it is set,
			// do not write or ack what is still queued: acking it could move the
			// source past batches that were left unacknowledged. It replays.
			// Checked under mu (flushError), where the flush error is recorded.
			if t.flushError() != nil {
				t.finishFlush(nil) // release this item's slot only
				cause = "flush error already set"
				return
			}
			err := t.flushPending(ctx, pending)
			t.finishFlush(err)
			if err != nil {
				cause = "flush failed"
				return
			}
		}
	}
}

// dropQueued discards what is still queued in flushCh when the flusher exits,
// releasing a slot in outstanding for each and logging how many batches will
// replay. The records were never acknowledged upstream.
func (t *DestinationTask) dropQueued(ctx context.Context, cause string) {
	if n := t.dropQueuedCount(ctx); n > 0 {
		t.logger.Warn(ctx).Int("dropped_batches", n).Str("cause", cause).Msg("queued batches not flushed (they replay on restart)")
	}
}

func (t *DestinationTask) dropQueuedCount(context.Context) int {
	n := 0
	for {
		select {
		case _, ok := <-t.flushCh:
			if !ok {
				return n // closed by Close
			}
			n++
			t.mu.Lock()
			t.outstanding--
			t.mu.Unlock()
		default:
			return n
		}
	}
}

// stopFlusher drains and stops the flusher goroutine before teardown. It is
// called from Close, after every worker has exited, so no doBuffered can race
// it. It enqueues any trailing partial batch, closes the channel so the flusher
// drains, waits for it to exit, and returns any flush error (which Close logs,
// not returns, so teardown still runs — the records replay on restart).
func (t *DestinationTask) stopFlusher(ctx context.Context) error {
	// enqueueMu orders this against every hand-off to flushCh (doBuffered,
	// deferOrdered, Drain): closing the channel while one is blocked in its send
	// would panic the process. A holder's send always resolves (send, flusherDone
	// or its ctx), so this waits instead.
	t.enqueueMu.Lock()
	defer t.enqueueMu.Unlock()

	t.mu.Lock()
	t.closed = true
	var trailing []pendingBatch
	dropped := 0
	if len(t.pending) > 0 {
		if t.flushErr == nil {
			// Invariant 3: after a flush error the trailing batch is not sent (same
			// rule as runFlusher); it stays unacknowledged and replays.
			trailing = t.pending
			t.outstanding++ // same critical section as the take
		} else {
			dropped = len(t.pending)
		}
		t.pending = nil
		t.bufferedCount = 0
		t.flushDeadline = time.Time{}
	}
	t.mu.Unlock()
	if dropped > 0 {
		t.logger.Warn(ctx).Int("dropped_batches", dropped).Msg("trailing batches not flushed after a flush error (they replay on restart)")
	}

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
	var err error
	select {
	case <-t.flusherDone:
		err = cerrors.Errorf("destination %s flusher stopped before flushing %d buffered batches", t.id, len(pending))
	default:
		if t.testBeforeEnqueueSend != nil {
			t.testBeforeEnqueueSend()
		}
		select {
		case t.flushCh <- pending:
			select {
			case <-t.flusherDone:
				// The flusher exited around our send (both select cases can be
				// ready). Our batch may be sitting in the channel with nobody left
				// to write it: reclaim whatever is queued, releasing a slot for
				// each (the flusher's own exit drain may already have taken ours),
				// and fail closed. It was never acknowledged and replays.
				t.dropQueued(ctx, "flusher exited during enqueue")
				t.mu.Lock()
				if t.flushErr == nil {
					t.flushErr = cerrors.Errorf("destination %s flusher stopped while %d batches were being queued", t.id, len(pending))
				}
				ferr := t.flushErr
				t.mu.Unlock()
				return ferr
			default:
				return errBuffered
			}
		case <-t.flusherDone:
			err = cerrors.Errorf("destination %s flusher stopped before flushing %d buffered batches", t.id, len(pending))
		case <-ctx.Done():
			err = cerrors.Errorf("destination %s: enqueue of %d buffered batches abandoned: %w", t.id, len(pending), ctx.Err())
		}
	}
	// Invariant 3 (enforcement site): the taken batches are dropped (never
	// written or acked, they replay). Release the slot and fail closed in one
	// critical section, as drainNotSent does; otherwise a live flusher would go
	// on to release a later queued ack past them.
	t.logger.Warn(ctx).Int("dropped_batches", len(pending)).Err(err).Msg("buffered batches dropped before reaching the flusher (they replay on restart)")
	t.mu.Lock()
	t.outstanding--
	if t.flushErr == nil {
		t.flushErr = err
	}
	ferr := t.flushErr
	t.mu.Unlock()
	return ferr
}

// setFlushErr records the first flush error and makes the task fail closed:
// doBuffered and deferOrdered return it from then on. Called by the flusher
// goroutine, by a failed synchronous flush, and by whoever poisons the shared
// node over this task (see Worker.poisonBatchingDestinations); guarded by mu.
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
