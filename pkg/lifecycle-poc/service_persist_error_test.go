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
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
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

// Regression tests for ConduitIO/conduit#2929: arch-v2 never read the
// connectors' Errors() channels, so a connector state write that failed
// (connector.Persister reports it asynchronously) never reached the pipeline.
// The ack stayed withheld (no loss, #2932), but the pipeline kept reporting
// Running, upstream retention grew, and every failed flush parked two
// goroutines until Teardown.

// failingTxDB is a database.DB whose NewTransaction fails while armed, so
// every persister flush fails with connector.state_persist_failed. Pipeline
// records live in a separate, healthy DB.
type failingTxDB struct {
	database.DB
	armed    atomic.Bool
	failures atomic.Int64
}

var errStoreDown = cerrors.New("store is down")

func (f *failingTxDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	if f.armed.Load() {
		f.failures.Add(1)
		return nil, ctx, errStoreDown
	}
	return f.DB.NewTransaction(ctx, update)
}

// TestServiceLifecycle_PersistFailure_FailsPipeline: a store that keeps
// failing must fail the arch-v2 pipeline (recovery, then Degraded once
// MaxRetries is spent) within bounded time, with no ack reaching the plugin,
// and must not leave goroutines behind. Before the fix the pipeline stayed
// Running indefinitely.
//
// The store fails only once every record is through and the source is idle
// in Read, which does not watch its context: the failure has to end the run
// from there, not only while a batch is in flight.
func TestServiceLifecycle_PersistFailure_FailsPipeline(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())

	storeDB := &failingTxDB{DB: &inmemory.DB{}}
	// No automatic flush: the test flushes once the store is armed, so no
	// position can commit (and no ack can be released) before that.
	persister := connector.NewPersister(logger, storeDB, time.Hour, 1_000_000)
	defer stopAndWaitPersister(t, killAll, persister)

	db := &inmemory.DB{}
	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	records := generateRecords(5)
	ctrl := gomock.NewController(t)

	source := dummySource(persister)
	srcDispenser := pmock.NewDispenser(ctrl)
	srcDispenser.EXPECT().DispenseSource().Return(pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(records, nil),
		// Invariant 1: no position was stored, so no ack may reach the plugin.
		pmock.SourcePluginWithAcks(0, true),
		pmock.SourcePluginWithTeardown(),
	), nil)
	destination, destDispenser := asserterDestination(ctrl, persister, records, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	cfg := testErrRecoveryCfg()
	cfg.MaxRetries = 0 // the first recovery attempt exhausts it: Running -> Recovering -> Degraded

	baseline := runtime.NumGoroutine()

	ls := NewService(
		logger,
		cfg,
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)
	events := make(chan FailureEvent, 1)
	ls.OnFailure(func(e FailureEvent) { events <- e })

	is.NoErr(ls.Start(ctx, pl.ID))

	// Every record acked by the pipeline (positions queued for persisting)
	// and the source back in Read with nothing left to read. Then the store
	// goes down and the pending writes are flushed.
	waitForPipelineRunning(t, pl)
	waitForRecordsAcked(t, source, records)
	storeDB.armed.Store(true)
	persister.Flush(ctx)

	// Bounded: that one failed flush must end the run.
	event, received, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 10*time.Second)
	is.NoErr(err)
	is.True(received) // a persistent store failure must fail an arch-v2 pipeline (#2929)
	is.True(cerrors.Is(event.Error, pipeline.ErrPipelineCannotRecover))
	waitForStatus(t, pl, pipeline.StatusDegraded)
	is.True(storeDB.failures.Load() > 0)

	// No persist callback is left parked on an unread errs channel: every
	// pending write resolves promptly.
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

	// Goroutines return to (about) the baseline once the run is down: the
	// failed flushes did not leave callbacks or waiters behind. A small slack
	// absorbs runtime and timer goroutines.
	waitForGoroutinesAtMost(t, baseline+5)
}

