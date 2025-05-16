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
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestProcessorTask_Do_Passthrough(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := mock.NewProcessor(ctrl)

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
	processorMock := mock.NewProcessor(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)

	// Records with index 0, 2, 3 and 9 are filtered out
	batch.Filter(0)    // 0
	batch.Filter(2, 4) // 2, 3
	batch.Filter(9)    // 9

	activeRecords := batch.ActiveRecords()
	is.Equal(activeRecords, []opencdc.Record{records[1], records[4], records[5], records[6], records[7], records[8]})

	wantErr := cerrors.New("error")
	processorMock.EXPECT().Process(ctx, activeRecords).Return(
		toProcessedRecords(
			activeRecords[:5],       // last record (index 8) is not processed and should be retried
			markFiltered(0),         // index 1 is filtered
			markFiltered(2),         // index 5 is filtered
			markErrored(4, wantErr), // index 7 is errored
		),
	)

	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), []opencdc.Record{records[4], records[6], records[7], records[8]})
	for i, status := range batch.recordStatuses {
		switch i {
		case 0, 1, 2, 3, 5, 9:
			is.Equal(status, RecordStatus{Flag: RecordFlagFilter})
		case 7:
			is.Equal(status, RecordStatus{Flag: RecordFlagNack, Error: wantErr})
		case 8:
			is.Equal(status, RecordStatus{Flag: RecordFlagRetry})
		default:
			is.Equal(status, RecordStatus{Flag: RecordFlagAck})
		}
	}
}

func randomRecords(count int) []opencdc.Record {
	records := make([]opencdc.Record, count)
	for i := 0; i < count; i++ {
		records[i] = opencdc.Record{
			Position:  opencdc.Position(strconv.Itoa(i)),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key": "value"},
			Payload:   opencdc.Change{After: opencdc.RawData("value")},
		}
	}
	return records
}

func toProcessedRecords(records []opencdc.Record, modifiers ...func([]sdk.ProcessedRecord)) []sdk.ProcessedRecord {
	processed := make([]sdk.ProcessedRecord, len(records))
	for i, record := range records {
		processed[i] = sdk.SingleRecord(record)
	}
	for _, modifier := range modifiers {
		modifier(processed)
	}
	return processed
}

func markFiltered(i int) func([]sdk.ProcessedRecord) {
	return func(records []sdk.ProcessedRecord) {
		records[i] = sdk.FilterRecord{}
	}
}

func markErrored(i int, err error) func([]sdk.ProcessedRecord) {
	return func(records []sdk.ProcessedRecord) {
		records[i] = sdk.ErrorRecord{
			Error: err,
		}
	}
}
