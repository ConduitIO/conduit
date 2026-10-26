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

package stream

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
				routed := newDLQWindow(size, threshold).Nack()
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
		size := rapid.IntRange(0, 10).Draw(t, "size")
		threshold := rapid.IntRange(0, 10).Draw(t, "threshold")
		routes := pipeline.DLQ{WindowSize: size, WindowNackThreshold: threshold}.RoutesNacks()
		w := newDLQWindow(size, threshold)
		nacked := false
		for i := range rapid.IntRange(1, 200).Draw(t, "ops") {
			if rapid.Bool().Draw(t, "nack") {
				ok := w.Nack()
				if !routes && ok {
					t.Fatalf("op %d: nack routed to the DLQ, but RoutesNacks() = false", i)
				}
				if routes && !nacked && !ok {
					t.Fatalf("op %d: first nack not routed, but RoutesNacks() = true", i)
				}
				nacked = true
				if !ok {
					return // the engine stops the pipeline here
				}
			} else {
				w.Ack()
			}
		}
	})
}
