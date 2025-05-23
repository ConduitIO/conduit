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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestWorker_repeatedTask(t *testing.T) {
	is := is.New(t)
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	_, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	err := taskNode.AppendToEnd(taskNode) // append same node to the end, creating a loop
	is.NoErr(err)

	worker, err := NewWorker(taskNode, nil, logger, noop.Timer{})
	is.True(err != nil)
	is.Equal(worker, nil)
}

func TestWorker_IsFirstTask(t *testing.T) {
	is := is.New(t)
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	_, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	worker, err := NewWorker(taskNode, nil, logger, noop.Timer{})
	is.NoErr(err)

	is.Equal(worker.FirstTask, taskNode)
	is.True(worker.FirstTask.IsFirst())
	is.True(!worker.FirstTask.Next[0].IsFirst())
}

func TestWorker_Ack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlqMock, _ := NewMockDLQ(ctrl, logger)

	batch := randomBatch(5)

	// Expect Source.Ack to be called with positions
	sourceMock.EXPECT().Ack(ctx, batch.positions).Return(nil)

	worker, err := NewWorker(taskNode, dlqMock, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Ack(ctx, batch)
	is.NoErr(err)
}

func TestWorker_Ack_SourceError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlqMock, _ := NewMockDLQ(ctrl, logger)

	batch := randomBatch(5)

	// Make Source.Ack return an error
	wantErr := cerrors.New("source error")
	sourceMock.EXPECT().Ack(ctx, batch.positions).Return(wantErr)

	worker, err := NewWorker(taskNode, dlqMock, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Ack(ctx, batch)
	is.True(cerrors.Is(err, wantErr))
}

func TestWorker_Nack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlq, dlqDestinationMock := NewMockDLQ(ctrl, logger)

	batch := randomBatch(3)

	// Nack all records in the batch
	wantErr := cerrors.New("record error")
	batch.Nack(0, wantErr, wantErr, wantErr)

	// Simulate DLQ writing all records successfully (DLQ.Do returns nil)
	destinationExpectSuccessfulWrites(is, dlqDestinationMock, batch.records)
	sourceMock.EXPECT().Ack(ctx, batch.positions).Return(nil)

	worker, err := NewWorker(taskNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Nack(ctx, batch, "taskID")
	is.NoErr(err)
}

func TestWorker_Nack_DLQError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	_, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlqMock, dlqDestinationMock := NewMockDLQ(ctrl, logger)

	batch := randomBatch(3)
	nackErr := cerrors.New("record error")
	batch.Nack(0, nackErr, nackErr, nackErr)

	wantErr := cerrors.New("dlq write error")
	dlqDestinationMock.EXPECT().Write(gomock.Any(), gomock.Any()).Return(wantErr)

	worker, err := NewWorker(taskNode, dlqMock, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Nack(ctx, batch, "taskID")
	is.True(cerrors.Is(err, wantErr))
}

func TestWorker_Nack_SplitRecord(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlq, dlqDestinationMock := NewMockDLQ(ctrl, logger)

	batch := randomBatch(3)
	originalBatch := batch.clone()

	// Simulate a split record (e.g. some records nacked)
	batch.SplitRecord(1, randomRecords(3))

	// Nack all records in the batch. Since the second record is split, we can
	// do this by only nacking one of the split records.
	wantErr := cerrors.New("record error")
	batch.Nack(0, wantErr)
	batch.Nack(1, wantErr) // Nack the split record, 2 and 3 are nacked as well.
	batch.Nack(4, wantErr)

	// Simulate DLQ writing all records successfully (DLQ.Do returns nil)
	destinationExpectSuccessfulWrites(is, dlqDestinationMock, originalBatch.records)
	sourceMock.EXPECT().Ack(ctx, originalBatch.positions).Return(nil)

	worker, err := NewWorker(taskNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Nack(ctx, batch, "taskID")
	is.NoErr(err)
}

func TestWorker_subBatchByFlag(t *testing.T) {
	is := is.New(t)
	logger := log.Test(t)
	ctrl := gomock.NewController(t)
	_, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	worker, err := NewWorker(taskNode, nil, logger, noop.Timer{})
	is.NoErr(err)

	batch := randomBatch(5)
	batch.Nack(4, cerrors.New("nack")) // Nack 4
	batch.Filter(2, 4)                 // Filter 2 and 3

	// Ack and Filter are grouped
	sub := worker.subBatchByFlag(batch, 0)
	is.Equal(len(sub.records), 4)
	is.Equal(cap(sub.records), 4) // Ensure capacity is correct
	is.Equal(sub.recordStatuses, []RecordStatus{
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
		{Flag: RecordFlagFilter},
		{Flag: RecordFlagFilter},
	})

	// Nack is alone
	sub2 := worker.subBatchByFlag(batch, 4)
	is.Equal(len(sub2.records), 1)
	is.Equal(cap(sub2.records), 1)
	is.Equal(sub2.recordStatuses[0].Flag, RecordFlagNack)

	// Out of bounds
	sub3 := worker.subBatchByFlag(batch, 5)
	is.Equal(sub3, nil)
}

func TestTaskNode_AppendToEnd(t *testing.T) {
	is := is.New(t)
	task1 := &TaskNode{}
	task2 := &TaskNode{}
	task3 := &TaskNode{}

	err := task1.AppendToEnd(task2)
	is.NoErr(err)
	is.True(task1.Next[0] == task2) // Check if the pointer is correct

	err = task1.AppendToEnd(task3)
	is.NoErr(err)
	is.True(task2.Next[0] == task3) // Check if the pointer is correct
}

func TestTaskNode_AppendToEnd_MultipleNext(t *testing.T) {
	is := is.New(t)
	task1 := &TaskNode{}
	task2 := &TaskNode{}
	task3 := &TaskNode{}
	task1.Next = []*TaskNode{task2, task3}

	err := task1.AppendToEnd(&TaskNode{})
	is.True(err != nil)
}

func TestTaskNode_Iterators(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)

	newTaskWithID := func(id string) Task {
		task := NewMockTask(ctrl)
		task.EXPECT().ID().Return(id).AnyTimes()
		return task
	}

	task1 := &TaskNode{Task: newTaskWithID("1")}
	task2 := &TaskNode{Task: newTaskWithID("2")}
	task3 := &TaskNode{Task: newTaskWithID("3")}
	task1.Next = []*TaskNode{task2}
	task2.Next = []*TaskNode{task3}

	var ids []string
	task1.TaskNodes()(func(n *TaskNode) bool {
		ids = append(ids, n.Task.ID())
		return true
	})
	is.Equal(ids, []string{"1", "2", "3"})

	var ids2 []string
	task1.Tasks()(func(t Task) bool {
		ids2 = append(ids2, t.ID())
		return true
	})
	is.Equal(ids2, []string{"1", "2", "3"})
}
