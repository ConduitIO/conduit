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

// Regression tests for admission by run liveness (#2899 item 2; design doc
// 20261007-lifecycle-status-write-failure, rule R3). Every interleaving is
// held open by a hook or a blocked channel; the timers are failure guards.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// admissionRun is a pipeline with real (mocked-plugin) connectors whose
// plugins can be dispensed any number of times, and a record of every run
// that was published.
type admissionRun struct {
	ls       *Service
	pl       *pipeline.Instance
	failures chan FailureEvent

	mu        sync.Mutex
	published []*runnablePipeline
	// holdPublish, if set, is called for the nth publication (1-based)
	// before the run is published.
	holdPublish func(n int)
}

func newAdmissionRun(t *testing.T) *admissionRun {
	t.Helper()
	ctx, killAll := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := gomock.NewController(t)
	source, srcDispenser, _ := countingSource(ctrl, persister, noRecords)
	destination, destDispenser := countingDestination(ctrl, persister, noRecords)
	dlq, dlqDispenser := countingDestination(ctrl, persister, noRecords)
	pl.DLQ.Plugin = dlq.Plugin
	if pl, err = ps.AddConnector(ctx, pl.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if pl, err = ps.AddConnector(ctx, pl.ID, destination.ID); err != nil {
		t.Fatal(err)
	}

	cfg := testErrRecoveryCfg()
	cfg.MinDelay = time.Millisecond
	cfg.MaxDelay = time.Millisecond
	r := &admissionRun{pl: pl, failures: make(chan FailureEvent, 8)}
	r.ls = NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps,
	)
	r.ls.OnFailure(func(e FailureEvent) { r.failures <- e })
	r.ls.testBeforePublish = func(rp *runnablePipeline) {
		r.mu.Lock()
		r.published = append(r.published, rp)
		n := len(r.published)
		hold := r.holdPublish
		r.mu.Unlock()
		if hold != nil {
			hold(n)
		}
	}

	// Registered first so it runs last: kill whatever a regression left
	// alive, then drain the persister.
	t.Cleanup(func() { stopAndWaitPersister(t, killAll, persister) })
	t.Cleanup(func() {
		for _, rp := range r.runs() {
			if rp.t != nil && rp.t.Alive() {
				rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
				select {
				case <-rp.t.Dead():
				case <-time.After(terminalStatusGuard):
					t.Error("a run did not die after a force kill")
				}
			}
		}
	})
	return r
}

// closeIfOpen closes ch unless the test already did, so a failing test
// releases whatever it was holding before its runs are killed. Only the test
// goroutine closes these channels.
func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (r *admissionRun) runs() []*runnablePipeline {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*runnablePipeline(nil), r.published...)
}

func (r *admissionRun) alive() []*runnablePipeline {
	var out []*runnablePipeline
	for _, rp := range r.runs() {
		if rp.t != nil && rp.t.Alive() {
			out = append(out, rp)
		}
	}
	return out
}

// TestServiceLifecycle_Recovery_ExternalStartRacesPendingRestart is the I3
// regression test. A run failed and its cleanup is about to restart it; an
// operator calls Start at the same moment. The restart's guard read the map
// without a lock, still found the failed run (the operator's run was not yet
// published), and started a second run. Two runs read one source: duplicate
// delivery, and positions last-writer-wins. Now the operator's Start takes
// the pipeline over under publishMu and the pending restart is abandoned:
// exactly one run, two dispenses (the failed run and the operator's).
func TestServiceLifecycle_Recovery_ExternalStartRacesPendingRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	// Hold the restart after its backoff wait, before it admits itself.
	restartWaiting := make(chan struct{})
	releaseRestart := make(chan struct{})
	var restartOnce sync.Once
	r.ls.testAfterBackoffWait = func(*runnablePipeline) {
		restartOnce.Do(func() { close(restartWaiting) })
		<-releaseRestart
	}
	// Hold the operator's run (the 2nd publication) after its nodes started,
	// before it is published.
	externalHeld := make(chan struct{})
	releaseExternal := make(chan struct{})
	var externalOnce sync.Once
	t.Cleanup(func() { closeIfOpen(releaseRestart); closeIfOpen(releaseExternal) })
	r.holdPublish = func(n int) {
		if n == 2 {
			externalOnce.Do(func() { close(externalHeld) })
			<-releaseExternal
		}
	}

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]
	injectSourceError(ctx, t, r.ls, r.pl.ID, cerrors.New("lost connection to source"))
	waitClosed(t, restartWaiting, "the restart to finish its backoff wait")

	external := make(chan error, 1)
	go func() { external <- r.ls.Start(ctx, r.pl.ID) }()
	waitClosed(t, externalHeld, "the operator's Start to start its nodes")

	// The restart goes ahead while the operator's run is not yet published.
	close(releaseRestart)
	waitClosed(t, first.t.Dead(), "the failed run's cleanup to finish")

	close(releaseExternal)
	select {
	case err := <-external:
		is.NoErr(err)
	case <-time.After(terminalStatusGuard):
		t.Fatal("the operator's Start did not return")
	}

	if alive := r.alive(); len(alive) != 1 {
		t.Fatalf("%d runs of one pipeline are live (want 1): two runs read one source (#2899 item 2)", len(alive))
	}
	is.Equal(len(r.runs()), 2) // the failed run and the operator's; no restart
	live, ok := r.ls.runningPipelines.Get(r.pl.ID)
	is.True(ok)
	is.True(live == r.alive()[0])
	is.Equal(len(r.failures), 0)

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	is.Equal(len(r.alive()), 0)
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
}

