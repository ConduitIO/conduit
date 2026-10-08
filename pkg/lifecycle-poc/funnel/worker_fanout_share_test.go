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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// These tests cover #2910: Worker.doNextTask no longer deep-copies a batch's
// records for a fan-out branch made only of destination tasks; those branches
// share one copy. A branch that can modify records in place still gets its
// own copy. The existing fan-out tests (worker_fanout_test.go) all fan out to
// destination-only branches, so they now run on the shared path and are what
// shows the ack accounting is unchanged. The tests here cover what sharing
// itself could break.

// metadataTouchTask stands in for a destination-scoped processor that edits
// records in place, the way a builtin processor is allowed to. It writes into
// each record's existing Metadata map rather than replacing it, which is the
// shape that would leak into a sibling branch if that branch shared the map.
type metadataTouchTask struct{ id string }

func (m metadataTouchTask) ID() string                  { return m.id }
func (m metadataTouchTask) Open(context.Context) error  { return nil }
func (m metadataTouchTask) Close(context.Context) error { return nil }
func (m metadataTouchTask) Do(_ context.Context, b *Batch) error {
	for _, r := range b.records {
		r.Metadata["touched-by"] = m.id
	}
	return nil
}

// TestDoNextTask_FanOut_SharedRecords_MutatingBranchGetsOwnCopy builds a
// fan-out with one branch that edits records in place (processor then destA)
// and one destination-only branch (destB). destB's Write is held until the
// mutating branch has finished, so if the two branches shared records destB
// would deterministically see the edit. It must not: the mutating branch
// works on its own deep copy, and only destination-only branches share.
func TestDoNextTask_FanOut_SharedRecords_MutatingBranchGetsOwnCopy(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	records := randomRecords(4)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	destANode := &TaskNode{Task: NewDestinationTask(destA.id, destA, logger, NoOpConnectorMetrics{})}
	procNode := &TaskNode{Task: metadataTouchTask{id: "procA"}, Next: []*TaskNode{destANode}}
	destBNode := &TaskNode{Task: NewDestinationTask(destB.id, destB, logger, NoOpConnectorMetrics{})}
	branchNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{procNode, destBNode}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{branchNode}}

	dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 0, 0)
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	blocked, unblock := destB.blockWrites(records[0].Position)
	batch := NewBatch(append([]opencdc.Record(nil), records...))

	done := make(chan error, 1)
	go func() { done <- w.doTask(ctx, branchNode, batch, w) }()

	<-blocked
	waitForCondition(t, 5*time.Second, func() bool { return len(destA.receivedPositions()) == len(records) })
	unblock()
	is.NoErr(<-done)

	destA.mu.Lock()
	for _, r := range destA.written {
		is.Equal(r.Metadata["touched-by"], "procA") // the mutating branch saw its own edit
	}
	destA.mu.Unlock()

	destB.mu.Lock()
	for _, r := range destB.written {
		_, leaked := r.Metadata["touched-by"]
		is.True(!leaked) // an in-place edit in one branch must never reach a sibling
	}
	destB.mu.Unlock()

	for _, r := range batch.records {
		_, leaked := r.Metadata["touched-by"]
		is.True(!leaked) // nor the batch the branches were cut from
	}

	acked := src.ackedPositions()
	is.Equal(len(acked), len(records))
}

// TestDoNextTask_FanOut_SharedRecords_DestinationBranchesShare pins the other
// half: destination-only branches really do read the same record values
// rather than copies. If this ever stops holding, the per-destination cost of
// fan-out goes back to a deep copy of every record (#2910).
func TestDoNextTask_FanOut_SharedRecords_DestinationBranchesShare(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	records := randomRecords(3)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	w, branchNode := buildFanoutWorker(t, src, destA, destB)
	batch := NewBatch(append([]opencdc.Record(nil), records...))
	is.NoErr(w.doTask(ctx, branchNode, batch, w))

	destA.mu.Lock()
	destB.mu.Lock()
	defer destA.mu.Unlock()
	defer destB.mu.Unlock()
	is.Equal(len(destA.written), len(records))
	is.Equal(len(destB.written), len(records))
	for i := range records {
		// Writing through one record's Metadata map and reading it back
		// through the other is the observable test for "same map".
		destA.written[i].Metadata["probe"] = "x"
		is.Equal(destB.written[i].Metadata["probe"], "x")
	}
}

