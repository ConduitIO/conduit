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

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline/stream"
	"github.com/conduitio/conduit/pkg/plugin"
	pmock "github.com/conduitio/conduit/pkg/plugin/mock"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
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
		&stream.DLQHandlerNode{},
		&stream.SourceNode{},
		&stream.SourceAckerNode{},
		&stream.MetricsNode{},
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

func TestServiceLifecycle_PipelineSuccess(t *testing.T) {
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
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, t, persister, wantRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, t, persister, nil, false)
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
}

func TestServiceLifecycle_PipelineError(t *testing.T) {
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
	wantErr := cerrors.New("source connector error")
	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, wantErr, true)
	destination, destDispenser := asserterDestination(ctrl, t, persister, wantRecords, true)
	dlq, dlqDispenser := asserterDestination(ctrl, t, persister, nil, true)
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

	// wait for pipeline to finish
	err = pl.Wait()
	is.True(err != nil)

	is.Equal(StatusDegraded, pl.Status)
	// pipeline errors contain only string messages, so we can only compare the errors by the messages
	is.True(
		strings.Contains(pl.Error, fmt.Sprintf("node %s stopped with error:", source.ID)),
	) // expected error message to have "node <source id> stopped with error"
	is.True(
		strings.Contains(pl.Error, wantErr.Error()),
	) // expected error message to contain "source connector error"
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
	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, ErrGracefulShutdown, true)
	destination, destDispenser := asserterDestination(ctrl, t, persister, wantRecords, true)
	dlq, dlqDispenser := asserterDestination(ctrl, t, persister, nil, true)
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
		logger := log.New(zerolog.Nop())
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
			source, sourceDispenser = generatorSource(ctrl, persister, nil, nil, false)
			destination, destDispenser = asserterDestination(ctrl, t, persister, nil, false)
			dlq, dlqDispenser = asserterDestination(ctrl, t, persister, nil, false)
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

func generateRecords(count int) []record.Record {
	records := make([]record.Record, count)
	for i := 0; i < count; i++ {
		records[i] = record.Record{
			Key: record.RawData{Raw: []byte(uuid.NewString())},
			Payload: record.Change{
				Before: record.RawData{},
				After:  record.RawData{Raw: []byte(uuid.NewString())},
			},
			Position: record.Position(strconv.Itoa(i)),
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
	records []record.Record,
	wantErr error,
	teardown bool,
) (*connector.Instance, *pmock.Dispenser) {
	position := 0
	recordCount := len(records)

	sourcePlugin := pmock.NewSourcePlugin(ctrl)
	sourcePlugin.EXPECT().Start(gomock.Any(), nil).Return(nil).Times(1)
	sourcePlugin.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	if teardown {
		sourcePlugin.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
	}

	sourcePlugin.EXPECT().Ack(gomock.Any(), gomock.Any()).Return(nil).Times(recordCount)
	sourcePlugin.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Record, error) {
		if position == recordCount {
			if wantErr != nil {
				return record.Record{}, wantErr
			}
			<-ctx.Done()
			return record.Record{}, ctx.Err()
		}
		r := records[position]
		position++
		return r, nil
	}).MinTimes(recordCount + 1)

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
	t *testing.T,
	persister *connector.Persister,
	want []record.Record,
	teardown bool,
) (*connector.Instance, *pmock.Dispenser) {
	is := is.New(t)
	rchan := make(chan record.Record, 1)
	recordCount := 0

	destinationPlugin := pmock.NewDestinationPlugin(ctrl)
	destinationPlugin.EXPECT().Start(gomock.Any()).Return(nil).Times(1)
	destinationPlugin.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	if teardown {
		var lastPosition record.Position
		if len(want) > 0 {
			lastPosition = want[len(want)-1].Position
		}
		destinationPlugin.EXPECT().Stop(gomock.Any(), lastPosition).Return(nil).Times(1)
		destinationPlugin.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
			close(rchan)
			return nil
		}).Times(1)
	}

	destinationPlugin.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, r record.Record) error {
		position, err := strconv.Atoi(r.Position.String())
		is.NoErr(err)
		// Conduit enriches metadata, assert and copy it over into the expectation
		is.Equal(len(r.Metadata), 1)
		want[position].Metadata = r.Metadata

		is.Equal(want[position], r)
		recordCount++
		rchan <- r
		return nil
	}).AnyTimes()
	destinationPlugin.EXPECT().Ack(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Position, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r, ok := <-rchan:
			if !ok {
				return nil, nil
			}
			return r.Position, nil
		}
	}).AnyTimes()
	t.Cleanup(func() {
		is.Equal(len(want), recordCount)
	})

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

func (tcf testConnectorFetcher) Get(ctx context.Context, id string) (*connector.Instance, error) {
	conn, ok := tcf[id]
	if !ok {
		return nil, connector.ErrInstanceNotFound
	}
	return conn, nil
}

func (tcf testConnectorFetcher) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	return tcf[testDLQID], nil
}

// testProcessorFetcher fulfills the ProcessorFetcher interface.
type testProcessorFetcher map[string]*processor.Instance

func (tpf testProcessorFetcher) Get(ctx context.Context, id string) (*processor.Instance, error) {
	proc, ok := tpf[id]
	if !ok {
		return nil, processor.ErrInstanceNotFound
	}
	return proc, nil
}

// testPluginFetcher fulfills the PluginFetcher interface.
type testPluginFetcher map[string]plugin.Dispenser

func (tpf testPluginFetcher) NewDispenser(logger log.CtxLogger, name string) (plugin.Dispenser, error) {
	plug, ok := tpf[name]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	return plug, nil
}