// TestServiceLifecycle_ConcurrentStarts_OneWins: two Starts of a stopped
// pipeline with no processors both passed the status check, because the
// status turns Running only after publication. Now the second is refused
// while the first holds its reservation.
func TestServiceLifecycle_ConcurrentStarts_OneWins(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	held := make(chan struct{})
	release := make(chan struct{})
	var heldOnce sync.Once
	t.Cleanup(func() { closeIfOpen(release) })
	r.holdPublish = func(n int) {
		if n == 1 {
			heldOnce.Do(func() { close(held) })
			<-release
		}
	}

	first := make(chan error, 1)
	go func() { first <- r.ls.Start(ctx, r.pl.ID) }()
	waitClosed(t, held, "the first Start to start its nodes")

	err := r.ls.Start(ctx, r.pl.ID)
	is.True(cerrors.Is(err, pipeline.ErrPipelineRunning))
	is.True(r.ls.IsActive(r.pl.ID)) // a starting run counts

	close(release)
	is.NoErr(<-first)
	is.Equal(len(r.alive()), 1)
	is.Equal(len(r.runs()), 1) // the second Start built nothing

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	is.NoErr(r.ls.WaitPipeline(r.pl.ID))
	is.True(!r.ls.IsActive(r.pl.ID))
}

// TestServiceLifecycle_StopWhileStarting_StopsRun: a Stop that arrives while
// a Start is still building its run is admitted (the run is starting) and
// applied when the run is published. It used to answer "not running" and
// leave the run to start.
func TestServiceLifecycle_StopWhileStarting_StopsRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	held := make(chan struct{})
	release := make(chan struct{})
	var heldOnce sync.Once
	t.Cleanup(func() { closeIfOpen(release) })
	r.holdPublish = func(n int) {
		if n == 1 {
			heldOnce.Do(func() { close(held) })
			<-release
		}
	}

	started := make(chan error, 1)
	go func() { started <- r.ls.Start(ctx, r.pl.ID) }()
	waitClosed(t, held, "Start to start its nodes")

	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false))
	waited := make(chan error, 1)
	go func() { waited <- r.ls.WaitPipeline(r.pl.ID) }()

	close(release)
	is.NoErr(<-started)
	select {
	case err := <-waited:
		is.NoErr(err)
	case <-time.After(terminalStatusGuard):
		t.Fatal("the run stopped while starting did not finish")
	}
	is.Equal(len(r.alive()), 0)
	is.Equal(r.pl.GetStatus(), pipeline.StatusUserStopped)
	is.Equal(len(r.failures), 0)
}

// TestServiceLifecycle_IsActive_DuringBackoff: a run waiting out a recovery
// backoff is active (Delete and Update are refused), whatever the status
// says; once it is stopped it is not.
func TestServiceLifecycle_IsActive_DuringBackoff(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	restartWaiting := make(chan struct{})
	releaseRestart := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { closeIfOpen(releaseRestart) })
	r.ls.testAfterBackoffWait = func(*runnablePipeline) {
		once.Do(func() { close(restartWaiting) })
		<-releaseRestart
	}

	is.NoErr(r.ls.Start(ctx, r.pl.ID))
	first := r.runs()[0]
	injectSourceError(ctx, t, r.ls, r.pl.ID, cerrors.New("lost connection to source"))
	waitClosed(t, restartWaiting, "the restart to finish its backoff wait")

	is.True(r.ls.IsActive(r.pl.ID))
	is.NoErr(r.ls.Stop(ctx, r.pl.ID, false)) // admitted in backoff
	close(releaseRestart)
	waitClosed(t, first.t.Dead(), "the stopped run to finish")
	is.True(!r.ls.IsActive(r.pl.ID))
	is.Equal(len(r.runs()), 1) // no restart after the stop
}
