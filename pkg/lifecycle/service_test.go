// Copyright © 2022 Meroxa, Inc.
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
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

const testDLQID = "test-dlq"

func TestServiceLifecycle_buildRunnablePipeline(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	source := dummySource(persister)
	destination := dummyDestination(persister)
	dlq := dummyDestination(persister)
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{source.ID, destination.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      pmock.NewDispenser(ctrl),
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		},
		testPipelineService{},
	)

	got, err := ls.buildRunnablePipeline(ctx, pl)

	is.NoErr(err)

	want := runnablePipeline{
		pipeline: pl,
		n: []stream.Node{
			&stream.SourceNode{},
			&stream.SourceAckerNode{},
			&stream.MetricsNode{},
			&stream.DLQHandlerNode{},
			&stream.FaninNode{},
			&stream.FanoutNode{},
			&stream.MetricsNode{},
			&stream.DestinationNode{},
			&stream.DestinationAckerNode{},
		},
	}

	is.Equal(len(want.n), len(got.n))
	for i := range want.n {
		want := want.n[i]
		got := got.n[i]
		is.Equal(reflect.TypeOf(want), reflect.TypeOf(got)) // unexpected node type

		switch got := got.(type) {
		case *stream.SourceNode:
			gotSource, ok := got.Source.(*connector.Source)
			is.True(ok)
			is.Equal(gotSource.Instance, source)
		case *stream.DestinationNode:
			gotDestination, ok := got.Destination.(*connector.Destination)
			is.True(ok)
			is.Equal(gotDestination.Instance, destination)
		case *stream.DLQHandlerNode:
			is.Equal(got.WindowSize, pl.DLQ.WindowSize)
			is.Equal(got.WindowNackThreshold, pl.DLQ.WindowNackThreshold)

			gotHandler, ok := got.Handler.(*DLQDestination)
			is.True(ok)
			gotDestination, ok := gotHandler.Destination.(*connector.Destination)
			is.True(ok)
			is.Equal(gotDestination.Instance, dlq)
		}
	}
}

func TestService_buildRunnablePipeline_NoSourceNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	destination := dummyDestination(persister)
	dlq := dummyDestination(persister)
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{destination.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
		destination.ID: destination,
		testDLQID:      dlq,
	}, testProcessorService{},
		testConnectorPluginService{
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		}, testPipelineService{})

	wantErr := "can't build pipeline without any source connectors"

	got, err := ls.buildRunnablePipeline(ctx, pl)

	is.True(err != nil)
	is.Equal(err.Error(), wantErr)
	is.Equal(got, nil)
}

func TestService_buildRunnablePipeline_NoDestinationNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	source := dummySource(persister)
	dlq := dummyDestination(persister)

	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
		source.ID: source,
		testDLQID: dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:    pmock.NewDispenser(ctrl),
		}, testPipelineService{})

	wantErr := "can't build pipeline without any destination connectors"

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{source.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	got, err := ls.buildRunnablePipeline(ctx, pl)

	is.True(err != nil)
	is.Equal(err.Error(), wantErr)
	is.Equal(got, nil)
}

func TestServiceLifecycle_PipelineSuccess(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, true, 1)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 1)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		}, ps)

	// start the pipeline now that everything is set up
	err = ls.Start(
		ctx,
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish consuming records from the source
	time.Sleep(100 * time.Millisecond)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	// stop pipeline before ending test
	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)

	is.NoErr(ls.WaitPipeline(pl.ID))
}

func TestServiceLifecycle_PipelineError(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors. The source error is fatal so the pipeline degrades
	// immediately with it, instead of entering the (infinite) recovery loop — this
	// isolates what #1659 is about: the *cause* reported for the degraded pipeline
	// must be the source's real error, not the io.EOF the acker sees when the closed
	// source stream rejects an ack.
	wantErr := cerrors.FatalError(cerrors.New("source connector error"))
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, srcDispenser := asserterSource(ctrl, persister, wantRecords, wantErr, false, 1)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 1)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		}, ps)

	events := make(chan FailureEvent, 1)
	ls.OnFailure(func(e FailureEvent) {
		events <- e
	})

	// start the pipeline now that everything is set up
	err = ls.Start(
		ctx,
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish
	err = ls.WaitPipeline(pl.ID)
	is.True(err != nil)

	is.Equal(pipeline.StatusDegraded, pl.GetStatus())
	// pipeline errors contain only string messages, so we can only compare the errors by the messages
	t.Log(pl.Error)

	// The OnFailure event is emitted as part of the degradation that WaitPipeline
	// above already observed, so it arrives ~immediately - but it's delivered on a
	// separate goroutine, so a too-tight timeout flakes under CI load (the full
	// -race suite saturating CPUs can delay the send past a sub-second window).
	// A generous bound removes the flake without weakening the assertion: the
	// event still MUST arrive.
	event, eventReceived, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 10*time.Second)
	is.NoErr(err)
	is.True(eventReceived)
	is.Equal(pl.ID, event.ID)

	// With #1659 fixed, the degraded pipeline reports the source's real error, not
	// the io.EOF the acker gets from the closed stream.
	is.True( // error message attributes the failure to the source node
		strings.Contains(pl.Error, fmt.Sprintf("node %s stopped with error:", source.ID)),
	)
	is.True( // and carries the real cause
		strings.Contains(pl.Error, wantErr.Error()),
	)
	is.True(cerrors.Is(event.Error, wantErr))
}

