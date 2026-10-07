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

// Tests for the shutdown mode StopAll puts the service in (#2901). Unlike
// service_stop_requested_test.go these use symbols the fix introduced, so they
// do not compile against the code before it.

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

func TestServiceLifecycle_StartRefusedAfterStopAll(t *testing.T) {
	is := is.New(t)
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, newScriptedNode(nil, nil))
	tr.ls.StopAll(context.Background(), pipeline.ErrGracefulShutdown)

	err := tr.ls.Start(context.Background(), tr.pl.ID)
	is.True(cerrors.Is(err, pipeline.ErrShuttingDown))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodeShuttingDown)
	is.True(ce.Suggestion != "")

	// runPipeline is the authoritative check (Start's is an early exit): it
	// must refuse before starting a single goroutine.
	err = tr.ls.runPipeline(context.Background(), tr.rp)
	is.True(cerrors.Is(err, pipeline.ErrShuttingDown))
	is.True(tr.rp.t == nil)
	_, published := tr.ls.runningPipelines.Get(tr.pl.ID)
	is.True(!published)
	is.Equal(tr.statuses(), []pipeline.Status(nil))
}

// TestServiceLifecycle_RunPublishedDuringShutdown_StopsItself holds a run in
// the window between admission and publication (its nodes are already
// running) and calls StopAll there. StopAll cannot see the run, so the run
// must notice the shutdown when it publishes and stop itself, and Wait must
// cover it even though it was not in runningPipelines when Wait began.
func TestServiceLifecycle_RunPublishedDuringShutdown_StopsItself(t *testing.T) {
	is := is.New(t)
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, newScriptedNode(nil, nil))

	inWindow := make(chan struct{})
	release := make(chan struct{})
	tr.ls.testBeforePublish = func(*runnablePipeline) {
		close(inWindow)
		<-release
	}

	started := make(chan error, 1)
	go func() { started <- tr.ls.runPipeline(context.Background(), tr.rp) }()
	waitClosed(t, inWindow, "runPipeline to reach the publication window")

	tr.ls.StopAll(context.Background(), pipeline.ErrGracefulShutdown)
	_, published := tr.ls.runningPipelines.Get(tr.pl.ID)
	is.True(!published) // StopAll had nothing to stop

	waitErr := make(chan error, 1)
	go func() { waitErr <- tr.ls.Wait(terminalStatusGuard) }()

	// Wait must not return while the held run is live.
	select {
	case err := <-waitErr:
		t.Fatalf("Wait returned (%v) while a run was live and unpublished", err)
	default:
	}

	close(release)
	is.NoErr(<-waitErr)
	// Wait returned, so the run must already be dead.
	select {
	case <-tr.rp.t.Dead():
	default:
		t.Fatal("Wait returned while the run that published during shutdown was still live")
	}
	is.NoErr(<-started)

	is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusSystemStopped}, tr.statuses())
	is.Equal(len(tr.failureEvents()), 0)
}

func TestRunTracker(t *testing.T) {
	is := is.New(t)
	var r runTracker

	r.wait() // zero value: nothing live, returns at once

	is.True(r.admit())
	is.True(r.admit())

	waited := make(chan struct{})
	go func() {
		r.wait()
		close(waited)
	}()

	r.release()
	r.mu.Lock()
	idle, live := r.idle, r.live
	r.mu.Unlock()
	is.Equal(live, 1)
	select {
	case <-idle:
		t.Fatal("idle closed with one run still live")
	default:
	}

	select {
	case <-r.shutdownStarted():
		t.Fatal("shutdownStarted closed before beginShutdown")
	default:
	}
	reason := cerrors.New("bye")
	r.beginShutdown(reason)
	r.beginShutdown(cerrors.New("ignored")) // idempotent, first reason wins
	down, got := r.shuttingDown()
	is.True(down)
	is.Equal(got, reason)
	<-r.shutdownStarted()
	is.True(!r.admit()) // nothing is admitted once shutdown began

	r.release()
	<-waited
}

func TestStopSignal(t *testing.T) {
	is := is.New(t)
	var s stopSignal
	is.True(!s.requested())
	done := s.done()
	select {
	case <-done:
		t.Fatal("done closed before fire")
	default:
	}
	s.fire()
	s.fire() // idempotent
	is.True(s.requested())
	<-done
	<-s.done()
}
