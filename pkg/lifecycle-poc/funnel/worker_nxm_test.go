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
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// This file tests N sources x M destinations TOGETHER - the combination
// slice 3a (M destinations, worker_fanout_test.go) and slice 3b (N sources,
// worker_nsource_test.go) each shipped independently, but which nothing
// exercised at the combination until now. See
// docs/design-documents/20260801-archv2-multiconnector-nsource.md and
// pkg/lifecycle-poc.Service.buildSharedTail's doc for the structural fact
// this file is built around:
//
// buildSharedTail returns TWO STRUCTURALLY DIFFERENT graphs depending on
// whether the pipeline has any pipeline-level processors:
//
//   - >=1 processor: ONE shared root (the processor chain, which itself fans
//     out internally to every destination branch) - funnel.NewSink marks
//     exactly that one node, so EVERY worker serializes on ONE mutex that
//     spans the processor AND every destination branch. A second source
//     cannot even begin writing to an otherwise-idle destination while a
//     first source is still mid-write to a DIFFERENT destination.
//   - 0 processors: M INDEPENDENT roots, one per destination branch - each
//     individually marked by funnel.NewSink, each with its OWN mutex. Two
//     different sources can write to two different destinations at the same
//     time.
//
// Adding a single pipeline-level processor to an N×M pipeline therefore
// silently collapses M independent per-destination locks into one global
// lock - no error, no warning, no metric. buildSharedTail's own shape is
// covered directly at the service level
// (TestBuildSharedTail_ShapeByProcessorPresence in
// pkg/lifecycle-poc/service_nxm_test.go); this file covers the RUNTIME
// CONSEQUENCE of that shape - the thing an operator would actually feel - by
// making the two shapes' concurrency behavior directly observable:
//
//   - TestNxM_NegativeSpace_NoProcessor_CrossDestinationConcurrency proves
//     two different sources CAN write to two different destinations at the
//     same time when there is no shared processor.
//   - TestNxM_PositiveComplement_WithProcessor_NoOverlapInSharedSubtree proves
//     they CANNOT when there is one.
//
// The remaining tests cover the other N×M-specific risk: source positions are
// only unique WITHIN a source, so two sources sharing M destinations can emit
// byte-identical positions, and the DLQ/ack routing must stay correctly
// attributed per source despite that collision.

// buildNxMWorkers wires len(sources) independent Workers (N), each with its
// own per-source DLQ, converging on a shared tail spanning len(dests)
// destination branches (M) - built exactly the way
// lifecycle-poc.Service.buildSharedTail does in production (see that
// function's doc, mirrored here):
//
//   - withProcessor == true: ONE shared root (a passthrough stand-in for a
//     pipeline-level processor chain) fans out internally to all M
//     destination branches, and is the ONLY node funnel.NewSink marks -
//     every source serializes on a SINGLE mutex spanning the "processor" and
//     every destination branch.
//   - withProcessor == false: M INDEPENDENT roots, one per destination
//     branch, each individually marked by funnel.NewSink - M separate
//     mutexes, so different destinations can proceed without blocking each
//     other.
//
// Returns the workers (index i is source i's own Worker), the Sink owning the
// shared tail's lifecycle, and the per-source DLQ fakeDestinations (index i
// is source i's own DLQ - windowSize/windowNackThreshold are both nonzero so
// the DLQ actually accepts nacks, needed by the collision/attribution tests
// below; see DLQ.Nack's windowNackThreshold==0-disables-the-DLQ behavior).
func buildNxMWorkers(t *testing.T, withProcessor bool, dests []Destination, sources ...Source) ([]*Worker, *Sink, []*fakeDestination) {
	t.Helper()
	is := is.New(t)
	logger := log.Test(t)

	destBranches := make([]*TaskNode, len(dests))
	for i, d := range dests {
		task := NewDestinationTask(fmt.Sprintf("dest-%d", i), d, logger, NoOpConnectorMetrics{})
		destBranches[i] = &TaskNode{Task: task}
	}

	var sharedRoots []*TaskNode
	if withProcessor {
		procRoot := &TaskNode{Task: passthroughTask{id: "shared-proc"}}
		is.NoErr(procRoot.AppendToEnd(destBranches...))
		sharedRoots = []*TaskNode{procRoot}
	} else {
		sharedRoots = destBranches
	}

	sink, err := NewSink(sharedRoots...)
	is.NoErr(err)

	workers := make([]*Worker, len(sources))
	dlqs := make([]*fakeDestination, len(sources))
	for i, src := range sources {
		dlqDest := newFakeDestination(fmt.Sprintf("dlq-%d", i))
		dlqs[i] = dlqDest
		dlq := NewDLQ(fmt.Sprintf("dlq-%d", i), dlqDest, logger, NoOpConnectorMetrics{}, 10, 10)

		srcNode := &TaskNode{Task: NewSourceTask(fmt.Sprintf("src-%d", i), src, logger, NoOpConnectorMetrics{})}
		is.NoErr(srcNode.AppendToEnd(sharedRoots...))

		w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
		is.NoErr(err)
		workers[i] = w
	}
	return workers, sink, dlqs
}

