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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// TestTaskNode_HasBatchingBelow_CachedAnswerIsStable: hasBatchingBelow decides
// whether a filtered or nacked batch's source ack is queued behind the batching
// destinations below a node (ackInOrder) or runs inline. A cached "false" for a
// node that has a batching destination below it would ack past buffered records
// (H1). The answer must match batchingDestinations on the first call and on every
// later (cached) call, for a direct child, a deeper destination, a fan-out and a
// shared boundary.
func TestTaskNode_HasBatchingBelow_CachedAnswerIsStable(t *testing.T) {
	logger := log.Nop()
	batching := func() *TaskNode {
		return &TaskNode{Task: NewBatchingDestinationTask("bd", newFakeDestination("bd"), logger, NoOpConnectorMetrics{}, 10, 0)}
	}
	plain := func() *TaskNode {
		return &TaskNode{Task: NewDestinationTask("pd", newFakeDestination("pd"), logger, NoOpConnectorMetrics{})}
	}
	shared := batching()
	shared.MarkSharedBoundary()
	cases := map[string]struct {
		n    *TaskNode
		want bool
	}{
		"direct batching child":    {&TaskNode{Next: []*TaskNode{batching()}}, true},
		"batching two levels down": {&TaskNode{Next: []*TaskNode{{Next: []*TaskNode{batching()}}}}, true},
		"fan-out, one batching":    {&TaskNode{Next: []*TaskNode{plain(), batching()}}, true},
		"behind a shared boundary": {&TaskNode{Next: []*TaskNode{shared}}, true},
		"only non-batching":        {&TaskNode{Next: []*TaskNode{plain()}}, false},
		"leaf":                     {&TaskNode{}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := len(batchingDestinations(c.n)) > 0; got != c.want {
				t.Fatalf("fixture: batchingDestinations=%v, want %v", got, c.want)
			}
			for i := 0; i < 3; i++ { // first call computes, later calls hit the cache
				if got := c.n.hasBatchingBelow(); got != c.want {
					t.Fatalf("call %d: hasBatchingBelow()=%v, want %v", i+1, got, c.want)
				}
			}
		})
	}
}
