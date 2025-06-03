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
	"slices"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestProcessorTask_Do_Passthrough(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	processorMock.EXPECT().Process(ctx, records).Return(toProcessedRecords(records))

	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), records)
	for _, status := range batch.recordStatuses {
		is.Equal(status, RecordStatus{Flag: RecordFlagAck})
	}
}

func TestProcessorTask_Do_BatchWithFilteredRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)

	records := randomRecords(10)
	batch := NewBatch(slices.Clone(records))

	// Records with index 0, 2, 3 and 9 are filtered out
	batch.Filter(9)    // 9
	batch.Filter(2, 4) // 2, 3
	batch.Filter(0)    // 0

	activeRecords := batch.ActiveRecords()
	is.Equal(activeRecords, []opencdc.Record{records[1], records[4], records[5], records[6], records[7], records[8]})

	multiRecord := randomRecords(3)

	wantErr := cerrors.New("error")
	processorMock.EXPECT().Process(ctx, activeRecords).Return(
		toProcessedRecords(
			activeRecords[:5],               // last record (index 8) is not processed and should be retried
			markFiltered(0),                 // index 1 is filtered
			markFiltered(2),                 // index 5 is filtered
			markMultiRecord(3, multiRecord), // index 6 is a multi-record
			markErrored(4, wantErr),         // index 7 is errored
		),
	)

	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), []opencdc.Record{records[4], multiRecord[0], multiRecord[1], multiRecord[2], records[7], records[8]})
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter},               // 0
		{Flag: RecordFlagFilter},               // 1
		{Flag: RecordFlagFilter},               // 2
		{Flag: RecordFlagFilter},               // 3
		{Flag: RecordFlagAck},                  // 4
		{Flag: RecordFlagFilter},               // 5
		{Flag: RecordFlagAck},                  // 6 (multi-record 0)
		{Flag: RecordFlagAck},                  // 6 (multi-record 1)
		{Flag: RecordFlagAck},                  // 6 (multi-record 2)
		{Flag: RecordFlagNack, Error: wantErr}, // 7
		{Flag: RecordFlagRetry},                // 8
		{Flag: RecordFlagFilter},               // 9
	})
	is.Equal(batch.splitRecords, map[string]opencdc.Record{
		records[6].Position.String(): records[6],
	})
	is.Equal(batch.filterCount, 6)
}

func TestProcessorTask_Do_MultiRecord(t *testing.T) {
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)
	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})

	records := randomRecords(5)
	batch := NewBatch(slices.Clone(records))

	t.Run("MultiRecord with 0 records filters the record", func(t *testing.T) {
		is := is.New(t)
		batch := batch.clone()

		processorMock.EXPECT().Process(ctx, batch.records).Return(
			toProcessedRecords(
				batch.records,
				markMultiRecord(0, []opencdc.Record{}),
			),
		)

		err := task.Do(ctx, batch)
		is.NoErr(err)

		is.Equal(batch.ActiveRecords(), []opencdc.Record{records[1], records[2], records[3], records[4]})
		is.Equal(batch.recordStatuses, []RecordStatus{
			{Flag: RecordFlagFilter}, // 0
			{Flag: RecordFlagAck},    // 1
			{Flag: RecordFlagAck},    // 2
			{Flag: RecordFlagAck},    // 3
			{Flag: RecordFlagAck},    // 4
		})
	})

	t.Run("MultiRecord with 1 record sets the record", func(t *testing.T) {
		is := is.New(t)
		batch := batch.clone()

		wantRecord := randomRecords(1)[0]
		processorMock.EXPECT().Process(ctx, batch.records).Return(
			toProcessedRecords(
				batch.records,
				markMultiRecord(0, []opencdc.Record{wantRecord}),
			),
		)

		err := task.Do(ctx, batch)
		is.NoErr(err)

		is.Equal(batch.ActiveRecords(), []opencdc.Record{wantRecord, records[1], records[2], records[3], records[4]})
		is.Equal(batch.recordStatuses, []RecordStatus{
			{Flag: RecordFlagAck}, // 0
			{Flag: RecordFlagAck}, // 1
			{Flag: RecordFlagAck}, // 2
			{Flag: RecordFlagAck}, // 3
			{Flag: RecordFlagAck}, // 4
		})
	})

	t.Run("MultiRecord with >1 records splits the record", func(t *testing.T) {
		is := is.New(t)
		batch := batch.clone()

		wantRecords := randomRecords(2)
		processorMock.EXPECT().Process(ctx, batch.records).Return(
			toProcessedRecords(
				batch.records,
				markMultiRecord(0, wantRecords),
			),
		)

		err := task.Do(ctx, batch)
		is.NoErr(err)

		is.Equal(batch.ActiveRecords(), []opencdc.Record{wantRecords[0], wantRecords[1], records[1], records[2], records[3], records[4]})
		is.Equal(batch.recordStatuses, []RecordStatus{
			{Flag: RecordFlagAck}, // 0 (MultiRecord 0)
			{Flag: RecordFlagAck}, // 0 (MultiRecord 1)
			{Flag: RecordFlagAck}, // 1
			{Flag: RecordFlagAck}, // 2
			{Flag: RecordFlagAck}, // 3
			{Flag: RecordFlagAck}, // 4
		})
	})
}
