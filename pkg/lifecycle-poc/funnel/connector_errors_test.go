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
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// errorsTopology builds two source workers sharing one destination through
// a funnel.Sink, the arch-v2 N-source shape. Each connector's Errors()
// returns the channel the test controls.
func errorsTopology(t *testing.T) (w1, w2 *Worker, src1, src2, dst chan error) {
	t.Helper()
	ctrl := gomock.NewController(t)
	logger := log.Test(t)

	src1, src2, dst = make(chan error), make(chan error), make(chan error)

	newSource := func(id string, errs chan error) *TaskNode {
		s := NewMockSource(ctrl)
		s.EXPECT().Errors().Return(errs).AnyTimes()
		return &TaskNode{Task: NewSourceTask(id, s, logger, NoOpConnectorMetrics{})}
	}
	d := NewMockDestination(ctrl)
	d.EXPECT().Errors().Return(dst).AnyTimes()
	dstNode := &TaskNode{Task: NewDestinationTask("dst", d, logger, NoOpConnectorMetrics{})}
	_, err := NewSink(dstNode) // marks dstNode as a shared boundary
	if err != nil {
		t.Fatal(err)
	}

	n1, n2 := newSource("src1", src1), newSource("src2", src2)
	n1.Next = []*TaskNode{dstNode}
	n2.Next = []*TaskNode{dstNode}
	return &Worker{FirstTask: n1}, &Worker{FirstTask: n2}, src1, src2, dst
}

// failRecorder returns a fail func for WatchConnectorErrors that records
// every call.
func failRecorder() (fail func(error), calls *atomic.Int64, got chan error) {
	calls = &atomic.Int64{}
	got = make(chan error, 10)
	return func(err error) {
		calls.Add(1)
		got <- err
	}, calls, got
}

func recvErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("fail was not called")
		return nil
	}
}

// assertUnread fails if a reader is parked on ch. ch is unbuffered, so the
// non-blocking send succeeds only if a receiver is waiting on it right now.
// Call it only after the watch's done is closed: every reader has returned
// by then, so correct code never takes the send, and there is nothing to
// wait for.
func assertUnread(t *testing.T, ch chan error) {
	t.Helper()
	select {
	case ch <- cerrors.New("late"):
		t.Fatal("a reader is still receiving after its watch ended")
	default:
	}
}

// isClosed reports whether done is closed, without blocking.
func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// waitClosed waits for done. The deadline only bounds a failing run; a
// passing run does not depend on it.
func waitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readers did not return")
	}
}

// stopWithin calls stop and fails if it does not return. The deadline only
// bounds a failing run, such as a stop that no longer releases its readers.
func stopWithin(t *testing.T, stop func()) {
	t.Helper()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		stop()
	}()
	waitClosed(t, returned)
}

// send delivers err to a reader of ch. ch is unbuffered, so it returns once
// a reader has received err. The deadline only bounds a failing run.
func send(t *testing.T, ch chan error, err error) {
	t.Helper()
	select {
	case ch <- err:
	case <-time.After(5 * time.Second):
		t.Fatal("no reader received the error")
	}
}

// TestWatchConnectorErrors_SourceError: a persist failure on the source's
// Errors() reaches fail, wrapped with the connector ID and still matching
// the original error (#2929).
func TestWatchConnectorErrors_SourceError(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, _ := errorsTopology(t)
	fail, calls, got := failRecorder()

	stop := w1.WatchConnectorErrors(context.Background(), fail)
	defer stop()

	persistErr := cerrors.New("failed to commit connector batch")
	src1 <- persistErr

	err := recvErr(t, got)
	is.True(cerrors.Is(err, persistErr))
	is.True(strings.Contains(err.Error(), "connector src1 reported an error"))
	stop()
	is.Equal(calls.Load(), int64(1))
}

// TestWatchConnectorErrors_SharedDestinationError: the shared sink's
// destination sits behind a shared boundary that Tasks() does not cross; its
// errors must still be read, by whichever worker gets there first, and fail
// exactly one run.
func TestWatchConnectorErrors_SharedDestinationError(t *testing.T) {
	is := is.New(t)
	w1, w2, _, _, dst := errorsTopology(t)
	fail1, calls1, got1 := failRecorder()
	fail2, calls2, got2 := failRecorder()

	stop1 := w1.WatchConnectorErrors(context.Background(), fail1)
	stop2 := w2.WatchConnectorErrors(context.Background(), fail2)

	persistErr := cerrors.New("failed to store connector batch")
	dst <- persistErr

	var err error
	select {
	case err = <-got1:
	case err = <-got2:
	case <-time.After(5 * time.Second):
		t.Fatal("no worker read the shared destination's error")
	}
	is.True(cerrors.Is(err, persistErr))
	is.True(strings.Contains(err.Error(), "connector dst reported an error"))

	stop1()
	stop2()
	is.Equal(calls1.Load()+calls2.Load(), int64(1))
}

// TestWatchConnectorErrors_FailOnce: fail is called once per watch even when
// several connectors report.
func TestWatchConnectorErrors_FailOnce(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, dst := errorsTopology(t)
	fail, calls, got := failRecorder()

	stop := w1.WatchConnectorErrors(context.Background(), fail)
	first := cerrors.New("first")
	src1 <- first
	is.True(cerrors.Is(recvErr(t, got), first))
	dst <- cerrors.New("second") // its reader still takes it, but fail is not called again
	stop()

	is.Equal(calls.Load(), int64(1))
}