// TestServiceLifecycle_Start_AlreadyRunning proves Service.Start still returns
// pipeline.ErrPipelineRunning (the sentinel every errors.Is(err,
// pipeline.ErrPipelineRunning) check throughout the codebase relies on) when
// the pipeline is already running, and that the error now also carries a
// machine-actionable ConduitError code + suggestion.
func TestServiceLifecycle_Start_AlreadyRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.Nop())

	pl := &pipeline.Instance{ID: uuid.NewString(), Config: pipeline.Config{Name: "test-pipeline"}}
	pl.SetStatus(pipeline.StatusRunning)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{},
		testProcessorService{},
		testConnectorPluginService{},
		testPipelineService{pl.ID: pl},
	)

	err := ls.Start(ctx, pl.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, pipeline.ErrPipelineRunning)) // sentinel still in the chain

	ce, ok := conduiterr.Get(err)
	is.True(ok) // also carries a machine-actionable ConduitError code
	is.Equal(ce.Code.Reason(), pipeline.CodePipelineRunning.Reason())
	is.True(ce.Suggestion != "") // with a suggested fix
}

// TestServiceLifecycle_Stop_NotRunning proves Service.Stop still returns
// pipeline.ErrPipelineNotRunning when no pipeline is running for the given
// ID, and that the error now also carries a machine-actionable ConduitError
// code + suggestion.
func TestServiceLifecycle_Stop_NotRunning(t *testing.T) {
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

	err := ls.Stop(ctx, uuid.NewString(), false)
	is.True(err != nil)
	is.True(cerrors.Is(err, pipeline.ErrPipelineNotRunning)) // sentinel still in the chain

	ce, ok := conduiterr.Get(err)
	is.True(ok) // also carries a machine-actionable ConduitError code
	is.Equal(ce.Code.Reason(), pipeline.CodePipelineNotRunning.Reason())
	is.True(ce.Suggestion != "") // with a suggested fix
}

func TestServiceLifecycle_Stop(t *testing.T) {
	type testCase struct {
		name      string
		stopFn    func(ctx context.Context, is *is.I, lifecycleService *Service, pipelineID string)
		forceStop bool
		want      pipeline.Status
		wantErr   error
	}

	testCases := []testCase{
		{
			name: "user stop: graceful",
			stopFn: func(ctx context.Context, is *is.I, ls *Service, pipelineID string) {
				err := ls.Stop(ctx, pipelineID, false)
				is.NoErr(err)
			},
			want: pipeline.StatusUserStopped,
		},
		{
			name: "user stop: forceful",
			stopFn: func(ctx context.Context, is *is.I, ls *Service, pipelineID string) {
				err := ls.Stop(ctx, pipelineID, true)
				is.NoErr(err)
			},
			forceStop: true,
			wantErr:   cerrors.FatalError(pipeline.ErrForceStop),
			// A force stop is a stop, not a failure (#2901): UserStopped,
			// with the force-stop error recorded.
			want: pipeline.StatusUserStopped,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx, killAll := context.WithCancel(context.Background())
			defer killAll()
			logger := log.Test(t)
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)

			ps := pipeline.NewService(logger, db)

			// create a host pipeline
			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)

			// create mocked connectors
			// source will stop and return ErrGracefulShutdown which should signal to the
			// service that everything went well and the pipeline was gracefully shutdown
			ctrl := gomock.NewController(t)
			wantRecords := generateRecords(0)
			source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, !tc.forceStop, 1)
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
				}, ps)

			// start the pipeline now that everything is set up
			err = ls.Start(
				ctx,
				pl.ID,
			)
			is.NoErr(err)

			// wait for pipeline to finish consuming records from the source
			time.Sleep(100 * time.Millisecond)

			tc.stopFn(ctx, is, ls, pl.ID)

			// wait for pipeline to finish
			err = ls.WaitPipeline(pl.ID)
			if tc.wantErr != nil {
				is.True(err != nil)
				is.True(pl.Error != "") // the error is recorded, never dropped (#2901)
			} else {
				is.NoErr(err)
				is.Equal("", pl.Error)
			}

			is.Equal(tc.want, pl.GetStatus())
		})
	}
}

