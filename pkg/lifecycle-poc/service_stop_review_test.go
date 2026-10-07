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

// Regression tests for the review findings on #2912 (B1, S1, S2, S3) in
// arch-v2. Each window is held open by a hook or a blocking mock, never by a
// sleep; timers are failure guards only.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// blockingTeardownDestination holds the first Teardown call until release is
// closed, after closing entered, and then returns err (after the mock's own
// Teardown). For the shared destination, Teardown runs inside the cleanup
// goroutine's sink.Close, after every worker has exited and before the run is
// classified, so holding it keeps a finished run alive and unclassified.
type blockingTeardownDestination struct {
	connectorPlugin.DestinationPlugin
	entered chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}

func (d *blockingTeardownDestination) Teardown(ctx context.Context, req pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	first := false
	d.once.Do(func() { first = true })
	if first {
		close(d.entered)
		<-d.release
	}
	resp, err := d.DestinationPlugin.Teardown(ctx, req)
	if err == nil && first {
		err = d.err
	}
	return resp, err
}

type heldRun struct {
	ls       *Service
	pl       *pipeline.Instance
	rec      *statusRecorder
	dest     *blockingTeardownDestination
	failures chan FailureEvent
}

// newHeldRun starts a single-source pipeline whose shared destination's
// Teardown blocks (see blockingTeardownDestination). sourceErr, if set, makes
// the source fail on its own with it; otherwise the source idles until
// stopped. Every plugin is dispensed exactly once: a recovery restart fails
// the test at controller finish.
func newHeldRun(t *testing.T, sourceErr, teardownErr error) *heldRun {
	t.Helper()
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	t.Cleanup(killAll)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	var source *connector.Instance
	var srcDispenser *pmock.Dispenser
	if sourceErr != nil {
		source, srcDispenser = generatorSourceFatalError(ctrl, persister, nil, sourceErr)
	} else {
		source, srcDispenser = generatorSource(ctrl, persister, nil, nil, false)
	}
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	dest := &blockingTeardownDestination{
		DestinationPlugin: pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(nil),
			pmock.DestinationPluginWithTeardown(),
		),
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     teardownErr,
	}
	t.Cleanup(func() {
		select {
		case <-dest.release:
		default:
			close(dest.release)
		}
	})
	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(dest, nil)

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	hr := &heldRun{pl: pl, rec: newStatusRecorder(ps), dest: dest, failures: make(chan FailureEvent, 4)}
	cfg := testErrRecoveryCfg()
	cfg.MinDelay = time.Millisecond
	cfg.MaxDelay = time.Millisecond
	hr.ls = NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		hr.rec, false,
	)
	hr.ls.OnFailure(func(e FailureEvent) { hr.failures <- e })
	is.NoErr(hr.ls.Start(ctx, pl.ID))
	return hr
}

func (hr *heldRun) waitTeardown(t *testing.T) {
	t.Helper()
	select {
	case <-hr.dest.entered:
	case <-time.After(shutdownTestGuard):
		t.Fatal("the shared destination's Teardown was not reached")
	}
}

// finish releases the held Teardown and waits for the run's cleanup.
func (hr *heldRun) finish(t *testing.T) error {
	t.Helper()
	close(hr.dest.release)
	done := make(chan error, 1)
	go func() { done <- hr.ls.WaitPipeline(hr.pl.ID) }()
	select {
	case err := <-done:
		return err
	case <-time.After(shutdownTestGuard):
		t.Fatalf("run did not finish; statuses %v", hr.rec.snapshot())
		return nil
	}
}

func (hr *heldRun) assertNeverRecovering(t *testing.T) {
	t.Helper()
	for _, s := range hr.rec.snapshot() {
		if s == pipeline.StatusRecovering {
			t.Fatalf("run entered recovery after a stop was requested (statuses %v)", hr.rec.snapshot())
		}
	}
}

