// Copyright © 2025 Meroxa, Inc.
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

package funnel

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDestinationTask_Do_Simple(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	destinationMock.EXPECT().Write(ctx, records).Return(nil)

	acks := make([]connector.DestinationAck, len(records))
	for i, record := range records {
		acks[i] = connector.DestinationAck{
			Position: record.Position,
			Error:    nil,
		}
	}

	destinationMock.EXPECT().Ack(ctx).Return(acks, nil)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), records)
	for _, status := range batch.recordStatuses {
		is.Equal(status, RecordStatus{Flag: RecordFlagAck})
	}
}

func TestDestinationTask_Do_WriteFailure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)

	wantErr := cerrors.New("error")
	destinationMock.EXPECT().Write(ctx, records).Return(wantErr)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.True(cerrors.Is(err, wantErr))
}

func TestDestinationTask_Do_FilteredAndNacks(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)

	// Records with index 0, 2, 3 and 9 are filtered out
	batch.Filter(0)    // 0
	batch.Filter(2, 4) // 2, 3
	batch.Filter(9)    // 9

	activeRecords := batch.ActiveRecords()
	is.Equal(activeRecords, []opencdc.Record{records[1], records[4], records[5], records[6], records[7], records[8]})

	destinationMock.EXPECT().Write(ctx, activeRecords).Return(nil)

	acks := make([]connector.DestinationAck, len(activeRecords))
	wantErr := cerrors.New("error")
	for i, record := range activeRecords {
		ack := connector.DestinationAck{
			Position: record.Position,
			Error:    nil,
		}
		if i == 0 || i == 2 || i == 4 {
			// records 1, 5 and 7 are nacked
			ack.Error = wantErr
		}
		acks[i] = ack
	}

	destinationMock.EXPECT().Ack(ctx).Return(acks, nil)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), activeRecords)
	for i, status := range batch.recordStatuses {
		switch i {
		case 0, 2, 3, 9:
			is.Equal(status, RecordStatus{Flag: RecordFlagFilter})
		case 1, 5, 7:
			is.Equal(status, RecordStatus{Flag: RecordFlagNack, Error: wantErr})
		default:
			is.Equal(status, RecordStatus{Flag: RecordFlagAck})
		}
	}
}

func TestDestinationTask_Do_MultipleAcks(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	destinationMock.EXPECT().Write(ctx, records).Return(nil)

	acks := make([]connector.DestinationAck, len(records))
	for i, record := range records {
		acks[i] = connector.DestinationAck{
			Position: record.Position,
			Error:    nil,
		}
	}

	call := destinationMock.EXPECT().Ack(ctx).Return(acks[0:3], nil).Call
	call = destinationMock.EXPECT().Ack(ctx).Return(acks[3:6], nil).After(call)
	call = destinationMock.EXPECT().Ack(ctx).Return(acks[6:7], nil).After(call)
	destinationMock.EXPECT().Ack(ctx).Return(acks[7:10], nil).After(call)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), records)
	for _, status := range batch.recordStatuses {
		is.Equal(status, RecordStatus{Flag: RecordFlagAck})
	}
}

func TestDestinationTask_Do_UnexpectedPosition(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	destinationMock.EXPECT().Write(ctx, records).Return(nil)

	acks := make([]connector.DestinationAck, len(records))
	for i, record := range records {
		acks[i] = connector.DestinationAck{
			Position: record.Position,
			Error:    nil,
		}
	}

	call := destinationMock.EXPECT().Ack(ctx).Return(acks[0:3], nil).Call
	// Note that we're skipping record 3 here
	destinationMock.EXPECT().Ack(ctx).Return(acks[4:8], nil).After(call)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.Equal(err.Error(), `failed to validate acks: received unexpected ack, expected position "3" but got "4"`)
}

func TestDestinationTask_Do_TooManyAcks(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	destinationMock.EXPECT().Write(ctx, records).Return(nil)

	// Note that we're creating more acks than records
	acks := make([]connector.DestinationAck, len(records)+1)

	destinationMock.EXPECT().Ack(ctx).Return(acks, nil)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.Equal(err.Error(), `failed to validate acks: received 11 acks, but expected at most 10`)
}

func TestDestinationTask_Do_FailedToReceiveAcks(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	destinationMock.EXPECT().Write(gomock.Any(), records).Return(nil)

	acks := make([]connector.DestinationAck, len(records))
	for i, record := range records {
		acks[i] = connector.DestinationAck{
			Position: record.Position,
			Error:    nil,
		}
	}

	// Note that we're returning only the first 9 acks, the second call blocks forever
	call := destinationMock.EXPECT().Ack(gomock.Any()).Return(acks[:9], nil).Call
	destinationMock.EXPECT().Ack(gomock.Any()).DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}).After(call)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})

	// We need to run Do in a goroutine, as we expect it to block forever
	var wg csync.WaitGroup
	wg.Add(1)
	var doErr error

	doCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		defer wg.Done()
		doErr = task.Do(doCtx, batch)
	}()

	err := wg.WaitTimeout(ctx, 100*time.Millisecond)
	is.Equal(err, context.DeadlineExceeded)

	cancel()
	err = wg.WaitTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.Equal(doErr.Error(), `failed to receive acks for 10 records from destination: context canceled`)
}
