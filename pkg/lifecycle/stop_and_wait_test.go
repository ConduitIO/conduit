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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// delayingDB wraps a database.DB and adds a fixed delay before every
// NewTransaction call completes, simulating a slow store. It exists purely so
// tests can deterministically observe whether a caller actually waited for a
// persister flush to land, rather than merely getting lucky with in-memory
// timing: connector.Persister.flushNow commits its batch via NewTransaction
// (see persister.go), so delaying it here makes "StopAndWait returned before
// the flush committed" a hard, always-reproducing failure instead of an
// occasional flake.
type delayingDB struct {
	database.DB
	delay time.Duration
}

func (d *delayingDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	time.Sleep(d.delay)
	return d.DB.NewTransaction(ctx, update)
}

// TestServiceLifecycle_StopAndWait_DrainsAndPersists is the regression test
// for the Tier-1 rework in
// docs/design-documents/20260708-live-server-deploy-apply.md ("Review
// outcome & required rework", blocker 1): Stop(ctx, id, false) alone only
// injects a stop-control-message into the source node and returns — it does
// not wait for the pipeline to drain, let alone for the resulting position
// write to be durably flushed. StopAndWait must block on both, and this test
// proves it does by reading the connector's position directly back out of
// the store (not the in-memory Instance, which would reflect the write
// whether or not it ever reached the store) immediately after StopAndWait
// returns.
//
// The persister's flush is made artificially slow via delayingDB, and the
// bundle-count/delay thresholds are set so high that the *only* flush that
// happens during the test is the one ConnectorStopped triggers during
// teardown — so a StopAndWait that returned before that flush landed would
// deterministically read a stale (pre-flush, "key not found") result here,
// not just flake occasionally.
func TestServiceLifecycle_StopAndWait_DrainsAndPersists(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)

	const flushDelay = 200 * time.Millisecond
	db := &delayingDB{DB: &inmemory.DB{}, delay: flushDelay}
	persister := connector.NewPersister(logger, db, time.Hour, 10000)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(5)
	source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, true, 1)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 1)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
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
		}, ps)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Let all 5 records flow end to end (source -> destination -> ack back to
	// source) before stopping. asserterSource/asserterDestination assert an
	// exact record match on teardown, so this synchronization is required for
	// the test itself to pass, independent of what we're proving here.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	err = ls.StopAndWait(ctx, pl.ID)
	is.NoErr(err)
	elapsed := time.Since(start)

	// StopAndWait must have actually blocked until the delayed flush
	// committed, not returned as soon as the drain finished.
	is.True(elapsed >= flushDelay)

	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())

	// The durability assertion: read the source's position directly from the
	// store. If StopAndWait returned before WaitPersisted's flush actually
	// landed, this read (immediately following StopAndWait with no
	// intervening sleep) would race the still-in-flight write.
	store := connector.NewStore(db, logger)
	got, err := store.Get(ctx, source.ID)
	is.NoErr(err)
	wantState := connector.SourceState{Position: wantRecords[len(wantRecords)-1].Position}
	is.Equal(got.State, wantState)
}

// TestServiceLifecycle_StopAndWait_NotRunning confirms StopAndWait propagates
// Stop's error (rather than e.g. blocking forever) when the pipeline isn't
// running — the same precondition Stop itself enforces.
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
	)

	err := ls.StopAndWait(ctx, uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, pipeline.ErrPipelineNotRunning)) // sentinel still in the chain
}
