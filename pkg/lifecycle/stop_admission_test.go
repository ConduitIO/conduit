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

// Regression tests for the review findings on admission by run liveness
// (#2955): a Stop must never be lost to a pending recovery restart, and a
// Start must not race a stopped run's terminal status write.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
)

var errBuildFailed = cerrors.New("injected: connector lookup failed")

// gatedConnectors makes a Start fail to build: while failGet is set, Get
// closes entered (once), waits for gate if it is set, and fails.
type gatedConnectors struct {
	testConnectorService
	failGet atomic.Bool
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (g *gatedConnectors) Get(ctx context.Context, id string) (*connector.Instance, error) {
	if g.failGet.Load() {
		g.once.Do(func() { close(g.entered) })
		if g.gate != nil {
			<-g.gate
		}
		return nil, errBuildFailed
	}
	return g.testConnectorService.Get(ctx, id)
}

// holdStatus holds the first write of status until release is closed.
type holdStatus struct {
	PipelineService
	status  pipeline.Status
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *holdStatus) UpdateStatus(ctx context.Context, id string, st pipeline.Status, msg string) error {
	if st == h.status {
		h.once.Do(func() {
			close(h.entered)
			<-h.release
		})
	}
	return h.PipelineService.UpdateStatus(ctx, id, st, msg)
}

// TestServiceLifecycle_StopBeforeRestartReserves_NotLost: a Stop that lands
// after the pending restart's last check and before it reserves used to be
// recorded on the dead run after the restart was admitted. Stop returned nil
// and the restart ran on. Stop now records the request under the lock the
// restart reserves under, and the restart refuses a run that was asked to
// stop.
func TestServiceLifecycle_StopBeforeRestartReserves_NotLost(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)
	transientErr := cerrors.New("lost connection to source")

	var once sync.Once
	stopErr := make(chan error, 1)
	r.ls.testAtReservation = func(id string, pred *runnablePipeline) {
		if pred != nil {
			once.Do(func() { stopErr <- r.ls.Stop(ctx, id, false) })
		}
	}

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]
	injectSourceError(ctx, t, r.ls, r.pl.ID, transientErr)
	waitClosed(t, first.t.Dead(), "the failed run's cleanup to finish")

	is.NoErr(<-stopErr)
	if alive := r.alive(); len(alive) != 0 {
		t.Fatalf("Stop returned nil but %d run(s) are live: the stop was lost to the pending restart", len(alive))
	}
	is.Equal(len(r.runs()), 1) // no restart was built
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.True(cerrors.Is(r.ls.WaitPipeline(r.pl.ID), transientErr)) // stopped, with the error kept
	is.Equal(len(r.failures), 0)
}

// TestServiceLifecycle_StopWhileFinishing_NoRestart: a Stop that arrives
// after the run's nodes died, while its cleanup is classifying it, used to
// answer pipeline.not_running, and a transient failure then went on to
// recover and restart the pipeline. The stop is now recorded on the
// finishing run.
func TestServiceLifecycle_StopWhileFinishing_NoRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)
	transientErr := cerrors.New("lost connection to source")

	var once sync.Once
	stopErr := make(chan error, 1)
	r.ls.testAfterFinishing = func(rp *runnablePipeline) {
		once.Do(func() { stopErr <- r.ls.Stop(ctx, rp.pipeline.ID, false) })
	}

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]
	injectSourceError(ctx, t, r.ls, r.pl.ID, transientErr)
	waitClosed(t, first.t.Dead(), "the failed run's cleanup to finish")

	is.NoErr(<-stopErr)
	is.Equal(len(r.runs()), 1) // not restarted
	is.Equal(len(r.alive()), 0)
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.Equal(len(r.failures), 0)
}

// TestServiceLifecycle_StopDuringRestartBuild_FailedBuild_EndsStopped: a
// Stop recorded on a recovery restart's reservation was dropped when that
// restart's build failed, and the run it restarted went Degraded and
// notified OnFailure, which trips exit-on-degraded after a user stop. The
// stop is now recorded on the restarted run too, so it ends stopped.
func TestServiceLifecycle_StopDuringRestartBuild_FailedBuild_EndsStopped(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)
	transientErr := cerrors.New("lost connection to source")

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]

	// From here on, building fails, after a hold.
	r.conns.gate = make(chan struct{})
	t.Cleanup(func() { closeIfOpen(r.conns.gate) })
	r.conns.failGet.Store(true)

	injectSourceError(ctx, t, r.ls, r.pl.ID, transientErr)
	waitClosed(t, r.conns.entered, "the restart to start building")
	is.True(r.ls.IsActive(r.pl.ID))
	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))

	close(r.conns.gate)
	waitClosed(t, first.t.Dead(), "the restarted run's cleanup to finish")

	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.True(cerrors.Is(r.ls.WaitPipeline(r.pl.ID), transientErr))
	is.Equal(len(r.alive()), 0)
	is.Equal(len(r.failures), 0) // a stop, not a failure: no exit-on-degraded
}

// TestServiceLifecycle_StopThenStart_RefusedWhileFinishing: Start used to be
// granted while the stopped run was still writing its terminal status, which
// then landed over the new run's Running: a live run reported UserStopped.
// Start is now refused with the retryable pipeline.stopping until the
// stopped run is done; the next Start succeeds and the pipeline reports
// Running.
func TestServiceLifecycle_StopThenStart_RefusedWhileFinishing(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)
	h := &holdStatus{PipelineService: r.ls.pipelines, status: pipeline.StatusUserStopped, entered: make(chan struct{}), release: make(chan struct{})}
	r.ls.pipelines = h
	t.Cleanup(func() { closeIfOpen(h.release) })

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]
	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	waitClosed(t, h.entered, "the stopped run's terminal write")

	err := r.ls.Start(ctx, r.pl.ID)
	is.True(cerrors.Is(err, pipeline.ErrPipelineStopping))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineStopping)
	is.Equal(ce.Code.GRPCCode(), codes.Unavailable) // retryable
	is.True(r.ls.IsActive(r.pl.ID))                 // Delete and Update are refused too

	close(h.release)
	waitClosed(t, first.t.Dead(), "the stopped run's cleanup to finish")

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	is.Equal(len(r.alive()), 1)
	is.Equal(r.pl.GetStatus(), pipeline.StatusRunning)

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
}
