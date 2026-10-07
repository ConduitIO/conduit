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

// Regression tests for #2901: a stop request (user Stop or StopAll) must never
// be followed by a recovery restart, and Wait must not return while any run
// is live. Every wait below is released by a channel or a tomb the code under
// test closes; the timers are failure guards only.

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// drainFailNode runs until it is stopped and then fails with err instead of
// returning the stop reason: a node whose drain hits a transient error, e.g. a
// destination write failing for a batch that was in flight when Stop arrived.
type drainFailNode struct {
	id   string
	err  error
	stop chan struct{}
	once sync.Once
}

func newDrainFailNode(err error) *drainFailNode {
	return &drainFailNode{id: "node-" + uuid.NewString(), err: err, stop: make(chan struct{})}
}

func (n *drainFailNode) ID() string { return n.id }

func (n *drainFailNode) Run(ctx context.Context) error {
	select {
	case <-n.stop:
		return n.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *drainFailNode) Stop(context.Context, error) error {
	n.once.Do(func() { close(n.stop) })
	return nil
}

var _ stream.StoppableNode = (*drainFailNode)(nil)

// logSignal is a log writer that closes seen the first time a line containing
// msg is written. It never blocks.
type logSignal struct {
	msg  []byte
	seen chan struct{}
	once *sync.Once
}

func newLogSignal(msg string) logSignal {
	return logSignal{msg: []byte(`"message":"` + msg + `"`), seen: make(chan struct{}), once: &sync.Once{}}
}

func (w logSignal) Write(p []byte) (int, error) {
	if bytes.Contains(p, w.msg) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(terminalStatusGuard):
		t.Fatalf("timed out after %s waiting for %s", terminalStatusGuard, what)
	}
}

// assertStoppedWithError checks the outcome every stopped-with-an-error run
// must have (ADR 20261007-stop-requested-never-recovers): the given status
// sequence, the error recorded on the pipeline and returned by WaitPipeline,
// and no OnFailure event (a stop is not a failure, so exit-on-degraded does
// not trip).
func assertStoppedWithError(t *testing.T, tr *terminalRun, want []pipeline.Status, wantErr error) {
	t.Helper()
	is := is.New(t)
	got := tr.statuses()
	for _, s := range got[:len(got)-1] {
		if s == pipeline.StatusRecovering && want[len(want)-2] != pipeline.StatusRecovering {
			t.Fatalf("run entered recovery after a stop was requested (statuses %v): it would restart a stopped pipeline (#2901)", got)
		}
	}
	is.Equal(want, got)
	// Recorded, not dropped. The stored message is %+v (with frames), so
	// look for the innermost cause's text.
	root := wantErr
	for u := cerrors.Unwrap(root); u != nil; u = cerrors.Unwrap(root) {
		root = u
	}
	is.True(strings.Contains(tr.pl.Error, root.Error()))
	is.True(cerrors.Is(tr.ls.WaitPipeline(tr.pl.ID), wantErr))
	is.Equal(len(tr.failureEvents()), 0) // not a failure
	_, live := tr.ls.runningPipelines.Get(tr.pl.ID)
	is.True(!live)
}

// TestServiceLifecycle_StopWithDrainError_DoesNotRecover: a stop is requested,
// and while the run drains a node fails with a transient error. Before the
// fix, cleanup classified the run by the tomb's error alone and sent it into
// recovery, which restarts the pipeline the user (or the shutdown) just
// stopped. Now the run ends UserStopped (user Stop) or SystemStopped
// (StopAll) with the drain error recorded, and recovery is never entered.
//
// noRetryRecoveryCfg makes the pre-fix path end deterministically (Recovering,
// then Degraded on the first attempt) instead of actually restarting, so the
// failure shows up as a Recovering status and a non-zero attempt count.
func TestServiceLifecycle_StopWithDrainError_DoesNotRecover(t *testing.T) {
	drainErr := cerrors.New("destination write failed during drain")
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
			tr := newTerminalRun(t, noRetryRecoveryCfg(), false, newDrainFailNode(drainErr))
			tr.start(t)

			is.NoErr(tc.stop(context.Background(), tr.ls, tr.pl.ID))
			tr.waitDead(t)

			assertStoppedWithError(t, tr, []pipeline.Status{pipeline.StatusRunning, tc.want}, drainErr)
			is.Equal(tr.rp.recoveryAttempts.Load(), int64(0)) // StartWithBackoff never ran
		})
	}
}

