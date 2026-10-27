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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// funcTask is a Task whose Do is a test-supplied function, standing in for a
// processor that filters or nacks records.
type funcTask struct {
	id string
	do func(*Batch) error
}

func (f funcTask) ID() string                           { return f.id }
func (f funcTask) Open(context.Context) error           { return nil }
func (f funcTask) Close(context.Context) error          { return nil }
func (f funcTask) Do(_ context.Context, b *Batch) error { return f.do(b) }

// orderedAckRig is source -> processor -> pipelined batching destination (size
// 2) with a destination whose first Write blocks, so batch 1 is in flight (or
// buffered) while later source batches are processed.
type orderedAckRig struct {
	w        *Worker
	src      *fakeSource
	dest     *fakeDestination
	dt       *DestinationTask
	procNode *TaskNode
	recs     []opencdc.Record
}

func newOrderedAckRig(t *testing.T, proc func(*Batch) error) *orderedAckRig {
	t.Helper()
	return newOrderedAckRigDLQ(t, proc, 10, 10)
}

func newOrderedAckRigDLQ(t *testing.T, proc func(*Batch) error, dlqWindow, dlqThreshold int) *orderedAckRig {
	t.Helper()
	logger := log.Test(t)
	recs := randomRecords(6)
	src := newFakeSource("src", nil)
	dest := newFakeDestination("dest")
	dt := newPipelinedTask(t, dest, 2, 0)

	dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, dlqWindow, dlqThreshold)
	destNode := &TaskNode{Task: dt}
	procNode := &TaskNode{Task: funcTask{id: "proc", do: proc}, Next: []*TaskNode{destNode}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{procNode}}
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.New(t).NoErr(err)
	return &orderedAckRig{w: w, src: src, dest: dest, dt: dt, procNode: procNode, recs: recs}
}

func (r *orderedAckRig) pass(ctx context.Context, rec ...opencdc.Record) error {
	return r.w.doTask(ctx, r.procNode, NewBatch(rec), newRunAckNacker(r.w))
}

func posOf(recs ...opencdc.Record) []opencdc.Position {
	out := make([]opencdc.Position, len(recs))
	for i, r := range recs {
		out[i] = r.Position
	}
	return out
}

// TestWorker_FilteredBatch_AckOrderedBehindInflightFlush is the regression test
// for the ack-ordering hole in the batching destination path.
//
// Batch 1 (p0, p1) fills the destination buffer and is handed to the flusher,
// which is blocked inside Write: p0/p1 are NOT durable. Batch 2 (p2) is
// filtered entirely by a processor, so it never reaches the destination.
// Before the fix the worker acked p2 on the spot; connector.Source.Ack persists
// State.Position = p[len-1] with no monotonic guard, so a crash at this instant
// resumed after p2 and skipped p0 and p1 (invariants 1, 2, 3).
func TestWorker_FilteredBatch_AckOrderedBehindInflightFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var rig *orderedAckRig
	rig = newOrderedAckRig(t, func(b *Batch) error {
		if string(b.records[0].Position) == string(rig.recs[2].Position) {
			b.Filter(0)
		}
		return nil
	})
	blocked, unblock := rig.dest.blockWrites(rig.recs[0].Position)

	is.NoErr(rig.pass(ctx, rig.recs[0], rig.recs[1]))
	<-blocked // batch 1 is in flight at the destination, not durable

	is.NoErr(rig.pass(ctx, rig.recs[2])) // fully filtered

	// Invariant 1/2/3: nothing may be acked to the source while p0/p1 are
	// in flight, filtered or not.
	time.Sleep(20 * time.Millisecond)
	is.Equal(rig.src.ackedPositions(), []opencdc.Position(nil))

	unblock()
	waitForCondition(t, time.Second, func() bool { return len(rig.src.ackedPositions()) == 3 })
	is.Equal(rig.src.ackedPositions(), posOf(rig.recs[:3]...)) // source order
	is.NoErr(rig.dt.Close(ctx))
}

