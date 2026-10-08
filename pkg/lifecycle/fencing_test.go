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
// fails to start. Every interleaving is held open by a hook or a blocked
// call; the timers are failure guards.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

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
	r := newAdmissionRun(t)
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

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first.Store(r.runs()[0])
	injectSourceError(ctx, t, r.ls, r.pl.ID, cerrors.New("lost connection to source"))
	waitClosed(t, held, "the failed run's Recovering write")

	// The failed run is in its recovery backoff: a Start takes it over.
	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	is.Equal(r.pl.GetStatus(), pipeline.StatusRunning)

	close(release)
	waitClosed(t, first.Load().t.Dead(), "the superseded run's cleanup to finish")

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
// over a run in recovery and then fails to build leaves the pipeline with no
// run. The superseded run left everything to the new one, so the status kept
// saying Recovering with nothing running. Now the pipeline ends Degraded with
// the start error, whichever of the two finishes last.
func TestServiceLifecycle_FailedTakeover_RecordsDegraded(t *testing.T) {
	t.Run("Start fails before the superseded run hands over", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		r := newAdmissionRun(t)

		// Hold the restart after its wait until the Start has failed.
		restartWaiting := make(chan struct{})
		releaseRestart := make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { closeIfOpen(releaseRestart) })
		r.ls.testAfterBackoffWait = func(*runnablePipeline) {
			once.Do(func() { close(restartWaiting) })
			<-releaseRestart
		}

		is.NoErr(r.ls.Start(ctx, r.pl.ID))
		rp1 := r.runs()[0]
		injectSourceError(ctx, t, r.ls, r.pl.ID, cerrors.New("lost connection to source"))
		waitClosed(t, restartWaiting, "the restart to finish its backoff wait")

		r.conns.failGet.Store(true)
		is.True(cerrors.Is(r.ls.Start(ctx, r.pl.ID), errBuildFailed))

		close(releaseRestart)
		waitClosed(t, rp1.t.Dead(), "the superseded run to hand over")
		assertFailedTakeover(t, r)
	})

	t.Run("superseded run hands over before the Start fails", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		r := newAdmissionRun(t)
		// A long backoff, so the run is waiting in it when the Start
		// supersedes it; the Start's build is held until the run has handed
		// over.
		r.ls.errRecoveryCfg.MinDelay = 10 * time.Minute
		r.ls.errRecoveryCfg.MaxDelay = 10 * time.Minute
		r.conns.gate = make(chan struct{})
		t.Cleanup(func() { closeIfOpen(r.conns.gate) })
		backoff := newLogSignal("restarting with backoff")
		r.ls.logger = log.New(zerolog.New(backoff))

		is.NoErr(r.ls.Start(ctx, r.pl.ID))
		rp1 := r.runs()[0]
		injectSourceError(ctx, t, r.ls, r.pl.ID, cerrors.New("lost connection to source"))
		waitClosed(t, backoff.seen, "the run to start its backoff wait")

		r.conns.failGet.Store(true)
		started := make(chan error, 1)
		go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
		waitClosed(t, r.conns.entered, "the Start to reach its build")
		waitClosed(t, rp1.t.Dead(), "the superseded run to hand over")

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

// blockGet holds the first connector lookup while armed, then lets it
// succeed: a Start that is building, admitted but not yet published.
type blockGet struct {
	ConnectorService
	armed   atomic.Bool
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (b *blockGet) Get(ctx context.Context, id string) (*connector.Instance, error) {
	if b.armed.Load() {
		b.once.Do(func() { close(b.entered) })
		<-b.gate
	}
	return b.ConnectorService.Get(ctx, id)
}

// TestServiceLifecycle_ShutdownDuringTakeover_SystemStopped: shutdown
// begins while a Start that took over a run in recovery is building. The
// Start is refused (pipeline.shutting_down) and the pipeline has no run. It
// used to be recorded Degraded, which is not started on the next boot; a
// shutdown during a plain recovery backoff ends SystemStopped, and so does
// this.
func TestServiceLifecycle_ShutdownDuringTakeover_SystemStopped(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)
	transientErr := cerrors.New("lost connection to source")
	r.ls.errRecoveryCfg.MinDelay = 10 * time.Minute
	r.ls.errRecoveryCfg.MaxDelay = 10 * time.Minute
	bg := &blockGet{ConnectorService: r.ls.connectors, entered: make(chan struct{}), gate: make(chan struct{})}
	r.ls.connectors = bg
	t.Cleanup(func() { closeIfOpen(bg.gate) })
	backoff := newLogSignal("restarting with backoff")
	r.ls.logger = log.New(zerolog.New(backoff))

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	rp1 := r.runs()[0]
	injectSourceError(ctx, t, r.ls, r.pl.ID, transientErr)
	waitClosed(t, backoff.seen, "the run to start its backoff wait")

	bg.armed.Store(true)
	started := make(chan error, 1)
	go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
	waitClosed(t, bg.entered, "the takeover Start to build")
	waitClosed(t, rp1.t.Dead(), "the superseded run to hand over")

	r.ls.StopAll(ctx, pipeline.ErrGracefulShutdown)
	close(bg.gate)
	is.True(cerrors.Is(<-started, pipeline.ErrShuttingDown))
	is.NoErr(r.ls.Wait(terminalStatusGuard))

	is.Equal(r.pl.GetStatus(), pipeline.StatusSystemStopped) // starts again on the next boot
	is.True(cerrors.Is(r.ls.WaitPipeline(r.pl.ID), transientErr))
	is.Equal(len(r.failures), 0)
}

// TestServiceLifecycle_FailedTakeover_SupersededRunGivesUp_OrderIndependent:
// the superseded run gives up its recovery (MaxRetries 0: Degraded,
// OnFailure) and the takeover Start fails. The outcome used to depend on
// which finished first: one order recorded Degraded with no terminal error
// and never notified OnFailure, so exit-on-degraded did not trip. Both
// orders now end the same way: the run's own decision stands.
func TestServiceLifecycle_FailedTakeover_SupersededRunGivesUp_OrderIndependent(t *testing.T) {
	for _, startFailsFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("startFailsFirst=%v", startFailsFirst), func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			r := newAdmissionRun(t)
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

			is.NoErr(r.ls.Start(ctx, r.pl.ID))
			first.Store(r.runs()[0])
			injectSourceError(ctx, t, r.ls, r.pl.ID, cerrors.New("lost connection to source"))
			waitClosed(t, held, "the failed run's Recovering write")

			r.conns.failGet.Store(true)
			if startFailsFirst {
				is.True(cerrors.Is(r.ls.Start(ctx, r.pl.ID), errBuildFailed))
				close(release)
				waitClosed(t, first.Load().t.Dead(), "the superseded run to finish")
			} else {
				r.conns.gate = make(chan struct{})
				t.Cleanup(func() { closeIfOpen(r.conns.gate) })
				started := make(chan error, 1)
				go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
				waitClosed(t, r.conns.entered, "the takeover Start to build")
				close(release)
				waitClosed(t, first.Load().t.Dead(), "the superseded run to finish")
				close(r.conns.gate)
				is.True(cerrors.Is(<-started, errBuildFailed))
			}

			is.Equal(r.pl.GetStatus(), pipeline.StatusDegraded)
			is.True(cerrors.Is(r.ls.WaitPipeline(r.pl.ID), pipeline.ErrPipelineCannotRecover))
			is.Equal(len(r.failures), 1) // exit-on-degraded trips in both orders
			e := <-r.failures
			is.True(cerrors.Is(e.Error, pipeline.ErrPipelineCannotRecover))
			is.True(!r.ls.IsActive(r.pl.ID))
		})
	}
}