// TestWatchConnectorErrors_OneReaderPerConnector: a watch starts exactly one
// reader per connector. One error on each of w1's two channels must be
// received (fewer readers would leave a send blocked), and those two
// receipts must end the watch on their own, without stop or ctx (a third
// reader would keep done open).
func TestWatchConnectorErrors_OneReaderPerConnector(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, dst := errorsTopology(t)
	fail, calls, _ := failRecorder()

	stop, done := w1.watchConnectorErrors(context.Background(), fail)
	send(t, src1, cerrors.New("from the source"))
	send(t, dst, cerrors.New("from the destination"))
	waitClosed(t, done)
	stop()
	is.Equal(calls.Load(), int64(1)) // fail is still called once per watch
}

// TestWatchConnectorErrors_StopReleasesReaders: after stop returns, every
// reader has returned and nothing reads the channels, so a connector's later
// error stays with the connector (Teardown returns it) and nothing grows per
// failed flush.
//
// The test asserts on each watch's done, not on runtime.NumGoroutine. The
// global goroutine count also moves when goroutines that other tests, the
// mock controller or the runtime own start and exit, which made the earlier
// version of this test flaky under load (#3035).
func TestWatchConnectorErrors_StopReleasesReaders(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, dst := errorsTopology(t)
	fail, calls, _ := failRecorder()

	type watch struct {
		stop func()
		done <-chan struct{}
	}
	watches := make([]watch, 0, 100)
	for range 100 {
		stop, done := w1.watchConnectorErrors(context.Background(), fail)
		watches = append(watches, watch{stop: stop, done: done})
	}
	for _, w := range watches {
		is.True(!isClosed(w.done)) // nothing has released the readers yet
	}
	for _, w := range watches {
		stopWithin(t, w.stop)
		is.True(isClosed(w.done)) // stop returned, so every reader has returned
	}

	assertUnread(t, src1)
	assertUnread(t, dst)
	is.Equal(calls.Load(), int64(0))
}

// TestWatchConnectorErrors_StopWaitsForReaderInFail: stop must not return,
// and done must not close, while a reader is still inside fail. Reader A
// takes an error and blocks in fail; reader B is parked and exits on quit.
// Code that ends the watch one reader early (a WaitGroup Add too few, Done
// before fail returns) lets stop return here. The 200ms window is negative
// evidence only: a correct run cannot fail on timing, and a slow scheduler
// only makes a broken run harder to catch (found in review of #3068).
func TestWatchConnectorErrors_StopWaitsForReaderInFail(t *testing.T) {
	w1, _, src1, _, _ := errorsTopology(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock) // a failing run must not leave reader A blocked
	fail := func(error) {
		close(entered)
		<-release
	}

	stop, done := w1.watchConnectorErrors(context.Background(), fail)
	send(t, src1, cerrors.New("boom"))
	waitClosed(t, entered) // reader A is inside fail; reader B (dst) is parked

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		stop() // releases B through quit, then must wait for A
	}()
	select {
	case <-returned:
		t.Fatal("stop returned while a reader was still inside fail")
	case <-done:
		t.Fatal("done closed while a reader was still inside fail")
	case <-time.After(200 * time.Millisecond):
	}

	unblock()
	waitClosed(t, returned)
	if !isClosed(done) {
		t.Fatal("done open after stop returned")
	}
}

// TestWatchConnectorErrors_ClosedErrorsChannel: a connector that closes its
// Errors() channel ends its reader without calling fail. With both channels
// closed the watch ends on its own, without stop or ctx.
func TestWatchConnectorErrors_ClosedErrorsChannel(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, dst := errorsTopology(t)
	fail, calls, _ := failRecorder()

	stop, done := w1.watchConnectorErrors(context.Background(), fail)
	close(src1)
	close(dst)
	waitClosed(t, done)
	stop()
	is.Equal(calls.Load(), int64(0))
}

// TestWatchConnectorErrors_ContextDone: the readers return when the run's
// context ends (the tomb was killed for another reason) without stop.
func TestWatchConnectorErrors_ContextDone(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, _ := errorsTopology(t)
	fail, calls, _ := failRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	stop, done := w1.watchConnectorErrors(ctx, fail)
	is.True(!isClosed(done))
	cancel()
	waitClosed(t, done) // released by ctx alone: stop has not been called
	assertUnread(t, src1)
	stop() // still safe after the readers are gone
	stop() // and idempotent
	is.Equal(calls.Load(), int64(0))
}

// TestWatchConnectorErrors_NoConnectors: a worker with no Errors() channel to
// read starts no reader; done is closed at once and stop does not block.
func TestWatchConnectorErrors_NoConnectors(t *testing.T) {
	is := is.New(t)
	s := NewMockSource(gomock.NewController(t))
	s.EXPECT().Errors().Return(nil).AnyTimes()
	w := &Worker{FirstTask: &TaskNode{Task: NewSourceTask("src", s, log.Test(t), NoOpConnectorMetrics{})}}
	fail, calls, _ := failRecorder()

	stop, done := w.watchConnectorErrors(context.Background(), fail)
	is.True(isClosed(done))
	stop()
	is.Equal(calls.Load(), int64(0))
}
