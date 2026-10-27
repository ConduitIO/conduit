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
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/pipeline"
	"pgregory.net/rapid"
)

// TestRoutesNacks_MatchesWindow pins pipeline.DLQ.RoutesNacks, which the
// start-time DLQ guard relies on, to what this engine's DLQ window does with
// the first nack: routed to the DLQ (ok) or failing the pipeline. If the two
// disagree, the guard either refuses a DLQ that routes nothing (the v0.21.0
// default-DLQ refusal) or admits one that acks records it cannot preserve.
func TestRoutesNacks_MatchesWindow(t *testing.T) {
	for _, size := range []int{0, 1, 2, 5} {
		for _, threshold := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("size=%d,threshold=%d", size, threshold), func(t *testing.T) {
				routed := newDLQWindow(size, threshold).Nack(1) == 1
				want := pipeline.DLQ{WindowSize: size, WindowNackThreshold: threshold}.RoutesNacks()
				if routed != want {
					t.Fatalf("window routes first nack = %v, RoutesNacks() = %v", routed, want)
				}
			})
		}
	}
}

// TestRoutesNacks_NeverUnderRefuses extends TestRoutesNacks_MatchesWindow to
// any sequence of acks and nacks (and, in arch-v2, any batch size): when
// RoutesNacks reports false, no nack may ever reach the DLQ, otherwise the
// start-time guard would admit a hash-only log DLQ that acks records it
// cannot preserve (invariant 3, GHSA-m3mw-fpw8-w765). When it reports true,
// the first nack must reach the DLQ. Negative sizes and thresholds are
// rejected when the DLQ is configured (pkg/pipeline/service.go).
func TestRoutesNacks_NeverUnderRefuses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// 0 is the boundary both windows special-case; draw it often.
		size := rapid.OneOf(rapid.Just(0), rapid.IntRange(0, 10)).Draw(t, "size")
		threshold := rapid.OneOf(rapid.Just(0), rapid.IntRange(0, 10)).Draw(t, "threshold")
		routes := pipeline.DLQ{WindowSize: size, WindowNackThreshold: threshold}.RoutesNacks()
		w := newDLQWindow(size, threshold)
		nacked := false
		for i := range rapid.IntRange(1, 200).Draw(t, "ops") {
			count := rapid.IntRange(1, 64).Draw(t, "count")
			if rapid.Bool().Draw(t, "nack") {
				accepted := w.Nack(count)
				if !routes && accepted > 0 {
					t.Fatalf("op %d: %d of %d nacks routed to the DLQ, but RoutesNacks() = false", i, accepted, count)
				}
				if routes && !nacked && accepted == 0 {
					t.Fatalf("op %d: first nack batch not routed, but RoutesNacks() = true", i)
				}
				nacked = true
				if accepted < count {
					return // the engine stops the pipeline here
				}
			} else {
				w.Ack(count)
			}
		}
	})
}