// TestServiceLifecycle_StopDuringTakeover_FailedStart_EndsStopped: a Stop
// that arrives while a takeover Start is building, which then fails, ends
// the pipeline stopped (with the run's error kept), not Degraded.
func TestServiceLifecycle_StopDuringTakeover_FailedStart_EndsStopped(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)
	transientErr := cerrors.New("lost connection to source")
	r.ls.errRecoveryCfg.MinDelay = 10 * time.Minute
	r.ls.errRecoveryCfg.MaxDelay = 10 * time.Minute
	r.conns.gate = make(chan struct{})
	t.Cleanup(func() { closeIfOpen(r.conns.gate) })
	backoff := newLogSignal("restarting with backoff")
	r.ls.logger = log.New(zerolog.New(backoff))

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	rp1 := r.runs()[0]
	injectSourceError(ctx, t, r.ls, r.pl.ID, transientErr)
	waitClosed(t, backoff.seen, "the run to start its backoff wait")

	r.conns.failGet.Store(true)
	started := make(chan error, 1)
	go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
	waitClosed(t, r.conns.entered, "the takeover Start to build")
	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	waitClosed(t, rp1.t.Dead(), "the superseded run to finish")
	close(r.conns.gate)
	is.True(cerrors.Is(<-started, errBuildFailed))

	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.True(cerrors.Is(r.ls.WaitPipeline(r.pl.ID), transientErr))
	is.Equal(len(r.failures), 0)
}

// TestServiceLifecycle_StatusLocksReleased: the per-pipeline status locks
// are reference-counted and leave nothing behind once a pipeline's runs are
// done, so deleted pipelines do not accumulate entries.
func TestServiceLifecycle_StatusLocksReleased(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	is.NoErr(r.ls.Wait(terminalStatusGuard))

	r.ls.statusLocksMu.Lock()
	n := len(r.ls.statusLocks)
	r.ls.statusLocksMu.Unlock()
	is.Equal(n, 0)
}