// TestWorker_ProcessorNack_AckOrderedBehindBufferedRecords covers the nacked
// sub-batch route: record p1 of a batch is nacked by a processor (DLQ) while
// p0 of the same batch sits in the destination buffer. Worker.Nack used to ack
// p1 to the source immediately, persisting a position past the unwritten p0.
func TestWorker_ProcessorNack_AckOrderedBehindBufferedRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	rig := newOrderedAckRig(t, func(b *Batch) error {
		if len(b.records) == 2 {
			b.Nack(1, cerrors.New("processor rejected record"))
		}
		return nil
	})

	// p0 is buffered (size 2, not flushed); p1 is nacked.
	is.NoErr(rig.pass(ctx, rig.recs[0], rig.recs[1]))

	// The queued nack counts toward the flush trigger (batch size 2), so the
	// flush may already be under way. Either way p1 must never be acked
	// without p0 ahead of it.
	time.Sleep(20 * time.Millisecond)
	if got := rig.src.ackedPositions(); len(got) > 0 {
		is.Equal(got, posOf(rig.recs[:2]...))
	}

	is.NoErr(rig.dt.Close(ctx)) // drains whatever is left
	waitForCondition(t, time.Second, func() bool { return len(rig.src.ackedPositions()) == 2 })
	is.Equal(rig.src.ackedPositions(), posOf(rig.recs[:2]...)) // p0 then p1
	is.Equal(rig.dest.receivedPositions(), posOf(rig.recs[0]))
}

// TestWorker_FilteredBatch_IdleAcksImmediately pins that the ordering fix does
// not add latency when nothing is buffered or in flight.
func TestWorker_FilteredBatch_IdleAcksImmediately(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	rig := newOrderedAckRig(t, func(b *Batch) error {
		b.Filter(0, len(b.ActiveRecords()))
		return nil
	})
	is.NoErr(rig.pass(ctx, rig.recs[0]))
	is.Equal(rig.src.ackedPositions(), posOf(rig.recs[0]))
	is.NoErr(rig.dt.Close(ctx))
}

// TestSplitRun_ConcurrentVotesReleaseOnce is the regression test for the
// unsynchronised splitRun. The destination flusher votes a run's members that
// reached the destination while the worker goroutine votes the members a
// processor nacked or filtered; both mutate terminalCount/released. Run under
// -race. The run must be released to the parent exactly once.
func TestSplitRun_ConcurrentVotesReleaseOnce(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	const members = 64
	batch := NewBatch([]opencdc.Record{{Position: opencdc.Position("orig")}})
	pieces := make([]opencdc.Record, members)
	for i := range pieces {
		pieces[i] = opencdc.Record{Position: opencdc.Position("piece")}
	}
	batch.SplitRecord(0, pieces)

	var acks atomic.Int32
	rec := &countingAckNacker{acks: &acks}
	voter := newRunAckNacker(rec)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := g; i < members; i += 2 {
				is.NoErr(voter.Ack(ctx, batch.sub(i, i+1)))
			}
		}()
	}
	close(start)
	wg.Wait()
	is.Equal(acks.Load(), int32(1))
}

type countingAckNacker struct{ acks *atomic.Int32 }

func (c *countingAckNacker) Ack(context.Context, *Batch) error {
	c.acks.Add(1)
	return nil
}
func (c *countingAckNacker) Nack(context.Context, *Batch, string) error { return nil }

// TestSplitRun_SplitRecordRacesWithVote: Batch.SplitRecord grows run.total on
// the worker goroutine while the flusher side credits a sibling member
// (terminalCount) and reads total in complete(). Run under -race; the run must
// still be released exactly once, after every member including the new pieces
// is terminal.
func TestSplitRun_SplitRecordRacesWithVote(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	batch := NewBatch([]opencdc.Record{{Position: opencdc.Position("orig")}})
	batch.SplitRecord(0, []opencdc.Record{{}, {}, {}, {}})
	subs := []*Batch{batch.sub(0, 1), batch.sub(1, 2), batch.sub(2, 3), batch.sub(3, 4)}

	var acks atomic.Int32
	voter := newRunAckNacker(&countingAckNacker{acks: &acks})

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() { // flusher side
		defer wg.Done()
		<-start
		is.NoErr(voter.Ack(ctx, subs[0]))
	}()
	go func() { // worker side: a later processor splits a sibling further
		defer wg.Done()
		<-start
		subs[1].SplitRecord(0, []opencdc.Record{{}, {}})
	}()
	close(start)
	wg.Wait()

	is.Equal(acks.Load(), int32(0)) // siblings still outstanding
	is.NoErr(voter.Ack(ctx, subs[1]))
	is.NoErr(voter.Ack(ctx, subs[2]))
	is.NoErr(voter.Ack(ctx, subs[3]))
	is.Equal(acks.Load(), int32(1))
}

