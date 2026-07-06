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

//go:build unix

package conduit

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestEntrypoint_CancelOnSignal_FirstSignalCancelsContext is the regression
// test for #2519: a termination signal must cancel the returned context
// (invariant 7's graceful-drain path), not fall through unnoticed.
//
// Unlike the previous version of this test, it drives cancelOnSignal (the
// core logic extracted from CancelOnInterrupt) directly with a local channel
// instead of going through signal.Notify/os.Exit. CancelOnInterrupt registers
// a process-global signal handler and hard os.Exit()s the binary on a second
// signal, which made the old test unsafe to run with -count>1 or alongside
// other signal-sending subtests: the leaked global handler from one iteration
// would catch a later iteration's signal and kill the test binary. Because
// the channel and the exit func here are both local to each test run, this
// test is safe under -count and -shuffle, and it can now also exercise the
// second-signal path, which the old test structurally couldn't.
func TestEntrypoint_CancelOnSignal_FirstSignalCancelsContext(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	exitCalled := make(chan int, 1)
	exit := func(code int) { exitCalled <- code }

	ctx := cancelOnSignal(context.Background(), sigChan, exit)

	sigChan <- syscall.SIGTERM

	select {
	case <-ctx.Done():
		// The first signal canceled the context: the graceful path runs.
	case <-time.After(5 * time.Second):
		t.Fatal("first signal did not cancel the context; it was not handled (invariant 7)")
	}

	select {
	case code := <-exitCalled:
		t.Fatalf("exit was called after a single signal with code %d; hard exit must wait for a second signal", code)
	case <-time.After(50 * time.Millisecond):
		// Expected: no hard exit yet, only one signal was delivered.
	}
}

// TestEntrypoint_CancelOnSignal_SecondSignalExits is the regression test
// covering the hard-exit-on-second-signal half of CancelOnInterrupt's
// contract, which the previous, os.Exit-based test could never assert
// (asserting it would have killed the test binary). By injecting a fake exit
// func, we can verify the second signal triggers the hard-exit path with the
// POSIX 128+signum exit code for that signal (SIGINT -> 130, SIGTERM -> 143)
// without terminating anything. This code moved off exitCodeInterrupt (2) in
// the deterministic-exit-codes change: 2 is now the config/validation bucket
// (pkg/conduit/exitcode), so a double-signal hard exit needed a value that
// can't collide with it — 128+signum both avoids the collision and matches
// what a shell reports for a process killed by that signal.
func TestEntrypoint_CancelOnSignal_SecondSignalExits(t *testing.T) {
	tests := []struct {
		name string
		sig  syscall.Signal
		want int
	}{
		{name: "SIGTERM", sig: syscall.SIGTERM, want: 143},
		{name: "SIGINT", sig: syscall.SIGINT, want: 130},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			sigChan := make(chan os.Signal, 1)
			exitCalled := make(chan int, 1)
			exit := func(code int) { exitCalled <- code }

			ctx := cancelOnSignal(context.Background(), sigChan, exit)

			sigChan <- syscall.SIGTERM // first signal: value doesn't affect the exit code

			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("first signal did not cancel the context")
			}

			sigChan <- tt.sig

			select {
			case code := <-exitCalled:
				is.Equal(code, tt.want)
			case <-time.After(5 * time.Second):
				t.Fatal("second signal did not trigger the hard-exit path")
			}
		})
	}
}

// TestEntrypoint_CancelOnSignal_ContextDoneWithoutSignal verifies that
// cancelOnSignal's goroutine returns (rather than blocking forever waiting
// for a second signal that will never arrive) when the passed-in context is
// canceled for a reason unrelated to any signal. This is what lets
// CancelOnInterrupt call signal.Stop and let the goroutine exit instead of
// leaking it for the remaining process lifetime.
func TestEntrypoint_CancelOnSignal_ContextDoneWithoutSignal(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	exitCalled := make(chan int, 1)
	exit := func(code int) { exitCalled <- code }

	parentCtx, cancel := context.WithCancel(context.Background())
	ctx := cancelOnSignal(parentCtx, sigChan, exit)
	cancel()

	select {
	case <-ctx.Done():
		// Canceling the parent propagates to the derived context, same as
		// context.WithCancel always guarantees.
	case <-time.After(5 * time.Second):
		t.Fatal("canceling the parent context did not cancel the derived context")
	}

	select {
	case code := <-exitCalled:
		t.Fatalf("exit must not be called when the context is canceled without any signal, got code %d", code)
	case <-time.After(50 * time.Millisecond):
		// Expected: no signal was ever sent, so exit must never be called.
	}
}

// Deliberately no test here exercises the exported CancelOnInterrupt with a
// real signal.Notify registration and a real syscall.Kill: CancelOnInterrupt
// installs a process-global handler that outlives the test (its parent ctx is
// typically context.Background(), which never completes, so the signal.Stop
// cleanup below never fires), and delivers to *every* channel still
// registered from earlier iterations. Under -count>1 the second iteration's
// SIGTERM lands on the first iteration's still-listening channel as its
// second signal and reaches the real os.Exit, killing the test binary — this
// was verified empirically while writing this fix. CancelOnInterrupt is kept
// intentionally minimal (signal.Notify + a signal.Stop cleanup goroutine +
// delegate to cancelOnSignal) so that its untested surface is small; all of
// its actual decision logic is covered above via cancelOnSignal with an
// injected channel and exit func.
