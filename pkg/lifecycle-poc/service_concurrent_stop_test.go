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

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// probeCtx is a cancelable context that reports the first call to Done.
// stopRunnablePipeline records the stop request before it dispatches the
// workers' Stop calls, and nothing before that point calls Done; the first
// Done call is funnel.Worker.Stop waiting for the processing lock. So reached
// closing means "this stop's request is recorded and it is waiting for the
// lock", with no sleep and no hook in the code under test.
type probeCtx struct {
	context.Context
	reached     chan struct{}
	reachedOnce sync.Once
	done        chan struct{}
	cancelOnce  sync.Once
	mu          sync.Mutex
	err         error
}

func newProbeCtx() *probeCtx {
	return &probeCtx{Context: context.Background(), reached: make(chan struct{}), done: make(chan struct{})}
}

func (c *probeCtx) Done() <-chan struct{} {
	c.reachedOnce.Do(func() { close(c.reached) })
	return c.done
}

func (c *probeCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *probeCtx) cancel() {
	c.cancelOnce.Do(func() {
		c.mu.Lock()
		c.err = context.Canceled
		c.mu.Unlock()
		close(c.done)
	})
}

// TestServiceLifecycle_ConcurrentStops_ExpiredStopKeepsOthersRequest: two
// stops race behind an in-flight batch that holds the worker's processing
// lock. Stop A records the run's stop request first; stop C (a second user
// Stop, or StopAll) records it again. A's context then expires before it gets
// the lock, so A armed nothing and rolls back. That rollback used to clear
// the request outright, including C's. C then armed the worker, the batch
// failed with a transient error, and with no stop request left the run went
// into recovery and was restarted (a second dispense, which Times(1) below
// rejects). A must only undo its own request when no other request arrived
// after it.
func TestServiceLifecycle_ConcurrentStops_ExpiredStopKeepsOthersRequest(t *testing.T) {
	testCases := []struct {
		name       string
		second     func(ctx context.Context, ls *Service, id string) error
		wantStatus pipeline.Status
	}{{
		name:       "second user Stop",
		second:     func(ctx context.Context, ls *Service, id string) error { return ls.Stop(ctx, id, false) },
		wantStatus: pipeline.StatusUserStopped,
	}, {
		name:       "StopAll",
		second:     func(ctx context.Context, ls *Service, _ string) error { return ls.StopAll(ctx, false) },
		wantStatus: pipeline.StatusUserStopped, // A's user request came first
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

			wantRecords := generateRecords(1)
			ctrl := gomock.NewController(t)
			sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl,
				pmock.SourcePluginWithConfigure(),
				pmock.SourcePluginWithOpen(),
				pmock.SourcePluginWithRun(),
				pmock.SourcePluginWithRecords(wantRecords, nil),
				pmock.SourcePluginWithAcks(0, false),
				pmock.SourcePluginWithTeardown(),
			)
			source := dummySource(persister)
			sourceDispenser := pmock.NewDispenser(ctrl)
			sourceDispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil).Times(1)

			// The destination takes the record and holds it (and with it the
			// worker's processing lock) until release, then fails it with a
			// transient error: the drain error.
			received := make(chan struct{})
			release := make(chan struct{})
			drainErr := cerrors.New("transient destination write failure mid-drain")
			destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
				pmock.DestinationPluginWithConfigure(),
				pmock.DestinationPluginWithOpen(),
				pmock.DestinationPluginWithRun(),
				pmock.DestinationPluginWithControlledError(wantRecords, received, release, drainErr),
				pmock.DestinationPluginWithTeardown(),
			)
			destination := dummyDestination(persister)
			destDispenser := pmock.NewDispenser(ctrl)
			destDispenser.EXPECT().DispenseDestination().Return(destPlugin, nil).Times(1)
			dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
			pl.DLQ.Plugin = dlq.Plugin
			pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
			is.NoErr(err)
			pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
			is.NoErr(err)

			rec := newStatusRecorder(ps)
			ls := NewService(logger, testErrRecoveryCfg(),
				testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
				testProcessorService{},
				testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
				rec, false,
			)
			is.NoErr(ls.Start(ctx, pl.ID))
			waitFor := func(ch <-chan struct{}, what string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(shutdownTestGuard):
					t.Fatalf("timed out waiting for %s", what)
				}
			}
			waitFor(received, "the record to be in flight at the destination")

			// Stop A: records the request first, then waits for the lock.
			ctxA := newProbeCtx()
			errA := make(chan error, 1)
			go func() { errA <- ls.Stop(ctxA, pl.ID, false) }()
			waitFor(ctxA.reached, "stop A to wait for the processing lock")

			// Stop C: records the request again, then waits for the lock.
			ctxC := newProbeCtx()
			errC := make(chan error, 1)
			go func() { errC <- tc.second(ctxC, ls, pl.ID) }()
			waitFor(ctxC.reached, "stop C to wait for the processing lock")

			// A's context expires: A arms nothing and rolls back.
			ctxA.cancel()
			select {
			case err := <-errA:
				is.True(err != nil) // A failed to stop anything
			case <-time.After(shutdownTestGuard):
				t.Fatal("stop A did not return after its context expired")
			}

			// The batch fails; C gets the lock and arms the worker.
			close(release)
			select {
			case <-errC:
			case <-time.After(shutdownTestGuard):
				t.Fatal("stop C did not return")
			}

			done := make(chan error, 1)
			go func() { done <- ls.WaitPipeline(pl.ID) }()
			var waitErr error
			select {
			case waitErr = <-done:
			case <-time.After(shutdownTestGuard):
				t.Fatalf("run did not finish; statuses %v", rec.snapshot())
			}

			for _, s := range rec.snapshot() {
				if s == pipeline.StatusRecovering {
					t.Fatalf("the run entered recovery: stop A's rollback erased stop C's request, and the drain error restarted a stopped pipeline (statuses %v)", rec.snapshot())
				}
			}
			is.Equal(tc.wantStatus, pl.GetStatus())
			is.True(waitErr != nil)
			is.True(strings.Contains(pl.Error, drainErr.Error()))
		})
	}
}

