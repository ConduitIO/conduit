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

package enginetest

import (
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
)

// Batching mirrors the connector SDK's destination batching: records are held
// until Size of them are pending or Delay has passed since the first one, then
// flushed together and acked. With Delay zero a partial batch is held until
// Size is reached, Flush, Stop or Teardown, which is exactly what the SDK does
// and what makes batching interact with an engine that waits for each write's
// ack before sending the next.
type Batching struct {
	Size  int
	Delay time.Duration
}

// DestinationConfig controls a fake Destination. The zero value acks every
// record immediately.
type DestinationConfig struct {
	// ID is the connector ID.
	ID string
	// WriteLatency is slept (real time) in every Write before it returns.
	WriteLatency time.Duration
	// AckLatency is slept (real time) in every Ack call before it returns.
	AckLatency time.Duration
	// AckChunk caps the number of acks one Ack call returns; 0 means all
	// available. A cap of 1 gives the engine acks one at a time.
	AckChunk int
	// AckPump delivers acks through a goroutine and a channel of AckBuffer
	// groups, one flush per group, the way a gRPC destination delivers them.
	// AckChunk is ignored when set. The default is to serve Ack from an
	// in-memory queue on the calling goroutine.
	AckPump   bool
	AckBuffer int
	// Batching enables SDK-style batching; Size <= 1 disables it.
	Batching Batching
	// Verdict decides each record when it is flushed: nil acks it, an error
	// nacks it (the nack is returned in the DestinationAck, the record is not
	// counted as delivered).
	Verdict func(rec opencdc.Record) error
	// WriteHook runs at the start of every Write with the 1-based call number.
	// A non-nil error fails the Write; the hook may block.
	WriteHook func(ctx context.Context, call int, recs []opencdc.Record) error
	// AckHook runs in every Ack once acks are available, before they are
	// returned, with the number about to be returned. A non-nil error fails the
	// Ack call and the acks stay queued; the hook may block.
	AckHook func(ctx context.Context, n int) error
	// Discard stops the destination retaining delivered and nacked records
	// (Delivered and Nacked return nil; the counters still work). Benchmarks set
	// it so memory does not grow with b.N.
	Discard bool
	// Ledger, if set, is told when a record becomes durable: when Ack hands its
	// non-error ack to the engine, not when it is flushed.
	Ledger *Ledger
}

// Destination is a fake destination connector. It satisfies the Destination
// interfaces of both engines. All methods are safe for concurrent use.
type Destination struct {
	cfg DestinationConfig

	// WriteGate stalls Write while closed; AckGate stalls Ack while closed
	// (after acks are available, so a test can hold records "in flight").
	WriteGate Gate
	AckGate   Gate

	mu        sync.Mutex
	opened    bool
	pending   []opencdc.Record
	origPos   map[string]opencdc.Position // ledger bookkeeping: record position -> source position
	timer     *time.Timer
	queue     []connector.DestinationAck
	delivered []opencdc.Record
	nacked    []opencdc.Record
	writeCall int
	crashed   error
	torn      chan struct{}
	tornOnce  sync.Once
	errCh     chan error
	queued    broadcast

	pumpWake chan struct{}
	ackCh    chan []connector.DestinationAck
	pumpQ    [][]connector.DestinationAck

	writtenCnt   counter
	deliveredCnt counter
}

// NewDestination returns a fake destination.
func NewDestination(cfg DestinationConfig) *Destination {
	if cfg.ID == "" {
		cfg.ID = "dst"
	}
	d := &Destination{
		cfg:     cfg,
		origPos: make(map[string]opencdc.Position),
		torn:    make(chan struct{}),
		errCh:   make(chan error, 1),
	}
	if cfg.AckPump {
		d.pumpWake = make(chan struct{}, 1)
		d.ackCh = make(chan []connector.DestinationAck, max(cfg.AckBuffer, 0))
	}
	return d
}

// ID implements the engines' Destination interfaces.
func (d *Destination) ID() string { return d.cfg.ID }

// Open implements the engines' Destination interfaces. Opening twice is an
// error, like a real connector.
func (d *Destination) Open(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opened {
		return ErrAlreadyOpen
	}
	d.opened = true
	if d.cfg.AckPump {
		go d.pump()
	}
	return nil
}

// Errors implements the engines' Destination interfaces. A value is sent by
// Crash.
func (d *Destination) Errors() <-chan error { return d.errCh }

