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
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/jpillora/backoff"
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
	b := &backoff.Backoff{}

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
		b,
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

	got, err := ls.buildRunnablePipeline(
		ctx,
		pl,
	)

	is.NoErr(err)

	is.Equal("", cmp.Diff(pl, got.pipeline, cmpopts.IgnoreUnexported(pipeline.Instance{})))

	wantTasks := []funnel.Task{
		&funnel.SourceTask{},
		&funnel.DestinationTask{},
	}
	is.Equal(len(got.w.Tasks), len(wantTasks))
	for i := range got.w.Tasks {
		want := wantTasks[i]
		got := got.w.Tasks[i]
		is.Equal(reflect.TypeOf(want), reflect.TypeOf(got)) // unexpected task type
	}
	is.Equal(got.w.Order, funnel.Order{{1}, nil})
	is.Equal(got.w.Source.(*connector.Source).Instance, source)
}

func TestService_buildRunnablePipeline_NoSourceNode(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	ctrl := gomock.NewController(t)
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	b := &backoff.Backoff{}

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

	ls := NewService(logger, b, testConnectorService{
		destination.ID: destination,
		testDLQID:      dlq,
	}, testProcessorService{},
		testConnectorPluginService{
			destination.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:         pmock.NewDispenser(ctrl),
		}, testPipelineService{})

	wantErr := "can't build pipeline without any source connectors"

	got, err := ls.buildRunnablePipeline(
		ctx,
		pl,
	)

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
	b := &backoff.Backoff{}

	source := dummySource(persister)
	dlq := dummyDestination(persister)

	ls := NewService(logger, b, testConnectorService{
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

	got, err := ls.buildRunnablePipeline(
		ctx,
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
	b := &backoff.Backoff{}

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, b, testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
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
	b := &backoff.Backoff{}

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	wantErr := cerrors.New("source connector error")
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, wantErr, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, b, testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
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

func TestServiceLifecycle_PipelineStop(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	b := &backoff.Backoff{}

	ps := pipeline.NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// create mocked connectors
	// source will stop and return ErrGracefulShutdown which should signal to the
	// service that everything went well and the pipeline was gracefully shutdown
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, b, testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
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
	err = ls.StopAll(ctx, false)
	is.NoErr(err)

	// wait for pipeline to finish
	err = ls.WaitPipeline(pl.ID)
	is.NoErr(err)

	is.Equal(pipeline.StatusSystemStopped, pl.GetStatus())
	is.Equal("", pl.Error)
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
	stop bool,
) (*connector.Instance, *pmock.Dispenser) {
	destinationPluginOptions := []pmock.ConfigurableDestinationPluginOption{
		pmock.DestinationPluginWithConfigure(),
		pmock.DestinationPluginWithOpen(),
		pmock.DestinationPluginWithRun(),
		pmock.DestinationPluginWithRecords(records),
		pmock.DestinationPluginWithTeardown(),
	}

	if stop {
		var lastPosition opencdc.Position
		if len(records) > 0 {
			lastPosition = records[len(records)-1].Position
		}
		destinationPluginOptions = append(destinationPluginOptions, pmock.DestinationPluginWithStop(lastPosition))
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
