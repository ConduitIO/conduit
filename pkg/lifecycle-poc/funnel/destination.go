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

//go:generate mockgen -typed -destination=destination_mock_test.go -package=funnel . Destination

package funnel

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
)

type DestinationTask struct {
	id          string
	destination Destination
	logger      log.CtxLogger

	metrics ConnectorMetrics

	// async is the staged-engine state, created on first Submit. The DLQ's
	// task never submits and keeps the synchronous Do.
	asyncOnce sync.Once
	async     atomic.Pointer[asyncState]
}

type Destination interface {
	ID() string
	Open(context.Context) error
	Write(context.Context, []opencdc.Record) error
	Ack(context.Context) ([]connector.DestinationAck, error)
	Teardown(context.Context) error
	// TODO figure out if we want to handle these errors. This returns errors
	//  coming from the persister, which persists the connector asynchronously.
	//  Are we even interested in these errors in the pipeline? Sounds like
	//  something we could surface and handle globally in the runtime instead.
	Errors() <-chan error
}

func NewDestinationTask(
	id string,
	destination Destination,
	logger log.CtxLogger,
	metrics ConnectorMetrics,
) *DestinationTask {
	logger = logger.WithComponent("task:destination")
	logger.Logger = logger.With().Str(log.ConnectorIDField, id).Logger()
	return &DestinationTask{
		id:          id,
		destination: destination,
		logger:      logger,
		metrics:     metrics,
	}
}

func (t *DestinationTask) ID() string {
	return t.id
}

func (t *DestinationTask) Open(ctx context.Context) error {
	t.logger.Debug(ctx).Msg("opening destination")
	err := t.destination.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open destination connector: %w", err)
	}
	t.logger.Debug(ctx).Msg("destination open")
	return nil
}

func (t *DestinationTask) Close(ctx context.Context) error {
	t.closeAsync()
	err := t.destination.Teardown(ctx)
	t.joinAsync()
	return err
}

func (t *DestinationTask) Do(ctx context.Context, batch *Batch) error {
	records := batch.ActiveRecords()

	// Store the positions of the records in the batch to be used for
	// validation of acks.
	positions := make([]opencdc.Position, len(records))
	for i, rec := range records {
		positions[i] = rec.Position
	}

	start := time.Now()
	err := t.destination.Write(ctx, records)
	if err != nil {
		return cerrors.Errorf("failed to write %d records to destination: %w", len(positions), err)
	}

	ackCount := 0
	for range len(positions) {
		acks, err := t.destination.Ack(ctx)
		if err != nil {
			return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
		}

		if err := t.validateAcks(acks, positions[ackCount:]); err != nil {
			return cerrors.Errorf("failed to validate acks: %w", err)
		}
		t.metrics.Observe(records[ackCount:ackCount+len(acks)], start)
		t.markBatchRecords(batch, ackCount, acks)

		ackCount += len(acks)
		if ackCount >= len(positions) {
			break
		}
	}

	return nil
}

func (t *DestinationTask) validateAcks(acks []connector.DestinationAck, positions []opencdc.Position) error {
	if len(acks) > len(positions) {
		return cerrors.Errorf("received %d acks, but expected at most %d", len(acks), len(positions))
	}

	for i, ack := range acks {
		if !bytes.Equal(positions[i], ack.Position) {
			return cerrors.Errorf("received unexpected ack, expected position %q but got %q", positions[i], ack.Position)
		}
	}

	return nil
}

// markBatchRecords marks every errored ack in acks as nacked in b.
//
// #2729: iterate end->start, same discipline as ProcessorTask.markBatchRecords
// (#2728) and Do's outer range-collapsing loop. b.Nack resolves "from+i"
// against activeRecordIndices(), which is recomputed on every call; a Nack
// call used to be able to change what that mapping considers active (see
// batch.go's Nack/setFlagWithErr for why that propagation was itself removed
// as the root cause), which shifted the indices of every later entry still to
// be processed in this same forward pass. The confirmed failure: nacking an
// early split-run member reshuffled the active set so a LATER ack for an
// unrelated ordinary record (e.g. p1) resolved to the wrong physical record,
// which both let the genuinely-failed record get acked to the source as a
// success and attached its error to the wrong DLQ entry. Marking end->start
// means a mutation at index i only affects indices ABOVE it, all already
// resolved and acted on - never the lower indices still to come. This is now
// Scope of the guarantee, stated precisely: Do calls this once per
// destination.Ack() return, with an INCREASING `from`, so the global order of
// Nack calls is "chunk 0 reversed, then chunk 1 reversed, ...". The reversal
// therefore protects indices within ONE ack chunk. If a future change made
// Nack mutate the active set, a mutation in chunk 0 would still shift every
// index in chunk 1 — closing that would require resolving all physical
// indices up front, before any mutation. Do not read this reversal as a
// blanket guarantee against index shift across the whole batch.
func (t *DestinationTask) markBatchRecords(b *Batch, from int, acks []connector.DestinationAck) {
	for i := len(acks) - 1; i >= 0; i-- {
		if acks[i].Error != nil {
			b.Nack(from+i, acks[i].Error)
		}
	}
}

