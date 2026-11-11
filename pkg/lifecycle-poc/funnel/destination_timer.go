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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// The flush timer bounds how long a partial destination batch waits when the
// source goes quiet (#3231, docs/design-documents/20261103-archv2-destination-batch-timing.md,
// Decision 1). Without it a buffer was flushed only by size, by the next
// arrival past sdk.batch.delay, or by a stop, so an idle source held its tail
// unwritten and unacked without bound.
//
// One one-shot time.AfterFunc per buffer, armed under mu by the first record
// of an empty buffer (bufferBatch), with the same deadline as flushDeadline.
// Every take of the buffer goes through resetBufferedLocked, which stops the
// timer and bumps bufferGen, so a callback that lost the race to a size flush,
// an arrival past the deadline, a queued-ack release, Drain or Close finds a
// different generation and does nothing. The generation is the only no-op
// check that is not terminal (closed, flushErr and an ended run context are),
// so a no-op callback never leaves a non-empty buffer of its own generation
// without a timer.
//
// The timer never adds a path to the source ack:
//
//   - Pipelined (one destination): the callback takes the buffer under
//     enqueueMu and hands it to the flusher, exactly as doBuffered does. The
//     flusher still does every Write, Ack and source ack, in channel order.
//   - Synchronous (fan-out): the callback takes the processing lock of every
//     worker that feeds the task, in worker order, and then flushes the way
//     Worker.Stop's drain does (shared boundary mutex, syncMu, mu). While it
//     holds the processing locks no pass of those workers is in flight, so its
//     source acks are serialised with every pass, the property fan-out is
//     synchronous for.

// FlushTrigger names what took a destination buffer for writing. It is the
// trigger label of conduit_funnel_destination_flushes_total.
type FlushTrigger string

const (
	// FlushTriggerSize: the buffer reached sdk.batch.size records (or as many
	// queued entries).
	FlushTriggerSize FlushTrigger = "size"
	// FlushTriggerBytes: the buffer reached the byte budget of one Write
	// (connectors.max-receive-record-size).
	FlushTriggerBytes FlushTrigger = "bytes"
	// FlushTriggerDeadlineArrival: a record or a queued source ack arrived after
	// the buffer's sdk.batch.delay deadline had passed.
	FlushTriggerDeadlineArrival FlushTrigger = "deadline_arrival"
	// FlushTriggerDeadlineTimer: the flush timer fired at the buffer's
	// sdk.batch.delay deadline with no arrival (partial batch flushed by timer).
	FlushTriggerDeadlineTimer FlushTrigger = "deadline_timer"
	// FlushTriggerAckQueue: a queued source ack (filtered or upstream-nacked
	// records) released the buffer.
	FlushTriggerAckQueue FlushTrigger = "ack_queue"
	// FlushTriggerDrain: Worker.Stop or a source's end drained the buffer
	// before the source was torn down.
	FlushTriggerDrain FlushTrigger = "drain"
	// FlushTriggerClose: Close flushed what was left before teardown.
	FlushTriggerClose FlushTrigger = "close"
)

// FlushMetrics observes the takes of a batching destination's buffer. The
// production implementation backs conduit_funnel_destination_flushes_total and
// conduit_funnel_destination_flush_linger_seconds.
type FlushMetrics interface {
	// ObserveFlush counts one take of a non-empty buffer.
	ObserveFlush(trigger FlushTrigger)
	// ObserveLinger records the age of the oldest buffered record when its
	// buffer was taken. It is not called for a buffer that held only queued
	// source acks.
	ObserveLinger(linger time.Duration)
}

// NoOpFlushMetrics discards every observation.
type NoOpFlushMetrics struct{}

func (NoOpFlushMetrics) ObserveFlush(FlushTrigger)   {}
func (NoOpFlushMetrics) ObserveLinger(time.Duration) {}

// SetFlushMetrics sets the metrics a batching task reports its buffer takes
// to. Must be called before Open; the default discards them.
func (t *DestinationTask) SetFlushMetrics(m FlushMetrics) {
	if m == nil {
		m = NoOpFlushMetrics{}
	}
	t.flushMetrics = m
}

// observeTakeLocked reports a take of taken, the whole buffer, by trigger. It
// must be called before resetBufferedLocked, which clears bufferedSince, and
// with t.mu held. It returns the linger (0 for a buffer without records).
func (t *DestinationTask) observeTakeLocked(trigger FlushTrigger, taken []pendingBatch, now time.Time) time.Duration {
	if len(taken) == 0 {
		return 0
	}
	t.flushMetrics.ObserveFlush(trigger)
	if t.bufferedSince.IsZero() {
		return 0
	}
	linger := now.Sub(t.bufferedSince)
	t.flushMetrics.ObserveLinger(linger)
	return linger
}