// TestServiceLifecycle_StopAllWithError_DoesNotRecover is the non-graceful
// shutdown path: the runtime's tomb died with an error, so registerCleanupV1
// calls StopAll with that error as the reason, and the source returns the
// reason as its own error. Before the fix that error (not fatal) sent every
// pipeline into recovery during shutdown, and a fatal one made every pipeline
// Degraded. Either way the pipeline is now SystemStopped with the reason
// recorded, so it starts again on the next boot.
func TestServiceLifecycle_StopAllWithError_DoesNotRecover(t *testing.T) {
	testCases := []struct {
		name   string
		reason error
	}{{
		name:   "non-fatal reason",
		reason: cerrors.New("conduit experienced an error: api server stopped"),
	}, {
		name:   "fatal reason (exit-on-degraded after another pipeline's fatal error)",
		reason: cerrors.Errorf("conduit experienced an error: %w", cerrors.FatalError(cerrors.New("other pipeline: fatal connector error"))),
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			tr := newTerminalRun(t, noRetryRecoveryCfg(), false, newScriptedNode(nil, nil))
			tr.start(t)

			tr.ls.StopAll(context.Background(), tc.reason)
			tr.waitDead(t)

			assertStoppedWithError(t, tr, []pipeline.Status{pipeline.StatusRunning, pipeline.StatusSystemStopped}, tc.reason)
			is.Equal(tr.rp.recoveryAttempts.Load(), int64(0))
		})
	}
}

// heldNode is a node without a Stop method whose Run returns nil only once
// release is closed. It keeps a run's nodesWg above zero, and so holds the
// cleanup goroutine off, for as long as a test needs.
type heldNode struct {
	id      string
	release chan struct{}
}

func (n *heldNode) ID() string { return n.id }

func (n *heldNode) Run(context.Context) error {
	<-n.release
	return nil
}

// TestServiceLifecycle_FatalErrorBeforeStop_StaysDegraded: a run that failed
// fatally on its own is degraded even if a stop arrives before its cleanup
// classifies it. This is the pipeline whose failure triggers exit-on-degraded:
// it genuinely failed, it was not stopped. A held node keeps the cleanup
// goroutine waiting until StopAll has been called, so the ordering is
// deterministic.
func TestServiceLifecycle_FatalErrorBeforeStop_StaysDegraded(t *testing.T) {
	is := is.New(t)
	fatalErr := cerrors.FatalError(cerrors.New("fatal connector error"))
	held := &heldNode{id: "held-" + uuid.NewString(), release: make(chan struct{})}
	tr := newTerminalRun(t, testErrRecoveryCfg(), false, newScriptedNode(fatalErr, nil), held)
	tr.start(t)

	waitClosed(t, tr.rp.t.Dying(), "the fatal error to reach the tomb")
	tr.ls.StopAll(context.Background(), pipeline.ErrGracefulShutdown)
	close(held.release)
	tr.waitDead(t)

	is.Equal([]pipeline.Status{pipeline.StatusRunning, pipeline.StatusDegraded}, tr.statuses())
	is.True(strings.Contains(tr.pl.Error, "fatal connector error"))
	events := tr.failureEvents()
	is.Equal(len(events), 1) // a real failure: OnFailure (and exit-on-degraded) fires
	is.True(cerrors.Is(events[0].Error, fatalErr))
}