// recordsWithPositions builds records carrying the given raw position values,
// distinguishable by a keyPrefix-derived Key even when two calls are given
// the same positions - used to deliberately construct colliding positions
// across two different sources (positions are only unique WITHIN a source).
func recordsWithPositions(keyPrefix string, positions []string) []opencdc.Record {
	recs := make([]opencdc.Record, len(positions))
	for i, p := range positions {
		recs[i] = opencdc.Record{
			Key: opencdc.RawData(fmt.Sprintf("%s-%d", keyPrefix, i)),
			Payload: opencdc.Change{
				Before: opencdc.RawData{},
				After:  opencdc.RawData(keyPrefix),
			},
			Position: opencdc.Position(p),
		}
	}
	return recs
}

func positionsOf(recs []opencdc.Record) []opencdc.Position {
	out := make([]opencdc.Position, len(recs))
	for i, r := range recs {
		out[i] = r.Position
	}
	return out
}

// TestNxM_CollidingPositionsAcrossSources_EachSourceAcksOwnOnly is the direct
// N×M collision test: two sources emit records at byte-IDENTICAL positions
// (legal - positions are only unique within a source), fanned out to M=2
// shared destinations. Each source's Source.Ack must receive EXACTLY its own
// positions, once each, in order - never a sibling's, and never duplicated -
// and both destinations must receive every record from BOTH sources.
func TestNxM_CollidingPositionsAcrossSources_EachSourceAcksOwnOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	posA := recordsWithPositions("A", []string{"p0", "p1", "p2"})
	posB := recordsWithPositions("B", []string{"p0", "p1", "p2"})
	srcA := newFakeSource("srcA", posA)
	srcB := newFakeSource("srcB", posB)
	destX := newFakeDestination("destX")
	destY := newFakeDestination("destY")

	workers, sink, _ := buildNxMWorkers(t, false, []Destination{destX, destY}, srcA, srcB)
	wA, wB := workers[0], workers[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()
	go func() { doErrB <- wB.Do(ctx) }()

	waitForCondition(t, 5*time.Second, func() bool {
		return len(srcA.ackedPositions()) == len(posA) && len(srcB.ackedPositions()) == len(posB)
	})

	is.NoErr(wA.Stop(ctx))
	is.NoErr(<-doErrA)
	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.NoErr(wA.Close(ctx))
	is.NoErr(wB.Close(ctx))
	is.NoErr(sink.Close(ctx))

	// Each source's Ack calls carried EXACTLY its own positions, once each,
	// in order - never a sibling's, despite the byte-identical values.
	is.Equal(positionsOf(posA), srcA.ackedPositions())
	is.Equal(positionsOf(posB), srcB.ackedPositions())

	// Both destinations received all 6 records (3 from A + 3 from B).
	wantKeys := make(map[string]bool, 6)
	for _, r := range append(append([]opencdc.Record{}, posA...), posB...) {
		wantKeys[string(r.Key.(opencdc.RawData))] = true
	}
	for _, d := range []*fakeDestination{destX, destY} {
		is.Equal(6, len(d.written))
		gotKeys := make(map[string]bool, 6)
		for _, r := range d.written {
			gotKeys[string(r.Key.(opencdc.RawData))] = true
		}
		is.Equal(wantKeys, gotKeys)
	}
}

