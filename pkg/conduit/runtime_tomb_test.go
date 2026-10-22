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

package conduit

import (
	"context"
	"testing"
	"time"

	"github.com/matryer/is"
	"gopkg.in/tomb.v2"
)

// A tomb dies, for good, the moment its last goroutine returns, and tomb.Go on
// a dead tomb panics ("tomb.Go called after all goroutines terminated"). In
// Run, services start goroutines one after another (initServices, then
// serveGRPCAPI, serveHTTPAPI) and a goroutine that sees an already-canceled
// context returns at once, so a context canceled during setup can take the
// count to zero before the next t.Go.
//
// startAndFinishOne reproduces that: it runs a goroutine that exits as soon as
// ctx is done, cancels, and waits for it to exit.
func startAndFinishOne(t *testing.T, tb *tomb.Tomb, ctx context.Context, cancel context.CancelFunc) {
	t.Helper()
	exited := make(chan struct{})
	tb.Go(func() error {
		defer close(exited)
		<-ctx.Done()
		return nil
	})
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("service goroutine did not exit after cancel")
	}
}

// Regression for the Run window: the next service's t.Go must not panic.
func TestNewRunTomb_ServiceGoroutinesMayStartAfterEarlierOnesExited(t *testing.T) {
	is := is.New(t)
	parent, cancel := context.WithCancel(context.Background())
	tb, ctx, setupDone := newRunTomb(parent)

	startAndFinishOne(t, tb, ctx, cancel)

	// Without the long-lived goroutine the tomb is dead by now and this panics.
	ran := make(chan struct{})
	tb.Go(func() error { close(ran); return nil })
	<-ran

	// The tomb stays open until setup ends, then finishes.
	select {
	case <-tb.Dead():
		t.Fatal("tomb died while setup was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(setupDone)
	select {
	case <-tb.Dead():
	case <-time.After(5 * time.Second):
		t.Fatal("tomb did not finish after setup ended")
	}
	is.Equal(tb.Wait(), context.Canceled)
}

// Control: with the construction newRunTomb replaced (tomb.WithContext alone)
// the same sequence panics, so the test above is a real regression test.
func TestTombWithContext_PanicsWhenTheLastGoroutineExitedDuringSetup(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	tb, ctx := tomb.WithContext(parent)

	startAndFinishOne(t, tb, ctx, cancel)
	select {
	case <-tb.Dead():
	case <-time.After(5 * time.Second):
		t.Fatal("expected the tomb to be dead")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected tomb.Go to panic on a dead tomb")
		}
	}()
	tb.Go(func() error { return nil })
}