// TestServiceLifecycle_StopDuringRecoveryBackoff_AbandonsRestart: the run
// failed on its own with a transient error and recovery is waiting out its
// backoff when a stop arrives. The stop must end the wait and the run must
// not be restarted. The backoff is 10 minutes, so before the fix the run
// cannot finish within the test's guard: the wait was not interruptible, and
// after it Start would have restarted the pipeline. The run ends stopped
// (user or system) with the error it failed with recorded.
func TestServiceLifecycle_StopDuringRecoveryBackoff_AbandonsRestart(t *testing.T) {
	transientErr := cerrors.New("lost connection")
	testCases := []struct {
		name string
		stop func(ctx context.Context, ls *Service, id string)
		want pipeline.Status
	}{{
		name: "user graceful Stop",
		// Stop resolves the dead pre-recovery run; its error from the
		// already-finished node is not what this test is about.
		stop: func(ctx context.Context, ls *Service, id string) { _ = ls.Stop(ctx, id, false) },
		want: pipeline.StatusUserStopped,
	}, {
		name: "user force Stop",
		stop: func(ctx context.Context, ls *Service, id string) { _ = ls.Stop(ctx, id, true) },
		want: pipeline.StatusUserStopped,
	}, {
		name: "StopAll",
		stop: func(ctx context.Context, ls *Service, _ string) { ls.StopAll(ctx, pipeline.ErrGracefulShutdown) },
		want: pipeline.StatusSystemStopped,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			cfg := testErrRecoveryCfg()
			cfg.MinDelay = 10 * time.Minute
			cfg.MaxDelay = 10 * time.Minute
			tr := newTerminalRun(t, cfg, false, newScriptedNode(transientErr, nil))
			backoffStarted := newLogSignal("restarting with backoff")
			tr.ls.logger = log.New(zerolog.New(backoffStarted))
			tr.start(t)

			waitClosed(t, backoffStarted.seen, "recovery to start its backoff wait")
			tc.stop(context.Background(), tr.ls, tr.pl.ID)
			tr.waitDead(t)

			assertStoppedWithError(t, tr, []pipeline.Status{pipeline.StatusRunning, pipeline.StatusRecovering, tc.want}, transientErr)
			is.Equal(tr.rp.recoveryAttempts.Load(), int64(1))
		})
	}
}

// closeTrackingDB counts writes that reach the store after Close. Close only
// records the call; the inner store stays usable so a late write is counted
// instead of panicking somewhere unrelated.
type closeTrackingDB struct {
	database.DB
	closed           atomic.Bool
	writesAfterClose atomic.Int32
}

func (d *closeTrackingDB) Close() error {
	d.closed.Store(true)
	return nil
}

func (d *closeTrackingDB) Set(ctx context.Context, key string, value []byte) error {
	if d.closed.Load() {
		d.writesAfterClose.Add(1)
	}
	return d.DB.Set(ctx, key, value)
}

func (d *closeTrackingDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	if update && d.closed.Load() {
		d.writesAfterClose.Add(1)
	}
	return d.DB.NewTransaction(ctx, update)
}

// countingSource is a source connector whose plugin can be dispensed any number
// of times; dispensed counts how often. Each dispensed plugin produces no
// records and supports a graceful stop.
func countingSource(ctrl *gomock.Controller, persister *connector.Persister) (*connector.Instance, *pmock.Dispenser, *atomic.Int32) {
	var dispensed atomic.Int32
	source := dummySource(persister)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		dispensed.Add(1)
		return pmock.NewConfigurableSourcePlugin(ctrl,
			pmock.SourcePluginWithConfigure(),
			pmock.SourcePluginWithOpen(),
			pmock.SourcePluginWithRun(),
			pmock.SourcePluginWithRecords(nil, nil),
			pmock.SourcePluginWithAcks(0, true),
			pmock.SourcePluginWithStop(),
			pmock.SourcePluginWithTeardown(),
		), nil
	}).AnyTimes()
	return source, dispenser, &dispensed
}