// TestWorker_FanOutBatching_FilteredBatchAckOrdered covers batched fan-out
// (M batching destinations, synchronous flush): a batch filtered before the
// fan-out must not ack the source ahead of an earlier batch buffered in BOTH
// destinations.
func TestWorker_FanOutBatching_FilteredBatchAckOrdered(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	recs := randomRecords(2)

	src := newFakeSource("src", nil)
	destA, destB := newFakeDestination("A"), newFakeDestination("B")
	dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 3, 0)
	dtB := NewBatchingDestinationTask("B", destB, logger, NoOpConnectorMetrics{}, 3, 0)
	proc := funcTask{id: "proc", do: func(b *Batch) error {
		if string(b.records[0].Position) == string(recs[1].Position) {
			b.Filter(0)
		}
		return nil
	}}
	procNode := &TaskNode{Task: proc, Next: []*TaskNode{{Task: dtA}, {Task: dtB}}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{procNode}}
	dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	is.NoErr(w.doTask(ctx, procNode, NewBatch(recs[:1]), newRunAckNacker(w))) // buffered in A and B
	is.NoErr(w.doTask(ctx, procNode, NewBatch(recs[1:]), newRunAckNacker(w))) // filtered
	is.Equal(src.ackedPositions(), []opencdc.Position(nil))

	is.NoErr(dtA.Close(ctx))
	is.NoErr(dtB.Close(ctx))
	is.Equal(src.ackedPositions(), posOf(recs...))
}

