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

package lifecycle

// Regression tests for #2969 at the Service level: a stop (user Stop or
// StopAll) that arrives after a real stream.SourceNode failed must neither
// hang the shutdown nor replace the source error as the recorded cause.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// drainSinkNode is the downstream of a SourceNode: it drains the source's out
// channel until the source closes it. A PubNode refuses to run without one.
type drainSinkNode struct {
	id string
	in <-chan *stream.Message
}

func (n *drainSinkNode) ID() string { return n.id }

func (n *drainSinkNode) Run(ctx context.Context) error {
	for {
		select {
		case _, ok := <-n.in:
			if !ok {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// failingSource returns a SourceNode wired to a sink, whose Read fails with
// readErr once failRead is closed. Teardown signals tearingDown and then
// blocks until releaseTeardown is closed, which keeps the node in the window
// of #2969: Run has stopped reading, but is not done, so a stop is still
// accepted by the node and its control message has no receiver.
type failingSource struct {
	node            *stream.SourceNode
	sink            *drainSinkNode
	failRead        chan struct{}
	tearingDown     chan struct{}
	releaseTeardown chan struct{}
	stopCalled      chan struct{}
}

func newFailingSource(ctrl *gomock.Controller, readErr error, blockTeardown bool) *failingSource {
	f := &failingSource{
		failRead:        make(chan struct{}),
		tearingDown:     make(chan struct{}),
		releaseTeardown: make(chan struct{}),
		stopCalled:      make(chan struct{}),
	}
	src := mock.NewSource(ctrl)
	src.EXPECT().ID().Return("source-connector").AnyTimes()
	src.EXPECT().Open(gomock.Any()).Return(nil)
	src.EXPECT().Errors().Return(make(chan error))
	src.EXPECT().Read(gomock.Any()).DoAndReturn(func(context.Context) ([]opencdc.Record, error) {
		<-f.failRead
		return nil, readErr
	})
	src.EXPECT().Stop(gomock.Any()).DoAndReturn(func(context.Context) (opencdc.Position, error) {
		close(f.stopCalled)
		return opencdc.Position("last-position"), nil
	}).AnyTimes()
	src.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(context.Context) error {
		close(f.tearingDown)
		if blockTeardown {
			<-f.releaseTeardown
		}
		return nil
	})

	f.node = &stream.SourceNode{Name: "source-" + uuid.NewString(), Source: src, PipelineTimer: noop.Timer{}}
	f.sink = &drainSinkNode{id: "sink-" + uuid.NewString(), in: f.node.Pub()}
	return f
}

// TestServiceLifecycle_StopAfterSourceError_RecordsSourceError: the source
// fails, and while the node tears down a stop arrives with a context that has
// no deadline (what the runtime passes on SIGTERM). Before #2969 the stop
// blocked in InjectControlMessage holding the PubNode lock, the node's cleanup
// blocked on that lock, and the pipeline never finished. Now Stop returns, and
// per ADR 20261007-stop-requested-never-recovers the run ends stopped with the
// source error recorded as the cause, not the stop's own error.
func TestServiceLifecycle_StopAfterSourceError_RecordsSourceError(t *testing.T) {
	srcErr := cerrors.New("source connector lost its replication slot")
	const bound = 5 * time.Second

	testCases := []struct {
		name string
		stop func(ctx context.Context, ls *Service, id string) error
		want pipeline.Status
	}{{
		name: "user graceful Stop",
		stop: func(ctx context.Context, ls *Service, id string) error { return ls.Stop(ctx, id, false) },
		want: pipeline.StatusUserStopped,
	}, {
		name: "StopAll(ErrGracefulShutdown), the SIGTERM path",
		stop: func(ctx context.Context, ls *Service, _ string) error {
			ls.StopAll(ctx, pipeline.ErrGracefulShutdown)
			return nil
		},
		want: pipeline.StatusSystemStopped,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			f := newFailingSource(gomock.NewController(t), srcErr, true)
			tr := newTerminalRun(t, noRetryRecoveryCfg(), false, f.node, f.sink)
			tr.start(t)

			// The source fails and Run goes into teardown.
			close(f.failRead)
			waitClosed(t, f.tearingDown, "the source node to start tearing down")

			stopDone := make(chan error, 1)
			go func() { stopDone <- tc.stop(context.Background(), tr.ls, tr.pl.ID) }()

			// Give the stop time to get stuck injecting its control message
			// (the bug) before teardown ends and cleanup needs the lock.
			waitClosed(t, f.stopCalled, "the stop to reach the source connector")
			time.Sleep(100 * time.Millisecond)
			close(f.releaseTeardown)

			select {
			case <-stopDone:
			case <-time.After(bound):
				t.Fatal("stop did not return: the source node is wedged injecting a control message (#2969)")
			}
			tr.waitDead(t)

			assertStoppedWithError(t, tr, []pipeline.Status{pipeline.StatusRunning, tc.want}, srcErr)
			// The stop's own failure must not become the recorded cause.
			is.True(!strings.Contains(tr.pl.Error, "PubNode has stopped running"))
			is.Equal(tr.rp.recoveryAttempts.Load(), int64(0))
		})
	}
}

// TestServiceLifecycle_FatalSourceErrorBeforeStop_StaysDegraded: a source that
// failed fatally on its own before the stop was requested is degraded, not
// stopped (ADR 20261007, decision 4), even though the stop then reaches a
// source node that is no longer running. A held node keeps the cleanup
// goroutine waiting until the stop has been requested, so the order is
// deterministic.
func TestServiceLifecycle_FatalSourceErrorBeforeStop_StaysDegraded(t *testing.T) {
	fatalErr := cerrors.FatalError(cerrors.New("fatal source error"))

	testCases := []struct {
		name string
		stop func(ctx context.Context, ls *Service, id string)
	}{{
		name: "StopAll",
		stop: func(ctx context.Context, ls *Service, _ string) { ls.StopAll(ctx, pipeline.ErrGracefulShutdown) },
	}, {
		name: "user graceful Stop",
		// The source node is gone, Stop reports that; the outcome is what counts.
		stop: func(ctx context.Context, ls *Service, id string) { _ = ls.Stop(ctx, id, false) },
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			f := newFailingSource(gomock.NewController(t), fatalErr, false)
			held := &heldNode{id: "held-" + uuid.NewString(), release: make(chan struct{})}
			tr := newTerminalRun(t, testErrRecoveryCfg(), false, f.node, f.sink, held)
			tr.start(t)

			close(f.failRead)
			waitClosed(t, tr.rp.t.Dying(), "the fatal error to reach the tomb")
			tc.stop(context.Background(), tr.ls, tr.pl.ID)
			close(held.release)
			tr.waitDead(t)

			is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusDegraded}, tr.statuses())
			is.True(strings.Contains(tr.pl.Error, "fatal source error"))
			events := tr.failureEvents()
			is.Equal(len(events), 1) // a real failure: OnFailure fires
			is.True(cerrors.Is(events[0].Error, fatalErr))
		})
	}
}

var _ stream.Node = (*drainSinkNode)(nil)
