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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// delayingDB wraps a database.DB and adds a fixed delay before every
// NewTransaction call completes, simulating a slow store. See the identical
// helper in pkg/lifecycle/stop_and_wait_test.go for the full rationale: it
// makes "StopAndWait returned before the flush committed" a hard,
// always-reproducing failure instead of an occasional flake.
type delayingDB struct {
	database.DB
	delay time.Duration
}

func (d *delayingDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	time.Sleep(d.delay)
	return d.DB.NewTransaction(ctx, update)
}

// TestServiceLifecycle_StopAndWait_DrainsAndPersists is the arch-v2 port of
// pkg/lifecycle's identical-named test: proves StopAndWait blocks until BOTH
// the drain (WaitPipeline) AND the resulting position write are durably
// flushed (WaitPersisted), not just the first. See the design doc's §3.1
// funnel drain audit for why this package's Stop/WaitPipeline/Persister
// interaction gives the same guarantee pkg/lifecycle's does.
func TestServiceLifecycle_StopAndWait_DrainsAndPersists(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)

	const flushDelay = 200 * time.Millisecond
	db := &delayingDB{DB: &inmemory.DB{}, delay: flushDelay}
	persister := connector.NewPersister(logger, db, time.Hour, 10000)
	defer persister.Wait()

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(5)
	source, srcDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorServiceWithPersister{
			testConnectorService: testConnectorService{
				source.ID:      source,
				destination.ID: destination,
				testDLQID:      dlq,
			},
			persister: persister,
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

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Let all 5 records flow end to end before stopping.
	waitForRecordsAcked(t, source, wantRecords)

	start := time.Now()
	err = ls.StopAndWait(ctx, pl.ID)
	is.NoErr(err)
	elapsed := time.Since(start)

	// StopAndWait must have actually blocked until the delayed flush
	// committed, not returned as soon as the drain finished.
	is.True(elapsed >= flushDelay)

	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())

	// The durability assertion: read the source's position directly from the
	// store, immediately after StopAndWait returns with no intervening sleep.
	store := connector.NewStore(db, logger)
	got, err := store.Get(ctx, source.ID)
	is.NoErr(err)
	wantState := connector.SourceState{Position: wantRecords[len(wantRecords)-1].Position}
	is.Equal(got.State, wantState)
}

// TestServiceLifecycle_StopAndWait_NotRunning confirms StopAndWait propagates
// Stop's error (rather than e.g. blocking forever) when the pipeline isn't
// running — the same precondition Stop itself enforces. Mirrors
// pkg/lifecycle's identical test.
func TestServiceLifecycle_StopAndWait_NotRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.Nop())

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{},
		testProcessorService{},
		testConnectorPluginService{},
		testPipelineService{},
		false,
	)

	err := ls.StopAndWait(ctx, uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, pipeline.ErrPipelineNotRunning)) // sentinel still in the chain
}

// TestServiceLifecycle_StopAndWait_Timeout is the O2 regression test
// (docs/design-documents/20260731-archv2-drain-reconfigure.md, "O2: bounding
// the drain"): a destination that never returns from the ack round-trip for
// its one in-flight batch holds funnel.Worker's processingLock forever, which
// would hang Stop (and thus StopAndWait) indefinitely without an explicit
// bound. A tight caller-supplied ctx deadline (shorter than
// DefaultStopAndWaitTimeout) proves the bound actually fires: StopAndWait
// returns promptly with a CodeStopAndWaitTimeout error, and — critically —
// nothing was force-killed: the pipeline is left exactly StatusRunning, still
// genuinely working through the (still wedged, from this call's point of
// view) batch, never torn down mid-write.
//
// The test then releases the wedge (unrelated to the O2 bound itself, just
// test cleanup) so the batch's now-real transient error unwinds the pipeline
// to a terminal state and every mocked Teardown expectation is satisfied
// before the test returns.
func TestServiceLifecycle_StopAndWait_Timeout(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer persister.Wait()

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
		pmock.SourcePluginWithAcks(0, false), // never acked - the wedge means the batch never completes
		pmock.SourcePluginWithTeardown(),
	)
	source := dummySource(persister)
	sourceDispenser := pmock.NewDispenser(ctrl)
	sourceDispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil).Times(1)

	received := make(chan struct{})
	release := make(chan struct{})
	wedgeErr := cerrors.New("simulated wedged destination write")
	destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledError(wantRecords, received, release, wedgeErr),
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

	// MaxRetries=0: once the wedge is released below and the batch's now-real
	// transient error surfaces, recovery must exhaust on the very first
	// attempt (fatal, degrade) rather than redispensing the source/destination
	// a second time — keeping the Times(1) expectations above valid.
	cfg := testErrRecoveryCfg()
	cfg.MaxRetries = 0

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
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	<-received // the record is in flight at the destination, processingLock held indefinitely

	stopCtx, stopCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stopCancel()

	start := time.Now()
	err = ls.StopAndWait(stopCtx, pl.ID)
	elapsed := time.Since(start)

	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeStopAndWaitTimeout.Reason())
	is.True(ce.Suggestion != "")
	// Bounded: returned promptly, not after actually waiting out the wedge.
	is.True(elapsed < 5*time.Second)

	// The core O2 assertion: nothing was force-killed. The pipeline is left
	// exactly as it was — still genuinely running the (from this call's view,
	// still wedged) batch — never torn down mid-write.
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	// Cleanup (unrelated to the O2 assertion): release the wedge so the batch
	// unwinds with a real transient error, which — since Stop's own attempt
	// above failed via ctx timeout, never actually setting the worker's stop
	// flag — is treated as an ordinary (not intentional-stop) failure and
	// exhausts recovery immediately (MaxRetries=0), degrading the pipeline.
	// This lets every mocked Teardown expectation resolve before the test
	// returns, instead of leaking a goroutine wedged forever.
	close(release)
	is.True(ls.WaitPipeline(pl.ID) != nil)
	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
}

// TestServiceLifecycle_ReconfigureProcessor_FallsBackToRestart is the O1
// regression test: under Preview.PipelineArchV2, ReconfigureProcessor has no
// live in-place swap capability at all (unlike pkg/lifecycle), so it must
// always return the shared lifecyclev1.ErrProcessorNotLiveReconfigurable
// sentinel — the exact error provisioning.applyInPlace matches via cerrors.Is
// to fall back to the StopAndWait restart path. Reusing the same sentinel
// (rather than a v2-specific one) is what lets applyInPlace stay
// arch-agnostic; this test pins that the reuse actually holds.
func TestServiceLifecycle_ReconfigureProcessor_FallsBackToRestart(t *testing.T) {
	is := is.New(t)
	logger := log.New(zerolog.Nop())

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{},
		testProcessorService{},
		testConnectorPluginService{},
		testPipelineService{},
		false,
	)

	err := ls.ReconfigureProcessor(context.Background(), uuid.NewString(), uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, lifecyclev1.ErrProcessorNotLiveReconfigurable))
}
