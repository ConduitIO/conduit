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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// These tests cover conduit#3151 (conduit-commons#336) on the arch-v2 funnel:
// a fan-out branch that can edit records in place works on opencdc.Record.Clone
// of the batch (batch.go, Batch.clone), while destination-only branches share
// the original records (#2910). If Clone is shallow, an in-place edit to a
// NESTED array or map in the mutating branch lands in the record the sibling
// branch is about to write. The older
// TestDoNextTask_FanOut_SharedRecords_MutatingBranchGetsOwnCopy only edits
// Metadata, a top-level map that even the shallow Clone copied, so it could
// not see this.

// nestedRecords returns n records with nested arrays, maps and bytes in the
// payload (both sides), the key and the metadata. Every call returns fresh
// values so a test can compare a branch's output with an untouched twin.
func nestedRecords(n int) []opencdc.Record {
	records := randomRecords(n)
	for i := range records {
		records[i].Key = opencdc.StructuredData{"ids": []any{"k1", "k2"}}
		records[i].Payload.Before = opencdc.StructuredData{
			"items": []any{map[string]any{"sku": "B", "qty": 7}},
		}
		records[i].Payload.After = opencdc.StructuredData{
			"items":  []any{map[string]any{"sku": "A", "qty": 1, "tags": []any{"x", "y"}}},
			"nested": map[string]any{"x": 1, "deeper": map[string]any{"y": 2}},
			"blob":   []byte("blob-bytes"),
			"typed":  []string{"s1", "s2"},
			"tmap":   map[string]string{"k": "v"},
		}
	}
	return records
}

// nestedEditTask stands in for a destination-scoped processor that edits
// records in place at depth (custom.javascript does this).
type nestedEditTask struct{ id string }

func (m nestedEditTask) ID() string                  { return m.id }
func (m nestedEditTask) Open(context.Context) error  { return nil }
func (m nestedEditTask) Close(context.Context) error { return nil }
func (m nestedEditTask) Do(_ context.Context, b *Batch) error {
	for _, r := range b.records {
		r.Key.(opencdc.StructuredData)["ids"].([]any)[0] = "EDITED"
		r.Payload.Before.(opencdc.StructuredData)["items"].([]any)[0].(map[string]any)["qty"] = 999
		after := r.Payload.After.(opencdc.StructuredData)
		item := asMap(after["items"].([]any)[0])
		item["qty"] = 999
		item["tags"].([]any)[0] = "EDITED"
		nested := asMap(after["nested"])
		nested["x"] = 999
		asMap(nested["deeper"])["y"] = 999
		after["blob"].([]byte)[0] = 'X'
		after["typed"].([]string)[0] = "EDITED"
		after["tmap"].(map[string]string)["k"] = "EDITED"
	}
	return nil
}

// asMap returns v as a map for in-place editing. A shallow Clone retypes nested
// map[string]any to opencdc.StructuredData, and a plain type assertion would
// then panic instead of letting the test report the leak it is looking for.
func asMap(v any) map[string]any {
	if sd, ok := v.(opencdc.StructuredData); ok {
		return sd
	}
	return v.(map[string]any)
}

// TestDoNextTask_FanOut_NestedEditInOneBranchDoesNotReachSibling is the
// arch-v2 fan-out case of #3151. destB's Write is held until the mutating
// branch (nestedEditTask then destA) has finished, so with a shallow Clone
// destB deterministically reads the edited nested values.
func TestDoNextTask_FanOut_NestedEditInOneBranchDoesNotReachSibling(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	records := nestedRecords(4)
	pristine := nestedRecords(4)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	destANode := &TaskNode{Task: NewDestinationTask(destA.id, destA, logger, NoOpConnectorMetrics{})}
	procNode := &TaskNode{Task: nestedEditTask{id: "procA"}, Next: []*TaskNode{destANode}}
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

	// The mutating branch saw its own edit (the test would pass vacuously if
	// the edit never ran).
	destA.mu.Lock()
	is.Equal(len(destA.written), len(records))
	for _, r := range destA.written {
		is.Equal(asMap(r.Payload.After.(opencdc.StructuredData)["items"].([]any)[0])["qty"], 999)
	}
	destA.mu.Unlock()

	// Invariant 6: the sibling branch got exactly what the source produced.
	destB.mu.Lock()
	defer destB.mu.Unlock()
	is.Equal(len(destB.written), len(records))
	for i, r := range destB.written {
		is.Equal(string(r.Bytes()), string(pristine[i].Bytes())) // nested edit leaked into destB
	}
	// ... and so did the batch the branches were cut from.
	for i, r := range batch.records {
		is.Equal(string(r.Bytes()), string(pristine[i].Bytes()))
	}
}

// TestDoNextTask_FanOut_TwoMutatingBranchesAreIndependent has two branches
// that both edit nested data in place. Each must see exactly its own edit:
// branch records come from separate Clone calls and must not alias each other.
func TestDoNextTask_FanOut_TwoMutatingBranchesAreIndependent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	records := nestedRecords(3)
	src := newFakeSource("src", records)
	destA := newFakeDestination("destA")
	destB := newFakeDestination("destB")

	mk := func(id string, d *fakeDestination) *TaskNode {
		dn := &TaskNode{Task: NewDestinationTask(d.id, d, logger, NoOpConnectorMetrics{})}
		return &TaskNode{Task: nestedWriteTask{id: id, val: id}, Next: []*TaskNode{dn}}
	}
	branchNode := &TaskNode{Task: passthroughTask{id: "shared"}, Next: []*TaskNode{mk("A", destA), mk("B", destB)}}
	srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{branchNode}}
	dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 0, 0)
	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	// Both branches write the same nested slot with different values and then
	// read it back in their destination. A shared slot makes one of them read
	// the other's value (and is a data race under -race).
	batch := NewBatch(append([]opencdc.Record(nil), records...))
	is.NoErr(w.doTask(ctx, branchNode, batch, w))

	for name, d := range map[string]*fakeDestination{"A": destA, "B": destB} {
		d.mu.Lock()
		is.Equal(len(d.written), len(records))
		for _, r := range d.written {
			got := asMap(r.Payload.After.(opencdc.StructuredData)["items"].([]any)[0])["owner"]
			is.Equal(got, name) // the branch must read back its own write
		}
		d.mu.Unlock()
	}
}

// nestedWriteTask writes val into a nested slot, yields, and writes it again,
// so a sibling that shares the slot has a window to overwrite it.
type nestedWriteTask struct{ id, val string }

func (m nestedWriteTask) ID() string                  { return m.id }
func (m nestedWriteTask) Open(context.Context) error  { return nil }
func (m nestedWriteTask) Close(context.Context) error { return nil }
func (m nestedWriteTask) Do(_ context.Context, b *Batch) error {
	for _, r := range b.records {
		item := asMap(r.Payload.After.(opencdc.StructuredData)["items"].([]any)[0])
		for range 50 {
			item["owner"] = m.val
			time.Sleep(10 * time.Microsecond)
		}
	}
	return nil
}
