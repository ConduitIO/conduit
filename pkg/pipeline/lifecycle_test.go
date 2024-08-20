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

package pipeline

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
	"github.com/conduitio/conduit/pkg/pipeline/stream"
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

func TestServiceLifecycle_buildNodes(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := NewService(logger, db)

	source := dummySource(persister)
	destination := dummyDestination(persister)
	dlq := dummyDestination(persister)
	pl := &Instance{
		ID:     uuid.NewString(),
		Config: Config{Name: "test-pipeline"},
		Status: StatusUserStopped,
		DLQ: DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{source.ID, destination.ID},
	}

	got, err := ps.buildNodes(
		ctx,
		testConnectorFetcher{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorFetcher{},
		testPluginFetcher{
			source.Plugin:      pmock.NewDispenser(ctrl),
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		},
		pl,
	)
	is.NoErr(err)

	want := []stream.Node{
		&stream.SourceNode{},
		&stream.SourceAckerNode{},
		&stream.MetricsNode{},
		&stream.DLQHandlerNode{},
		&stream.FaninNode{},
		&stream.FanoutNode{},
		&stream.MetricsNode{},
		&stream.DestinationNode{},
		&stream.DestinationAckerNode{},
	}
	is.Equal(len(want), len(got))
	for i := range want {
		want := want[i]
		got := got[i]
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

func TestService_buildNodes_NoSourceNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := NewService(logger, db)

	wantErr := "can't build pipeline without any source connectors"

	destination := dummyDestination(persister)
	dlq := dummyDestination(persister)
	pl := &Instance{
		ID:     uuid.NewString(),
		Config: Config{Name: "test-pipeline"},
		Status: StatusUserStopped,
		DLQ: DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{destination.ID},
	}

	got, err := ps.buildNodes(
		ctx,
		testConnectorFetcher{
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorFetcher{},
		testPluginFetcher{
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		},
		pl,
	)

	is.True(err != nil)
	is.Equal(err.Error(), wantErr)
	is.Equal(got, nil)
}

func TestService_buildNodes_NoDestinationNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := NewService(logger, db)

	wantErr := "can't build pipeline without any destination connectors"

	source := dummySource(persister)
	dlq := dummyDestination(persister)

	pl := &Instance{
		ID:     uuid.NewString(),
		Config: Config{Name: "test-pipeline"},
		Status: StatusUserStopped,
		DLQ: DLQ{
			Plugin:              dlq.Plugin,
			Settings:            map[string]string{},
			WindowSize:          3,
			WindowNackThreshold: 2,
		},
		ConnectorIDs: []string{source.ID},
	}

	got, err := ps.buildNodes(
		ctx,
		testConnectorFetcher{
			source.ID: source,
			testDLQID: dlq,
		},
		testProcessorFetcher{},
		testPluginFetcher{
			source.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:    pmock.NewDispenser(ctrl),
		},
		pl,
	)

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

	ps := NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, true)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// start the pipeline now that everything is set up
	err = ps.Start(
		ctx,
		testConnectorFetcher{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorFetcher{},
		testPluginFetcher{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish consuming records from the source
	time.Sleep(100 * time.Millisecond)

	is.Equal(StatusRunning, pl.Status)
	is.Equal("", pl.Error)

	// stop pipeline before ending test
	err = ps.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(pl.Wait())
}

func TestServiceLifecycle_PipelineError(t *testing.T) {
	t.Skipf("this test fails, see github.com/ConduitIO/conduit/issues/1659")

	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Test(t)
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	wantErr := cerrors.New("source connector error")
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, wantErr, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	events := make(chan FailureEvent, 1)
	ps.OnFailure(func(e FailureEvent) {
		events <- e
	})
	// start the pipeline now that everything is set up
	err = ps.Start(
		ctx,
		testConnectorFetcher{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorFetcher{},
		testPluginFetcher{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish
	err = pl.Wait()
	is.True(err != nil)

	is.Equal(StatusDegraded, pl.Status)
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

func TestServiceLifecycle_StopAll_Recovering(t *testing.T) {
	type testCase struct {
		name   string
		stopFn func(ctx context.Context, is *is.I, pipelineService *Service, pipelineID string)
		// whether we expect the source plugin's Stop() function to be called
		// (doesn't happen when force-stopping)
		wantSourceStop bool
		want           Status
		wantErr        error
	}

	runTest := func(t *testing.T, tc testCase) {
		is := is.New(t)
		ctx, killAll := context.WithCancel(context.Background())
		defer killAll()
		logger := log.New(zerolog.Nop())
		db := &inmemory.DB{}
		persister := connector.NewPersister(logger, db, time.Second, 3)

		ps := NewService(logger, db)

		// create a host pipeline
		pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"}, ProvisionTypeAPI)
		is.NoErr(err)

		// create mocked connectors
		// source will stop and return ErrGracefulShutdown which should signal to the
		// service that everything went well and the pipeline was gracefully shutdown
		ctrl := gomock.NewController(t)
		wantRecords := generateRecords(0)
		source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, tc.wantSourceStop)
		destination, destDispenser := asserterDestination(ctrl, persister, wantRecords)
		dlq, dlqDispenser := asserterDestination(ctrl, persister, nil)
		pl.DLQ.Plugin = dlq.Plugin

		pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
		is.NoErr(err)
		pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
		is.NoErr(err)

		// start the pipeline now that everything is set up
		err = ps.Start(
			ctx,
			testConnectorFetcher{
				source.ID:      source,
				destination.ID: destination,
				testDLQID:      dlq,
			},
			testProcessorFetcher{},
			testPluginFetcher{
				source.Plugin:      sourceDispenser,
				destination.Plugin: destDispenser,
				dlq.Plugin:         dlqDispenser,
			},
			pl.ID,
		)
		is.NoErr(err)

		// wait for pipeline to finish consuming records from the source
		time.Sleep(100 * time.Millisecond)

		pl.Status = StatusRecovering
		tc.stopFn(ctx, is, ps, pl.ID)

		// wait for pipeline to finish
		err = pl.Wait()
		if tc.wantErr != nil {
			is.True(err != nil)
		} else {
			is.NoErr(err)
			is.Equal("", pl.Error)
		}

		is.Equal(tc.want, pl.Status)
	}

	testCases := []testCase{
		{
			name: "system stop (graceful shutdown err)",
			stopFn: func(ctx context.Context, is *is.I, ps *Service, pipelineID string) {
				ps.StopAll(ctx, ErrGracefulShutdown)
			},
			wantSourceStop: true,
			want:           StatusSystemStopped,
		},
		{
			name: "system stop (terrible err)",
			stopFn: func(ctx context.Context, is *is.I, ps *Service, pipelineID string) {
				ps.StopAll(ctx, cerrors.New("terrible err"))
			},
			wantSourceStop: true,
			want:           StatusDegraded,
			wantErr:        cerrors.New("terrible err"),
		},
		{
			name: "user stop (graceful)",
			stopFn: func(ctx context.Context, is *is.I, ps *Service, pipelineID string) {
				err := ps.Stop(ctx, pipelineID, false)
				is.NoErr(err)
			},
			wantSourceStop: true,
			want:           StatusUserStopped,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runTest(t, tc)
		})
	}
}

func TestServiceLifecycle_PipelineStop(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	// source will stop and return ErrGracefulShutdown which should signal to the
	// service that everything went well and the pipeline was gracefully shutdown
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, true)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// start the pipeline now that everything is set up
	err = ps.Start(
		ctx,
		testConnectorFetcher{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		testProcessorFetcher{},
		testPluginFetcher{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		pl.ID,
	)
	is.NoErr(err)

	// wait for pipeline to finish consuming records from the source
	time.Sleep(100 * time.Millisecond)
	ps.StopAll(ctx, ErrGracefulShutdown)

	// wait for pipeline to finish
	err = pl.Wait()
	is.NoErr(err)

	is.Equal(StatusSystemStopped, pl.Status)
	is.Equal("", pl.Error)
}

func TestService_Run_Rerun(t *testing.T) {
	runTest := func(t *testing.T, status Status, expected Status) {
		is := is.New(t)
		ctx, killAll := context.WithCancel(context.Background())
		defer killAll()
		ctrl := gomock.NewController(t)
		logger := log.Test(t)
		db := &inmemory.DB{}
		persister := connector.NewPersister(logger, db, time.Second, 3)

		ps := NewService(logger, db)

		// create a host pipeline
		pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"}, ProvisionTypeAPI)
		is.NoErr(err)

		// create mocked connectors
		var (
			source          *connector.Instance
			sourceDispenser *pmock.Dispenser
			destination     *connector.Instance
			destDispenser   *pmock.Dispenser
			dlq             *connector.Instance
			dlqDispenser    *pmock.Dispenser
		)
		if expected == StatusRunning {
			// mocked connectors that are expected to be started
			source, sourceDispenser = generatorSource(ctrl, persister, nil, nil, true)
			destination, destDispenser = asserterDestination(ctrl, persister, nil)
			dlq, dlqDispenser = asserterDestination(ctrl, persister, nil)
		} else {
			// dummy connectors that are not expected to be started
			source = dummySource(persister)
			destination = dummyDestination(persister)
			dlq = dummyDestination(persister)
		}

		// update internal fields, they will be stored when we add the connectors
		pl.DLQ.Plugin = dlq.Plugin
		pl.Status = status

		pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
		is.NoErr(err)
		pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
		is.NoErr(err)

		// create a new pipeline service and initialize it
		ps = NewService(logger, db)
		err = ps.Init(ctx)
		is.NoErr(err)
		err = ps.Run(
			ctx,
			testConnectorFetcher{
				source.ID:      source,
				destination.ID: destination,
				testDLQID:      dlq,
			},
			testProcessorFetcher{},
			testPluginFetcher{
				source.Plugin:      sourceDispenser,
				destination.Plugin: destDispenser,
				dlq.Plugin:         dlqDispenser,
			},
		)
		is.NoErr(err)

		// give pipeline a chance to start if needed
		time.Sleep(time.Millisecond * 100)

		got := ps.List(ctx)
		is.Equal(len(got), 1)
		is.True(got[pl.ID] != nil)
		is.Equal(got[pl.ID].Status, expected)

		if expected == StatusRunning {
			pl, _ = ps.Get(ctx, pl.ID)
			is.NoErr(ps.Stop(ctx, pl.ID, false))
			is.NoErr(pl.Wait())
		}
	}

	testCases := []struct {
		have Status
		want Status
	}{
		{have: StatusRunning, want: StatusRunning},
		{have: StatusUserStopped, want: StatusUserStopped},
		{have: StatusSystemStopped, want: StatusRunning},
		{have: StatusDegraded, want: StatusDegraded},
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

// generatorSource creates a connector source that fills up the returned slice
// with generated records as they are produced. After producing the requested
// number of records it returns wantErr.
func generatorSource(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
	wantErr error,
	stop bool,
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
	sourcePlugin := pmock.NewConfigurableSourcePlugin(ctrl, sourcePluginOptions...)

	source := dummySource(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(sourcePlugin, nil)

	return source, dispenser
}

// asserterDestination creates a connector destination that checks if the records it gets
// match the expected records. On teardown it also makes sure that it received
// all expected records.
func asserterDestination(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
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

	destinationPlugin := pmock.NewConfigurableDestinationPlugin(ctrl, destinationPluginOptions...)

	dest := dummyDestination(persister)

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().Return(destinationPlugin, nil)

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

// testConnectorFetcher fulfills the ConnectorFetcher interface.
type testConnectorFetcher map[string]*connector.Instance

func (tcf testConnectorFetcher) Get(_ context.Context, id string) (*connector.Instance, error) {
	conn, ok := tcf[id]
	if !ok {
		return nil, connector.ErrInstanceNotFound
	}
	return conn, nil
}

func (tcf testConnectorFetcher) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	return tcf[testDLQID], nil
}

// testProcessorFetcher fulfills the ProcessorService interface.
type testProcessorFetcher map[string]*processor.Instance

func (tpf testProcessorFetcher) MakeRunnableProcessor(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
	return nil, cerrors.New("not implemented")
}

func (tpf testProcessorFetcher) Get(_ context.Context, id string) (*processor.Instance, error) {
	proc, ok := tpf[id]
	if !ok {
		return nil, processor.ErrInstanceNotFound
	}
	return proc, nil
}

// testPluginFetcher fulfills the PluginFetcher interface.
type testPluginFetcher map[string]connectorPlugin.Dispenser

func (tpf testPluginFetcher) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	plug, ok := tpf[name]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	return plug, nil
}
