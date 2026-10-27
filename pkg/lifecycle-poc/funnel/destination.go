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

	// Batching state (see destination_batch.go). Zero values mean batching is
	// disabled and the task writes every batch immediately (the pre-batching
	// behavior, still used by the DLQ).
	batchSize  int
	batchDelay time.Duration

	mu            sync.Mutex
	pending       []pendingBatch // buffered source batches awaiting flush
	bufferedCount int            // number of active records currently buffered
	flushDeadline time.Time      // zero when nothing is buffered
	// outstanding counts flushes handed to the flusher and not yet fully acked
	// (queued or in flight); guarded by mu. Together with pending it tells
	// deferOrdered whether any earlier batch is still ahead of a source ack.
	outstanding int

	// Pipelining state (see destination_batch.go). Zero values mean the
	// flusher goroutine is not running (pipelining disabled, batching
	// disabled, or Open not called yet).
	//
	// pipelined is set once, by SetPipelined, before Open; it is not mutated
	// after Open and is therefore read without t.mu.
	pipelined bool
	// flushCh is the bounded hand-off from doBuffered to the flusher goroutine.
	// Its capacity is the read-ahead credit (maxInFlightFlushes). A full channel
	// blocks doBuffered, which is the backpressure when the destination is
	// slower than the source.
	flushCh chan []pendingBatch
	// flushErr holds the first error the flusher hit; guarded by mu. It is
	// surfaced by the next doBuffered (and by Close), which propagates it
	// through the worker exactly like a synchronous flush error would.
	flushErr error
	// flusherDone is closed when the flusher goroutine exits (on a flush error,
	// run-context cancellation, or Close draining the channel).
	flusherDone chan struct{}
	// closed is set by Close before it drains the flusher; guarded by mu.
	// doBuffered refuses to buffer after it, so no batch can be appended behind
	// the drain.
	closed bool

	// enqueueMu makes "take pending + send on flushCh" atomic with respect to
	// deferOrdered on the pipelined path. Lock order: enqueueMu, then mu. The
	// flusher never takes it.
	enqueueMu sync.Mutex

	// testAfterTake, if set, is called by doBuffered after it took the pending
	// buffer to flush and before it hands it to the flusher. Tests use it to
	// hold that window open; nil in production.
	testAfterTake func()
}

type Destination interface {
	ID() string
	Open(context.Context) error
	Write(context.Context, []opencdc.Record) error
	Ack(context.Context) ([]connector.DestinationAck, error)
	Teardown(context.Context) error
	// Errors carries failures the connector reports asynchronously, chiefly
	// a state write the persister could not commit. Worker.WatchConnectorErrors
	// reads it and the run fails on the first error (#2929).
	Errors() <-chan error
}

func NewDestinationTask(
	id string,
	destination Destination,
	logger log.CtxLogger,
	metrics ConnectorMetrics,
) *DestinationTask {
	return newDestinationTask(id, destination, logger, metrics, 0, 0)
}

// NewBatchingDestinationTask returns a destination task that buffers records
// and flushes them in one multi-record Write when batchSize records are
// pending or batchDelay has elapsed since the first buffered record (see
// destination_batch.go). batchSize <= 1 and batchDelay <= 0 together disable
// batching; a batchDelay > 0 without a batchSize uses defaultMaxBatchSize to
// bound memory.
func NewBatchingDestinationTask(
	id string,
	destination Destination,
	logger log.CtxLogger,
	metrics ConnectorMetrics,
	batchSize int,
	batchDelay time.Duration,
) *DestinationTask {
	return newDestinationTask(id, destination, logger, metrics, batchSize, batchDelay)
}

func newDestinationTask(
	id string,
	destination Destination,
	logger log.CtxLogger,
	metrics ConnectorMetrics,
	batchSize int,
	batchDelay time.Duration,
) *DestinationTask {
	logger = logger.WithComponent("task:destination")
	logger.Logger = logger.With().Str(log.ConnectorIDField, id).Logger()
	return &DestinationTask{
		id:          id,
		destination: destination,
		logger:      logger,
		metrics:     metrics,
		batchSize:   batchSize,
		batchDelay:  batchDelay,
	}
}

func (t *DestinationTask) ID() string {
	return t.id
}

// SetPipelined enables read-ahead pipelining (a dedicated flusher goroutine)
// for this destination. It must be called before Open, and only for a
// destination that is not part of a destination fan-out: with M>1 destinations,
// M flushers would race on the single Worker's cross-batch source ack, so
// fan-out keeps the synchronous flush (see
// docs/design-documents/20261019-funnel-read-ahead-pipelining.md).
func (t *DestinationTask) SetPipelined() {
	t.pipelined = true
}

func (t *DestinationTask) Open(ctx context.Context) error {
	t.logger.Debug(ctx).Msg("opening destination")
	err := t.destination.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open destination connector: %w", err)
	}
	if t.pipelined && t.batchingEnabled() {
		// Invariant 1/4 (enforcement site): start the flusher goroutine that
		// owns the single-writer, position-ordered Write/Ack pairing on the
		// destination's gRPC stream. doBuffered hands full buffers to it and
		// keeps reading, so the source read overlaps the destination write
		// while acks are still issued in enqueue (source) order.
		t.startFlusher(ctx)
	}
	t.logger.Debug(ctx).Msg("destination open")
	return nil
}

func (t *DestinationTask) connectorErrors() <-chan error {
	return t.destination.Errors()
}

func (t *DestinationTask) Close(ctx context.Context) error {
	// Invariant 7: flush any buffered records before tearing down the
	// destination so a graceful stop writes them durably instead of dropping
	// them. On a graceful stop Worker.Stop has already drained this task
	// (Drain) while the source was still running, so there is normally nothing
	// left here. After a force-stop or error the source may already be torn down
	// and the deferred source acks fail: the records were written durably but
	// replay on restart — at-least-once, never a loss. A flush error is
	// therefore logged, not returned, so teardown still runs.
	//
	// On the pipelined path this also drains the flusher goroutine: the
	// trailing partial batch is enqueued, the channel is closed, and Close
	// waits for the flusher to finish every in-flight flush before teardown.
	var flushErr error
	if t.pipelined && t.flusherDone != nil {
		flushErr = t.stopFlusher(ctx)
	} else {
		flushErr = t.flush(ctx)
	}
	var deferredErr error
	if flushErr != nil {
		t.logger.Err(ctx, flushErr).Msg("failed to flush buffered records before destination teardown (records replay on restart)")
		// A queued source nack that failed (DLQ disabled or over threshold) is a
		// pipeline failure, not a replayable ack: report it so a stop is not
		// mistaken for a clean one. A source already torn down is the expected
		// replay case.
		if isDeferredAckError(flushErr) && !cerrors.Is(flushErr, plugin.ErrPluginNotRunning) {
			deferredErr = flushErr
		}
	}
	return cerrors.Join(deferredErr, t.destination.Teardown(ctx))
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
		t.metrics.ObserveError()
		return cerrors.Errorf("failed to write %d records to destination: %w", len(positions), err)
	}

	ackCount := 0
	for range len(positions) {
		acks, err := t.destination.Ack(ctx)
		if err != nil {
			t.metrics.ObserveError()
			return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
		}

		if err := t.validateAcks(acks, positions[ackCount:]); err != nil {
			t.metrics.ObserveError()
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
			return cerrors.Errorf("received unexpected ack, expected position hash %s but got %s", log.KeyHash(positions[i]), log.KeyHash(ack.Position))
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
