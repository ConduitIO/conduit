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
	"testing"
	"time"

	"github.com/matryer/is"
)

// awaitApply blocks until applied receives a value, failing the test after a
// generous timeout instead of hanging forever if the debouncer has a bug.
func awaitApply(t *testing.T, applied <-chan struct{}) {
	t.Helper()
	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for apply to run")
	}
}

// assertNoApply asserts apply is NOT called within a short window — used to
// prove coalescing actually suppressed extra applies, not just that they
// haven't happened yet.
func assertNoApply(t *testing.T, applied <-chan struct{}) {
	t.Helper()
	select {
	case <-applied:
		t.Fatal("apply ran, expected it to be coalesced")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDebouncer_SingleTrigger_AppliesOnce(t *testing.T) {
	is := is.New(t)
	clock := newFakeClock()
	applied := make(chan struct{}, 8)

	d := newDebouncer(clock, time.Second, func(context.Context) { applied <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	d.trigger()
	clock.awaitCall()
	clock.fireLatest()

	awaitApply(t, applied)
	assertNoApply(t, applied) // exactly one apply, not more
	is.True(true)
}

func TestDebouncer_Burst_CollapsesToOneApply(t *testing.T) {
	clock := newFakeClock()
	applied := make(chan struct{}, 8)

	d := newDebouncer(clock, time.Second, func(context.Context) { applied <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	// A burst of 5 triggers, each resetting the debounce window (as a real
	// save-storm would): every trigger causes a fresh After call.
	const burst = 5
	for i := 0; i < burst; i++ {
		d.trigger()
		clock.awaitCall()
	}
	// Only the LAST window should ever fire in practice (a real clock would
	// have superseded the earlier ones); fire it and confirm exactly one
	// apply happens.
	clock.fireLatest()

	awaitApply(t, applied)
	assertNoApply(t, applied)
}

func TestDebouncer_TriggerDuringApply_QueuesExactlyOneFollowUp(t *testing.T) {
	is := is.New(t)
	clock := newFakeClock()
	applied := make(chan struct{}, 8)
	release := make(chan struct{})

	callCount := 0
	d := newDebouncer(clock, time.Second, func(context.Context) {
		callCount++
		applied <- struct{}{}
		<-release // block "in flight" until the test lets it finish
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	// First apply starts.
	d.trigger()
	clock.awaitCall()
	clock.fireLatest()
	awaitApply(t, applied)

	// While the first apply is still in flight (blocked on release), fire a
	// storm of triggers — design doc: "an in-flight apply queues at most one
	// follow-up".
	d.trigger()
	d.trigger()
	d.trigger()

	// Let the first apply finish.
	close(release)

	// Exactly one more debounce window should be requested (the queued
	// follow-up), then exactly one more apply.
	clock.awaitCall()
	clock.fireLatest()
	awaitApply(t, applied)
	assertNoApply(t, applied)

	is.Equal(callCount, 2)
}

func TestDebouncer_NoTriggerDuringApply_NoFollowUp(t *testing.T) {
	clock := newFakeClock()
	applied := make(chan struct{}, 8)

	d := newDebouncer(clock, time.Second, func(context.Context) { applied <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	d.trigger()
	clock.awaitCall()
	clock.fireLatest()
	awaitApply(t, applied)

	// No trigger arrived during (or after) the apply; nothing more should
	// ever run.
	assertNoApply(t, applied)
}

func TestDebouncer_StopsOnContextCancel(t *testing.T) {
	clock := newFakeClock()
	applied := make(chan struct{}, 8)
	done := make(chan struct{})

	d := newDebouncer(clock, time.Second, func(context.Context) { applied <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		d.run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("debouncer.run did not return after context cancellation")
	}
}

// TestDebouncer_ContextCancel_WaitsForInFlightApply is the regression test for
// the shutdown race: run must NOT return while an apply it started is still
// executing, because that apply mutates engine + DB state and Watcher.consume's
// wg.Wait (which only tracks run, not the apply goroutine run spawns) would
// otherwise unblock, Watcher.Run would return, and the runtime would tear the
// engine and database down underneath the still-running apply. run owns the
// apply goroutine's lifetime: on cancel it blocks until the apply returns.
func TestDebouncer_ContextCancel_WaitsForInFlightApply(t *testing.T) {
	clock := newFakeClock()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	d := newDebouncer(clock, time.Second, func(context.Context) {
		close(started)
		<-release // hold the apply "in flight" until the test releases it
		close(finished)
	})
	ctx, cancel := context.WithCancel(context.Background())
	runReturned := make(chan struct{})
	go func() {
		d.run(ctx)
		close(runReturned)
	}()

	// Get an apply in flight, then cancel while it is still blocked.
	d.trigger()
	clock.awaitCall()
	clock.fireLatest()
	<-started
	cancel()

	// run must still be blocked on the in-flight apply — it has NOT returned.
	select {
	case <-runReturned:
		t.Fatal("run returned while an apply was still in flight — shutdown race")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the apply finish; only now may run return.
	close(release)
	<-finished
	select {
	case <-runReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the in-flight apply completed")
	}
}
