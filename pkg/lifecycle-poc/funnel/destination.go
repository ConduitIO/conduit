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
	"fmt"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// maxConsecutiveEmptyAcks bounds how many ack responses in a row may carry no
// ack while records are still unacked. The connector protocol allows an empty
// response, so this is engine policy, not protocol enforcement: it keeps a
// misbehaving destination from holding the writer in a receive loop forever.
const maxConsecutiveEmptyAcks = 1000

// errEmptyAckFlood is the coded error for maxConsecutiveEmptyAcks.
func (t *DestinationTask) errEmptyAckFlood(unacked int) error {
	ce := conduiterr.New(CodeDestinationEmptyAckFlood, fmt.Sprintf(
		"destination %s sent %d consecutive ack responses with no acks while %d records were still unacked",
		t.id, maxConsecutiveEmptyAcks, unacked,
	))
	ce.Suggestion = "the destination connector is answering writes with empty ack responses; check its logs " +
		"and version. No records were acked upstream and they are redelivered on restart"
	return ce
}

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

	// writeLimit is the byte budget of one destination Write, derived from
	// connectors.max-receive-record-size (0 = unlimited). Set once, before Open,
	// by SetMaxReceiveRecordSize.
	writeLimit int

	mu            sync.Mutex
	pending       []pendingBatch // buffered source batches awaiting flush
	bufferedCount int            // number of active records currently buffered
	bufferedBytes int            // estimated wire size of the buffered records (only when writeLimit > 0)
	flushDeadline time.Time      // zero when nothing is buffered

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

// SetMaxReceiveRecordSize bounds every Write to this destination by the
// estimated wire size of its records (see recordWireSize): a batch, or a
// coalesced flush, larger than connectors.max-receive-record-size is sent as
// several Writes whose acks are drained in order. It exists for standalone
// connectors, whose gRPC server rejects a request over that size; leave it
// unset (0) for a built-in connector, which has no such limit. Must be called
// before Open.
func (t *DestinationTask) SetMaxReceiveRecordSize(n int) {
	t.writeLimit = writeLimit(n)
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
	// them. The shared sink is closed only after every worker has exited, so
	// the source is already torn down here and the deferred source acks fail:
	// the records were written durably but replay on restart — at-least-once,
	// never a loss. A flush error is therefore logged, not returned, so
	// teardown still runs.
	//
	// On the pipelined path this also drains the flusher goroutine: the
	// trailing partial batch is enqueued, the channel is closed, and Close
	// waits for the flusher to finish every in-flight flush before teardown.
	if t.pipelined && t.flusherDone != nil {
		if err := t.stopFlusher(ctx); err != nil {
			t.logger.Err(ctx, err).Msg("failed to flush buffered records before destination teardown (records replay on restart)")
		}
	} else if err := t.flush(ctx); err != nil {
		t.logger.Err(ctx, err).Msg("failed to flush buffered records before destination teardown (records replay on restart)")
	}
	return t.destination.Teardown(ctx)
}

func (t *DestinationTask) Do(ctx context.Context, batch *Batch) error {
	records := batch.ActiveRecords()

	// Store the positions of the records in the batch to be used for
	// validation of acks.
	positions := make([]opencdc.Position, len(records))
	for i, rec := range records {
		positions[i] = rec.Position
	}

	// Invariant 1: a batch larger than the destination can receive is written as
	// several chunks, each fully acked before the next is sent, so acks stay in
	// source order. Nothing is acked upstream until Do returns, so a failure in
	// a later chunk replays the whole batch (at-least-once).
	start := time.Now()
	from := 0
	for {
		end := chunkEnd(records, from, t.writeLimit)
		if err := t.writeChunk(ctx, batch, records[from:end], positions[from:end], from, start); err != nil {
			return err
		}
		from = end
		if from >= len(records) {
			return nil
		}
	}
}

// writeChunk writes records (a chunk of the batch starting at active index base)
// and drains the acks for exactly those records.
func (t *DestinationTask) writeChunk(
	ctx context.Context,
	batch *Batch,
	records []opencdc.Record,
	positions []opencdc.Position,
	base int,
	start time.Time,
) error {
	err := t.destination.Write(ctx, records)
	if err != nil {
		t.metrics.ObserveError()
		return cerrors.Errorf("failed to write %d records to destination: %w", len(positions), err)
	}

	// Invariant 1 (enforcement site): count acked RECORDS, never responses. The
	// protocol allows a response with no acks (the Go SDK never sends one), so
	// bounding this loop by len(positions) responses could leave it with records
	// unacked and return nil, and the worker would then ack them upstream. Keep
	// reading until every record has its ack; a stream that ends first surfaces
	// as an Ack error, and an over-long response is rejected by validateAcks.
	ackCount := 0
	empty := 0
	for ackCount < len(positions) {
		acks, err := t.destination.Ack(ctx)
		if err != nil {
			t.metrics.ObserveError()
			return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
		}
		// Engine policy (the protocol allows empty responses): bound the run of
		// consecutive empty responses. The count resets on any response with acks.
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
		t.metrics.Observe(records[ackCount:ackCount+len(acks)], start)
		t.markBatchRecords(batch, base+ackCount, acks)

		ackCount += len(acks)
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