// TestServiceLifecycle_WaitPipeline_AfterCleanup is the deterministic regression
// test for #2521: WaitPipeline must return the pipeline's terminal error even when
// called after the pipeline has already removed itself from runningPipelines.
// Before the fix it returned a false nil there, dropping ErrForceStop — the flake
// the forceful-stop case hit intermittently. This forces the race window
// explicitly by waiting for cleanup before calling WaitPipeline, so it fails 100%
// of the time without the fix.
func TestServiceLifecycle_WaitPipeline_AfterCleanup(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	source, srcDispenser := asserterSource(ctrl, persister, generateRecords(0), nil, false, 1)
	destination, destDispenser := asserterDestination(ctrl, persister, generateRecords(0), 1)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
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
		}, ps)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Let the source node finish initializing before force-stopping (matches the
	// existing forceful case). Force-stopping mid-startup is a separate,
	// independently-tracked robustness gap and not what this test exercises.
	time.Sleep(100 * time.Millisecond)

	err = ls.Stop(ctx, pl.ID, true)
	is.NoErr(err)

	// Deterministically wait until the pipeline has cleaned itself up (removed from
	// runningPipelines) — this is exactly the window where WaitPipeline used to
	// return a false nil.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := ls.runningPipelines.Get(pl.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pipeline was not cleaned up within timeout")
		}
		time.Sleep(time.Millisecond)
	}

	// WaitPipeline must still surface the force-stop error, not a false nil.
	err = ls.WaitPipeline(pl.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, pipeline.ErrForceStop))
	// A force stop ends UserStopped with the error recorded (#2901).
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
	is.True(strings.Contains(pl.Error, pipeline.ErrForceStop.Error()))
}

func TestServiceLifecycle_StopAll(t *testing.T) {
	type testCase struct {
		name    string
		stopFn  func(ctx context.Context, is *is.I, lifecycleService *Service, pipelineID string)
		want    pipeline.Status
		wantErr error
	}

	testCases := []testCase{
		{
			name: "system stop (graceful shutdown err)",
			stopFn: func(ctx context.Context, is *is.I, ls *Service, pipelineID string) {
				ls.StopAll(ctx, pipeline.ErrGracefulShutdown)
			},
			want: pipeline.StatusSystemStopped,
		},
		{
			name: "system stop (fatal error)",
			stopFn: func(ctx context.Context, is *is.I, ls *Service, pipelineID string) {
				ls.StopAll(ctx, cerrors.FatalError(cerrors.New("terrible err")))
			},
			// Before #2901 this was Degraded. A pipeline caught in a
			// shutdown was stopped, not failed: SystemStopped, with the
			// reason recorded, so it starts again on the next boot.
			want:    pipeline.StatusSystemStopped,
			wantErr: cerrors.New("terrible err"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx, killAll := context.WithCancel(context.Background())
			defer killAll()
			logger := log.New(zerolog.Nop())
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)

			ps := pipeline.NewService(logger, db)

			// create a host pipeline
			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)

			// create mocked connectors
			// source will stop and return ErrGracefulShutdown which should signal to the
			// service that everything went well and the pipeline was gracefully shutdown
			ctrl := gomock.NewController(t)
			wantRecords := generateRecords(0)
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
				}, ps)

			// start the pipeline now that everything is set up
			err = ls.Start(
				ctx,
				pl.ID,
			)
			is.NoErr(err)

			// wait for pipeline to finish consuming records from the source
			time.Sleep(100 * time.Millisecond)

			tc.stopFn(ctx, is, ls, pl.ID)

			// wait for pipeline to finish
			err = ls.WaitPipeline(pl.ID)
			if tc.wantErr != nil {
				is.True(err != nil)
				is.True(pl.Error != "") // the error is recorded, never dropped (#2901)
			} else {
				is.NoErr(err)
				is.Equal("", pl.Error)
			}

			is.Equal(tc.want, pl.GetStatus())
		})
	}
}

