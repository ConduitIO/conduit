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
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// maxReleaseCoalesce bounds how many positions one Source.Ack call carries.
const maxReleaseCoalesce = 4096

type posState uint8

const (
	posPending posState = iota
	posAcked
	posNacked
)

type nackInfo struct {
	rec    opencdc.Record
	err    error
	taskID string
}

// ledger is the per-source ack ledger. It generalizes multiAckNacker's
// "release strictly as an in-order prefix" rule from one batch to a window of
// batches read ahead of the destination.
//
// # What it holds
//
// One ledgerEntry per batch read from the source, in read (sequence) order,
// each with the batch's ORIGINAL source positions and a per-position state:
// pending, acked, or nacked. Votes (Ack/Nack, via ledgerEntry) arrive from any
// goroutine and in any order: the runner (records filtered or nacked by a
// processor), and each destination's AckReader (records a destination
// confirmed or rejected). Votes only mutate in-memory state under mu and never
// perform I/O, so an AckReader is never blocked by a slow DLQ or Source.Ack.
//
// # What releases
//
// A single coordinator goroutine (Worker.coordinate) is the only caller of
// Source.Ack and of DLQ writes for this source. It releases the longest prefix
// of positions, in source order, that has reached a terminal disposition:
// contiguous acked positions are coalesced into one Source.Ack; a nacked
// position is written to the DLQ first and only then acked to the source.
// Position-level (not batch-level) prefix release is deliberate: a DLQ failure
// or a split run must be able to release exactly the confirmed prefix.
//
// # Invariants this type enforces
//
// Invariant 1: a position is released only after it was voted terminal, and a
// destination vote only comes from an explicit positive ack matched to a write.
// Invariant 2: positions reach Source.Ack in ascending source order, from one
// goroutine; after any failure nothing further is released.
// Invariant 3: nothing is dropped; an unreleased position replays on restart.
type ledger struct {
	mu      sync.Mutex
	entries []*ledgerEntry
	nextSeq uint64

	// registered and dispatched count entries handed out by register and
	// finished with by the runner (all processors done, sent to destinations
	// or resolved). Graceful stop waits for them to meet.
	registered uint64
	dispatched uint64
	// runnerExited is set when the runner (Worker.Do) is gone: nothing more
	// will be dispatched, so waiting for dispatched == registered is pointless.
	runnerExited bool

	// changed is closed and replaced whenever something a waiter may care
	// about changes (entry popped, entry dispatched, entry registered).
	changed chan struct{}

	signal chan struct{} // cap 1: wakes the coordinator

	err    error
	failCh chan struct{}

	// nackGate, if set, may veto a nack at vote time with a fatal error. See
	// DLQ.failFast.
	nackGate func(origErr error) error
	// halted is set (and haltCh closed) when a nack that can never be accepted
	// was voted. The runner stops dispatching; the nack stays in the ledger, the
	// coordinator releases everything before it and then fails the pipeline on
	// it (DLQ.Nack returns the same fatal error).
	halted bool
	haltCh chan struct{}
}

// haltError is returned by a vote that halts the pipeline (see
// ledger.nackGate). The vote itself was recorded.
type haltError struct{ err error }

func (e *haltError) Error() string { return e.err.Error() }
func (e *haltError) Unwrap() error { return e.err }

func newLedger() *ledger {
	return &ledger{
		changed: make(chan struct{}),
		signal:  make(chan struct{}, 1),
		failCh:  make(chan struct{}),
		haltCh:  make(chan struct{}),
	}
}

func (l *ledger) halt() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.halted {
		l.halted = true
		close(l.haltCh)
	}
}

// ledgerEntry is the ledger's record of one batch. It implements ackNacker for
// the ORIGINAL positions of that batch; Worker.doTask hands one to
// newRunAckNacker as the terminal acker of a pass.
type ledgerEntry struct {
	l *ledger

	seq       uint64
	positions []opencdc.Position
	records   []opencdc.Record
	bytes     int64
	readAt    time.Time

	// state, nacks, resolved, released and the lookup fields are guarded by
	// l.mu. released is advanced only by the coordinator.
	state    []posState
	nacks    map[int]nackInfo
	resolved int
	released int
	hint     int
	index    map[string]int
}