// inFlightRun is a running single-source pipeline whose one record is held at
// the destination, and with it the worker's processing lock, until release is
// closed; the batch then fails with the transient drainErr. Every plugin is
// dispensed exactly once, so a recovery restart fails the test.
type inFlightRun struct {
	ls       *Service
	pl       *pipeline.Instance
	rec      *statusRecorder
	release  chan struct{}
	drainErr error
}

func newInFlightRun(t *testing.T, configure func(ls *Service)) *inFlightRun {
	t.Helper()
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	t.Cleanup(func() { stopAndWaitPersister(t, killAll, persister) })

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	wantRecords := generateRecords(1)
	ctrl := gomock.NewController(t)
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(wantRecords, nil),
		pmock.SourcePluginWithAcks(0, false),
		pmock.SourcePluginWithTeardown(),
	)
	source := dummySource(persister)
	sourceDispenser := pmock.NewDispenser(ctrl)
	sourceDispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil).Times(1)

	r := &inFlightRun{release: make(chan struct{}), drainErr: cerrors.New("transient destination write failure mid-drain")}
	received := make(chan struct{})
	destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledError(wantRecords, received, r.release, r.drainErr),
		pmock.DestinationPluginWithTeardown(),
	)
	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(destPlugin, nil).Times(1)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin
	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	r.pl = pl
	r.rec = newStatusRecorder(ps)
	r.ls = NewService(logger, testErrRecoveryCfg(),
		testConnectorService{source.ID: source, destination.ID: destination, testDLQID: dlq},
		testProcessorService{},
		testConnectorPluginService{source.Plugin: sourceDispenser, destination.Plugin: destDispenser, dlq.Plugin: dlqDispenser},
		r.rec, false,
	)
	if configure != nil {
		configure(r.ls)
	}
	is.NoErr(r.ls.Start(ctx, pl.ID))
	r.waitFor(t, received, "the record to be in flight at the destination")
	return r
}

