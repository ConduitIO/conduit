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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// TestServiceLifecycle_StopAndWait_BatchingDrainTimeout_IsNotNeverArmed pins the
// R2-1 contract end to end: a batching destination wedged in its ack holds the
// flusher, so the stop drain (Worker.Stop, before source teardown) runs out of
// the StopAndWait deadline. The worker DID arm and tear its source down, so
// StopAndWait must not report the "stop" phase ("never began stopping ... still
// running ... safe to retry"). It must time out in a later phase, and the run
// must end stopped (not Running, not recovered) with the unwritten batch reported
// as pipeline.flush_on_close_failed by Close.
func TestServiceLifecycle_StopAndWait_BatchingDrainTimeout_IsNotNeverArmed(t *testing.T) {
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

	wantRecords := generateRecords(2)

	ctrl := gomock.NewController(t)
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(wantRecords, nil),
		pmock.SourcePluginWithAcks(0, false), // the batch never completes
		pmock.SourcePluginWithTeardown(),
	)
	source := dummySource(persister)
	sourceDispenser := pmock.NewDispenser(ctrl)
	sourceDispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil).Times(1)

	received := make(chan struct{})
	release := make(chan struct{})
	destPlugin := pmock.NewConfigurableDestinationPlugin(ctrl,
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithControlledError(wantRecords, received, release, cerrors.New("simulated wedged destination ack")),
		pmock.DestinationPluginWithTeardown(),
	)
	destination := dummyDestination(persister)
	// Engine-side batching: the funnel buffers both records and hands them to
	// the pipelined flusher in one Write.
	destination.Config.Settings = map[string]string{"sdk.batch.size": "2"}
	destination.LastActiveConfig = destination.Config // no lifecycle "created" event on start
	destDispenser := pmock.NewDispenser(ctrl)
	destDispenser.EXPECT().DispenseDestination().Return(destPlugin, nil).Times(1)

	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

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

	is.NoErr(ls.Start(ctx, pl.ID))
	<-received // flusher wedged in the destination ack; the worker is idle (processingLock free)

	stopCtx, stopCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stopCancel()
	start := time.Now()
	err = ls.StopAndWait(stopCtx, pl.ID)
	elapsed := time.Since(start)
	t.Logf("StopAndWait after %v: %v", elapsed, err)

	is.True(err != nil) // the drain could not finish inside the deadline
	is.True(elapsed < 5*time.Second)
	// R2-1: the worker armed and tore its source down; "never began stopping" is false.
	is.True(!strings.Contains(err.Error(), "never began stopping"))
	if ce, ok := conduiterr.Get(err); ok {
		t.Logf("code=%s", ce.Code.Reason())
	}

	// Release the wedge; the run must end stopped (a stop was requested: never
	// recovered, never Running) with the unwritten batch reported by Close.
	close(release)
	werr := ls.WaitPipeline(pl.ID)
	t.Logf("WaitPipeline: %v status=%v", werr, pl.GetStatus())
	is.True(werr != nil)
	ce, ok := conduiterr.Get(werr)
	is.True(ok)
	is.Equal(ce.Code.Reason(), "pipeline.flush_on_close_failed")
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}