// TestServiceLifecycle_StopAll_Recovering covers StopAll with a recoverable
// (non-fatal) reason, which is what the runtime passes when it shuts down
// because its tomb died with an error. The source returns that reason as its
// error.
//
// This test used to assert that the pipeline then went into recovery and was
// restarted. That was the bug in #2901: the restart happens during shutdown,
// after the runtime's Wait has stopped looking, and the restarted run keeps
// writing while the persister is flushed and the database closed. It now
// asserts the intended behaviour: a run whose stop was requested is never
// recovered; it ends SystemStopped with the reason recorded as its error (so
// it starts again on the next boot), the source is
// dispensed exactly once (Times(1) below fails at controller finish on a
// restart), Wait returns, and Start is refused because the service is
// shutting down.
func TestServiceLifecycle_StopAll_Recovering(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	wantErr := cerrors.New("lost connection to database")

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// Exactly one dispense of each plugin: a recovery restart would be a
	// second one.
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(0)
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
		}, ps)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// StopAll waits for the source to be running before it stops it, so no
	// sleep is needed here.
	ls.StopAll(ctx, wantErr)

	err = ls.Wait(10 * time.Second)
	is.True(err != context.DeadlineExceeded) // Wait must return: nothing restarts
	is.True(cerrors.Is(err, wantErr))

	// The run was stopped by the shutdown, with the reason recorded, and
	// was not recovered.
	is.True(cerrors.Is(ls.WaitPipeline(pl.ID), wantErr))
	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
	is.True(strings.Contains(pl.Error, wantErr.Error()))
	_, live := ls.runningPipelines.Get(pl.ID)
	is.True(!live)

	// After StopAll the service starts nothing.
	err = ls.Start(ctx, pl.ID)
	is.True(cerrors.Is(err, pipeline.ErrShuttingDown))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodeShuttingDown)
}

func TestServiceLifecycle_PipelineStop(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	// source will stop and return ErrGracefulShutdown which should signal to the
	// service that everything went well and the pipeline was gracefully shutdown
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, true, 1)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 1)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		}, ps)

	// start the pipeline now that everything is set up
	err = ls.Start(
		ctx,
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish consuming records from the source
	time.Sleep(100 * time.Millisecond)
	ls.StopAll(ctx, pipeline.ErrGracefulShutdown)

	// wait for pipeline to finish
	err = ls.WaitPipeline(pl.ID)
	is.NoErr(err)

	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
	is.Equal("", pl.Error)
}

func TestServiceLifecycle_Run_Rerun(t *testing.T) {
	runTest := func(t *testing.T, status pipeline.Status, expected pipeline.Status) {
		is := is.New(t)
		ctx, killAll := context.WithCancel(context.Background())
		defer killAll()
		ctrl := gomock.NewController(t)
		logger := log.Test(t)
		db := &inmemory.DB{}
		persister := connector.NewPersister(logger, db, time.Second, 3)

		ps := pipeline.NewService(logger, db)

		// create a host pipeline
		pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
		is.NoErr(err)

		// create mocked connectors
		var (
			source        *connector.Instance
			srcDispenser  *pmock.Dispenser
			destination   *connector.Instance
			destDispenser *pmock.Dispenser
			dlq           *connector.Instance
			dlqDispenser  *pmock.Dispenser
		)
		if expected == pipeline.StatusRunning {
			// mocked connectors that are expected to be started
			source, srcDispenser = asserterSource(ctrl, persister, nil, nil, true, 1)
			destination, destDispenser = asserterDestination(ctrl, persister, nil, 1)
			dlq, dlqDispenser = asserterDestination(ctrl, persister, nil, 1)
		} else {
			// dummy connectors that are not expected to be started
			source = dummySource(persister)
			destination = dummyDestination(persister)
			dlq = dummyDestination(persister)
		}

		// update internal fields, they will be stored when we add the connectors
		pl.DLQ.Plugin = dlq.Plugin
		pl.SetStatus(status)

		pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
		is.NoErr(err)
		pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
		is.NoErr(err)

		// create a new pipeline service and initialize it
		ps = pipeline.NewService(logger, db)
		err = ps.Init(ctx)
		is.NoErr(err)

		ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
			testProcessorService{},
			testConnectorPluginService{
				source.Plugin:      srcDispenser,
				destination.Plugin: destDispenser,
				dlq.Plugin:         dlqDispenser,
			}, ps)
		err = ls.Init(ctx)
		is.NoErr(err)

		// give pipeline a chance to start if needed
		time.Sleep(time.Millisecond * 100)

		got := ps.List(ctx)
		is.Equal(len(got), 1)
		is.True(got[pl.ID] != nil)
		is.Equal(got[pl.ID].GetStatus(), expected)

		if expected == pipeline.StatusRunning {
			pl, _ = ps.Get(ctx, pl.ID)

			is.NoErr(ls.Stop(ctx, pl.ID, false))
			is.NoErr(ls.WaitPipeline(pl.ID))
		}
	}

	testCases := []struct {
		have pipeline.Status
		want pipeline.Status
	}{
		{have: pipeline.StatusRunning, want: pipeline.StatusRunning},
		{have: pipeline.StatusUserStopped, want: pipeline.StatusUserStopped},
		{have: pipeline.StatusSystemStopped, want: pipeline.StatusRunning},
		{have: pipeline.StatusDegraded, want: pipeline.StatusDegraded},
	}
	for _, tt := range testCases {
		t.Run(fmt.Sprintf("%s->%s", tt.have, tt.want), func(t *testing.T) {
			runTest(t, tt.have, tt.want)
		})
	}
}

