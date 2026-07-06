// Copyright © 2022 Meroxa, Inc.
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

package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestPersister_EmptyFlushDoesNothing(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, store := initPersisterTest(time.Millisecond*100, 2)

	persister.Flush(ctx)
	persister.Wait()

	got, err := store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(0, len(got))
}

// TestPersister_PersistFlushesAfterDelayThreshold verifies the delay-based
// side of the flush contract described on Persist: a batch is flushed once
// delayThreshold has elapsed (as long as the bundle-count threshold wasn't
// hit first). It uses a fakeClock instead of real sleeps so the assertions
// are exact rather than tolerance-based: advancing by less than the
// threshold must never flush, and advancing to (or past) the threshold must
// always flush. This replaces a previous version of the test that measured
// real wall-clock delay against a fixed tolerance window, which flaked under
// load when the timer fired later than the tolerance allowed.
func TestPersister_PersistFlushesAfterDelayThreshold(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	delayThreshold := time.Millisecond * 100

	persister, store := initPersisterTest(delayThreshold, 2)
	clk := newFakeClock()
	persister.clock = clk

	conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
	callbackCalled := make(chan struct{})
	err := persister.Persist(ctx, conn, func(err error) {
		if err != nil {
			t.Errorf("expected nil error, got: %v", err)
		}
		close(callbackCalled)
	})
	is.NoErr(err)

	// Advancing by less than the threshold must not schedule a flush.
	// fakeClock.Advance fires any due timers synchronously before
	// returning, so this check is deterministic: there is no window in
	// which the flush could still be "in flight".
	clk.Advance(delayThreshold - time.Millisecond)
	select {
	case <-callbackCalled:
		t.Fatal("flush was triggered before the delay threshold elapsed")
	default:
	}

	// Advancing past the threshold must trigger the flush. The flush itself
	// still runs in a background goroutine (flushNow, and the callback
	// invocation within it), so we wait on the channel rather than asserting
	// immediately - but with a generous timeout, not a tight tolerance.
	clk.Advance(time.Millisecond * 2)
	select {
	case <-callbackCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("expected callback to be called after the delay threshold elapsed")
	}

	got, err := store.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(conn, got)
}

func TestPersister_PersistFlushesAfterBundleCountThreshold(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	bundleCountThreshold := 50

	persister, store := initPersisterTest(time.Second, bundleCountThreshold)

	allCallbacksCalled := make(chan struct{})
	var wgCallbacks sync.WaitGroup
	wgCallbacks.Add(bundleCountThreshold / 2)
	go func() {
		wgCallbacks.Wait()
		close(allCallbacksCalled)
	}()

	for i := 0; i < bundleCountThreshold/2; i++ {
		conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
		err := persister.Persist(ctx, conn, func(err error) {
			t.Fatal("expected callback to be overwritten!")
		})
		is.NoErr(err)
		// second persist will overwrite first callback
		err = persister.Persist(ctx, conn, func(err error) {
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			wgCallbacks.Done()
		})
		is.NoErr(err)
	}
	lastPersistAt := time.Now()

	// we are testing a delay which is not exact, this is the acceptable margin
	maxDelay := time.Millisecond * 100
	select {
	case <-allCallbacksCalled:
		if gotDelay := time.Since(lastPersistAt); gotDelay > maxDelay {
			t.Fatalf("flush delay should be less than %s, actual delay: %s", maxDelay, gotDelay)
		}
	case <-time.After(maxDelay):
		t.Fatalf("expected callbacks to be called in %s", maxDelay)
	}

	conns, err := store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(bundleCountThreshold/2, len(conns))
}

func TestPersister_FlushStoresRightAway(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, store := initPersisterTest(time.Millisecond*100, 2)

	conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
	callbackCalled := make(chan struct{})
	timeAtPersist := time.Now()
	err := persister.Persist(ctx, conn, func(err error) {
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		close(callbackCalled)
	})
	is.NoErr(err)

	// flush right away
	persister.Flush(ctx)
	persister.Wait()

	// we are testing a delay which is not exact, this is the acceptable margin
	maxDelay := time.Millisecond * 10
	select {
	case <-callbackCalled:
		if gotDelay := time.Since(timeAtPersist); gotDelay > maxDelay {
			t.Fatalf("flush delay should be less than %s, actual delay: %s", maxDelay, gotDelay)
		}
	case <-time.After(maxDelay):
		t.Fatalf("expected callback to be called in a certain time frame")
	}

	got, err := store.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(conn, got)
}

