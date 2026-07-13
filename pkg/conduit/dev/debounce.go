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

package dev

import (
	"context"
	"time"
)

// DefaultDebounce is the debounce window used when Options.Debounce is zero
// (design doc §4: "a 300ms debounce collapses save-storms").
const DefaultDebounce = 300 * time.Millisecond

// debouncer coalesces a burst of trigger() calls for one file path into a
// single call to apply, run after delay has elapsed with no further
// triggers (a classic trailing-edge debounce), and ensures at most one
// apply runs at a time for that path — see run's doc for the two rules
// this implements.
//
// One debouncer exists per watched file path (see Watcher.consume, which
// creates them lazily) and owns exactly one goroutine (run), so state is
// unsynchronized by design: trigger only ever sends on a channel, and run is
// the only goroutine that reads it or mutates the debouncer's fields.
type debouncer struct {
	clock Clock
	delay time.Duration
	apply func(ctx context.Context)

	// triggerCh is buffered to 1 so trigger() never blocks the caller (the
	// single-goroutine event loop in Watcher.consume): a burst of N events
	// collapses to at most one buffered signal, which is exactly the
	// coalescing debounce wants.
	triggerCh chan struct{}
}

func newDebouncer(clock Clock, delay time.Duration, apply func(ctx context.Context)) *debouncer {
	return &debouncer{
		clock:     clock,
		delay:     delay,
		apply:     apply,
		triggerCh: make(chan struct{}, 1),
	}
}

// trigger records that a relevant fs event happened for this debouncer's
// path. It never blocks: if a trigger is already pending (the buffered
// channel is full), this call is a no-op — one pending trigger is exactly as
// informative as several, since run's debounce window restarts on every
// trigger it observes regardless of count.
func (d *debouncer) trigger() {
	select {
	case d.triggerCh <- struct{}{}:
	default:
	}
}

// run is the debouncer's only goroutine. It implements two rules from the
// design doc's §4 "Debounce/coalesce":
//
//  1. A burst of triggers collapses to one apply, run only after delay has
//     passed with no further triggers (each new trigger resets the window —
//     "quiet for delay" is the condition, not "delay after the first
//     trigger").
//  2. A trigger that arrives while an apply is already in flight for this
//     path is coalesced into at most one queued follow-up, run (after
//     another debounce window) once the in-flight apply completes — never a
//     pile of queued applies, and never a second apply running concurrently
//     with the first for the same path.
//
// run returns when ctx is cancelled (invariant 7: tied to the serve
// context), which is also how its apply goroutine (spawned when the debounce
// timer fires) is guaranteed not to leak: it always completes apply(ctx) and
// then either hands the result back on done or observes ctx.Done() instead,
// never blocking forever on a send nobody will ever receive.
func (d *debouncer) run(ctx context.Context) {
	var timerC <-chan time.Time
	applying := false
	queued := false
	done := make(chan struct{})

	for {
		select {
		case <-ctx.Done():
			return

		case <-d.triggerCh:
			if applying {
				// Rule 2: coalesce into a single queued follow-up.
				queued = true
				continue
			}
			// Rule 1: (re)start the debounce window. Replacing timerC here is
			// intentional even if a previous window was already pending — a
			// new event means "not quiet yet", so the window must restart.
			timerC = d.clock.After(d.delay)

		case <-timerC:
			timerC = nil
			applying = true
			go func() {
				d.apply(ctx)
				select {
				case done <- struct{}{}:
				case <-ctx.Done():
				}
			}()

		case <-done:
			applying = false
			if queued {
				queued = false
				// One more debounce window, not an immediate re-apply: a
				// save that lands the instant the in-flight apply finishes
				// deserves the same coalescing window as any other burst.
				timerC = d.clock.After(d.delay)
			}
		}
	}
}