func generateRecords(count int) []opencdc.Record {
	records := make([]opencdc.Record, count)
	for i := 0; i < count; i++ {
		records[i] = opencdc.Record{
			Key: opencdc.RawData(uuid.NewString()),
			Payload: opencdc.Change{
				Before: opencdc.RawData{},
				After:  opencdc.RawData(uuid.NewString()),
			},
			Position: opencdc.Position(strconv.Itoa(i)),
		}
	}
	return records
}

// asserterSource creates a connector source that fills up the returned slice
// with generated records as they are produced. After producing the requested
// number of records it returns wantErr.
func asserterSource(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
	wantErr error,
	stop bool,
	times int,
) (*connector.Instance, *pmock.Dispenser) {
	sourcePluginOptions := []pmock.ConfigurableSourcePluginOption{
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(records, wantErr),
		pmock.SourcePluginWithAcks(len(records), wantErr == nil),
		pmock.SourcePluginWithTeardown(),
	}

	if stop {
		sourcePluginOptions = append(sourcePluginOptions, pmock.SourcePluginWithStop())
	}
	source := dummySource(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().DoAndReturn(func() (connectorPlugin.SourcePlugin, error) {
		return pmock.NewConfigurableSourcePlugin(ctrl, sourcePluginOptions...), nil
	}).Times(times)

	return source, dispenser
}

// asserterDestination creates a connector destination that checks if the records it gets
// match the expected records. On teardown, it also makes sure that it received
// all expected records.
func asserterDestination(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
	times int,
) (*connector.Instance, *pmock.Dispenser) {
	var lastPosition opencdc.Position
	if len(records) > 0 {
		lastPosition = records[len(records)-1].Position
	}

	destinationPluginOptions := []pmock.ConfigurableDestinationPluginOption{
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithRecords(records),
		pmock.DestinationPluginWithStop(lastPosition),
		pmock.DestinationPluginWithTeardown(),
	}

	dest := dummyDestination(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().DoAndReturn(func() (connectorPlugin.DestinationPlugin, error) {
		return pmock.NewConfigurableDestinationPlugin(ctrl, destinationPluginOptions...), nil
	}).Times(times)

	return dest, dispenser
}

// dummySource creates a dummy source connector.
func dummySource(persister *connector.Persister) *connector.Instance {
	// randomize plugin name in case of multiple sources
	testPluginName := "test-source-plugin-" + uuid.NewString()
	source := &connector.Instance{
		ID:         uuid.NewString(),
		Type:       connector.TypeSource,
		PipelineID: uuid.NewString(),
		Plugin:     testPluginName,
	}
	source.Init(log.Nop(), persister)

	return source
}

// dummyDestination creates a dummy destination connector.
func dummyDestination(persister *connector.Persister) *connector.Instance {
	// randomize plugin name in case of multiple destinations
	testPluginName := "test-destination-plugin-" + uuid.NewString()

	destination := &connector.Instance{
		ID:         uuid.NewString(),
		Type:       connector.TypeDestination,
		PipelineID: uuid.NewString(),
		Plugin:     testPluginName,
	}
	destination.Init(log.Nop(), persister)

	return destination
}

func testErrRecoveryCfg() *ErrRecoveryCfg {
	return &ErrRecoveryCfg{
		MinDelay:         time.Second,
		MaxDelay:         10 * time.Minute,
		BackoffFactor:    2,
		MaxRetries:       InfiniteRetriesErrRecovery,
		MaxRetriesWindow: 5 * time.Minute,
	}
}

// testConnectorService fulfills the ConnectorService interface.
type testConnectorService map[string]*connector.Instance

func (s testConnectorService) Get(_ context.Context, id string) (*connector.Instance, error) {
	conn, ok := s[id]
	if !ok {
		return nil, connector.ErrInstanceNotFound
	}
	return conn, nil
}

func (s testConnectorService) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	return s[testDLQID], nil
}

// WaitPersisted is a no-op here: none of the existing tests using
// testConnectorService directly exercise StopAndWait's durability wait (they
// assert on WaitPipeline/Stop instead). Tests that do need a real durability
// signal use testConnectorServiceWithPersister below, which forwards to the
// real *connector.Persister the test's connectors were actually Init'd with.
func (s testConnectorService) WaitPersisted() {}

// testConnectorServiceWithPersister wraps testConnectorService and forwards
// WaitPersisted to the real persister backing its connectors, for tests that
// must observe actual durability (StopAndWait's drain-and-persist guarantee).
type testConnectorServiceWithPersister struct {
	testConnectorService
	persister *connector.Persister
}

func (s testConnectorServiceWithPersister) WaitPersisted() {
	s.persister.WaitPendingWrites()
}

// testProcessorService fulfills the ProcessorService interface.
type testProcessorService map[string]*processor.Instance

func (s testProcessorService) MakeRunnableProcessor(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
	return nil, cerrors.New("not implemented")
}

func (s testProcessorService) MakeRunnableProcessorForReconfigure(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
	return nil, cerrors.New("not implemented")
}

func (s testProcessorService) Get(_ context.Context, id string) (*processor.Instance, error) {
	proc, ok := s[id]
	if !ok {
		return nil, processor.ErrInstanceNotFound
	}
	return proc, nil
}

// testConnectorPluginService fulfills the ConnectorPluginService interface.
type testConnectorPluginService map[string]connectorPlugin.Dispenser

func (s testConnectorPluginService) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	plug, ok := s[name]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	return plug, nil
}

