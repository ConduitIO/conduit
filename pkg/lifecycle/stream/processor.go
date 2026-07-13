// Copyright © 2022 Meroxa, Inc.
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

//go:generate mockgen -typed -destination=mock/processor.go -package=mock -mock_names=Processor=Processor . Processor

package stream

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

type ProcessorNode struct {
	Name           string
	Processor      Processor
	ProcessorTimer metrics.Timer

	base   pubSubNodeBase
	logger log.CtxLogger

	// Live-reconfigure state (see Reconfigure). swapMu guards pending and the
	// lazy creation of wakeCh; Processor itself is never guarded because it is
	// only ever read or swapped by the Run goroutine (applyPendingSwap runs there,
	// at a record boundary), so there is no concurrent access to guard.
	swapMu  sync.Mutex
	pending *pendingSwap
	wakeCh  chan struct{}
}

// pendingSwap is a staged live-reconfigure request. done carries the outcome back
// to the Reconfigure caller: nil on success, or an error if the new processor
// failed to open (in which case the old processor is kept). done is buffered
// (cap 1) so the Run goroutine never blocks delivering the result even if the
// caller has already given up (e.g. its context was cancelled).
type pendingSwap struct {
	newProcessor Processor
	done         chan error
}

type Processor interface {
	// Open configures and opens a processor plugin
	Open(ctx context.Context) error
	Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord
	// Teardown tears down a processor plugin.
	// In case of standalone plugins, that means stopping the WASM module.
	Teardown(context.Context) error
}

func (n *ProcessorNode) ID() string {
	return n.Name
}

func (n *ProcessorNode) Run(ctx context.Context) error {
	_, cleanup, err := n.base.Trigger(ctx, n.logger, nil)
	if err != nil {
		return err
	}
	defer cleanup()
	// Read the inbound channel directly and select over it together with the
	// wake signal (below), rather than using Trigger's blocking Receive, so a
	// live reconfigure applies promptly even when no records are flowing.
	in := n.base.In()
	wake := n.wake()

	// Teardown needs to be called even if Open() fails
	// (to mark the processor as not running)
	defer func() {
		n.logger.Debug(ctx).Msg("tearing down processor")
		tdErr := n.Processor.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down processor")
		})
	}()

	n.logger.Debug(ctx).Msg("opening processor")
	err = n.Processor.Open(ctx)
	if err != nil {
		n.logger.Err(ctx, err).Msg("failed opening processor")
		return cerrors.Errorf("couldn't open processor: %w", err)
	}

	for {
		// Invariant 4/1: apply a staged live reconfigure at this record boundary,
		// before receiving the next record. applyPendingSwap runs in THIS
		// goroutine, so n.Processor is never swapped while Process (below) is
		// executing — no record straddles a swap, and the swap sees no in-flight
		// record inside this node. Records already forwarded used the old config;
		// records received after the swap use the new one, in order.
		n.applyPendingSwap(ctx)

		var msg *Message
		select {
		case <-ctx.Done():
			// Mirrors nodeBase.Receive's logging for observability parity.
			n.logger.Debug(ctx).Msg("context closed while waiting for message")
			return ctx.Err()
		case <-wake:
			// Woken by Reconfigure to apply a staged swap promptly (the pipeline
			// may be idle, with nothing arriving on in). Loop back to
			// applyPendingSwap above.
			continue
		case m, ok := <-in:
			if !ok {
				// Inbound channel closed: upstream is done, so are we.
				n.logger.Debug(ctx).Msg("incoming messages channel closed")
				return nil
			}
			msg = m
		}

		if msg.filtered {
			n.logger.Trace(ctx).Str(log.MessageIDField, msg.ID()).
				Msg("message marked as filtered, sending directly to next node")
			err = n.base.Send(ctx, n.logger, msg)
			if err != nil {
				return msg.Nack(err, n.ID())
			}
			continue
		}

		executeTime := time.Now()
		recsIn := []opencdc.Record{msg.Record}
		recsOut := n.Processor.Process(msg.Ctx, recsIn)
		n.ProcessorTimer.Update(time.Since(executeTime))

		if len(recsIn) != len(recsOut) {
			err := cerrors.Errorf("processor was given %v record(s), but returned %v", len(recsIn), len(recsOut))
			// todo when processors can accept multiple records
			// make sure that we ack as many records as possible
			// (here we simply nack all of them, which is always only one)
			if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
				return cerrors.FatalError(nackErr)
			}
			return cerrors.FatalError(err)
		}

		switch v := recsOut[0].(type) {
		case sdk.SingleRecord:
			err := n.handleSingleRecord(ctx, msg, v)
			// handleSingleRecord already checks the nack error (if any)
			// so it's enough to just return the error from it
			if err != nil {
				return err
			}
		case sdk.FilterRecord:
			msg.filtered = true
			n.logger.Trace(ctx).
				Str(log.MessageIDField, msg.ID()).
				Msg("message marked as filtered, sending directly to next node")
			err = n.base.Send(ctx, n.logger, msg)
			if err != nil {
				return msg.Nack(err, n.ID())
			}
		case sdk.ErrorRecord:
			err = msg.Nack(v.Error, n.ID())
			if err != nil {
				return cerrors.FatalError(cerrors.Errorf("error executing processor: %w", err))
			}
		default:
			err := cerrors.Errorf("processor returned unknown record type: %T", v)
			if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
				return cerrors.FatalError(nackErr)
			}
			return cerrors.FatalError(err)
		}
	}
}

