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
	"io"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
)

// This file is the staged engine of one source (PROTOTYPE, see the PR). The
// stop-and-wait loop it replaces read a batch, ran it to the destination, waited
// for every ack, called Source.Ack and only then read again, so throughput was
// 1 / (read + process + write + wait for acks + ack).
//
// Goroutines per Worker:
//
//	reader       the only caller of the source task's Do (Source.Read). Gated by
//	             credits. Registers each batch in the ledger, which stamps its
//	             sequence number, and queues it.
//	runner       Worker.Do itself. Runs the source's processors and dispatches
//	             to the destinations. Destination tasks only SUBMIT.
//	coordinator  the only caller of Source.Ack and of DLQ writes. Releases the
//	             contiguous completed prefix from the ledger, returns credits.
//
// plus, per destination (owned by the DestinationTask, shared by all workers
// writing to it), an AckReader that turns destination acks into ledger votes.
//
// Not built in this prototype: merge stage for pipeline-level processors (the
// shared tail keeps its mutex when it holds processors), runner-side natural
// batching and linger, per-destination inboxes and write coalescing, and the
// Source.Stop read-until-last-position stop protocol (a graceful stop drains,
// then tears the source down).

// readResult is a registered batch on its way from the reader to the runner.
type readResult struct {
	b *Batch
	e *ledgerEntry
}

type pipelineState struct {
	ledger  *ledger
	credits *credits

	// admitMu guards quiesced and admitChanged. The reader registers a batch
	// under it, so after setQuiesced(true) returns no new batch can appear.
	admitMu      sync.Mutex
	quiesced     bool
	admitChanged chan struct{}

	stopCh   chan struct{} // closed when the stop flag is armed
	stopOnce sync.Once

	readCh chan readResult
	// readErr and exhausted are written by the reader before it closes readCh
	// and read by the runner after it observed the close.
	readErr   error
	exhausted bool

	cancel   context.CancelFunc
	readerWG sync.WaitGroup
	coordWG  sync.WaitGroup
}

func newPipelineState() *pipelineState {
	return &pipelineState{
		ledger:       newLedger(),
		credits:      newCredits(defaultCreditRecords, defaultCreditBytes),
		admitChanged: make(chan struct{}),
		stopCh:       make(chan struct{}),
		readCh:       make(chan readResult, 1024),
	}
}

func (p *pipelineState) setQuiesced(q bool) {
	p.admitMu.Lock()
	p.quiesced = q
	close(p.admitChanged)
	p.admitChanged = make(chan struct{})
	p.admitMu.Unlock()
}

// arm sets the stop flag and wakes a reader parked on it.
func (w *Worker) arm() {
	w.stop.Store(true)
	if p := w.pl; p != nil {
		p.stopOnce.Do(func() { close(p.stopCh) })
		p.admitMu.Lock()
		close(p.admitChanged)
		p.admitChanged = make(chan struct{})
		p.admitMu.Unlock()
	}
}

// shutdown cancels and joins the worker's goroutines. The reader is joined
// only when joinReader is set, i.e. after the source teardown released it from
// a blocked Read. Safe on a worker that never ran.
func (p *pipelineState) shutdown(joinReader bool) {
	if p == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.coordWG.Wait()
	if joinReader {
		p.readerWG.Wait()
	}
}