// TestServiceLifecycle_StopAllDuringRunningAnnouncement is B1: StopAll from
// inside the run's first UpdateStatus(StatusRunning), i.e. after the run was
// published and read the (not yet set) shutdown flag, but while its entry
// still carries the previous status. StopAll used to skip it by status.
func TestServiceLifecycle_StopAllDuringRunningAnnouncement(t *testing.T) {
	testCases := []struct {
		name  string
		prior pipeline.Status
		start func(ctx context.Context, ls *Service, id string) error
	}{{
		name:  "restart after a user stop (ApplyPlanLive)",
		prior: pipeline.StatusUserStopped,
		start: func(ctx context.Context, ls *Service, id string) error { return ls.Start(ctx, id) },
	}, {
		name:  "boot Init racing SIGTERM",
		prior: pipeline.StatusSystemStopped,
		start: func(ctx context.Context, ls *Service, _ string) error { return ls.Init(ctx) },
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx, killAll := context.WithCancel(context.Background())
			defer killAll()
			logger := log.New(zerolog.Nop())
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)
			defer stopAndWaitPersister(t, killAll, persister)

			ps := pipeline.NewService(logger, db)
			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)
			ctrl := gomock.NewController(t)
			source, sourceDispenser := generatorSource(ctrl, persister, nil, nil, false)
			destination, destDispenser := asserterDestination(ctrl, persister, nil, false)
			dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
			pl.DLQ.Plugin = dlq.Plugin
			pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
			is.NoErr(err)
			pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
			is.NoErr(err)
			is.NoErr(ps.UpdateStatus(ctx, pl.ID, tc.prior, ""))

			var ls *Service
			rec := newStatusRecorder(ps)
			rec.onUpdate = func(status pipeline.Status, nth int) {
				if status == pipeline.StatusRunning && nth == 1 {
					_ = ls.StopAll(context.Background(), false)
				}
			}
			ls = NewService(logger, testErrRecoveryCfg(),
				testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
				testProcessorService{},
				testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
				rec, false,
			)
			var published atomic.Pointer[runnablePipeline]
			ls.testWorkersReleased = func(rp *runnablePipeline) { published.Store(rp) }
			t.Cleanup(func() { // failure path only: stop a run that outlived the test
				if rp := published.Load(); rp != nil && rp.t.Alive() {
					_ = ls.stopRunnablePipeline(context.Background(), rp, true, true)
				}
			})

			is.NoErr(tc.start(ctx, ls, pl.ID))
			if err := ls.Wait(shutdownTestGuard); err == context.DeadlineExceeded {
				t.Fatalf("run still live after StopAll+Wait (%v); statuses=%v (#2912 B1)", err, rec.snapshot())
			}
			rp := published.Load()
			is.True(rp != nil)
			is.True(!rp.t.Alive())
			is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
		})
	}
}

// TestServiceLifecycle_FatalErrorBeforeShutdown_StaysDegraded is S1: a run
// that failed fatally on its own and is classified after StopAll set the
// service-wide shutdown flag, but before StopAll reached the run, must be
// Degraded (and notify OnFailure), not SystemStopped. The run is held in its
// cleanup (shared destination Teardown) while the flag is set.
func TestServiceLifecycle_FatalErrorBeforeShutdown_StaysDegraded(t *testing.T) {
	is := is.New(t)
	fatalErr := cerrors.FatalError(cerrors.New("fatal source error"))
	hr := newHeldRun(t, fatalErr, nil)
	hr.waitTeardown(t)

	// What StopAll does first, before it iterates the running pipelines.
	hr.ls.runs.beginShutdown()
	hr.ls.isGracefulShutdown.Store(true)

	err := hr.finish(t)
	is.True(cerrors.Is(err, fatalErr))
	is.Equal(pipeline.StatusDegraded, hr.pl.GetStatus())
	select {
	case e := <-hr.failures:
		is.True(cerrors.Is(e.Error, fatalErr))
	default:
		t.Fatal("OnFailure not notified for a pipeline that failed on its own")
	}
}

// TestServiceLifecycle_UserStopThenShutdown_StaysUserStopped is S2: a user
// Stop, then a shutdown reaching the same run before it finished. The first
// request wins, so the pipeline is UserStopped and is not auto-started on the
// next boot.
func TestServiceLifecycle_UserStopThenShutdown_StaysUserStopped(t *testing.T) {
	is := is.New(t)
	hr := newHeldRun(t, nil, nil)
	is.NoErr(hr.ls.Stop(context.Background(), hr.pl.ID, false))
	hr.waitTeardown(t)

	_ = hr.ls.StopAll(context.Background(), false)

	is.NoErr(hr.finish(t))
	is.Equal(pipeline.StatusUserStopped, hr.pl.GetStatus())
}

// TestServiceLifecycle_SecondStop_KeepsStopRequest is S3: a second Stop on a
// run whose workers the first Stop already armed. Every worker is pre-armed,
// so this call arms nothing, and the "nothing armed" rollback used to clear
// the first Stop's request. The drain then ended with an error (the shared
// destination's Teardown fails), which went into recovery and restarted the
// pipeline the user had stopped.
func TestServiceLifecycle_SecondStop_KeepsStopRequest(t *testing.T) {
	testCases := []struct {
		name       string
		second     func(ls *Service, id string) error
		wantStatus pipeline.Status
	}{{
		name:       "second user Stop",
		second:     func(ls *Service, id string) error { return ls.Stop(context.Background(), id, false) },
		wantStatus: pipeline.StatusUserStopped,
	}, {
		name: "StopAll after a user Stop",
		second: func(ls *Service, _ string) error {
			_ = ls.StopAll(context.Background(), false)
			return nil
		},
		wantStatus: pipeline.StatusUserStopped,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			teardownErr := cerrors.New("destination teardown failed")
			hr := newHeldRun(t, nil, teardownErr)
			is.NoErr(hr.ls.Stop(context.Background(), hr.pl.ID, false))
			hr.waitTeardown(t)

			_ = tc.second(hr.ls, hr.pl.ID)

			err := hr.finish(t)
			hr.assertNeverRecovering(t)
			is.True(cerrors.Is(err, teardownErr))
			is.Equal(tc.wantStatus, hr.pl.GetStatus())
			is.True(strings.Contains(hr.pl.Error, teardownErr.Error()))
		})
	}
}