func (n *ProcessorNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

func (n *ProcessorNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *ProcessorNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}

// wake returns the node's wake channel, creating it on first use. It is a
// buffered (cap 1) signal channel used by Reconfigure to nudge an idle Run loop.
// Guarded by swapMu because Run and Reconfigure may first touch it concurrently.
func (n *ProcessorNode) wake() chan struct{} {
	n.swapMu.Lock()
	defer n.swapMu.Unlock()
	if n.wakeCh == nil {
		n.wakeCh = make(chan struct{}, 1)
	}
	return n.wakeCh
}

// Reconfigure performs a live, in-place swap of this node's Processor to
// newProcessor, applied by the Run goroutine at the next record boundary without
// restarting the pipeline. It blocks until the swap is applied (returns nil), the
// new processor fails to Open (returns the error; the old processor is kept
// running so the pipeline never drops), or ctx is done (returns ctx.Err()).
//
// Concurrency: Reconfigure only stages the request and signals Run; the swap
// itself — Open of the new processor, the switch, and Teardown of the old — is
// performed exclusively by the Run goroutine (see applyPendingSwap), so
// n.Processor is never mutated concurrently with Process. Only one reconfigure
// may be in flight at a time; a second concurrent call returns an error.
//
// Invariant 3: a reconfigure never drops or reorders records. The swap happens at
// a record boundary; during it, backpressure pauses the source, so no record is
// lost and no position advances.
func (n *ProcessorNode) Reconfigure(ctx context.Context, newProcessor Processor) error {
	done := make(chan error, 1)
	wake := n.wake()

	n.swapMu.Lock()
	if n.pending != nil {
		n.swapMu.Unlock()
		return cerrors.New("a processor reconfigure is already in progress")
	}
	n.pending = &pendingSwap{newProcessor: newProcessor, done: done}
	n.swapMu.Unlock()

	// Nudge an idle Run loop so the swap applies promptly even with no records
	// flowing. Non-blocking on a cap-1 buffered channel: never blocks the caller,
	// and a token already in the buffer is enough to wake the loop.
	select {
	case wake <- struct{}{}:
	default:
	}

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Caller gave up. Best-effort withdraw so a later reconfigure isn't
		// blocked; if Run already claimed the request, this is a no-op and the
		// swap still completes (done is buffered, so Run never blocks delivering
		// its now-ignored result).
		n.swapMu.Lock()
		if n.pending != nil && n.pending.done == done {
			n.pending = nil
		}
		n.swapMu.Unlock()
		return ctx.Err()
	}
}

// applyPendingSwap applies a staged Reconfigure request, if any. It MUST be
// called only from the Run goroutine, between records, so that n.Processor is
// never swapped while Process is executing.
//
// Open-before-teardown: the new processor is opened first; only if Open succeeds
// is it switched in and the old one torn down. If Open fails, the old processor
// keeps running and the error is reported to the Reconfigure caller — a bad edit
// never drops the pipeline (invariant 3).
func (n *ProcessorNode) applyPendingSwap(ctx context.Context) {
	n.swapMu.Lock()
	p := n.pending
	n.pending = nil
	n.swapMu.Unlock()
	if p == nil {
		return
	}

	if err := p.newProcessor.Open(ctx); err != nil {
		// Keep the current processor running; report the failure. Best-effort
		// teardown of the failed new processor (Open may have partially
		// initialized it, e.g. started a WASM module), mirroring Run's defer.
		if tdErr := p.newProcessor.Teardown(ctx); tdErr != nil {
			n.logger.Warn(ctx).Err(tdErr).Msg("could not tear down new processor after failed live-reconfigure open")
		}
		p.done <- cerrors.Errorf("could not open new processor for live reconfigure, keeping current processor: %w", err)
		return
	}

	old := n.Processor
	n.Processor = p.newProcessor
	if tdErr := old.Teardown(ctx); tdErr != nil {
		// The swap already succeeded; a teardown error on the old processor is
		// logged, not surfaced as a swap failure.
		n.logger.Warn(ctx).Err(tdErr).Msg("could not tear down previous processor after live reconfigure")
	}
	p.done <- nil
}

// handleSingleRecord handles a sdk.SingleRecord by checking the position,
// setting the new record on the message and sending it downstream.
// If there are any errors, the method nacks the message and returns
// an appropriate error (if nack-ing failed, it returns the nack error)
func (n *ProcessorNode) handleSingleRecord(ctx context.Context, msg *Message, rec sdk.SingleRecord) error {
	if !bytes.Equal(rec.Position, msg.Record.Position) {
		err := cerrors.Errorf(
			"processor changed position from '%v' to '%v' "+
				"(not allowed because source connector cannot correctly acknowledge messages)",
			msg.Record.Position,
			rec.Position,
		)

		if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
			return nackErr
		}
		// correctly nacked (sent to the DLQ)
		// so we return the "original" error here
		return err
	}

	msg.Record = opencdc.Record(rec)
	err := n.base.Send(ctx, n.logger, msg)
	if err != nil {
		return msg.Nack(err, n.ID())
	}

	return nil
}