// TestWorker_NSourceFanOut_FilteredAckOrderedBehindSyncFlush: two sources share
// a fan-out to a batching destination A (synchronous flush) and a plain
// destination B. Source 2's record s2a is buffered in A; source 1's record
// triggers A's flush, which blocks in Write. Source 2's next batch is filtered
// by a source-scoped processor, i.e. OUTSIDE the shared lock, so nothing
// serialises it with A's flush. It must not ack source 2 while s2a is in flight.
func TestWorker_NSourceFanOut_FilteredAckOrderedBehindSyncFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	recs := randomRecords(3) // s2a, s1a, s2b

	destA, destB := newFakeDestination("A"), newFakeDestination("B")
	dtA := NewBatchingDestinationTask("A", destA, logger, NoOpConnectorMetrics{}, 2, 0)
	dtB := NewDestinationTask("B", destB, logger, NoOpConnectorMetrics{})
	nodeA, nodeB := &TaskNode{Task: dtA}, &TaskNode{Task: dtB}
	nodeA.MarkSharedBoundary()
	nodeB.MarkSharedBoundary()

	mk := func(id string, filter opencdc.Position) (*Worker, *fakeSource, *TaskNode) {
		src := newFakeSource(id, nil)
		proc := funcTask{id: "proc-" + id, do: func(b *Batch) error {
			if filter != nil && string(b.records[0].Position) == string(filter) {
				b.Filter(0)
			}
			return nil
		}}
		pn := &TaskNode{Task: proc, Next: []*TaskNode{nodeA, nodeB}}
		sn := &TaskNode{Task: NewSourceTask(id, src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{pn}}
		dlq := NewDLQ("dlq-"+id, newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
		w, err := NewWorker(sn, dlq, logger, noop.Timer{})
		is.NoErr(err)
		return w, src, pn
	}
	w1, _, p1 := mk("s1", nil)
	w2, src2, p2 := mk("s2", recs[2].Position)

	is.NoErr(w2.doTask(ctx, p2, NewBatch(recs[:1]), newRunAckNacker(w2))) // s2a buffered in A
	blocked, release := destA.blockWrites(recs[0].Position)
	var once sync.Once
	unblock := func() { once.Do(release) }
	defer unblock()
	done := make(chan error, 1)
	go func() { done <- w1.doTask(ctx, p1, NewBatch(recs[1:2]), newRunAckNacker(w1)) }() // flush [s2a,s1a] blocks
	<-blocked

	is.NoErr(w2.doTask(ctx, p2, NewBatch(recs[2:3]), newRunAckNacker(w2))) // s2b filtered
	is.Equal(src2.ackedPositions(), []opencdc.Position(nil))               // s2a is not durable yet

	unblock()
	is.NoErr(<-done)
}

// TestDestinationTask_DeferOrdered_NotOvertakenByConcurrentFlushHandoff: another
// source's deferOrdered must not reach the flusher ahead of a batch doBuffered
// has already taken from pending. Without a lock around "take pending + send",
// the action is queued first and acks the source ahead of the earlier batch.
func TestDestinationTask_DeferOrdered_NotOvertakenByConcurrentFlushHandoff(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := newPipelinedTask(t, dest, 2, 0)
	recs := randomRecords(2)
	log := &eventLog{}

	taken := make(chan struct{})
	release := make(chan struct{})
	task.testAfterTake = func() {
		close(taken)
		<-release
	}

	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs[:1]), newRecordingAckNacker(log))))
	go func() {
		_ = task.doBuffered(ctx, NewBatch(recs[1:]), newRecordingAckNacker(log)) // takes [r0,r1], then waits in the hook
	}()
	<-taken

	deferred := make(chan error, 1)
	go func() {
		deferred <- task.deferOrdered(ctx, func(context.Context) error {
			log.append("action")
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	is.NoErr(<-deferred)

	waitForCondition(t, time.Second, func() bool { return len(log.snapshot()) == 3 })
	is.Equal(log.snapshot(), []string{"acker.ack", "acker.ack", "action"})
	is.NoErr(task.Close(ctx))
}

// TestDestinationTask_DeferOrdered_ActionsCountTowardFlush: queued source acks
// hold a *Batch each and pin the source position. Filter-heavy input behind one
// buffered record must flush by count, not pile up until the next real record.
func TestDestinationTask_DeferOrdered_ActionsCountTowardFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := newPipelinedTask(t, dest, 4, 0)
	recs := randomRecords(1)
	log := &eventLog{}

	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(log))))
	for i := 0; i < 4; i++ {
		is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
			log.append("action")
			return nil
		}))
	}
	waitForCondition(t, time.Second, func() bool { return len(log.snapshot()) == 5 })
	is.Equal(len(dest.receivedPositions()), 1) // the buffered record was flushed
	is.NoErr(task.Close(ctx))
}

// TestDestinationTask_DeferOrdered_HonoursFlushDeadline: once the batch delay
// has passed, a queued source ack triggers the flush that releases it.
func TestDestinationTask_DeferOrdered_HonoursFlushDeadline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	dest := newFakeDestination("d")
	task := newPipelinedTask(t, dest, 100, 20*time.Millisecond)
	recs := randomRecords(1)
	log := &eventLog{}

	is.True(isBuffered(task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(log))))
	time.Sleep(40 * time.Millisecond)
	is.NoErr(task.deferOrdered(ctx, func(context.Context) error {
		log.append("action")
		return nil
	}))
	waitForCondition(t, time.Second, func() bool { return len(log.snapshot()) == 2 })
	is.Equal(len(dest.receivedPositions()), 1)
	is.NoErr(task.Close(ctx))
}

// TestWorker_ProcessorNack_DLQDisabled_FailsPipelineOnStop: a processor nack
// with no DLQ budget is a pipeline error. Deferred behind a buffered record it
// now fails on the flusher; a graceful stop must report it, not swallow it as a
// clean stop.
func TestWorker_ProcessorNack_DLQDisabled_FailsPipelineOnStop(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	rig := newOrderedAckRigDLQ(t, func(b *Batch) error {
		if len(b.records) == 3 {
			e := cerrors.New("processor rejected record")
			b.Nack(1, e, e)
		}
		return nil
	}, 1, 0) // a DLQ budget of zero nacks: the second nack in a row is refused
	is.NoErr(rig.pass(ctx, rig.recs[0], rig.recs[1], rig.recs[2])) // p0 buffered, p1+p2's nack deferred

	err := rig.dt.Close(ctx)
	is.True(err != nil)
	is.True(isDeferredAckError(err))
}
