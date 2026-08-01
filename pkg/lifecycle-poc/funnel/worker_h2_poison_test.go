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
	"slices"
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// This file is the direct regression test for adversarial-review finding H2
// on #2734: an error inside a shared-destination pass (most critically
// DestinationTask.Do returning early on an Ack() error, leaving acks still
// queued on the shared stream) used to release sharedMu via doTask's plain
// `defer sharedMu.Unlock()` with no signal that the stream might now be
// desynchronized. A sibling worker that next acquired sharedMu could read
// the previous worker's leftover, unread ack as if it were its own -
// validateAcks' byte-for-byte comparison against ITS OWN positions PASSES
// when the two sources' position bytes happen to collide (legitimate across
// sources - positions are only unique WITHIN a source), silently acking
// records upstream that were never durably written by that worker's own
// destination write (invariant 1).
//
// The fix (see TaskNode.poisoned and its use in Worker.doTask) poisons the
// shared boundary INSIDE the critical section, before sharedMu is released,
// so no sibling can ever observe an unlocked-but-desynchronized shared
// destination - closing the race regardless of how fast rp.t.Kill's context
// cancellation propagates.

// poisonProbeDestination simulates a single shared gRPC ack stream: Ack()
// hands out whatever is next in a FIFO queue that every Write call appends
// to, with NO correlation to which caller's Write produced which entry -
// exactly like a real destination's stream, which is what lets a "leftover"
// ack from one caller be read by the next if nothing prevents it (see the
// type doc above).
type poisonProbeDestination struct {
	id string

	mu    sync.Mutex
	queue []connector.DestinationAck
	// failAt makes the failAt-th Ack() call (1-indexed, across the whole
	// destination's lifetime) return errAck instead of a real ack, leaving
	// whatever is still queued behind it un-delivered - modeling
	// DestinationTask.Do returning early on an Ack() error with acks still
	// queued on the stream (H2's exact trigger).
	failAt  int
	ackCall int
	errAck  error

	writes int
	// secondWriteEntered is closed the first time Write is called a SECOND
	// time - i.e. a sibling worker got past doTask's poisoning check and
	// actually reached DestinationTask.Do again. It staying open (checked
	// via a non-blocking select) is exactly what the fix guarantees: a
	// worker refused entry at doTask never calls Write at all.
	secondWriteEntered chan struct{}
}

func newPoisonProbeDestination(id string, failAt int, errAck error) *poisonProbeDestination {
	return &poisonProbeDestination{
		id:                 id,
		failAt:             failAt,
		errAck:             errAck,
		secondWriteEntered: make(chan struct{}),
	}
}

func (d *poisonProbeDestination) ID() string                     { return d.id }
func (d *poisonProbeDestination) Open(context.Context) error     { return nil }
func (d *poisonProbeDestination) Teardown(context.Context) error { return nil }
func (d *poisonProbeDestination) Errors() <-chan error           { return make(chan error) }

func (d *poisonProbeDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	d.writes++
	if d.writes == 2 {
		close(d.secondWriteEntered)
	}
	for _, r := range recs {
		d.queue = append(d.queue, connector.DestinationAck{Position: r.Position})
	}
	d.mu.Unlock()
	return nil
}

func (d *poisonProbeDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ackCall++
	if d.ackCall == d.failAt {
		return nil, d.errAck
	}
	if len(d.queue) == 0 {
		return nil, cerrors.New("poisonProbeDestination: Ack called with nothing queued")
	}
	ack := d.queue[0]
	d.queue = d.queue[1:]
	return []connector.DestinationAck{ack}, nil
}

// TestNSource_H2_AckStreamErrorPoisonsSharedDestination is the fail-without/
// pass-with regression test for H2. Source A writes two records; the SECOND
// carries a position that collides, BYTE FOR BYTE, with source B's only
// record - legitimate across sources (positions are only unique WITHIN a
// source; see tests/chaos/nsource_child.go's deliberately DISJOINT posOffset,
// which is exactly the collision case this test does NOT avoid). A's second
// Ack() call fails, leaving its second record's ack sitting unread in the
// probe's queue. Without the H2 fix, B's subsequent pass reads THAT leftover
// ack (byte-identical to B's own expected position) and validateAcks
// incorrectly passes, acking a record B's own write never durably confirmed.
// With the fix, B's doTask call is refused entry outright - it never calls
// Write, and never acks anything.
func TestNSource_H2_AckStreamErrorPoisonsSharedDestination(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	collidingPos := opencdc.Position("collide")
	recordsA := []opencdc.Record{
		{Position: opencdc.Position("a1")},
		{Position: collidingPos},
	}
	recordsB := []opencdc.Record{
		{Position: collidingPos},
	}

	ackErr := cerrors.New("simulated ack stream read failure")
	dest := newPoisonProbeDestination("shared", 2, ackErr)

	destTask := NewDestinationTask("shared-dest", dest, log.Nop(), NoOpConnectorMetrics{})
	sharedRoot := &TaskNode{Task: destTask}
	sink, err := NewSink(sharedRoot)
	is.NoErr(err)
	is.NoErr(sink.Open(ctx))
	defer func() { is.NoErr(sink.Close(ctx)) }()

	wA := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}
	wB := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}
	parentA := &fakeParentAckNacker{}
	parentB := &fakeParentAckNacker{}

	// Worker A's pass: Write succeeds; the first Ack (a1) succeeds; the
	// second Ack (the colliding position) fails with ackErr. Do returns the
	// error, and doTask must poison sharedRoot BEFORE releasing sharedMu.
	errA := wA.doTask(ctx, sharedRoot, NewBatch(slices.Clone(recordsA)), parentA)
	is.True(errA != nil)
	is.True(cerrors.Is(errA, ackErr))

	// Worker B's pass must be refused ENTRY - never reach Write, and
	// therefore never read A's leftover ack for the colliding position.
	errB := wB.doTask(ctx, sharedRoot, NewBatch(slices.Clone(recordsB)), parentB)
	is.True(errB != nil)
	ce, ok := conduiterr.Get(errB)
	is.True(ok)
	is.Equal(ce.Code, CodeSharedDestinationPoisoned)

	select {
	case <-dest.secondWriteEntered:
		t.Fatal("worker B's Write was called on a poisoned shared destination - H2 not fixed: " +
			"a sibling could read a leftover, unread ack from a previous worker's failed pass")
	default:
		// Correct: B never even attempted the write.
	}

	// The core invariant-1 assertion: B must never have acked anything -
	// acking the colliding position here would mean B acked a record
	// upstream on the strength of A's ack, not its own.
	is.Equal(0, len(parentB.ackCalls()))
}
