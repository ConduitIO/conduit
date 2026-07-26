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

package external

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/matryer/is"
)

func testAddr(t *testing.T) net.Addr {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr()
	_ = l.Close()
	return addr
}

// TestAttachedRunner_Wait_BlocksUntilMarkDone proves Wait does not return on
// its own - unlike go-plugin's default, PID-based CmdAttachedRunner.Wait
// (which returns once the OS-level process actually exits), this runner has
// no independent way to observe the external connector's death (see the
// type's doc: that gap is deliberate, not a bug). It only unblocks via
// markDone (called from Kill, or proactively from Dispenser.teardown) or
// context cancellation.
func TestAttachedRunner_Wait_BlocksUntilMarkDone(t *testing.T) {
	is := is.New(t)
	r := newAttachedRunner(testAddr(t))

	waitReturned := make(chan error, 1)
	go func() { waitReturned <- r.Wait(context.Background()) }()

	select {
	case <-waitReturned:
		t.Fatal("Wait returned before markDone was called")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	r.markDone()

	select {
	case err := <-waitReturned:
		is.NoErr(err)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after markDone")
	}
}

// TestAttachedRunner_Wait_RespectsContext proves Wait also unblocks on
// context cancellation (go-plugin's own reattach() bookkeeping goroutine
// calls Wait with context.Background(), but this behavior matters for the
// contract callers of Wait can rely on independent of go-plugin's own usage).
func TestAttachedRunner_Wait_RespectsContext(t *testing.T) {
	is := is.New(t)
	r := newAttachedRunner(testAddr(t))

	ctx, cancel := context.WithCancel(context.Background())
	waitReturned := make(chan error, 1)
	go func() { waitReturned <- r.Wait(ctx) }()

	cancel()

	select {
	case err := <-waitReturned:
		is.True(err != nil) // ctx.Err()
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after context cancellation")
	}
}

// TestAttachedRunner_Kill_NeverSignalsAProcess is the safety-critical
// invariant this type exists to enforce: Kill must be a pure no-op against
// the outside world. There is no OS process handle anywhere in this type to
// signal - this test documents and pins that structural guarantee (there is
// no os.Process field to accidentally wire up in a future change) alongside
// asserting Kill's only observable effect: unblocking Wait.
func TestAttachedRunner_Kill_NeverSignalsAProcess(t *testing.T) {
	is := is.New(t)
	r := newAttachedRunner(testAddr(t))

	err := r.Kill(context.Background())
	is.NoErr(err)

	// Kill's only observable side effect is unblocking Wait.
	select {
	case err := <-waitCh(r):
		is.NoErr(err)
	case <-time.After(2 * time.Second):
		t.Fatal("Kill did not unblock Wait")
	}
}

// TestAttachedRunner_Kill_Idempotent proves calling Kill (or markDone)
// multiple times, including concurrently, never panics (closing an
// already-closed channel would) - go-plugin's Client.Kill() plus a
// proactive Dispenser.teardown call both reach this path, and must both be
// safe to do so.
func TestAttachedRunner_Kill_Idempotent(t *testing.T) {
	is := is.New(t)
	r := newAttachedRunner(testAddr(t))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			is.NoErr(r.Kill(context.Background()))
		}()
	}
	wg.Wait()
}

func TestAttachedRunner_ID_IsAddr(t *testing.T) {
	is := is.New(t)
	addr := testAddr(t)
	r := newAttachedRunner(addr)
	is.Equal(r.ID(), addr.String())
}

func waitCh(r *attachedRunner) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- r.Wait(context.Background()) }()
	return ch
}
