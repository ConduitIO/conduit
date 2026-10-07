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

// Shutdown tests for arch-v2 (#2901). v2 already keeps a stopped run out of
// recovery (intentionalStop, isGracefulShutdown); these cover what it did not:
// a run that starts while StopAll is iterating, a recovery backoff that holds
// up the shutdown, and Start after StopAll. Every wait is released by a
// channel or tomb the code under test closes; timers are failure guards only.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

const shutdownTestGuard = 10 * time.Second

// blockingOpenDestination holds the first Open call until release is closed,
// after closing entered. It keeps a Start inside runPipeline, admitted but
// not yet published, for as long as a test needs.
type blockingOpenDestination struct {
	connectorPlugin.DestinationPlugin
	entered chan struct{}
	release chan struct{}
}

func (d *blockingOpenDestination) Open(ctx context.Context, req pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
	close(d.entered)
	<-d.release
	return d.DestinationPlugin.Open(ctx, req)
}

// TestServiceLifecycle_StartDuringShutdown_IsStoppedAndWaitedFor: a Start that
// is already past its own shutdown check is opening its connectors while
// StopAll runs. StopAll cannot see the run (it is not published yet). Before
// the fix the run then published and kept running, and Wait, which only
// looked at a snapshot of runningPipelines, returned at once, so the runtime
// would close the database under it. Now the run stops itself when it
// publishes, and Wait does not return until it is dead.
func TestServiceLifecycle_StartDuringShutdown_IsStoppedAndWaitedFor(t *testing.T) {
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
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	blocking := &blockingOpenDestination{
		DestinationPlugin: pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(nil),
			pmock.DestinationPluginWithTeardown(),
		),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(blocking, nil)

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps, false,
	)
	var published atomic.Pointer[runnablePipeline]
	ls.testWorkersReleased = func(rp *runnablePipeline) { published.Store(rp) }

	// Failure-path cleanup: stop a run that outlived the test body.
	t.Cleanup(func() {
		if rp := published.Load(); rp != nil && rp.t.Alive() {
			_ = ls.Stop(context.Background(), pl.ID, true)
			select {
			case <-rp.t.Dead():
			case <-time.After(shutdownTestGuard):
				t.Error("run did not stop after a force stop")
			}
		}
	})

	started := make(chan error, 1)
	go func() { started <- ls.Start(ctx, pl.ID) }()
	select {
	case <-blocking.entered:
	case <-time.After(shutdownTestGuard):
		t.Fatal("Start did not reach the destination's Open")
	}

	is.NoErr(ls.StopAll(ctx, false)) // nothing published yet, so nothing to stop

	type waitResult struct {
		err          error
		deadAtReturn bool
	}
	waited := make(chan waitResult, 1)
	go func() {
		err := ls.Wait(shutdownTestGuard)
		rp := published.Load()
		waited <- waitResult{err: err, deadAtReturn: rp != nil && !rp.t.Alive()}
	}()

	close(blocking.release)
	is.NoErr(<-started)

	res := <-waited
	if !res.deadAtReturn {
		t.Fatalf("Wait returned (err %v) while a run started during shutdown was not yet dead (#2901)", res.err)
	}
	is.NoErr(res.err)
	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
}

// TestServiceLifecycle_Recovery_GracefulShutdownDuringLongBackoff: a shutdown
// that begins while recovery is waiting out a long backoff must end the wait
// at once. Before the fix the wait was not interruptible: StartWithBackoff
// checked for the shutdown only after sleeping, so the runtime's Wait timed
// out first (exitTimeout is 30s, MaxDelay defaults to 10m) and the runtime
// closed the database with the cleanup goroutine still to write a status.
//
// The "shutdown wakes the wait first" case holds the window between StopAll
// waking the backoff wait and setting the service's graceful-shutdown flag:
// StopAll used to wake the wait first, so the abandoned run could read the
// flag still unset and report UserStopped, which is not auto-started on the
// next boot (1 in 50 under -race). Only the wake-up is triggered here.
func TestServiceLifecycle_Recovery_GracefulShutdownDuringLongBackoff(t *testing.T) {
	testCases := []struct {
		name     string
		shutdown func(ctx context.Context, ls *Service) error
	}{{
		name:     "StopAll",
		shutdown: func(ctx context.Context, ls *Service) error { return ls.StopAll(ctx, false) },
	}, {
		name: "shutdown wakes the wait first",
		shutdown: func(_ context.Context, ls *Service) error {
			ls.runs.beginShutdown()
			return nil
		},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testGracefulShutdownDuringLongBackoff(t, tc.shutdown)
		})
	}
}