// TestNxM_DLQAttribution_CollidingPositions_NackedOnlyInFailingSourcesDLQ
// covers DLQ attribution under the same collision: destination Y nacks
// source A's "p2" specifically (matched by content, not position - see
// fakeDestination.nackMatch's doc), while source B's byte-identical "p2" is
// left alone. The nacked record must land in A's own DLQ exactly once,
// tagged with destY's task ID ("dest-1"), and B's DLQ must stay empty. Both
// sources must still be fully acked (invariant 3: the DLQ write earns the
// ack, it is never dropped).
func TestNxM_DLQAttribution_CollidingPositions_NackedOnlyInFailingSourcesDLQ(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	posA := recordsWithPositions("A", []string{"p0", "p1", "p2"})
	posB := recordsWithPositions("B", []string{"p0", "p1", "p2"})
	srcA := newFakeSource("srcA", posA)
	srcB := newFakeSource("srcB", posB)

	destX := newFakeDestination("destX") // dest-0: never fails
	destY := newFakeDestination("destY") // dest-1: fails A's p2 only
	failKey := string(posA[2].Key.(opencdc.RawData))
	wantErr := cerrors.New("destY: simulated failure for source A's p2")
	destY.setNackMatch(func(r opencdc.Record) error {
		if string(r.Key.(opencdc.RawData)) == failKey {
			return wantErr
		}
		return nil
	})

	workers, sink, dlqs := buildNxMWorkers(t, false, []Destination{destX, destY}, srcA, srcB)
	wA, wB := workers[0], workers[1]
	dlqA, dlqB := dlqs[0], dlqs[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()
	go func() { doErrB <- wB.Do(ctx) }()

	waitForCondition(t, 5*time.Second, func() bool {
		return len(srcA.ackedPositions()) == len(posA) && len(srcB.ackedPositions()) == len(posB)
	})

	is.NoErr(wA.Stop(ctx))
	is.NoErr(<-doErrA)
	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.NoErr(wA.Close(ctx))
	is.NoErr(wB.Close(ctx))
	is.NoErr(sink.Close(ctx))

	// A's p2 landed in A's own DLQ exactly once, tagged with destY's task ID.
	is.Equal(1, len(dlqA.written))
	is.Equal(posA[2].Position, dlqA.written[0].Position)
	gotTaskID, err := dlqA.written[0].Metadata.GetConduitDLQNackNodeID()
	is.NoErr(err)
	is.Equal("dest-1", gotTaskID)
	gotErr, err := dlqA.written[0].Metadata.GetConduitDLQNackError()
	is.NoErr(err)
	is.Equal(wantErr.Error(), gotErr)

	// B's byte-identical position was never nacked: B's DLQ is empty.
	is.Equal(0, len(dlqB.written))

	// Both sources were still fully (and correctly ordered) acked, including
	// A's p2 - it was handled via the DLQ, not dropped.
	is.Equal(positionsOf(posA), srcA.ackedPositions())
	is.Equal(positionsOf(posB), srcB.ackedPositions())
}

// rendezvousPair is a hard, deadlocking 2-party barrier: it only resolves
// once BOTH parties have called Done, at which point both Waits unblock
// together. See rendezvousDestination for why a barrier - not merely an
// assertion - is what makes the negative-space concurrency test below fail
// LOUD (as a timeout) rather than possibly passing by accident on a
// regression that only partially serializes access.
type rendezvousPair struct {
	wg sync.WaitGroup
}

func newRendezvousPair() *rendezvousPair {
	p := &rendezvousPair{}
	p.wg.Add(2)
	return p
}

// rendezvousDestination wraps a fakeDestination and blocks the Write call
// that carries the record at gatePos until the SIBLING rendezvousDestination
// sharing the same pair has simultaneously reached its own gated Write. If
// the two destinations can never be written to concurrently - e.g.
// buildSharedTail's no-processor, M-independent-root shape ever regressed
// into a single shared lock - this deadlocks rather than merely failing an
// assertion, which is what makes the regression this test targets ("an
// M-fold throughput regression that no correctness test would otherwise
// notice") impossible to pass by accident.
type rendezvousDestination struct {
	*fakeDestination
	gatePos opencdc.Position
	pair    *rendezvousPair
}

func (d *rendezvousDestination) Write(ctx context.Context, recs []opencdc.Record) error {
	for _, r := range recs {
		if string(r.Position) == string(d.gatePos) {
			d.pair.wg.Done()
			d.pair.wg.Wait()
			break
		}
	}
	return d.fakeDestination.Write(ctx, recs)
}

// TestNxM_NegativeSpace_NoProcessor_CrossDestinationConcurrency is the
// negative-space concurrency test (buildSharedTail's no-processor shape):
// with NO pipeline-level processor, source A writing to destination A and
// source B writing to destination B (a DIFFERENT source, a DIFFERENT
// destination) must be able to be inside Write AT THE SAME TIME - proven by a
// hard rendezvous barrier both must reach. This fails LOUD (a timeout, not a
// wrong assertion) if buildSharedTail's M-independent-root shape were ever
// collapsed to a single shared root: a regression that would otherwise be an
// invisible M-fold throughput loss, since nothing about per-record
// correctness would change.
func TestNxM_NegativeSpace_NoProcessor_CrossDestinationConcurrency(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	recA := randomRecords(1)
	recB := randomRecords(1)
	recB[0].Position = opencdc.Position("b-0") // distinct from A's - this test is about lock independence, not collision

	srcA := newFakeSource("srcA", recA)
	srcB := newFakeSource("srcB", recB)

	pair := newRendezvousPair()
	destA := &rendezvousDestination{fakeDestination: newFakeDestination("destA"), gatePos: recA[0].Position, pair: pair}
	destB := &rendezvousDestination{fakeDestination: newFakeDestination("destB"), gatePos: recB[0].Position, pair: pair}

	workers, sink, _ := buildNxMWorkers(t, false, []Destination{destA, destB}, srcA, srcB)
	wA, wB := workers[0], workers[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()
	go func() { doErrB <- wB.Do(ctx) }()

	// The rendezvous only resolves if A's write into destA and B's write
	// into destB are simultaneously in progress - i.e. genuinely concurrent
	// across two DIFFERENT sources' passes into two DIFFERENT destination
	// branches, each behind its own independent lock.
	rendezvousDone := make(chan struct{})
	go func() {
		pair.wg.Wait()
		close(rendezvousDone)
	}()
	select {
	case <-rendezvousDone:
	case <-time.After(10 * time.Second):
		t.Fatal("rendezvous never resolved: destA and destB (written by DIFFERENT sources) were never " +
			"simultaneously inside Write - buildSharedTail's no-processor shape must give each destination " +
			"branch its own independent lock, not one shared lock across all M branches")
	}

	waitForCondition(t, 5*time.Second, func() bool {
		return len(srcA.ackedPositions()) == 1 && len(srcB.ackedPositions()) == 1
	})

	is.NoErr(wA.Stop(ctx))
	is.NoErr(<-doErrA)
	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.NoErr(wA.Close(ctx))
	is.NoErr(wB.Close(ctx))
	is.NoErr(sink.Close(ctx))
}

// TestNxM_PositiveComplement_WithProcessor_NoOverlapInSharedSubtree is the
// positive complement of the negative-space test above: WITH a pipeline-level
// processor (buildSharedTail's single-shared-root shape), no two workers may
// ever be inside the shared subtree at the same time - not even to write to a
// destination branch neither of them is currently contending over. Source A
// is stuck writing to destA; source B must NOT be able to enter destB (which
// is otherwise completely free) until A's ENTIRE pass - across every branch -
// has released the single shared mutex.
func TestNxM_PositiveComplement_WithProcessor_NoOverlapInSharedSubtree(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	recA := randomRecords(1)
	recB := randomRecords(1)
	recB[0].Position = opencdc.Position("b-0")

	srcA := newFakeSource("srcA", recA)
	srcB := newFakeSource("srcB", recB)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	workers, sink, _ := buildNxMWorkers(t, true, []Destination{destA, destB}, srcA, srcB)
	wA, wB := workers[0], workers[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	// A gets stuck writing to destA. With withProcessor=true this holds the
	// ONE shared-root mutex for A's ENTIRE pass (destA AND destB), not just
	// destA's own branch - see buildSharedTail's doc.
	blockedA, unblockA := destA.blockWrites(recA[0].Position)

	doErrA := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()

	select {
	case <-blockedA:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worker A to block on destA")
	}

	// B tries to enter destB - nothing else is touching it, and in the
	// M-independent-root shape (see the negative-space test above) it would
	// be free to write to immediately. Gated on the exact record B will
	// write, so "entered" only fires once B actually reaches this Write
	// call.
	enteredB, unblockB := destB.blockWrites(recB[0].Position)

	doErrB := make(chan error, 1)
	go func() { doErrB <- wB.Do(ctx) }()

	select {
	case <-enteredB:
		t.Fatal("worker B entered destB while worker A still held the single shared-root mutex (A was still " +
			"blocked inside destA) - buildSharedTail's with-processor shape must serialize EVERY branch " +
			"behind ONE lock, not just the branch A happened to be using")
	case <-time.After(300 * time.Millisecond):
		// Expected: B is still blocked trying to acquire the single shared
		// mutex. 300ms is generous relative to how fast an uncontended
		// acquire+entry would be if the lock were ever missing.
	}

	// Release A - B must now be able to make progress, promptly: it only
	// needed the SAME single shared-root mutex A just released, and that
	// release happens the instant A's own synchronous pass through the
	// shared subtree returns.
	unblockA()

	select {
	case <-enteredB:
	case <-time.After(5 * time.Second):
		t.Fatal("worker B never entered destB even after worker A released the shared lock")
	}
	unblockB()

	// Every source writes to every destination (the shared tail is attached
	// identically to each source - see buildRunnablePipeline), so each of
	// destA/destB ends up with ONE record from A and ONE from B: 2 each.
	waitForCondition(t, 5*time.Second, func() bool {
		return len(destA.receivedPositions()) == 2 && len(destB.receivedPositions()) == 2
	})

	is.NoErr(wA.Stop(ctx))
	is.NoErr(<-doErrA)
	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.NoErr(wA.Close(ctx))
	is.NoErr(wB.Close(ctx))
	is.NoErr(sink.Close(ctx))
}

// TestNxM_SharedBoundary_RetryDoesNotSelfDeadlock_MultipleDestinations
// generalizes TestNSource_SharedBoundary_RetryDoesNotSelfDeadlock (M=1) to
// M>1: a retry inside a shared-boundary node must not self-deadlock on the
// non-reentrant sharedMu (the lock lives in the doTask WRAPPER precisely
// because the retry recursion re-enters doTaskAttempt directly - see
// worker.go's doTask doc), and once the retry resolves, the fan-out to every
// one of the M destination branches beneath it must still run correctly.
func TestNxM_SharedBoundary_RetryDoesNotSelfDeadlock_MultipleDestinations(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(4)

	retryOnce := &retryOnceThenPassTask{id: "shared-retry"}
	node := &TaskNode{Task: retryOnce}

	destX := newFakeDestination("destX")
	destY := newFakeDestination("destY")
	destXNode := &TaskNode{Task: NewDestinationTask("destX", destX, log.Nop(), NoOpConnectorMetrics{})}
	destYNode := &TaskNode{Task: NewDestinationTask("destY", destY, log.Nop(), NoOpConnectorMetrics{})}
	is.NoErr(node.AppendToEnd(destXNode, destYNode))
	node.MarkSharedBoundary() // exactly what funnel.Sink does for a shared root

	parent := &fakeParentAckNacker{}
	w := &Worker{logger: log.Nop(), processingLock: make(chan struct{}, 1)}

	done := make(chan error, 1)
	go func() { done <- w.doTask(ctx, node, NewBatch(slices.Clone(records)), parent) }()

	select {
	case err := <-done:
		is.NoErr(err)
		is.True(retryOnce.calls >= 2) // the retry actually happened
	case <-time.After(10 * time.Second):
		t.Fatal("doTask deadlocked: a retry inside a shared-boundary node with M>1 destination branches " +
			"re-entered the shared mutex. The lock must be acquired in the doTask wrapper, not in " +
			"doTaskAttempt (which the retry recursion calls directly).")
	}

	is.Equal(4, len(destX.receivedPositions()))
	is.Equal(4, len(destY.receivedPositions()))
}

// TestNxM_OneSourceExhausts_OtherStreams_BothDestinationsWritable covers the
// Service-level scenario the arch-v2 N×M gate requires ("one source exhausts
// while another streams, pipeline stays Running, both destinations still
// writable") at the funnel level, with a GENUINELY, concurrently-streaming
// second source - unlike the Service-level counterpart in
// pkg/lifecycle-poc/service_nxm_test.go
// (TestServiceLifecycle_NxM_OneSourceExhausts_OtherStreams_BothDestinationsWritable),
// which had to fall back to an IDLE sibling because a genuinely concurrent
// second source at that layer reliably reproduces a pre-existing,
// independent data race/panic in connector.Persister (see that test's doc
// comment for the full finding). This package never touches
// connector.Persister, so the real property - a second source actually
// writing new records through both shared destinations AFTER the first
// source has exhausted and torn itself down - is provable here directly.
func TestNxM_OneSourceExhausts_OtherStreams_BothDestinationsWritable(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	recordsA := randomRecords(2)
	recordsB := randomRecords(3)
	for i := range recordsB {
		recordsB[i].Position = opencdc.Position(fmt.Sprintf("b-%d", i)) // distinct from A's
	}

	srcA := newFiniteSource("srcA", recordsA) // exhausts via io.EOF, nobody calls Stop
	srcB := newFakeSource("srcB", recordsB)
	destX := newFakeDestination("destX")
	destY := newFakeDestination("destY")

	workers, sink, _ := buildNxMWorkers(t, false, []Destination{destX, destY}, srcA, srcB)
	wA, wB := workers[0], workers[1]

	is.NoErr(sink.Open(ctx))
	is.NoErr(wA.Open(ctx))
	is.NoErr(wB.Open(ctx))

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- wA.Do(ctx) }()
	go func() { doErrB <- wB.Do(ctx) }()

	// A must exit on its own (nil, not an error) once it runs out of records.
	select {
	case err := <-doErrA:
		is.NoErr(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for srcA's worker to finish gracefully on io.EOF")
	}
	is.NoErr(wA.Close(ctx))

	// B keeps streaming AFTER A is fully gone, through the SAME two shared
	// destinations - proof both are still writable post-A's exit.
	waitForCondition(t, 5*time.Second, func() bool { return len(srcB.ackedPositions()) == len(recordsB) })

	is.NoErr(wB.Stop(ctx))
	is.NoErr(<-doErrB)
	is.NoErr(wB.Close(ctx))
	is.NoErr(sink.Close(ctx))

	wantKeys := make(map[string]bool, len(recordsA)+len(recordsB))
	for _, r := range append(append([]opencdc.Record{}, recordsA...), recordsB...) {
		wantKeys[string(r.Position)] = true
	}
	for _, d := range []*fakeDestination{destX, destY} {
		is.Equal(len(recordsA)+len(recordsB), len(d.written))
		gotKeys := make(map[string]bool, len(d.written))
		for _, r := range d.written {
			gotKeys[string(r.Position)] = true
		}
		is.Equal(wantKeys, gotKeys)
	}
}