// asyncTask is a task that can accept a batch without waiting for it to
// complete. Worker.doTaskAttempt calls Submit instead of Do when the worker
// runs the staged engine; done runs exactly once per accepted batch, on the
// task's own goroutine, once the batch's acks were received (nil) or the task
// failed (non-nil). A Submit that returns an error never calls done.
type asyncTask interface {
	Task
	Submit(ctx context.Context, b *Batch, done func(error)) error
}

// maxUnackedRecords bounds, per destination, how many records may be written
// but not yet acked. It is the destination-side half of backpressure: the
// writer blocks when the window is full. It is a var only for tests.
var maxUnackedRecords = 4000

// inboxSize is how many submitted batches may wait for the writer. A var only
// for tests.
var inboxSize = 1024

// inflightWrite is one batch submitted to a destination: queued in the inbox,
// then written, then waiting for its acks.
type inflightWrite struct {
	batch     *Batch
	records   []opencdc.Record
	positions []opencdc.Position
	start     time.Time
	acked     int
	done      func(error)
}

// asyncState is the staged-engine half of a DestinationTask.
//
//	Submit ──inbox──▶ writer ──Write──▶ plugin
//	                     │ pending FIFO (appended BEFORE Write)
//	plugin ──acks──▶ AckReader ──done()──▶ worker continuation ──▶ ledger vote
//
// The inbox merges all sources writing to this destination in arrival order and
// keeps each source's order (one runner goroutine per source submits in
// sequence). The writer is the only caller of Destination.Write and the
// AckReader the only caller of Destination.Ack. qMu guards the pending FIFO and
// window counters and is never held across Write: Write can block until the
// plugin reads, and the plugin can be blocked sending an ack the AckReader
// needs qMu to take.
type asyncState struct {
	inbox chan *inflightWrite
	dead  chan struct{} // closed on failure or close; wakes blocked submitters

	qMu     sync.Mutex
	pending []*inflightWrite
	unacked int
	err     error
	closing bool
	changed chan struct{} // closed and replaced when the window or err changes

	ackCtx    context.Context //nolint:containedctx // owned by the AckReader goroutine
	ackCancel context.CancelFunc
	wg        sync.WaitGroup
}

func (t *DestinationTask) asyncState() *asyncState {
	t.asyncOnce.Do(func() {
		a := &asyncState{
			inbox:   make(chan *inflightWrite, inboxSize),
			dead:    make(chan struct{}),
			changed: make(chan struct{}),
		}
		a.ackCtx, a.ackCancel = context.WithCancel(context.Background())
		a.wg.Add(2)
		go t.writer(a)
		go t.ackReader(a)
		t.async.Store(a)
	})
	return t.async.Load()
}