// countingDestination is countingSource's destination counterpart.
func countingDestination(ctrl *gomock.Controller, persister *connector.Persister) (*connector.Instance, *pmock.Dispenser) {
	dest := dummyDestination(persister)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().DoAndReturn(func() (connectorPlugin.DestinationPlugin, error) {
		return pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(nil),
			pmock.DestinationPluginWithStop(nil),
			pmock.DestinationPluginWithTeardown(),
		), nil
	}).AnyTimes()
	return dest, dispenser
}

// TestServiceLifecycle_Shutdown_NoRunOutlivesWait replays
// pkg/conduit.Runtime.registerCleanupV1 for a runtime that died with an error:
// StopAll(err), Wait, then the persister flush and the database close.
//
// Before the fix the pipeline went into recovery, StartWithBackoff restarted
// it inside the old run's cleanup goroutine, and Wait returned as soon as the
// OLD tomb died, with the restarted run live. The runtime would then either
// hang in persister.Wait (the restarted connectors keep it waiting) or close
// the database under a live run (invariant 7). The test checks each step: one
// dispense only, no live run when Wait returns, persister.Wait returns, and
// nothing is written after the close.
func TestServiceLifecycle_Shutdown_NoRunOutlivesWait(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.Nop())
	db := &closeTrackingDB{DB: &inmemory.DB{}}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	source, srcDispenser, srcDispensed := countingSource(ctrl, persister)
	destination, destDispenser := countingDestination(ctrl, persister)
	dlq, dlqDispenser := countingDestination(ctrl, persister)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// A 1ms backoff keeps the pre-fix restart quick; it has no effect with
	// the fix, which never reaches the backoff.
	cfg := testErrRecoveryCfg()
	cfg.MinDelay = time.Millisecond
	cfg.MaxDelay = time.Millisecond

	ls := NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps,
	)
	is.NoErr(ls.Start(ctx, pl.ID))

	// stopLive force-stops whatever is still running, so a failing run of
	// this test does not leave connectors behind for the rest of the package.
	stopLive := func() {
		for _, rp := range ls.runningPipelines.All() {
			if rp.t == nil || !rp.t.Alive() {
				continue
			}
			_ = ls.Stop(ctx, rp.pipeline.ID, true)
			select {
			case <-rp.t.Dead():
			case <-time.After(terminalStatusGuard):
				t.Errorf("run of pipeline %s did not stop after a force stop", rp.pipeline.ID)
			}
		}
	}
	t.Cleanup(stopLive)

	shutdownErr := cerrors.New("conduit experienced an error: shut down due to 'exit-on-degraded' error")
	ls.StopAll(ctx, shutdownErr)
	if err := ls.Wait(terminalStatusGuard); err == context.DeadlineExceeded {
		t.Fatalf("Wait did not return within %s", terminalStatusGuard)
	}

	for _, rp := range ls.runningPipelines.All() {
		if rp.t != nil && rp.t.Alive() {
			t.Errorf("a run of pipeline %s is live after Wait returned: recovery restarted it during shutdown (#2901)", rp.pipeline.ID)
		}
	}
	if n := srcDispensed.Load(); n != 1 {
		t.Errorf("source plugin dispensed %d times, want 1: the pipeline was restarted during shutdown (#2901)", n)
	}

	// The runtime's next two steps.
	persisterDone := make(chan struct{})
	go func() {
		defer close(persisterDone)
		persister.Wait()
	}()
	select {
	case <-persisterDone:
	case <-time.After(terminalStatusGuard):
		t.Errorf("persister.Wait did not return within %s: a connector is still running after Wait (#2901)", terminalStatusGuard)
	}
	is.NoErr(db.Close())

	// Whatever is still live would keep writing (positions, statuses) after
	// the close. Stop it here, inside the test, so those writes are counted.
	stopLive()
	if n := db.writesAfterClose.Load(); n != 0 {
		t.Errorf("%d write(s) reached the database after it was closed (invariant 7, #2901)", n)
	}

	// Stopped by the shutdown, not failed: SystemStopped with the reason
	// recorded, and not reported to OnFailure.
	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
	is.True(strings.Contains(pl.Error, shutdownErr.Error()))
	is.True(cerrors.Is(ls.WaitPipeline(pl.ID), shutdownErr))

	// Next boot: a fresh pipeline service loads the stored instance, which
	// kept its status and error, and lifecycle Init starts it again, as it
	// does for every SystemStopped pipeline.
	ps2 := pipeline.NewService(logger, db.DB)
	is.NoErr(ps2.Init(ctx))
	pl2, err := ps2.Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(pipeline.StatusSystemStopped, pl2.GetStatus())
	is.True(strings.Contains(pl2.Error, shutdownErr.Error()))

	ls2 := NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		ps2,
	)
	is.NoErr(ls2.Init(ctx))
	is.Equal(pipeline.StatusRunning, pl2.GetStatus())
	is.Equal(pl2.Error, "") // a new run clears the previous run's error
	// Stop waits for the source to be running, i.e. for the plugin to have
	// been dispensed and opened, so the count is final afterwards.
	is.NoErr(ls2.Stop(ctx, pl.ID, false))
	is.NoErr(ls2.WaitPipeline(pl.ID))
	is.Equal(srcDispensed.Load(), int32(2)) // started again on the next boot
}