func testGracefulShutdownDuringLongBackoff(t *testing.T, shutdown func(ctx context.Context, ls *Service) error) {
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
	transientErr := cerrors.New("lost connection to source")
	source, srcDispenser := failingSourceTimes(ctrl, persister, transientErr, 1)
	destination, destDispenser := destinationTimes(ctrl, persister, 1)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 1)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	cfg := testErrRecoveryCfg()
	cfg.MinDelay = 10 * time.Minute
	cfg.MaxDelay = 10 * time.Minute

	recovering := make(chan struct{})
	rec := newStatusRecorder(ps)
	rec.onUpdate = func(status pipeline.Status, nth int) {
		if status == pipeline.StatusRecovering && nth == 1 {
			close(recovering)
		}
	}
	ls := NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		rec, false,
	)
	is.NoErr(ls.Start(ctx, pl.ID))

	select {
	case <-recovering:
	case <-time.After(shutdownTestGuard):
		t.Fatal("pipeline did not enter recovery")
	}
	is.NoErr(shutdown(ctx, ls))

	if err := ls.Wait(shutdownTestGuard); err == context.DeadlineExceeded {
		t.Fatalf("Wait timed out: a 10m recovery backoff held up the shutdown (#2901)")
	}
	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
	// The error the run failed with is kept, not dropped (#2901).
	is.True(strings.Contains(pl.Error, "lost connection to source"))
	is.True(cerrors.Is(ls.WaitPipeline(pl.ID), transientErr))
}

func TestServiceLifecycle_StartRefusedAfterStopAll(t *testing.T) {
	is := is.New(t)
	ls := NewService(log.Nop(), testErrRecoveryCfg(),
		testConnectorService{}, testProcessorService{}, testConnectorPluginService{},
		newStatusRecorder(testPipelineService{}), false,
	)
	is.NoErr(ls.StopAll(context.Background(), false))

	err := ls.Start(context.Background(), uuid.NewString())
	is.True(cerrors.Is(err, pipeline.ErrShuttingDown))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodeShuttingDown)

	// runPipeline is the authoritative check and refuses before opening
	// anything (rp.sink is nil here: touching it would panic).
	rp := &runnablePipeline{pipeline: &pipeline.Instance{ID: uuid.NewString()}}
	err = ls.runPipeline(rp)
	is.True(cerrors.Is(err, pipeline.ErrShuttingDown))
	is.True(rp.t == nil)

	ls.runs.wait() // nothing was admitted
}

// TestServiceLifecycle_Recovery_UserStopDuringBackoff_NoRestart: a user Stop
// on a pipeline whose run failed transiently and is waiting out its recovery
// backoff. Stop resolves the dead run, marks it intentionalStop and returns
// success. Before the fix StartWithBackoff never looked at that flag: after
// the backoff it restarted the pipeline the user had stopped (a second
// dispense, which the Times(1) expectations reject at controller finish).
//
// The Stop is issued from inside the Recovering status write, which happens
// before StartWithBackoff is entered, so it is ordered before the post-wait
// check without any sleep.
func TestServiceLifecycle_Recovery_UserStopDuringBackoff_NoRestart(t *testing.T) {
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
	source, srcDispenser := failingSourceTimes(ctrl, persister, cerrors.New("lost connection to source"), 1)
	destination, destDispenser := destinationTimes(ctrl, persister, 1)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 1)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	cfg := testErrRecoveryCfg()
	cfg.MinDelay = time.Millisecond
	cfg.MaxDelay = time.Millisecond

	var ls *Service
	stopErr := make(chan error, 1)
	rec := newStatusRecorder(ps)
	rec.onUpdate = func(status pipeline.Status, nth int) {
		if status == pipeline.StatusRecovering && nth == 1 {
			stopErr <- ls.Stop(context.Background(), pl.ID, false)
		}
	}
	ls = NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		rec, false,
	)
	first := make(chan *runnablePipeline, 1)
	ls.testWorkersReleased = func(rp *runnablePipeline) {
		select {
		case first <- rp:
		default:
		}
	}
	is.NoErr(ls.Start(ctx, pl.ID))
	rp := <-first

	select {
	case <-rp.t.Dead():
	case <-time.After(shutdownTestGuard):
		t.Fatal("the failed run's cleanup did not finish")
	}
	is.NoErr(<-stopErr)

	if got := pl.GetStatus(); got != pipeline.StatusUserStopped {
		t.Fatalf("status %s after a user Stop during recovery backoff, want %s: the pipeline was restarted (#2901)", got, pipeline.StatusUserStopped)
	}
	is.True(strings.Contains(pl.Error, "lost connection to source")) // kept, not dropped
	_, live := ls.runningPipelines.Get(pl.ID)
	is.True(!live)
}