// testPipelineService fulfills the PipelineService interface.
type testPipelineService map[string]*pipeline.Instance

func (s testPipelineService) Get(_ context.Context, pipelineID string) (*pipeline.Instance, error) {
	p, ok := s[pipelineID]
	if !ok {
		return nil, processor.ErrInstanceNotFound
	}
	return p, nil
}

func (s testPipelineService) List(_ context.Context) map[string]*pipeline.Instance {
	instances := make(map[string]*pipeline.Instance)
	return instances
}

func (s testPipelineService) UpdateStatus(_ context.Context, pipelineID string, status pipeline.Status, errMsg string) error {
	p, ok := s[pipelineID]
	if !ok {
		return processor.ErrInstanceNotFound
	}
	p.SetStatus(status)
	p.Error = errMsg
	return nil
}

// persisterDrainTimeout bounds the deferred persister wait. See
// stopAndWaitPersister.
const persisterDrainTimeout = 10 * time.Second

// stopAndWaitPersister cancels the pipeline context and waits for the persister
// to drain, but only for a bounded time.
//
// Persister.Wait blocks on connWg, which only reaches zero once every connector
// has called ConnectorStopped — and that happens through pipeline teardown
// (ls.Stop), not through context cancellation.
//
// On the happy path this is free: the test body already called ls.Stop, so the
// wait returns at once. The bound only matters when an assertion has ALREADY
// failed. is.NoErr calls t.FailNow, which is runtime.Goexit: the rest of the
// test body is skipped — including ls.Stop — so the connectors never stop,
// connWg never reaches zero, and an unbounded Wait blocks until the
// package-wide timeout fires. A one-line assertion failure becomes a 10-minute
// hang whose goroutine dump points at the engine instead of the assertion.
//
// See #2746. The same shape existed across pkg/lifecycle-poc; this package had
// one instance of it.
//
// t.Errorf, never t.Fatalf: this runs as a deferred function, and Fatalf's
// Goexit inside a defer would abandon the remaining cleanup.
func stopAndWaitPersister(t *testing.T, killAll context.CancelFunc, p *connector.Persister) {
	t.Helper()
	killAll()

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Wait()
	}()

	select {
	case <-done:
	case <-time.After(persisterDrainTimeout):
		t.Errorf("persister did not drain within %s: some connector never reported "+
			"ConnectorStopped (see #2746)", persisterDrainTimeout)
	}
}

// statusRecorder wraps a PipelineService and gives tests a seam into
// UpdateStatus calls, for the #2806 regression tests below (mirrors
// pkg/lifecycle-poc's #2746 test helper of the same name and shape — v1's
// runPipeline orders its cleanup-goroutine registration differently, which is
// exactly why v1 needed its own fix and its own tests, but the "hold the
// window open instead of racing it" technique transfers directly).
type statusRecorder struct {
	PipelineService

	mu       sync.Mutex
	statuses []pipeline.Status

	// onUpdate, if set, is called from inside UpdateStatus for every call,
	// with the status being written and its 1-based occurrence count for
	// that status (so a hook can distinguish e.g. the initial StatusRunning
	// from a post-recovery one). It is the seam that lets a test hold the
	// lifecycle inside a "run is going live" window and inspect it
	// deterministically, instead of trying to catch that window by racing
	// it — see TestServiceLifecycle_StopAll_Recovering's sibling tests
	// below, none of which sleep-and-hope.
	//
	// If onUpdate returns a non-nil error, that error is returned in place
	// of calling the wrapped PipelineService — simulating a transient
	// status-store failure without going anywhere near the real store. In
	// particular it does NOT mutate the real pipeline.Instance's in-memory
	// status as a side effect, unlike the genuine store write path (see
	// #2809: pipeline.Service.UpdateStatus mutates the shared instance's
	// status before attempting the store write and never rolls that back on
	// error). Tests below intentionally do not depend on #2809's behavior —
	// it is a separate, already-filed bug.
	onUpdate func(status pipeline.Status, nth int) error
}

