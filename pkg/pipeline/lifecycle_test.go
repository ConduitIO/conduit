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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestServiceLifecycle_PipelineSuccess(t *testing.T) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}

	ps := NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"})
	assert.Ok(t, err)

	// create mocked connectors
	ctrl := gomock.NewController(t)
	source, wantRecords := generatorSource(ctrl, 10, nil, false)
	destination := asserterDestination(ctrl, t, wantRecords, false)

	pl, err = ps.AddConnector(ctx, pl, source.ID())
	assert.Ok(t, err)
	pl, err = ps.AddConnector(ctx, pl, destination.ID())
	assert.Ok(t, err)

	// start the pipeline now that everything is set up
	err = ps.Start(
		ctx,
		testConnectorFetcher{source.ID(): source, destination.ID(): destination},
		testProcessorFetcher{},
		pl,
	)
	assert.Ok(t, err)

	// wait for pipeline to finish consuming records from the source
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, StatusRunning, pl.Status)
	assert.Equal(t, "", pl.Error)
}

func TestServiceLifecycle_PipelineError(t *testing.T) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}

	ps := NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"})
	assert.Ok(t, err)

	// create mocked connectors
	wantErr := cerrors.New("source connector error")
	ctrl := gomock.NewController(t)
	source, wantRecords := generatorSource(ctrl, 10, wantErr, true)
	destination := asserterDestination(ctrl, t, wantRecords, true)

	pl, err = ps.AddConnector(ctx, pl, source.ID())
	assert.Ok(t, err)
	pl, err = ps.AddConnector(ctx, pl, destination.ID())
	assert.Ok(t, err)

	// start the pipeline now that everything is set up
	err = ps.Start(
		ctx,
		testConnectorFetcher{source.ID(): source, destination.ID(): destination},
		testProcessorFetcher{},
		pl,
	)
	assert.Ok(t, err)

	// wait for pipeline to finish
	err = pl.Wait()
	assert.Error(t, err)
	t.Log(err)

	assert.Equal(t, StatusDegraded, pl.Status)
	// pipeline errors contain only string messages, so we can only compare the errors by the messages
	assert.True(
		t,
		strings.Contains(pl.Error, fmt.Sprintf("node %s stopped with error:", source.ID())),
		`expected error message to have "node <source id> stopped with error"`,
	)
	assert.True(
		t,
		strings.Contains(pl.Error, wantErr.Error()),
		"expected error message to have: "+wantErr.Error(),
	)
}

func TestServiceLifecycle_PipelineStop(t *testing.T) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}

	ps := NewService(logger, db)

	// create a host pipeline
	pl, err := ps.Create(ctx, uuid.NewString(), Config{Name: "test pipeline"})
	assert.Ok(t, err)

	// create mocked connectors
	ctrl := gomock.NewController(t)
	// source will stop returning ErrGracefulShutdown which should signal to the
	// service that everything went well and the pipeline was gracefully shutdown
	source, wantRecords := generatorSource(ctrl, 10, ErrGracefulShutdown, true)
	destination := asserterDestination(ctrl, t, wantRecords, true)

	pl, err = ps.AddConnector(ctx, pl, source.ID())
	assert.Ok(t, err)
	pl, err = ps.AddConnector(ctx, pl, destination.ID())
	assert.Ok(t, err)

	// start the pipeline now that everything is set up
	err = ps.Start(
		ctx,
		testConnectorFetcher{source.ID(): source, destination.ID(): destination},
		testProcessorFetcher{},
		pl,
	)
	assert.Ok(t, err)

	// wait for pipeline to finish
	err = pl.Wait()
	assert.Ok(t, err)

	assert.Equal(t, StatusSystemStopped, pl.Status)
	assert.Equal(t, "", pl.Error)
}

// testConnectorFetcher fulfills the ConnectorFetcher interface.
type testConnectorFetcher map[string]connector.Connector

func (tcf testConnectorFetcher) Get(ctx context.Context, id string) (connector.Connector, error) {
	conn, ok := tcf[id]
	if !ok {
		return nil, connector.ErrInstanceNotFound
	}
	return conn, nil
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

// generatorSource creates a connector source that fills up the returned slice
// with generated records as they are produced. After producing the requested
// number of records it returns wantErr.
func generatorSource(ctrl *gomock.Controller, recordCount int, wantErr error, teardown bool) (connector.Source, []record.Record) {
	position := 0
	records := make([]record.Record, recordCount)
	for i := 0; i < recordCount; i++ {
		records[i] = record.Record{
			Key:      record.RawData{Raw: []byte(uuid.NewString())},
			Payload:  record.RawData{Raw: []byte(uuid.NewString())},
			Position: record.Position(strconv.Itoa(i)),
		}
	}

	source := basicSourceMock(ctrl)
	if teardown {
		source.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
	}
	source.EXPECT().Errors().Return(make(chan error))
	source.EXPECT().Ack(gomock.Any(), gomock.Any()).Return(nil).Times(recordCount)
	source.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Record, error) {
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

	return source, records
}

func basicSourceMock(ctrl *gomock.Controller) *connmock.Source {
	source := connmock.NewSource(ctrl)
	source.EXPECT().ID().Return(uuid.NewString()).AnyTimes()
	source.EXPECT().Type().Return(connector.TypeSource).AnyTimes()
	source.EXPECT().Config().Return(connector.Config{}).AnyTimes()
	source.EXPECT().Open(gomock.Any()).Return(nil).Times(1)

	return source
}

// asserterDestination creates a connector destination that checks if the records it gets
// match the expected records. On teardown it also makes sure that it received
// all expected records.
func asserterDestination(ctrl *gomock.Controller, t *testing.T, want []record.Record, teardown bool) connector.Destination {
	rchan := make(chan record.Record, 1)
	recordCount := 0

	destination := connmock.NewDestination(ctrl)
	destination.EXPECT().ID().Return(uuid.NewString()).AnyTimes()
	destination.EXPECT().Type().Return(connector.TypeDestination).AnyTimes()
	destination.EXPECT().Config().Return(connector.Config{}).AnyTimes()
	destination.EXPECT().Open(gomock.Any()).Return(nil).Times(1)
	destination.EXPECT().Errors().Return(make(chan error))
	if teardown {
		destination.EXPECT().Stop(gomock.Any(), want[len(want)-1].Position).Return(nil).Times(1)
		destination.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
			close(rchan)
			return nil
		}).Times(1)
	}
	destination.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, r record.Record) error {
		position, err := strconv.Atoi(r.Position.String())
		assert.Ok(t, err)
		assert.Equal(t, want[position], r)
		recordCount++
		rchan <- r
		return nil
	}).AnyTimes()
	destination.EXPECT().Ack(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Position, error) {
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
		assert.Equal(t, len(want), recordCount)
	})
	return destination
}