func (a *asyncState) broadcastLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// Submit implements asyncTask. It blocks only while the inbox is full.
//
// Invariant 1: Submit never produces a vote. A vote only comes from the
// AckReader, from an explicit ack matched to a write of these records.
func (t *DestinationTask) Submit(ctx context.Context, batch *Batch, done func(error)) error {
	records := batch.ActiveRecords()
	if len(records) == 0 {
		done(nil)
		return nil
	}
	a := t.asyncState()

	positions := make([]opencdc.Position, len(records))
	for i, rec := range records {
		positions[i] = rec.Position
	}
	w := &inflightWrite{batch: batch, records: records, positions: positions, start: time.Now(), done: done}

	// A sticky failure is reported by the next Submit even if the inbox has room.
	a.qMu.Lock()
	err, closing := a.err, a.closing
	a.qMu.Unlock()
	if err != nil {
		return err
	}
	if closing {
		return plugin.ErrPluginNotRunning
	}

	select {
	case a.inbox <- w:
		return nil
	case <-a.dead:
		a.qMu.Lock()
		err = a.err
		a.qMu.Unlock()
		if err == nil {
			err = plugin.ErrPluginNotRunning
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writer is the only caller of Destination.Write on this task.
func (t *DestinationTask) writer(a *asyncState) {
	defer a.wg.Done()
	for {
		var w *inflightWrite
		select {
		case w = <-a.inbox:
		case <-a.dead:
			return
		}

		a.qMu.Lock()
		for a.err == nil && !a.closing && a.unacked > 0 && a.unacked+len(w.records) > maxUnackedRecords {
			ch := a.changed
			a.qMu.Unlock()
			select {
			case <-ch:
			case <-a.dead:
			}
			a.qMu.Lock()
		}
		if a.err != nil || a.closing {
			a.qMu.Unlock()
			return
		}
		// Appended BEFORE Write so the AckReader can never see an ack it has
		// no entry for, and so the FIFO order equals the stream order.
		a.pending = append(a.pending, w)
		a.unacked += len(w.records)
		a.broadcastLocked()
		a.qMu.Unlock()

		if err := t.destination.Write(a.ackCtx, w.records); err != nil {
			t.failAsync(a, cerrors.Errorf("failed to write %d records to destination: %w", len(w.records), err))
			return
		}
	}
}

// failAsync makes the destination's failure sticky and reports it to every
// write still in flight. No vote is cast for any of them (Invariant 1): their
// positions stay unreleased and replay.
func (t *DestinationTask) failAsync(a *asyncState, err error) {
	a.qMu.Lock()
	if a.err != nil || a.closing {
		a.qMu.Unlock()
		return
	}
	a.err = err
	pending := a.pending
	a.pending = nil
	a.broadcastLocked()
	close(a.dead)
	a.qMu.Unlock()

	for _, w := range pending {
		w.done(err)
	}
	// Writes still in the inbox were never sent. Report them too so the
	// failure reaches every worker that has something in flight here.
	for {
		select {
		case w := <-a.inbox:
			w.done(err)
		default:
			return
		}
	}
}

// ackReader is the only caller of Destination.Ack on this task. It matches
// acks to the FIFO of writes by order, checking position bytes, and completes
// a write once all of its records were acked.
//
// An ack may cover more than one write (an SDK destination that batches
// combines several writes into one response), or only part of one, so the
// matching walks the FIFO rather than assuming one response per write.
func (t *DestinationTask) ackReader(a *asyncState) {
	defer a.wg.Done()
	for {
		a.qMu.Lock()
		for len(a.pending) == 0 && a.err == nil && !a.closing {
			ch := a.changed
			a.qMu.Unlock()
			select {
			case <-ch:
			case <-a.ackCtx.Done():
				return
			}
			a.qMu.Lock()
		}
		stop := a.err != nil || a.closing
		a.qMu.Unlock()
		if stop {
			return
		}

		acks, err := t.destination.Ack(a.ackCtx)
		if err != nil {
			a.qMu.Lock()
			closing := a.closing
			a.qMu.Unlock()
			if closing {
				return
			}
			t.failAsync(a, cerrors.Errorf("failed to receive acks from destination: %w", err))
			return
		}
		if err := t.matchAcks(a, acks); err != nil {
			t.failAsync(a, cerrors.Errorf("failed to validate acks: %w", err))
			return
		}
	}
}

func (t *DestinationTask) matchAcks(a *asyncState, acks []connector.DestinationAck) error {
	var completed []*inflightWrite

	a.qMu.Lock()

	// Validate the whole response against the FIFO before applying any of it.
	// A destination that sends a wrong or surplus ack cannot be trusted for the
	// rest of that response either, so a violation casts no vote at all
	// (Invariant 1); every write in flight fails instead.
	{
		rest := acks
		for i := 0; len(rest) > 0; i++ {
			if i >= len(a.pending) {
				a.qMu.Unlock()
				return cerrors.Errorf("received %d acks, but no write is outstanding", len(rest))
			}
			w := a.pending[i]
			from := 0
			if i == 0 {
				from = w.acked
			}
			n := min(len(rest), len(w.positions)-from)
			if err := t.validateAcks(rest[:n], w.positions[from:]); err != nil {
				a.qMu.Unlock()
				return err
			}
			rest = rest[n:]
		}
	}

	consumed := 0
	for len(acks) > 0 {
		head := a.pending[0]
		n := min(len(acks), len(head.positions)-head.acked)
		t.metrics.Observe(head.records[head.acked:head.acked+n], head.start)
		t.markBatchRecords(head.batch, head.acked, acks[:n])
		head.acked += n
		consumed += n
		acks = acks[n:]
		if head.acked == len(head.positions) {
			a.pending[0] = nil
			a.pending = a.pending[1:]
			completed = append(completed, head)
		}
	}
	a.unacked -= consumed
	a.broadcastLocked()
	a.qMu.Unlock()

	for _, w := range completed {
		w.done(nil)
	}
	return nil
}

// closeAsync stops the AckReader. Called by Close before Teardown so a Recv
// error caused by the teardown is not reported as a failure.
func (t *DestinationTask) closeAsync() {
	a := t.async.Load()
	if a == nil {
		return
	}
	a.qMu.Lock()
	if !a.closing {
		a.closing = true
		select {
		case <-a.dead:
		default:
			close(a.dead)
		}
	}
	a.broadcastLocked()
	a.qMu.Unlock()
}

func (t *DestinationTask) joinAsync() {
	a := t.async.Load()
	if a == nil {
		return
	}
	a.ackCancel()
	a.wg.Wait()
}
