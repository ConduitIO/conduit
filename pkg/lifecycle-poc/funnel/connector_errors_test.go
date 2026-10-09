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
	"runtime"
	"strings"
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

// assertUnread fails if anything is still reading ch.
func assertUnread(t *testing.T, ch chan error) {
	t.Helper()
	select {
	case ch <- cerrors.New("late"):
		t.Fatal("a reader is still receiving after stop")
	case <-time.After(20 * time.Millisecond):
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

// TestWatchConnectorErrors_StopReleasesReaders: after stop returns, nothing
// reads the channels and the reader goroutines are gone, so a connector's
// later error stays with the connector (Teardown returns it) and nothing
// grows per failed flush.
func TestWatchConnectorErrors_StopReleasesReaders(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, dst := errorsTopology(t)
	fail, calls, _ := failRecorder()

	before := runtime.NumGoroutine()
	stops := make([]func(), 0, 100)
	for range 100 {
		stops = append(stops, w1.WatchConnectorErrors(context.Background(), fail))
	}
	is.True(runtime.NumGoroutine() >= before+200) // one reader per connector per watch
	for _, stop := range stops {
		stop()
	}
	waitGoroutines(t, before)

	assertUnread(t, src1)
	assertUnread(t, dst)
	is.Equal(calls.Load(), int64(0))
}

// TestWatchConnectorErrors_ContextDone: the readers exit when the run's
// context ends (the tomb was killed for another reason) without stop.
func TestWatchConnectorErrors_ContextDone(t *testing.T) {
	is := is.New(t)
	w1, _, src1, _, _ := errorsTopology(t)
	fail, calls, _ := failRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	before := runtime.NumGoroutine()
	stop := w1.WatchConnectorErrors(ctx, fail)
	cancel()
	waitGoroutines(t, before)
	assertUnread(t, src1)
	stop() // still safe after the readers are gone
	stop() // and idempotent
	is.Equal(calls.Load(), int64(0))
}

func waitGoroutines(t *testing.T, limit int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > limit {
		if time.Now().After(deadline) {
			t.Fatalf("readers did not exit: %d goroutines, want at most %d", runtime.NumGoroutine(), limit)
		}
		time.Sleep(time.Millisecond)
	}
}