// Write implements the engines' Destination interfaces.
func (d *Destination) Write(ctx context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	d.writeCall++
	call := d.writeCall
	crashed := d.crashed
	d.mu.Unlock()
	if crashed != nil {
		return crashed
	}

	if err := d.WriteGate.Wait(ctx, d.torn); err != nil {
		return d.closedOr(err)
	}
	if err := sleep(ctx, d.cfg.WriteLatency, d.torn); err != nil {
		return d.closedOr(err)
	}
	if d.cfg.WriteHook != nil {
		hctx, cancel := withDone(ctx, d.torn)
		err := d.cfg.WriteHook(hctx, call, recs)
		cancel()
		if err != nil {
			return d.hookErr(err)
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.crashed != nil {
		return d.crashed
	}
	d.pending = append(d.pending, recs...)
	d.writtenCnt.add(len(recs))
	switch {
	case d.cfg.Batching.Size <= 1:
		d.flushLocked()
	case len(d.pending) >= d.cfg.Batching.Size:
		d.flushLocked()
	case d.cfg.Batching.Delay > 0 && d.timer == nil:
		d.timer = time.AfterFunc(d.cfg.Batching.Delay, d.Flush)
	}
	return nil
}

// Flush flushes the pending batch now, as the batching timer or a full batch
// would. It is a no-op when nothing is pending.
func (d *Destination) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushLocked()
}

func (d *Destination) flushLocked() {
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if len(d.pending) == 0 || d.crashed != nil {
		return
	}
	acks := make([]connector.DestinationAck, len(d.pending))
	for i, rec := range d.pending {
		acks[i] = connector.DestinationAck{Position: rec.Position}
		if d.cfg.Verdict != nil {
			if err := d.cfg.Verdict(rec); err != nil {
				acks[i].Error = err
				if !d.cfg.Discard {
					d.nacked = append(d.nacked, rec)
				}
				continue
			}
		}
		if !d.cfg.Discard {
			d.delivered = append(d.delivered, rec)
		}
		if d.cfg.Ledger != nil {
			// Remembered, not yet durable as far as the engine is concerned:
			// the Ledger is told when Ack hands the ack over (markAcked), so a
			// source ack that does not wait for the destination's ack is a
			// violation even though the fake flushed inside Write.
			d.origPos[string(rec.Position)] = originalPosition(rec)
		}
	}
	d.deliveredCnt.add(len(d.pending))
	d.pending = d.pending[:0]

	if d.cfg.AckPump {
		d.pumpQ = append(d.pumpQ, acks)
		select {
		case d.pumpWake <- struct{}{}:
		default:
		}
		return
	}
	if len(d.queue) == 0 {
		d.queue = acks // no copy in the common case of an empty queue
	} else {
		d.queue = append(d.queue, acks...)
	}
	d.queued.Fire()
}

// pump forwards flushed ack groups to the Ack caller through a channel.
func (d *Destination) pump() {
	for {
		select {
		case <-d.pumpWake:
		case <-d.torn:
			return
		}
		for {
			d.mu.Lock()
			if len(d.pumpQ) == 0 {
				d.mu.Unlock()
				break
			}
			group := d.pumpQ[0]
			d.pumpQ = d.pumpQ[1:]
			d.mu.Unlock()
			select {
			case d.ackCh <- group:
			case <-d.torn:
				return
			}
		}
	}
}

// Ack implements the engines' Destination interfaces. It blocks until acks are
// available, like a real destination's ack stream.
func (d *Destination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	if d.cfg.AckPump {
		return d.ackPumped(ctx)
	}
	for {
		d.mu.Lock()
		if d.crashed != nil {
			err := d.crashed
			d.mu.Unlock()
			return nil, err
		}
		n := len(d.queue)
		if n > 0 {
			d.mu.Unlock()
			break
		}
		wake := d.queued.C() // only allocate the wake channel when we must wait
		d.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.torn:
			return nil, d.closedOr(plugin.ErrPluginNotRunning)
		}
	}

	if err := d.AckGate.Wait(ctx, d.torn); err != nil {
		return nil, d.closedOr(err)
	}
	d.mu.Lock()
	n := len(d.queue)
	if d.cfg.AckChunk > 0 && n > d.cfg.AckChunk {
		n = d.cfg.AckChunk
	}
	d.mu.Unlock()
	if d.cfg.AckHook != nil {
		hctx, cancel := withDone(ctx, d.torn)
		err := d.cfg.AckHook(hctx, n)
		cancel()
		if err != nil {
			return nil, d.hookErr(err)
		}
	}
	if err := sleep(ctx, d.cfg.AckLatency, d.torn); err != nil {
		return nil, d.closedOr(err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// The queue is append-only elsewhere, so a capped sub-slice is safe to hand
	// out without copying.
	acks := d.queue[:n:n]
	d.queue = d.queue[n:]
	d.markAckedLocked(acks)
	return acks, nil
}

func (d *Destination) ackPumped(ctx context.Context) ([]connector.DestinationAck, error) {
	var acks []connector.DestinationAck
	select {
	case acks = <-d.ackCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.torn:
		return nil, d.closedOr(plugin.ErrPluginNotRunning)
	}
	if err := d.AckGate.Wait(ctx, d.torn); err != nil {
		return nil, d.closedOr(err)
	}
	if d.cfg.AckHook != nil {
		hctx, cancel := withDone(ctx, d.torn)
		err := d.cfg.AckHook(hctx, len(acks))
		cancel()
		if err != nil {
			return nil, d.hookErr(err)
		}
	}
	if err := sleep(ctx, d.cfg.AckLatency, d.torn); err != nil {
		return nil, d.closedOr(err)
	}
	d.mu.Lock()
	d.markAckedLocked(acks)
	d.mu.Unlock()
	return acks, nil
}

// markAckedLocked tells the Ledger that the records behind these acks are
// durably handled. This is the durability event the Ledger keys on, because
// it is the moment the engine is told: a source ack before it is an ack that
// did not wait for the destination.
func (d *Destination) markAckedLocked(acks []connector.DestinationAck) {
	if d.cfg.Ledger == nil {
		return
	}
	for _, a := range acks {
		orig, ok := d.origPos[string(a.Position)]
		if !ok {
			continue
		}
		delete(d.origPos, string(a.Position))
		if a.Error == nil {
			d.cfg.Ledger.markDurable(d.cfg.ID, orig)
		}
	}
}

// Stop implements the arch v1 Destination interface. Like the SDK's Stop it
// flushes a held partial batch.
func (d *Destination) Stop(context.Context, opencdc.Position) error {
	d.Flush()
	return nil
}

// Teardown implements the engines' Destination interfaces. It flushes a held
// partial batch (the SDK does the same) and releases any stalled call.
func (d *Destination) Teardown(context.Context) error {
	d.Flush()
	d.tornOnce.Do(func() { close(d.torn) })
	return nil
}

// Crash simulates the plugin dying: pending and future Write and Ack calls fail
// with err (ErrCrashed when nil) and err is delivered on Errors(). Records still
// held in a partial batch are lost, as they would be.
func (d *Destination) Crash(err error) {
	if err == nil {
		err = ErrCrashed
	}
	d.mu.Lock()
	d.crashed = err
	d.pending = nil
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.mu.Unlock()
	select {
	case d.errCh <- err:
	default:
	}
	d.tornOnce.Do(func() { close(d.torn) })
}

// hookErr maps a hook error to the connector-closed error when the hook was
// released by teardown, and passes it through otherwise.
func (d *Destination) hookErr(err error) error {
	select {
	case <-d.torn:
		return d.closedOr(plugin.ErrPluginNotRunning)
	default:
		return err
	}
}

func (d *Destination) closedOr(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.crashed != nil {
		return d.crashed
	}
	if cerrors.Is(err, errGateAbandoned) {
		return plugin.ErrPluginNotRunning
	}
	return err
}

// Delivered returns the records durably handled (flushed and not nacked), in
// flush order.
func (d *Destination) Delivered() []opencdc.Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]opencdc.Record(nil), d.delivered...)
}

