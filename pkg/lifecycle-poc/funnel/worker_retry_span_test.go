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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// retryThenSplitTask marks one record Retry on its first pass, then on the
// retry pass splits that record into several. The split GROWS the sub-batch
// that doTask handed it — which is exactly the shape that made the parent
// loop's index overshoot.
type retryThenSplitTask struct {
	id         string
	retryIndex int
	splitInto  int
	seen       bool
}

func (t *retryThenSplitTask) ID() string                  { return t.id }
func (t *retryThenSplitTask) Open(context.Context) error  { return nil }
func (t *retryThenSplitTask) Close(context.Context) error { return nil }

func (t *retryThenSplitTask) Do(_ context.Context, b *Batch) error {
	if !t.seen {
		t.seen = true
		// First pass over the whole batch: mark exactly one record for retry,
		// which splits the batch into [ack..][retry][ack..] sub-batches.
		b.Retry(t.retryIndex, t.retryIndex+1)
		return nil
	}
	// Retry pass: this sub-batch holds just the retried record. Split it,
	// growing the sub-batch beyond the span it covered in the parent.
	active := b.ActiveRecords()
	if len(active) == 1 {
		pieces := make([]opencdc.Record, t.splitInto)
		for i := range pieces {
			pieces[i] = active[0]
		}
		b.SplitRecord(0, pieces)
	}
	return nil
}

// TestDoTask_RetryThatSplits_DoesNotSkipRecords is the regression test for
// issue #2722.
//
// doTask's tainted loop advanced its cursor with `idx += len(subBatch.positions)`
// AFTER handing the sub-batch to a task. A task may GROW that sub-batch
// (SplitRecord appends), and because subBatchByFlag three-index-clips, the
// growth reallocates and leaves the parent batch untouched — so the post-call
// length exceeded the span actually covered. The cursor overshot, and the
// records in between were never handed to any downstream task and never acked,
// while a later sub-batch's ack still advanced the persisted source position
// PAST them. Silent, permanent record loss: no error, and no gap in the
// position sequence to notice afterwards.
//
// Without the fix (advancing by the span captured BEFORE the call) this test
// fails: positions p2 and p3 never reach the acker, yet p4/p5 do — proving the
// source position would have moved past undelivered records.
func TestDoTask_RetryThatSplits_DoesNotSkipRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	const total = 6
	records := make([]opencdc.Record, total)
	positions := make([]opencdc.Position, total)
	for i := range records {
		records[i] = opencdc.Record{Position: opencdc.Position{byte('p'), byte('0' + i)}}
		positions[i] = records[i].Position
	}

	parent := &fakeParentAckNacker{}
	task := &retryThenSplitTask{id: "retry-split", retryIndex: 1, splitInto: 3}
	node := &TaskNode{Task: task}

	w := &Worker{
		logger:         log.Nop(),
		processingLock: make(chan struct{}, 1),
	}

	b := NewBatch(records)
	is.NoErr(w.doTask(ctx, node, b, parent))

	// Every original position must have reached the acker exactly once. The
	// bug's signature is p2/p3 missing while p4/p5 are present.
	got := map[string]int{}
	for _, call := range parent.ackCalls() {
		for _, p := range call.positions {
			got[string(p)]++
		}
	}
	for i, p := range positions {
		if got[string(p)] != 1 {
			t.Fatalf("position %d (%q) was acked %d times, want exactly 1 — "+
				"a record was skipped by the retry-span overshoot (acked: %v)",
				i, p, got[string(p)], got)
		}
	}
}
