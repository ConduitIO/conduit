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