func (w *Worker) doPipelined(ctx context.Context) error {
	p := w.pl
	w.pipelined.Store(true)

	workCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	defer func() {
		p.ledger.markRunnerExited()
		cancel()
		p.coordWG.Wait()
	}()

	p.coordWG.Add(1)
	go func() {
		defer p.coordWG.Done()
		w.coordinate(workCtx)
	}()
	p.readerWG.Add(1)
	go func() {
		defer p.readerWG.Done()
		w.readLoop(workCtx)
	}()

	for {
		select {
		case rb, ok := <-p.readCh:
			if !ok {
				return w.finishReading(ctx)
			}
			w.logger.Trace(ctx).Uint64("seq", rb.e.seq).Int("batch_size", len(rb.b.records)).Msg("starting next batch")
			// Invariant 1/3 (enforcement site, #2723): a fresh runAckNacker
			// per pass; its terminal acker is this batch's ledger entry, NOT
			// the worker, so nothing reaches Source.Ack except through the
			// coordinator's in-order prefix release.
			if err := w.afterTask(ctx, w.FirstTask, rb.b, newRunAckNacker(rb.e), nil); err != nil {
				return err
			}
			p.ledger.markDispatched()
		case <-p.ledger.failCh:
			return p.ledger.failure()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// finishReading runs once the reader exited and every queued batch was
// dispatched. It waits for the ledger to drain so Do never returns (and the
// caller never tears anything down) while a position is still unreleased.
func (w *Worker) finishReading(ctx context.Context) error {
	p := w.pl
	if p.readErr != nil {
		return p.readErr
	}
	if err := p.ledger.waitDrained(ctx); err != nil {
		return err
	}
	if p.exhausted {
		// Everything that was read is released; now the source can go. See
		// the io.EOF discussion in doTaskAttempt for why this is graceful.
		w.arm()
		if tdErr := w.tearDownSource(ctx); tdErr != nil {
			return cerrors.Errorf("source finished but failed to tear down: %w", tdErr)
		}
	}
	return nil
}

// readLoop is the reader goroutine.
func (w *Worker) readLoop(ctx context.Context) {
	p := w.pl
	defer close(p.readCh)
	task := w.FirstTask.Task

	for {
		if !p.credits.wait(ctx, p.stopCh) || w.stop.Load() {
			return
		}

		b := &Batch{}
		err := task.Do(ctx, b)
		if err != nil {
			p.readErr = w.classifyReadError(ctx, task, err)
			return
		}
		now := time.Now()
		w.lastReadAt.Store(now.UnixNano())
		if len(b.records) == 0 {
			continue
		}

		var size int64
		for _, r := range b.records {
			size += estimateRecordSize(r)
		}

		// Admission. A quiesced worker holds the batch instead of dropping
		// it, so a rolled-back Stop loses nothing (Invariant 3). Only an
		// armed stop discards it, and an unregistered batch is never acked,
		// so it replays on restart.
		p.admitMu.Lock()
		for p.quiesced && !w.stop.Load() {
			ch := p.admitChanged
			p.admitMu.Unlock()
			select {
			case <-ch:
			case <-ctx.Done():
				return
			}
			p.admitMu.Lock()
		}
		if w.stop.Load() {
			p.admitMu.Unlock()
			w.logger.Warn(ctx).
				Str("task_id", task.ID()).
				Int("batch_size", len(b.records)).
				Msg("stop signal received just after reading a batch, gracefully stopping without flushing the batch")
			return
		}
		p.credits.charge(int64(len(b.records)), size)
		e := p.ledger.register(b, size, now)
		p.admitMu.Unlock()

		select {
		case p.readCh <- readResult{b: b, e: e}:
		case <-ctx.Done():
			return
		}
	}
}

// classifyReadError maps a Source.Read error the way the stop-and-wait loop
// did: a cancel or a torn-down plugin during a stop is graceful, io.EOF means
// the source is exhausted (graceful, this source only), anything else fails.
func (w *Worker) classifyReadError(ctx context.Context, task Task, err error) error {
	p := w.pl
	if cerrors.Is(err, context.Canceled) ||
		(cerrors.Is(err, plugin.ErrPluginNotRunning) && w.stop.Load()) {
		if ctx.Err() == nil && !w.stop.Load() {
			// The stop-and-wait loop swallowed this and read again, spinning on
			// a dead stream. A source that cancels its own read when nobody
			// asked it to stop has failed; say so.
			return cerrors.Errorf("task %s: source read was canceled but no stop was requested: %w", task.ID(), err)
		}
		return ctx.Err()
	}
	if cerrors.Is(err, io.EOF) {
		w.logger.Info(ctx).
			Str("source_id", task.ID()).
			Bool("stop_requested", w.stop.Load()).
			Msg("source exhausted its records (io.EOF) and is stopping gracefully; sibling sources, if any, are unaffected")
		p.exhausted = true
		return nil
	}
	return cerrors.Errorf("task %s: %w", task.ID(), err)
}

// coordinate is the coordinator goroutine: the only place Source.Ack and DLQ
// writes happen.
func (w *Worker) coordinate(ctx context.Context) {
	l := w.pl.ledger
	for {
		select {
		case <-l.signal:
		case <-ctx.Done():
			return
		}
		if err := w.releasePrefix(ctx); err != nil {
			l.fail(err)
			return
		}
	}
}

// releasePrefix releases everything that is releasable right now.
//
// Invariant 2: the single caller of Source.Ack, always with the next positions
// in source order. After an error nothing further is released. After the
// context is canceled nothing further is released either (force stop: the
// unreleased tail replays).
func (w *Worker) releasePrefix(ctx context.Context) error {
	l := w.pl.ledger
	for ctx.Err() == nil {
		a := l.next()
		if a == nil {
			return nil
		}
		if a.nack {
			// Invariant 3: the DLQ write is confirmed before the position is
			// acked to the source. Worker.Nack acks only what the DLQ accepted
			// and fails the pipeline on a DLQ error or an exceeded threshold.
			nb := &Batch{
				records:        []opencdc.Record{a.nackInfo.rec},
				recordStatuses: []RecordStatus{{Flag: RecordFlagNack, Error: a.nackInfo.err}},
				positions:      []opencdc.Position{a.nackPos},
				tainted:        true,
			}
			if err := w.Nack(ctx, nb, a.nackInfo.taskID); err != nil {
				return err
			}
		} else {
			// Invariant 1: every position in a.positions has a terminal ack
			// vote, and everything before it was released already.
			ab := &Batch{
				records:        a.records,
				recordStatuses: make([]RecordStatus, len(a.positions)),
				positions:      a.positions,
			}
			if err := w.Ack(ctx, ab); err != nil {
				return err
			}
		}
		l.advance(a, w.pl.credits)
	}
	return nil
}