// register creates the entry for a freshly read batch and appends it to the
// ledger. The reader is the only caller, so sequence order equals read order.
func (l *ledger) register(b *Batch, size int64, readAt time.Time) *ledgerEntry {
	e := &ledgerEntry{
		l:         l,
		positions: b.positions,
		records:   b.records,
		bytes:     size,
		readAt:    readAt,
		state:     make([]posState, len(b.positions)),
	}
	l.mu.Lock()
	l.nextSeq++
	e.seq = l.nextSeq
	l.entries = append(l.entries, e)
	l.registered++
	l.broadcastLocked()
	l.mu.Unlock()
	return e
}

func (l *ledger) broadcastLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *ledger) wake() {
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func (l *ledger) markDispatched() {
	l.mu.Lock()
	l.dispatched++
	l.broadcastLocked()
	l.mu.Unlock()
}

// fail records the first fatal error. Nothing is released afterwards.
func (l *ledger) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return
	}
	l.err = err
	close(l.failCh)
	l.broadcastLocked() // wake waitFor
}

func (l *ledger) failure() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// waitFor blocks until cond (evaluated under l.mu) holds, ctx is done, or the
// ledger failed.
func (l *ledger) waitFor(ctx context.Context, cond func() bool) error {
	for {
		l.mu.Lock()
		if l.err != nil {
			err := l.err
			l.mu.Unlock()
			return err
		}
		if cond() {
			l.mu.Unlock()
			return nil
		}
		ch := l.changed
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *ledger) waitDispatched(ctx context.Context) error {
	return l.waitFor(ctx, func() bool { return l.dispatched == l.registered || l.runnerExited })
}

func (l *ledger) markRunnerExited() {
	l.mu.Lock()
	l.runnerExited = true
	l.broadcastLocked()
	l.mu.Unlock()
}

func (l *ledger) waitDrained(ctx context.Context) error {
	return l.waitFor(ctx, func() bool { return len(l.entries) == 0 })
}

// depth returns the number of unreleased entries.
func (l *ledger) depth() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// indexOfLocked resolves an original position to its slot. Votes mostly
// arrive in order, so the next unresolved slot is tried first and the lookup
// map is only built when something votes out of order.
func (e *ledgerEntry) indexOfLocked(pos opencdc.Position) (int, error) {
	if e.index == nil {
		if e.hint < len(e.positions) && e.state[e.hint] == posPending && bytes.Equal(e.positions[e.hint], pos) {
			idx := e.hint
			e.hint++
			return idx, nil
		}
		e.index = make(map[string]int, len(e.positions))
		for i, p := range e.positions {
			if _, dup := e.index[string(p)]; dup {
				// Same condition newMultiAckNacker reports; it can only be
				// resolved by position, so refuse rather than guess.
				return 0, cerrors.Errorf("(bug) ledger: duplicate position %q in batch %d", p, e.seq)
			}
			e.index[string(p)] = i
		}
	}
	idx, ok := e.index[string(pos)]
	if !ok {
		return 0, cerrors.Errorf("(bug) ledger: position %q is not part of batch %d", pos, e.seq)
	}
	return idx, nil
}

func (e *ledgerEntry) voteLocked(pos opencdc.Position, to posState) (int, error) {
	idx, err := e.indexOfLocked(pos)
	if err != nil {
		return 0, err
	}
	if e.state[idx] != posPending {
		// A second vote for a position that already has a terminal
		// disposition would mean it is being credited twice, i.e. released
		// earlier than its work finished. Asserted, not assumed.
		return 0, cerrors.Errorf("(bug) ledger: position %q of batch %d voted on twice", pos, e.seq)
	}
	e.state[idx] = to
	e.resolved++
	return idx, nil
}

// Ack records that every position in batch reached its terminal ack
// disposition (written by all destinations, or filtered).
func (e *ledgerEntry) Ack(_ context.Context, batch *Batch) error {
	ob := batch.originalBatch()
	l := e.l
	l.mu.Lock()
	for _, pos := range ob.positions {
		if _, err := e.voteLocked(pos, posAcked); err != nil {
			l.mu.Unlock()
			l.fail(err)
			return err
		}
	}
	l.mu.Unlock()
	l.wake()
	return nil
}

// Nack records that every position in batch must go to the DLQ. The DLQ write
// itself happens in the coordinator, when the prefix reaches the position.
func (e *ledgerEntry) Nack(_ context.Context, batch *Batch, taskID string) error {
	ob := batch.originalBatch()
	l := e.l
	// A nack the DLQ can never accept halts the pipeline AT the nack, but the
	// nack is still recorded: the positions before it must be released first
	// (Invariant 3: they were handled), and the coordinator fails the pipeline
	// when it reaches this one. The error returned here only stops the caller
	// from dispatching the rest of its batch.
	var haltErr error
	if l.nackGate != nil {
		for i := range ob.positions {
			if err := l.nackGate(ob.recordStatuses[i].Error); err != nil {
				haltErr = &haltError{err: err}
				break
			}
		}
	}
	l.mu.Lock()
	for i, pos := range ob.positions {
		idx, err := e.voteLocked(pos, posNacked)
		if err != nil {
			l.mu.Unlock()
			l.fail(err)
			return err
		}
		if e.nacks == nil {
			e.nacks = make(map[int]nackInfo, 1)
		}
		e.nacks[idx] = nackInfo{rec: ob.records[i], err: ob.recordStatuses[i].Error, taskID: taskID}
	}
	l.mu.Unlock()
	l.wake()
	if haltErr != nil {
		l.halt()
		return haltErr
	}
	return nil
}

// releaseAction is what the coordinator should do next.
type releaseAction struct {
	// ack: positions/records to hand to Source.Ack (coalesced across entries).
	positions []opencdc.Position
	records   []opencdc.Record
	readAt    time.Time
	// nack: a single position to write to the DLQ and then ack.
	nack     bool
	nackPos  opencdc.Position
	nackInfo nackInfo
	// spans to advance once the I/O succeeded.
	spans []releaseSpan
}

type releaseSpan struct {
	e  *ledgerEntry
	to int
}

// next returns the next release action, or nil if the head of the ledger is
// not terminal yet. Invariant 2: it only ever looks at the head, in order.
func (l *ledger) next() *releaseAction {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == 0 || l.err != nil {
		return nil
	}
	head := l.entries[0]
	if head.released >= len(head.positions) {
		return nil
	}
	switch head.state[head.released] { //nolint:exhaustive // posAcked is the fall-through: coalesce below
	case posPending:
		return nil
	case posNacked:
		return &releaseAction{
			nack:     true,
			nackPos:  head.positions[head.released],
			nackInfo: head.nacks[head.released],
			readAt:   head.readAt,
			spans:    []releaseSpan{{e: head, to: head.released + 1}},
		}
	}

	a := &releaseAction{readAt: head.readAt}
	for _, e := range l.entries {
		from := e.released
		to := from
		for to < len(e.positions) && e.state[to] == posAcked && len(a.positions)+(to-from) < maxReleaseCoalesce {
			to++
		}
		if to > from {
			a.positions = append(a.positions, e.positions[from:to]...)
			a.records = append(a.records, e.records[from:to]...)
			a.spans = append(a.spans, releaseSpan{e: e, to: to})
		}
		if to < len(e.positions) {
			break // stopped inside an entry: pending, nacked, or the cap
		}
	}
	return a
}

// advance applies a completed release: it moves the released marks, pops
// entries that are fully released and returns their credits.
func (l *ledger) advance(a *releaseAction, c *credits) {
	l.mu.Lock()
	for _, s := range a.spans {
		s.e.released = s.to
	}
	for len(l.entries) > 0 {
		h := l.entries[0]
		if h.released < len(h.positions) {
			break
		}
		l.entries[0] = nil
		l.entries = l.entries[1:]
		c.release(int64(len(h.positions)), h.bytes)
	}
	l.broadcastLocked()
	l.mu.Unlock()
}