// TestPersister_WaitsForOpenConnectorsAndFlush verifies that Wait blocks
// until every started connector has been stopped and the flush triggered by
// the last stop has actually completed. It used to simulate "connectors are
// still open" by having a background goroutine time.Sleep(100ms) before
// calling ConnectorStopped, then asserting Wait() unblocked within a
// delay..delay+10ms wall-clock window. Under load the goroutine could be
// descheduled past the 10ms tolerance (or, on a fast/idle machine, Wait()
// could unblock suspiciously close to the sleep boundary), so the test
// flaked independently of the fakeClock work in #2544 - it never went
// through clock.AfterFunc (ConnectorStopped triggers an immediate flush, not
// the delay-threshold timer), so there is no timer to fake here.
//
// Instead of tightening an already-flaky tolerance, this drops wall-clock
// timing entirely: it synchronizes the goroutine and the Wait() call with
// channels, and proves the "and flush" half of the contract by asserting the
// persisted state is already visible in the store by the time Wait()
// returns - which is only possible if Wait() actually blocked on flushWg
// until the automatic flush triggered by the final ConnectorStopped call
// finished. A 5s timeout guards against a genuine deadlock; it is not part
// of the assertion.
func TestPersister_WaitsForOpenConnectorsAndFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, store := initPersisterTest(time.Millisecond*100, 2)

	conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
	persister.ConnectorStarted()
	persister.ConnectorStarted()
	persister.ConnectorStarted()

	stoppedAll := make(chan struct{})
	go func() {
		defer close(stoppedAll)
		persister.ConnectorStopped()
		persister.ConnectorStopped()
		// before the last stop we persist another change which should be
		// flushed automatically when the last connector is stopped
		err := persister.Persist(ctx, conn, func(err error) {})
		is.NoErr(err)
		persister.ConnectorStopped()
	}()

	waitReturned := make(chan struct{})
	go func() {
		persister.Wait()
		close(waitReturned)
	}()

	select {
	case <-waitReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("expected Wait to return once all connectors stopped and the final flush completed")
	}

	// connWg.Wait() (inside Wait) cannot unblock until all three
	// ConnectorStopped calls have run, so the goroutine above must already
	// be done - deterministically, not by timing.
	select {
	case <-stoppedAll:
	default:
		t.Fatal("Wait returned before the connector-stop goroutine finished")
	}

	// Wait also blocks on flushWg, so the flush triggered by the last
	// ConnectorStopped must have completed by the time Wait returns - no
	// polling or sleeping needed to observe the persisted state.
	got, err := store.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(conn, got)
}

func initPersisterTest(
	delayThreshold time.Duration,
	bundleCountThreshold int,
) (*Persister, *Store) {
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}

	persister := NewPersister(logger, db, delayThreshold, bundleCountThreshold)
	return persister, NewStore(db, logger)
}

// fakeClock is a test-only implementation of the connector package's clock
// interface. It gives tests explicit, synchronous control over time so that
// delay-threshold behavior (see Persister.Persist) can be asserted exactly
// instead of within a real-time tolerance window.
//
// Advance fires any due timers synchronously, in the calling goroutine,
// before returning. That means a call to Advance which does not cross a
// timer's deadline is guaranteed to have triggered no side effects by the
// time it returns - callers don't need to sleep or poll to check that
// "nothing happened yet".
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) stoppableTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{fireAt: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the fake clock forward by d and synchronously fires any
// timers whose deadline is now due, in the order they were scheduled. Firing
// happens after releasing the clock's internal lock (but before Advance
// returns), so timer callbacks are free to call back into the clock (e.g.
// schedule a new AfterFunc) without deadlocking.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now

	remaining := make([]*fakeTimer, 0, len(c.timers))
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.fireAt.After(now) {
			due = append(due, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	c.mu.Unlock()

	for _, t := range due {
		t.fire()
	}
}

// fakeTimer is the fakeClock counterpart to *time.Timer, tracked by the
// clock so Advance can fire it once its deadline is due.
type fakeTimer struct {
	fireAt time.Time
	fn     func()

	mu      sync.Mutex
	stopped bool
	fired   bool
}

// Stop mirrors *time.Timer.Stop: it returns true if this call is the one
// that prevents the timer from firing, false if it had already fired or
// been stopped.
func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	stoppedNow := !t.stopped && !t.fired
	t.stopped = true
	return stoppedNow
}

func (t *fakeTimer) fire() {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	t.mu.Unlock()

	t.fn()
}