func newStatusRecorder(inner PipelineService) *statusRecorder {
	return &statusRecorder{PipelineService: inner}
}

func (r *statusRecorder) UpdateStatus(ctx context.Context, id string, status pipeline.Status, errMsg string) error {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	nth := 0
	for _, s := range r.statuses {
		if s == status {
			nth++
		}
	}
	hook := r.onUpdate
	r.mu.Unlock()

	if hook != nil {
		if err := hook(status, nth); err != nil {
			return err
		}
	}
	return r.PipelineService.UpdateStatus(ctx, id, status, errMsg)
}

// TestServiceLifecycle_Recovery_LiveEntryPublishedBeforeRunningStatus is the
// #2806 regression test (v1's counterpart of #2746, fixed in
// pkg/lifecycle-poc by #2807).
//
// The invariant: whenever a caller can observe the pipeline as running,
// runningPipelines[id] is the run that is ACTUALLY running. Everything
// public resolves a pipeline through that map — Stop, StopAll, WaitPipeline,
// StopAndWait (and so provisioning.ApplyPlanLive) — and StartWithBackoff's
// "am I still the live pipeline" guard is a pointer comparison against it.
//
// Start used to publish the new run only AFTER runPipeline returned, i.e.
// after runPipeline had already announced StatusRunning. On a recovery
// restart the previous entry is deliberately left in place until that swap,
// so in the gap the map pointed at the FAILED run: WaitPipeline joined the
// dead tomb and returned the pre-recovery error for a pipeline that had just
// recovered, and Stop stopped the dead run while the recovered one kept
// running (invariant 7, silently).
//
// This does not try to catch that gap by racing it, which is what made the
// original bug intermittent (a pre-fix -count=60 sweep of this package
// passed while it was live). It HOLDS the lifecycle inside the gap:
// statusRecorder.onUpdate blocks in the middle of the post-recovery
// StatusRunning write — the exact instant an observer first learns the
// pipeline is running again — and the assertions run there. Pre-fix that is
// a deterministic failure, not a probabilistic one.
func TestServiceLifecycle_Recovery_LiveEntryPublishedBeforeRunningStatus(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)
	wantErr := cerrors.New("lost connection to database")

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(0)
	source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, true, 2)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 2)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 2)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// inWindow closes when the recovered run is mid-announcement; release
	// unblocks it once the assertions below have run.
	inWindow := make(chan struct{})
	release := make(chan struct{})
	rec := newStatusRecorder(ps)
	rec.onUpdate = func(status pipeline.Status, nth int) error {
		// The 2nd StatusRunning is the recovery restart (the 1st is the
		// initial run). Fire once: a later run must not re-block.
		if status == pipeline.StatusRunning && nth == 2 {
			close(inWindow)
			<-release
		}
		return nil
	}

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
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
	)

	is.NoErr(ls.Start(ctx, pl.ID))

	// deadRp is the pre-recovery run. Capturing it lets the assertion below
	// tell "the map points at the run that just failed" apart from "the map
	// points at the new one" by identity, not just by aliveness.
	deadRp, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok)

	// Force a recoverable error. Not via StopAll(wantErr), which this test
	// used before #2901: a stop request never leads to recovery any more.
	injectSourceError(ctx, t, ls, pl.ID, wantErr)

	<-inWindow

	// The assertion, taken while the lifecycle is frozen in the window: the
	// entry a caller would resolve right now must be the live run, not the
	// one that just failed. Reading runningPipelines directly (rather than
	// going through Stop/WaitPipeline, both of which legitimately BLOCK on a
	// live tomb and so cannot distinguish "correct" from "hung" here) is
	// what keeps this deterministic. Pre-fix, ok is true but the entry is
	// still deadRp, whose tomb died from wantErr.
	rp, ok := ls.runningPipelines.Get(pl.ID)
	is.True(ok) // no live entry at all while the pipeline reports Running
	if rp == deadRp {
		t.Fatalf("runningPipelines[%s] still points at the pre-recovery run while the recovered run's StatusRunning is being announced (#2806)", pl.ID)
	}
	is.True(rp.t != nil)
	if !rp.t.Alive() {
		t.Fatalf("runningPipelines[%s] points at a dead tomb while StatusRunning is being announced — Stop would tear down the wrong run (#2806)", pl.ID)
	}

	close(release)

	// Let the recovered run actually finish starting, then tear it down —
	// both dispensed plugin instances (rp1's and rp2's) still need their
	// Stop/Teardown expectations satisfied, and an un-stopped pipeline would
	// otherwise race the deferred persister drain (see stopAndWaitPersister).
	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}