// injectSourceError makes the live run's source nodes stop with err, as if the
// source had failed on its own. It calls the nodes' Stop directly, bypassing
// Service.Stop and StopAll, so no stop is recorded for the run and the error
// takes the ordinary recovery path. Tests that need "a transient error, no
// stop requested" use this; StopAll(err) no longer leads to recovery (#2901).
func injectSourceError(ctx context.Context, t *testing.T, ls *Service, pipelineID string, err error) {
	t.Helper()
	rp, ok := ls.runningPipelines.Get(pipelineID)
	if !ok {
		t.Fatalf("pipeline %s is not running", pipelineID)
	}
	for _, n := range rp.n {
		if sn, ok := n.(stream.StoppableNode); ok {
			if stopErr := sn.Stop(ctx, err); stopErr != nil {
				t.Fatalf("could not inject error into node %s: %v", n.ID(), stopErr)
			}
		}
	}
}

// TestServiceLifecycle_TransientErrorWithoutStop_StillRestarts is the other
// side of #2901: a transient error with no stop requested must still recover,
// and the restarted run must be the live one.
func TestServiceLifecycle_TransientErrorWithoutStop_StillRestarts(t *testing.T) {
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
	source, srcDispenser := asserterSource(ctrl, persister, generateRecords(0), nil, true, 2)
	destination, destDispenser := asserterDestination(ctrl, persister, nil, 2)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 2)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	restarted := make(chan struct{})
	rec := newStatusRecorder(ps)
	rec.onUpdate = func(status pipeline.Status, nth int) error {
		if status == pipeline.StatusRunning && nth == 2 {
			close(restarted)
		}
		return nil
	}

	cfg := testErrRecoveryCfg()
	cfg.MinDelay = time.Millisecond
	cfg.MaxDelay = time.Millisecond
	ls := NewService(logger, cfg,
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: srcDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		rec,
	)
	is.NoErr(ls.Start(ctx, pl.ID))
	first, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)

	injectSourceError(ctx, t, ls, pl.ID, cerrors.New("lost connection to source"))
	waitClosed(t, restarted, "the recovery restart to announce StatusRunning")
	// The old run's cleanup goroutine returns once the restart is up.
	waitClosed(t, first.t.Dead(), "the pre-recovery run's tomb to die")

	second, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)
	is.True(second != first)
	is.True(second.t.Alive())

	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}