// takeLocked takes the whole buffer for one flush, counting it in outstanding,
// and reports the take. t.mu must be held.
func (t *DestinationTask) takeLocked(trigger FlushTrigger) (taken []pendingBatch, records int, linger time.Duration) {
	taken = t.pending
	records = t.bufferedCount
	linger = t.observeTakeLocked(trigger, taken, time.Now())
	t.pending = nil
	t.resetBufferedLocked()
	t.outstanding++
	return taken, records, linger
}

// armFlushTimerLocked arms the one-shot flush timer for the current buffer
// generation. Called once per buffer, by the arrival of its first record, when
// the buffer is not flushed on the spot. t.mu must be held.
//
// Size-only batching (sdk.batch.size > 1, sdk.batch.delay 0) gets no timer:
// the SDK has none either. The engine warns about it at build time
// (CodeArchV2DestinationBatchUnboundedWait).
func (t *DestinationTask) armFlushTimerLocked() {
	if t.batchDelay <= 0 || t.flushTimer != nil {
		return
	}
	gen := t.bufferGen
	t.flushTimer = time.AfterFunc(t.batchDelay, func() { t.flushDue(gen) })
}

// stopFlushTimerLocked stops the flush timer, if armed. A callback that is
// already running is not waited for: it finds the new generation, closed or
// the flush error and returns. t.mu must be held.
func (t *DestinationTask) stopFlushTimerLocked() {
	if t.flushTimer != nil {
		t.flushTimer.Stop()
		t.flushTimer = nil
	}
}

// timerDueLocked reports whether the timer armed for generation gen may still
// take the buffer: nothing took it since (same generation), it is not empty,
// the task is not closed and has not failed. t.mu must be held.
//
// closed is a second guard, not the one that usually decides. Pipelined,
// stopFlusher sets closed and takes (new generation) in one enqueueMu section,
// so the generation check fails first. Synchronous, it decides only in the
// window between Close setting closed and Close's own flush taking the buffer;
// a timer that won syncMu there would flush what Close is about to flush, under
// the same locks. It keeps "nothing after Close" from resting on the
// generation reasoning alone.
func (t *DestinationTask) timerDueLocked(gen uint64) bool {
	return gen == t.bufferGen && len(t.pending) > 0 && !t.closed && t.flushErr == nil
}

// addFeeder registers w as a worker whose passes reach this task, below the
// shared boundary node shared (nil if none). NewWorker calls it for every
// batching destination below the worker's first task. The task tree, and so
// this list, is fixed before any worker starts, and workers are built in order,
// so every task sees its feeders in the same global order (see flushDueSync).
//
// Every feeder must reach the task through the same shared boundary node,
// because the synchronous timer flushes under that one node's mutex and
// poisons that one node on failure. A second feeder with a different node, or
// a second feeder where either has none (the roots were not marked before the
// workers were built, MarkSharedBoundary's contract), is a build bug: it returns an error
// and leaves the task unchanged, as validateTasks does for a bad task order.
func (t *DestinationTask) addFeeder(w *Worker, shared *TaskNode) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, f := range t.feeders {
		if f == w {
			return nil
		}
	}
	if len(t.feeders) > 0 {
		switch {
		case shared == nil || t.feederShared == nil:
			// Either feeder reaching the task with no node means the roots
			// were not marked when one of the workers was built.
			return cerrors.Errorf("(bug) destination %s is reached by more than one worker, not all of them through a shared boundary node; mark the shared roots (NewSink) before building workers", t.id)
		case shared != t.feederShared:
			return cerrors.Errorf("(bug) destination %s is reached by workers below different shared boundary nodes", t.id)
		}
	}
	t.feeders = append(t.feeders, w)
	t.feederShared = shared
	return nil
}

// FeederBoundary returns the workers registered as feeders of this batching
// task, in build order, and the shared boundary node they reach it through
// (nil if none). It is read-only: it lets the pipeline builder's tests check
// that the roots were marked before the workers were built, which the
// synchronous timer relies on to flush under the shared mutex.
func (t *DestinationTask) FeederBoundary() ([]*Worker, *TaskNode) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*Worker(nil), t.feeders...), t.feederShared
}

// flushDue is the flush timer's callback for buffer generation gen.
func (t *DestinationTask) flushDue(gen uint64) {
	if t.testBeforeTimerFlush != nil {
		t.testBeforeTimerFlush()
	}
	if t.flushCh != nil {
		t.flushDuePipelined(gen)
		return
	}
	t.flushDueSync(gen)
}

