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

// Regression tests for per-run fencing (#2899 item 3; design doc
// 20261007-lifecycle-status-write-failure, rule R4) and for a takeover that
// fails to start. The arch-v2 half of pkg/lifecycle's fencing_test.go.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

// waitPhase waits until rp is in phase.
func waitPhase(t *testing.T, ls *Service, rp *runnablePipeline, phase runPhase) {
	t.Helper()
	deadline := time.Now().Add(admissionGuard)
	for {
		ls.publishMu.Lock()
		got := rp.phase
		ls.publishMu.Unlock()
		if got == phase {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not reach phase %d", phase)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestServiceLifecycle_SupersededRunCannotOverwriteNewRun is the I4
// regression test. A run failed and its cleanup moved it to a recovery
// backoff; while its Recovering write was in flight, an operator's Start took
// the pipeline over and the new run went live. The old run's Recovering then
// landed on the live pipeline, and with recovery exhausted it went on to write
// Degraded, record its error as the pipeline's terminal error and notify
// OnFailure (exit-on-degraded) for a pipeline that was running. A run now
// writes only while it owns the pipeline.
func TestServiceLifecycle_SupersededRunCannotOverwriteNewRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t, errTransient, 0)
	// Recovery gives up at once, so the superseded run takes the
	// recovery-failed arm: Degraded, terminal error, OnFailure.
	r.ls.errRecoveryCfg.MaxRetries = 0

	var first atomic.Pointer[runnablePipeline]
	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { closeIfOpen(release) })
	r.ls.testBeforeStatusWrite = func(rp *runnablePipeline, status pipeline.Status) {
		if rp == first.Load() && status == pipeline.StatusRecovering {
			once.Do(func() { close(held) })
			<-release
		}
	}

	r.ls.testWorkersReleased = func(rp *runnablePipeline) {
		r.mu.Lock()
		r.published = append(r.published, rp)
		r.mu.Unlock()
		first.CompareAndSwap(nil, rp)
	}

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	waitFor(t, held, "the failed run's Recovering write")

	// The failed run is in its recovery backoff: a Start takes it over.
	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	is.Equal(r.pl.GetStatus(), pipeline.StatusRunning)

	close(release)
	waitFor(t, first.Load().t.Dead(), "the superseded run's cleanup to finish")

	is.Equal(r.pl.GetStatus(), pipeline.StatusRunning) // not overwritten by the old run
	select {
	case e := <-r.failures:
		t.Fatalf("OnFailure was notified for a pipeline that is running again: %v", e.Error)
	default:
	}
	is.Equal(len(r.alive()), 1)

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID)) // the new run's result, not the old run's error
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
}

// TestServiceLifecycle_FailedTakeover_RecordsDegraded: a Start that takes
// over a run in recovery and then fails to build leaves the pipeline Degraded
// with the start error, whichever of the two finishes last, instead of
// Recovering with nothing running.
func TestServiceLifecycle_FailedTakeover_RecordsDegraded(t *testing.T) {
	t.Run("Start fails before the superseded run hands over", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		r := newAdmissionRun(t, errTransient, 0)

		restartWaiting := make(chan struct{})
		releaseRestart := make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { closeIfOpen(releaseRestart) })
		r.ls.testAfterBackoffWait = func(*runnablePipeline) {
			once.Do(func() { close(restartWaiting) })
			<-releaseRestart
		}

		is.NoErr(r.ls.Start(ctx, r.pl.ID))
		waitFor(t, restartWaiting, "the restart to finish its backoff wait")
		rp1 := r.runs()[0]

		r.conns.failGet.Store(true)
		is.True(cerrors.Is(r.ls.Start(ctx, r.pl.ID), errBuildFailed))

		close(releaseRestart)
		waitFor(t, rp1.t.Dead(), "the superseded run to hand over")
		assertFailedTakeover(t, r)
	})

	t.Run("superseded run hands over before the Start fails", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		r := newAdmissionRun(t, errTransient, 0)
		// A long backoff, so the run is waiting in it when the Start
		// supersedes it; the Start's build is held until the run has handed
		// over.
		r.ls.errRecoveryCfg.MinDelay = 10 * time.Minute
		r.ls.errRecoveryCfg.MaxDelay = 10 * time.Minute
		r.conns.gate = make(chan struct{})
		t.Cleanup(func() { closeIfOpen(r.conns.gate) })

		is.NoErr(r.ls.Start(ctx, r.pl.ID))
		rp1 := r.runs()[0]
		waitPhase(t, r.ls, rp1, phaseBackoff)

		r.conns.failGet.Store(true)
		started := make(chan error, 1)
		go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
		waitFor(t, r.conns.entered, "the Start to reach its build")
		waitFor(t, rp1.t.Dead(), "the superseded run to hand over")

		close(r.conns.gate)
		is.True(cerrors.Is(<-started, errBuildFailed))
		assertFailedTakeover(t, r)
	})
}

func assertFailedTakeover(t *testing.T, r *admissionRun) {
	t.Helper()
	is := is.New(t)
	is.Equal(r.pl.GetStatus(), pipeline.StatusDegraded)
	is.True(strings.Contains(r.pl.Error, errBuildFailed.Error()))
	is.True(cerrors.Is(r.ls.WaitPipeline(r.pl.ID), errBuildFailed))
	is.True(!r.ls.IsActive(r.pl.ID))
	is.Equal(len(r.alive()), 0)
	is.Equal(len(r.failures), 0) // the Start returned the failure; no exit-on-degraded
}