// The #2806 tests that pinned a rollback of the publication when the
// StatusRunning write failed (AC 2, and AC 2b for the nested recovery
// restart) were replaced by status_write_failure_test.go when that rollback
// was removed (#2898): a run whose nodes are running stays published and owns
// its cleanup whatever its status write returns.

// TestServiceLifecycle_StartWithBackoff_SupersededRunDoesNotRestart is AC 3:
// the recovery pointer guard in StartWithBackoff must still detect that it
// has been superseded and decline to restart.
//
// This is existing behavior (unchanged by #2806) that the fix must not
// regress: StartWithBackoff compares the runnablePipeline it was given
// against whatever is currently published under the same ID, and returns
// nil without restarting if they differ.
func TestServiceLifecycle_StartWithBackoff_SupersededRunDoesNotRestart(t *testing.T) {
	is := is.New(t)
	logger := log.New(zerolog.Nop())

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{},
		testProcessorService{},
		testConnectorPluginService{},
		testPipelineService{},
	)

	pipelineID := uuid.NewString()
	staleRp := &runnablePipeline{
		pipeline:         &pipeline.Instance{ID: pipelineID, Config: pipeline.Config{Name: "test"}},
		backoff:          testErrRecoveryCfg().toBackoff(),
		recoveryAttempts: &atomic.Int64{},
	}
	freshRp := &runnablePipeline{
		pipeline:         &pipeline.Instance{ID: pipelineID, Config: pipeline.Config{Name: "test"}},
		recoveryAttempts: &atomic.Int64{},
	}

	// freshRp supersedes staleRp in the map before staleRp's backoff elapses
	// — e.g. the pipeline was stopped and restarted by a user while a
	// recovery attempt for it was still waiting out its backoff.
	ls.runningPipelines.Set(pipelineID, freshRp)

	err := ls.StartWithBackoff(context.Background(), staleRp)
	is.NoErr(err) // the pointer guard must return nil, not attempt to restart a superseded run

	got, ok := ls.runningPipelines.Get(pipelineID)
	is.True(ok)
	is.True(got == freshRp) // the superseded run's early return must not touch the entry that superseded it
}

// TestServiceLifecycle_Recovery_StopDuringWindowTargetsLiveRun is AC 4: Stop
// called during the announcement window must act on the LIVE (recovered)
// run, not the dead pre-recovery one.
//
// This is the concrete failure mode #2806 describes: Stop resolving to a
// dead tomb during the window either errors against nodes that already
// finished, or silently no-ops, while the actually-live run keeps going
// forever — connectors never torn down, a drain reported complete (or
// simply never attempted) when it never happened (invariant 7). The
// assertion that matters is the outcome: the live run must actually stop,
// promptly, and reach StatusUserStopped. Stop's own immediate return value
// during the window is logged but not asserted on, since it can legitimately
// differ pre-fix; that its target never stops is the real bug.
func TestServiceLifecycle_Recovery_StopDuringWindowTargetsLiveRun(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)
	wantErr := cerrors.New("lost connection to database")

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(0)
	source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, true, 2)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 2)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 2)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	inWindow := make(chan struct{})
	release := make(chan struct{})
	rec := newStatusRecorder(ps)
	rec.onUpdate = func(status pipeline.Status, nth int) error {
		if status == pipeline.StatusRunning && nth == 2 {
			close(inWindow)
			<-release
		}
		return nil
	}

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
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
	)

	is.NoErr(ls.Start(ctx, pl.ID))
	// Force a recoverable error. Not via StopAll(wantErr), which this test
	// used before #2901: a stop request never leads to recovery any more.
	injectSourceError(ctx, t, ls, pl.ID, wantErr)

	<-inWindow
	// Stop while the recovered run's own StatusRunning announcement is
	// still in flight — the exact window #2806 describes.
	stopErr := ls.Stop(ctx, pl.ID, false)
	close(release)
	t.Logf("Stop() returned during the announcement window: %v", stopErr)

	c := make(cchan.Chan[error])
	go func() { c <- ls.WaitPipeline(pl.ID) }()
	waitErr, _, ctxErr := c.RecvTimeout(ctx, 5*time.Second)
	is.NoErr(ctxErr) // the LIVE run must actually stop, not run forever because Stop targeted a dead tomb
	is.NoErr(waitErr)
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}