// flushDuePipelined takes the buffer and hands it to the flusher.
//
// Invariant 1/2/4 (enforcement site): this callback never calls Write, Ack or
// a source ack. It takes the buffer under enqueueMu, as doBuffered does, so an
// ordered source ack deferOrdered queues stays behind the records ahead of it,
// and the flusher, still the only writer, writes and acks in channel order.
func (t *DestinationTask) flushDuePipelined(gen uint64) {
	ctx := t.runCtx
	if err := t.enqueueMu.LockCtx(ctx); err != nil {
		return // the run ended: the buffer is unacked and replays
	}
	defer t.enqueueMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	t.mu.Lock()
	// closed is set by stopFlusher under enqueueMu before it closes flushCh, so
	// a callback that gets here after it never sends on a closed channel.
	if !t.timerDueLocked(gen) {
		t.mu.Unlock()
		return
	}
	toFlush, records, linger := t.takeLocked(FlushTriggerDeadlineTimer)
	t.mu.Unlock()
	t.logTimerFlush(ctx, records, linger)
	// Invariant 3: enqueueFlush either hands the buffer over or records the
	// failure (fail closed) and reports it to the run; nothing is acked here.
	_ = t.enqueueFlush(ctx, toFlush)
}

// flushDueSync flushes the buffer on the synchronous (fan-out) path, under the
// processing lock of every worker that feeds this task.
//
// Invariant 1/2 (enforcement site): fan-out is synchronous so that every source
// ack of a worker is issued inside one pass at a time. The timer holds every
// feeder's processing lock for the whole flush, so no pass of those workers is
// in flight and its source acks (including a sibling's deferred nack whose
// last vote this flush casts, #3324) are serialised with every pass, exactly
// as Worker.Stop's drain is.
//
// Lock order: processing locks (feeder order), shared boundary mutex, syncMu,
// mu. A pass holds only its own worker's processing lock before the shared
// mutex, Worker.Stop and a source's end hold only their own, and timers are the
// only holders of several, always in the same order, so no cycle is possible.
// The callback holds nothing when it starts.
func (t *DestinationTask) flushDueSync(gen uint64) {
	ctx := t.runCtx
	t.mu.Lock()
	feeders, shared := t.feeders, t.feederShared
	due := t.timerDueLocked(gen)
	t.mu.Unlock()
	if !due || ctx == nil {
		return
	}
	for i, w := range feeders {
		release, err := w.acquireProcessingLock(ctx)
		if err != nil {
			return // the run ended: the buffer is unacked and replays
		}
		defer release()
		if t.testAfterFeederLock != nil {
			t.testAfterFeederLock(i)
		}
	}
	if ctx.Err() != nil {
		return
	}
	if t.testTimerHoldsLocks != nil {
		t.testTimerHoldsLocks()
	}
	// The same step drainNode runs for one task: under the shared boundary
	// mutex, poisoning the shared node before releasing it if the flush fails.
	err := underSharedLock(ctx, t, shared, func(ctx context.Context) error {
		return t.flushTimerSync(ctx, gen)
	})
	if err != nil && !isDrainNotStarted(err) {
		// finishFlush recorded it as the flush error and reported it to the
		// run; nothing behind it is acked (invariant 3).
		t.logger.Debug(ctx).Err(err).Msg("timer flush failed")
	}
}

// flushTimerSync is flush for the timer: it takes the buffer only if it is
// still the one the timer was armed for. syncMu before pending, as in
// doBuffered: take and flush are one unit.
func (t *DestinationTask) flushTimerSync(ctx context.Context, gen uint64) error {
	t.syncMu.Lock()
	t.mu.Lock()
	// Invariant 3: a closed task may already be torn down, and after a flush
	// error nothing queued may run; both are checked in timerDueLocked.
	if !t.timerDueLocked(gen) {
		t.mu.Unlock()
		t.syncMu.Unlock()
		return nil
	}
	pending, records, linger := t.takeLocked(FlushTriggerDeadlineTimer)
	t.mu.Unlock()
	t.logTimerFlush(ctx, records, linger)
	err := t.flushTaken(ctx, pending)
	t.syncMu.Unlock()
	if err != nil {
		return err
	}
	return t.releaseSync(ctx)
}

func (t *DestinationTask) logTimerFlush(ctx context.Context, records int, linger time.Duration) {
	t.logger.Debug(ctx).
		Int("records", records).
		Dur("linger", linger).
		Msg("partial batch flushed by timer")
}