// TestDoNextTask_FanOut_SharedRecords_NackToDLQLeavesSharedRecordIntact
// covers the one path where a shared record leaves the fan-out while a
// sibling may still be reading it: a nack. multiAckNacker releases a nacked
// position as soon as it heads the queue, which can be before every other
// branch has finished with that record, and the DLQ then builds its entry
// from it. The DLQ must build a NEW record and leave the shared one as it
// was; editing it (adding the nack error to its metadata, say) would be seen
// by every sibling branch.
func TestDoNextTask_FanOut_SharedRecords_NackToDLQLeavesSharedRecordIntact(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	records := randomRecords(3)
	failPos := records[0].Position
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destA.setNackErr(failPos, cerrors.New("destA: simulated write failure"))
	destB := newFakeDestination("destB")

	destANode := &TaskNode{Task: NewDestinationTask(destA.id, destA, logger, NoOpConnectorMetrics{})}
	destBNode := &TaskNode{Task: NewDestinationTask(destB.id, destB, logger, NoOpConnectorMetrics{})}
	branchNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{destANode, destBNode}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{branchNode}}

	dlqDest := newFakeDestination("dlq")
	dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	// Hold destB inside its Write. A nack is terminal on its own and position
	// 0 heads the queue, so destA's nack is released to the DLQ while destB
	// is still holding the same record.
	blocked, unblock := destB.blockWrites(records[0].Position)
	batch := NewBatch(append([]opencdc.Record(nil), records...))

	done := make(chan error, 1)
	go func() { done <- w.doTask(ctx, branchNode, batch, w) }()

	<-blocked
	waitForCondition(t, 5*time.Second, func() bool { return len(dlqDest.receivedPositions()) == 1 })
	unblock()
	is.NoErr(<-done)

	is.Equal(dlqDest.receivedPositions(), []opencdc.Position{failPos})

	destB.mu.Lock()
	defer destB.mu.Unlock()
	is.Equal(len(destB.written), len(records))
	is.Equal(destB.written[0].Metadata, opencdc.Metadata{"key": "value"}) // untouched by the DLQ

	// Every position acked once: position 0 through the DLQ, the rest through
	// both destinations. Same accounting as before the change.
	acked := src.ackedPositions()
	is.Equal(len(acked), len(records))
	for i, r := range records {
		is.Equal(acked[i], r.Position)
	}
}

func TestBranchMutatesRecords(t *testing.T) {
	logger := log.Nop()
	dest := func(id string) *TaskNode {
		return &TaskNode{Task: NewDestinationTask(id, newFakeDestination(id), logger, NoOpConnectorMetrics{})}
	}

	testCases := []struct {
		name string
		node *TaskNode
		want bool
	}{{
		name: "destination only",
		node: dest("d"),
		want: false,
	}, {
		name: "processor before destination",
		node: &TaskNode{Task: metadataTouchTask{id: "p"}, Next: []*TaskNode{dest("d")}},
		want: true,
	}, {
		name: "non-destination task below a destination",
		node: &TaskNode{Task: dest("d").Task, Next: []*TaskNode{{Task: passthroughTask{id: "x"}}}},
		want: true,
	}, {
		name: "unknown task type is assumed to mutate",
		node: &TaskNode{Task: passthroughTask{id: "x"}},
		want: true,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is.New(t).Equal(branchMutatesRecords(tc.node), tc.want)
		})
	}
}

// TestDoNextTask_FanOut_DestinationBranchesDoNotCopyRecords is the
// regression test for #2910's cost, in allocations because they are
// deterministic where timings on shared hardware are not. Adding a
// destination-only branch must cost a bounded number of allocations per pass,
// independent of how many records the batch holds. Before the fix every
// branch deep-copied every record, so going from 2 to 4 destinations added
// about 5 allocations per record per destination (1655 -> 2683 allocs for a
// 100-record pass); after it, about 12 per destination in total.
func TestDoNextTask_FanOut_DestinationBranchesDoNotCopyRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation measurement runs each pass many times")
	}
	const batchSize = 100
	allocsPerPass := func(destCount int) float64 {
		w := benchWorker(t, batchSize, destCount, false)
		ctx := context.Background()
		return testing.AllocsPerRun(50, func() {
			if err := w.doTask(ctx, w.FirstTask, &Batch{}, newRunAckNacker(w)); err != nil {
				t.Fatal(err)
			}
		})
	}

	two, four := allocsPerPass(2), allocsPerPass(4)
	perExtraDestination := (four - two) / 2
	t.Logf("allocs per pass: 2 destinations %.0f, 4 destinations %.0f, %.1f per extra destination", two, four, perExtraDestination)
	if perExtraDestination >= batchSize/2 {
		t.Fatalf("each extra destination-only branch costs %.1f allocations per %d-record pass; "+
			"that scales with the batch, so records are being copied per branch again (#2910)",
			perExtraDestination, batchSize)
	}
}
