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

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func randomBatch(count int) *Batch {
	records := randomRecords(count)
	return NewBatch(records)
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

func markMultiRecord(i int, records []opencdc.Record) func([]sdk.ProcessedRecord) {
	return func(processed []sdk.ProcessedRecord) {
		processed[i] = sdk.MultiRecord(records)
	}
}

func newMockTasks(ctrl *gomock.Controller, ids ...string) (*MockSource, []*MockTask, *TaskNode) {
	if len(ids) == 0 {
		panic("invalid use, must provide at least one id")
	}

	// Create first task (source task)
	sourceTask := NewMockSourceTask(ctrl)
	sourceTask.EXPECT().ID().Return(ids[0]).AnyTimes()

	tasks := []*MockTask{sourceTask.MockTask}
	firstNode := &TaskNode{Task: sourceTask}
	curNode := firstNode

	for _, id := range ids[1:] {
		task := NewMockTask(ctrl)
		task.EXPECT().ID().Return(id).AnyTimes()

		tasks = append(tasks, task)

		curNode.Next = []*TaskNode{{Task: task}}
		curNode = curNode.Next[0]
	}

	return sourceTask.source, tasks, firstNode
}

func destinationExpectSuccessfulWrites(
	is *is.I,
	destination *MockDestination,
	wantRecords []opencdc.Record,
) {
	destination.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, gotRecords []opencdc.Record) error {
		is.Equal(len(gotRecords), len(wantRecords))
		for i, got := range gotRecords {
			is.Equal(got.Position, wantRecords[i].Position)
		}
		return nil
	})
	destination.EXPECT().Ack(gomock.Any()).Return(
		toDestinationAcks(wantRecords, nil),
		nil,
	)
}

func toDestinationAcks(records []opencdc.Record, errs []error) []connector.DestinationAck {
	acks := make([]connector.DestinationAck, len(records))
	for i, record := range records {
		var ackErr error
		if i < len(errs) {
			ackErr = errs[i]
		}
		acks[i] = connector.DestinationAck{
			Position: record.Position,
			Error:    ackErr,
		}
	}
	return acks
}

// -- Mock Source Task --

type MockSourceTask struct {
	*MockTask
	source *MockSource
}

func NewMockSourceTask(ctrl *gomock.Controller) *MockSourceTask {
	return &MockSourceTask{
		MockTask: NewMockTask(ctrl),
		source:   NewMockSource(ctrl),
	}
}

// GetSource mocks base method.
func (m *MockSourceTask) GetSource() Source {
	return m.source
}

// -- Test DLQ --

func NewMockDLQ(
	ctrl *gomock.Controller,
	logger log.CtxLogger,
	windowSizeAndThreshold ...int,
) (*DLQ, *MockDestination) {
	mockDestination := NewMockDestination(ctrl)

	windowSize, windowNackThreshold := 0, 0
	switch len(windowSizeAndThreshold) {
	case 2:
		windowNackThreshold = windowSizeAndThreshold[1]
		fallthrough
	case 1:
		windowSize = windowSizeAndThreshold[0]
	}

	return NewDLQ(
		"test-dlq",
		mockDestination,
		logger,
		NoOpConnectorMetrics{},
		windowSize,
		windowNackThreshold,
	), mockDestination
}
