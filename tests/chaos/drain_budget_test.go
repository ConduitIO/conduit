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

package chaos

import (
	"testing"
	"time"
)

// drainBudget must scale with the work in flight, not sit at a flat wall-clock
// guess — the flat 5s it replaced was the direct cause of an intermittent CI
// failure on property2Cases' mid-snapshot case (total=500, paceMS=1).
func TestDrainBudget_ScalesWithWorkAndIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total uint64
		want  time.Duration
	}{
		{"zero uses the floor", 0, 5 * time.Second},
		{"small run uses the floor", 100, 5 * time.Second},
		{"floor boundary", 500, 5 * time.Second},
		{"mid-snapshot's own size clears the flat 5s it used to get", 501, 5010 * time.Millisecond},
		{"large run scales", 1000, 10 * time.Second},
		{"ceiling caps a runaway", 100000, 25 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := drainBudget(tc.total); got != tc.want {
				t.Fatalf("drainBudget(%d) = %s, want %s", tc.total, got, tc.want)
			}
		})
	}

	// The property that actually matters: every property2 case must get at
	// least the flat budget it had before, so this can only loosen, never
	// tighten, an existing timeout.
	for _, c := range property2Cases {
		if got := drainBudget(c.total); got < 5*time.Second {
			t.Fatalf("case %q (total=%d) got %s, less than the previous flat 5s", c.name, c.total, got)
		}
	}
}

// The clamp must happen before the uint64 -> int64 conversion. Multiplying
// first and bounding afterwards lets a large total overflow into a NEGATIVE
// duration, which makes the deadline already past and turns the budget into
// no wait at all — the precise opposite of what it is for, and a fail-open
// that would reintroduce the flake it was written to fix.
func TestDrainBudget_HugeTotalCannotOverflowIntoNoWait(t *testing.T) {
	for _, total := range []uint64{
		1 << 40,
		1 << 60,
		^uint64(0), // max uint64: overflows to a negative duration if converted first
	} {
		got := drainBudget(total)
		if got != 25*time.Second {
			t.Fatalf("drainBudget(%d) = %s, want the 25s ceiling", total, got)
		}
		if got <= 0 {
			t.Fatalf("drainBudget(%d) = %s, which is not a wait at all", total, got)
		}
	}
}