// Nacked returns the records the Verdict rejected.
func (d *Destination) Nacked() []opencdc.Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]opencdc.Record(nil), d.nacked...)
}

// Pending is the number of records written but not yet flushed.
func (d *Destination) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}

// WriteCalls is the number of Write calls so far.
func (d *Destination) WriteCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeCall
}

// Written is the number of records received by Write so far.
func (d *Destination) Written() int { return d.writtenCnt.load() }

// DeliveredCount is the number of records flushed so far, nacked ones included.
func (d *Destination) DeliveredCount() int { return d.deliveredCnt.load() }

// WaitWritten blocks until at least n records were received or ctx ends.
func (d *Destination) WaitWritten(ctx context.Context, n int) error {
	return d.writtenCnt.waitAtLeast(ctx, n)
}

// WaitDelivered blocks until at least n records were flushed or ctx ends.
func (d *Destination) WaitDelivered(ctx context.Context, n int) error {
	return d.deliveredCnt.waitAtLeast(ctx, n)
}

// TornDown reports whether Teardown was called (or the destination crashed).
func (d *Destination) TornDown() bool {
	select {
	case <-d.torn:
		return true
	default:
		return false
	}
}

// originalPosition is the position of the source record a destination record
// stands for. For an ordinary record that is its own position. A DLQ record
// wraps the failed record in its payload and, in the v1 engine, carries a
// message ID as its own position; the wrapped record's position is what the
// source will be acked with.
func originalPosition(rec opencdc.Record) opencdc.Position {
	if _, isDLQ := rec.Metadata[opencdc.MetadataConduitDLQNackError]; !isDLQ {
		return rec.Position
	}
	if sd, ok := rec.Payload.After.(opencdc.StructuredData); ok {
		if p, ok := sd["position"].([]byte); ok {
			return opencdc.Position(p)
		}
	}
	return rec.Position
}