func (r *inFlightRun) waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(shutdownTestGuard):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// stop starts a user Stop with a probeCtx and waits until it has recorded its
// request and is waiting for the processing lock.
func (r *inFlightRun) stop(t *testing.T) (*probeCtx, <-chan error) {
	t.Helper()
	ctx := newProbeCtx()
	errc := make(chan error, 1)
	go func() { errc <- r.ls.Stop(ctx, r.pl.ID, false) }()
	r.waitFor(t, ctx.reached, "the stop to wait for the processing lock")
	return ctx, errc
}

// finishUserStopped waits for the run's cleanup and asserts the outcome every
// test here expects: UserStopped with the drain error recorded, never
// Recovering.
func (r *inFlightRun) finishUserStopped(t *testing.T) {
	t.Helper()
	is := is.New(t)
	done := make(chan error, 1)
	go func() { done <- r.ls.WaitPipeline(r.pl.ID) }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(shutdownTestGuard):
		t.Fatalf("run did not finish; statuses %v", r.rec.snapshot())
	}
	for _, s := range r.rec.snapshot() {
		if s == pipeline.StatusRecovering {
			t.Fatalf("the run entered recovery although a stop was requested (statuses %v)", r.rec.snapshot())
		}
	}
	is.Equal(pipeline.StatusUserStopped, r.pl.GetStatus())
	is.True(cerrors.Is(waitErr, r.drainErr))
	is.True(strings.Contains(r.pl.Error, r.drainErr.Error()))
}

// TestServiceLifecycle_StopRolledBackAfterSnapshot_UsesSnapshot: the cleanup
// goroutine reads the run's stop request once and must classify the run on
// that read alone. A stop is recorded, the batch fails, the cleanup reads the
// request, and then (testAfterStopSnapshot) the request is rolled back, as a
// concurrent stop that armed nothing does. The cleanup used to re-read the
// live flag for the user-stop arm, find it cleared, and send a run it had
// seen as stopped into recovery (a second dispense, rejected by Times(1)).
func TestServiceLifecycle_StopRolledBackAfterSnapshot_UsesSnapshot(t *testing.T) {
	r := newInFlightRun(t, func(ls *Service) {
		ls.testAfterStopSnapshot = func(rp *runnablePipeline) {
			rp.stopMu.Lock()
			latest := rp.stopGen
			rp.stopMu.Unlock()
			rp.rollbackStopRequest(latest)
		}
	})
	_, errA := r.stop(t)

	close(r.release) // the batch fails; the stop then gets the lock and arms
	select {
	case <-errA:
	case <-time.After(shutdownTestGuard):
		t.Fatal("the stop did not return")
	}
	r.finishUserStopped(t)
}

// TestServiceLifecycle_TwoStopsBothExpire_StaysStopped pins a deliberate
// choice (ADR 20261007-stop-requested-never-recovers): when two stops are
// recorded and both give up before arming, the run stays marked stopped. The
// first stop's rollback is a no-op because a later request exists, and the
// second never rolls back because it was not the first. A transient error
// that ends the run afterwards is therefore a stopped run's error:
// UserStopped with the error recorded, no recovery.
func TestServiceLifecycle_TwoStopsBothExpire_StaysStopped(t *testing.T) {
	is := is.New(t)
	r := newInFlightRun(t, nil)
	ctxA, errA := r.stop(t)
	ctxC, errC := r.stop(t)

	ctxA.cancel()
	ctxC.cancel()
	for _, errc := range []<-chan error{errA, errC} {
		select {
		case err := <-errc:
			is.True(err != nil) // neither stop armed the worker
		case <-time.After(shutdownTestGuard):
			t.Fatal("a stop did not return after its context expired")
		}
	}

	close(r.release) // the batch fails with a transient error; nobody arms
	r.finishUserStopped(t)
}
