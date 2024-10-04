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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
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
	defer persister.Wait()

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
	t.Skipf("this test fails, see github.com/ConduitIO/conduit/issues/1659")

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
	wantErr := cerrors.New("source connector error")
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

	event, eventReceived, err := cchan.Chan[FailureEvent](events).RecvTimeout(ctx, 200*time.Millisecond)
	is.NoErr(err)
	is.True(eventReceived)
	is.Equal(pl.ID, event.ID)

	// These conditions are NOT met
	is.True( // expected error message to have "node <source id> stopped with error"
		strings.Contains(pl.Error, fmt.Sprintf("node %s stopped with error:", source.ID)),
	)
	is.True( // expected error message to contain "source connector error"
		strings.Contains(pl.Error, wantErr.Error()),
	)
	is.True(cerrors.Is(event.Error, wantErr))
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
			want:      pipeline.StatusDegraded,
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
			} else {
				is.NoErr(err)
				is.Equal("", pl.Error)
			}

			is.Equal(tc.want, pl.GetStatus())
		})
	}
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
			want:    pipeline.StatusDegraded,
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
			} else {
				is.NoErr(err)
				is.Equal("", pl.Error)
			}

			is.Equal(tc.want, pl.GetStatus())
		})
	}
}

// Creates first a pipeline that will stop with a recoverable error, to check later that it restarted and it's running.
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

	// create mocked connectors
	// source will stop and return ErrGracefulShutdown which should signal to the
	// service that everything went well and the pipeline was gracefully shutdown
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

	c := make(cchan.Chan[error])
	go func() {
		c <- ls.WaitPipeline(pl.ID)
	}()

	// force the pipeline to stop with a recoverable error
	ls.StopAll(ctx, wantErr)
	err, _, ctxErr := c.RecvTimeout(ctx, 10000*time.Millisecond)
	is.NoErr(ctxErr)

	// check the first pipeline stopped with the error that caused the restart
	is.True(cerrors.Is(err, wantErr))

	go func() {
		c <- ls.WaitPipeline(pl.ID)
	}()

	_, _, err = c.RecvTimeout(ctx, 1000*time.Millisecond)
	is.True(cerrors.Is(err, context.DeadlineExceeded))

	// stop the running pipeline
	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)

	// Check pipeline ended in a running state
	is.Equal(pipeline.StatusRunning, pl.GetStatus())

	go func() {
		c <- ls.WaitPipeline(pl.ID)
	}()
	err, _, _ = c.RecvTimeout(ctx, 1000*time.Millisecond)
	is.NoErr(err)

	// This is to demonstrate the test indeed stopped the pipeline
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
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
		MinDelay:      time.Second,
		MaxDelay:      10 * time.Minute,
		BackoffFactor: 2,
		MaxRetries:    InfiniteRetriesErrRecovery,
		HealthyAfter:  5 * time.Minute,
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

// testProcessorService fulfills the ProcessorService interface.
type testProcessorService map[string]*processor.Instance

func (s testProcessorService) MakeRunnableProcessor(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
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