// TestServiceLifecycle_PersistFailure_TransientRecovers: a store failure that
// clears fails the run like any other transient pipeline error (as in v1):
// exactly one recovery, then the pipeline runs again, the records are
// re-read from the last stored position and their acks reach the plugin. It
// never goes Degraded.
func TestServiceLifecycle_PersistFailure_TransientRecovers(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())

	storeDB := &failingTxDB{DB: &inmemory.DB{}}
	storeDB.armed.Store(true)
	persister := connector.NewPersister(logger, storeDB, 10*time.Millisecond, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	db := &inmemory.DB{}
	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	records := generateRecords(5)
	ctrl := gomock.NewController(t)

	source := dummySource(persister)
	var srcCall atomic.Int64
	srcDispenser := pmock.NewDispenser(ctrl)
	srcDispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		wantAcks := 0 // first run: the store is down, nothing may be acked
		if srcCall.Add(1) > 1 {
			wantAcks = len(records) // recovered run: every record acked
		}
		return pmock.NewConfigurableSourcePlugin(ctrl,
			pmock.SourcePluginWithConfigure(),
			pmock.SourcePluginWithOpen(),
			pmock.SourcePluginWithRun(),
			pmock.SourcePluginWithRecords(records, nil),
			pmock.SourcePluginWithAcks(wantAcks, true),
			pmock.SourcePluginWithTeardown(),
		), nil
	}).Times(2)

	destination := dummyDestination(persister)
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().DoAndReturn(func() (connectorPlugin.DestinationPlugin, error) {
		return pmock.NewConfigurableDestinationPlugin(ctrl,
			pmock.DestinationPluginWithConfigure(),
			pmock.DestinationPluginWithOpen(),
			pmock.DestinationPluginWithRun(),
			pmock.DestinationPluginWithRecords(records),
			pmock.DestinationPluginWithTeardown(),
		), nil
	}).Times(2)
	dlq, dlqDispenser := dlqDispenserTimes(ctrl, persister, 2)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	rec := newStatusRecorder(ps)
	// The store comes back once the failed run is down and recovery starts.
	rec.onUpdate = func(status pipeline.Status, _ int) {
		if status == pipeline.StatusRecovering {
			storeDB.armed.Store(false)
		}
	}

	ls := NewService(
		logger,
		testErrRecoveryCfg(), // infinite retries, as by default
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		rec,
		false,
	)

	is.NoErr(ls.Start(ctx, pl.ID))

	waitForRecovered(t, rec, pl)
	waitForRecordsAcked(t, source, records)
	waitForStoredPosition(t, storeDB, persister, source, records[len(records)-1].Position)

	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())

	statuses := rec.snapshot()
	is.True(!slices.Contains(statuses, pipeline.StatusDegraded))
	recovering := 0
	for _, s := range statuses {
		if s == pipeline.StatusRecovering {
			recovering++
		}
	}
	is.Equal(recovering, 1) // the failed run was recovered exactly once
	is.True(storeDB.failures.Load() > 0)
}

// waitForStoredPosition blocks until the source's position in the store is
// want, i.e. the recovered run's position write committed.
func waitForStoredPosition(t *testing.T, db database.DB, persister *connector.Persister, source *connector.Instance, want []byte) {
	t.Helper()
	store := connector.NewStore(db, log.Nop())
	deadline := time.Now().Add(5 * time.Second)
	for {
		persister.Flush(context.Background())
		got, err := store.Get(context.Background(), source.ID)
		if err == nil {
			if st, ok := got.State.(connector.SourceState); ok && string(st.Position) == string(want) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("source position %q was never stored (last err: %v)", want, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForGoroutinesAtMost polls runtime.NumGoroutine until it is at most max.
func waitForGoroutinesAtMost(t *testing.T, limit int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= limit {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutines did not drain: %d running, want at most %d\n%s", n, limit, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
