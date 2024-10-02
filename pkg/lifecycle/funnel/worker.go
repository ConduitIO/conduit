// Copyright © 2024 Meroxa, Inc.
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
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/rollback"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/sourcegraph/conc/pool"
)

// TODO update docs once done.
type Task interface {
	// ID returns the identifier of this Task. Each Task in a pipeline must be
	// uniquely identified by the ID.
	ID() string

	Open(context.Context) error
	Close(context.Context) error

	// Do processes the given batch of records. It returns the count of
	// successfully processed records, the count of failed records, and an error
	// that caused the failed records. Conduit interprets the counts as
	// continuous regions in the batch, so the first `successCount` records are
	// considered successfully processed, and the next `failCount` records are
	// considered failed, the rest are considered skipped entirely. The failed
	// records are sent to the DLQ and if the nack limit is not reached, the
	// rest is retried.
	Do(context.Context, *Batch) error
}

// Worker is a collection of tasks that are executed sequentially. It is safe
// for concurrent use.
type Worker struct {
	Source Source
	Tasks  []Task
	// TasksOrder show next task to be executed. Multiple indices are used to
	// show parallel execution of tasks.
	//
	// Example:
	// [[1], [2], [3], [4,6], [5], [], [7], []]
	//
	//                   /-> 4, 5
	// 0 -> 1 -> 2 -> 3 --
	//                   \-> 6, 7
	Order [][]int
	DLQ   *DLQ

	lastReadAt time.Time
	timer      metrics.Timer
	stop       atomic.Bool

	logger log.CtxLogger
}

func NewWorker(
	tasks []Task,
	order [][]int,
	dlq *DLQ,
	logger log.CtxLogger,
	timer metrics.Timer,
) (*Worker, error) {
	err := validateTaskOrder(tasks, order)
	if err != nil {
		return nil, cerrors.Errorf("invalid task order: %w", err)
	}

	st, ok := tasks[0].(interface{ GetSource() Source })
	if !ok {
		return nil, cerrors.Errorf("first task must be a source task, got %T", tasks[0])
	}

	return &Worker{
		Source: st.GetSource(),
		Tasks:  tasks,
		Order:  order,
		DLQ:    dlq,
		logger: logger.WithComponent("funnel.Worker"),
		timer:  timer,
	}, nil
}

func validateTaskOrder(tasks []Task, order [][]int) error {
	// Traverse the tasks according to the order and validate that each task
	// is included exactly once.
	if len(order) != len(tasks) {
		return cerrors.Errorf("order length (%d) does not match tasks length (%d)", len(order), len(tasks))
	}
	seenCount := make([]int, len(tasks))
	var traverse func(i int) error
	traverse = func(i int) error {
		if i < 0 || i >= len(tasks) {
			return cerrors.Errorf("invalid index (%d), expected a number between 0 and %d", i, len(tasks)-1)
		}
		seenCount[i]++
		if seenCount[i] > 1 {
			return cerrors.Errorf("task %d included multiple times in order", i)
		}
		for _, nextIdx := range order[i] {
			if nextIdx == i {
				return cerrors.Errorf("task %d cannot call itself as next task", i)
			}
			err := traverse(nextIdx)
			if err != nil {
				return err
			}
		}
		return nil
	}
	err := traverse(0)
	if err != nil {
		return err
	}
	for i, count := range seenCount {
		if count == 0 {
			return cerrors.Errorf("task %d not included in order", i)
		}
	}
	return nil
}

func (w *Worker) Open(ctx context.Context) (err error) {
	var r rollback.R
	defer func() {
		rollbackErr := r.Execute()
		err = cerrors.LogOrReplace(err, rollbackErr, func() {
			w.logger.Err(ctx, rollbackErr).Msg("failed to execute rollback")
		})
	}()

	for _, task := range w.Tasks {
		err = task.Open(ctx)
		if err != nil {
			return cerrors.Errorf("task %s failed to open: %w", task.ID(), err)
		}

		r.Append(func() error {
			return task.Close(ctx)
		})
	}

	err = w.DLQ.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open DLQ: %w", err)
	}

	r.Skip()
	return nil
}

// Stop stops the worker from processing more records. It does not stop the
// current batch from being processed.
func (w *Worker) Stop() {
	w.stop.Store(true)
}

func (w *Worker) Close(ctx context.Context) error {
	var errs []error

	for _, task := range w.Tasks {
		err := task.Close(ctx)
		if err != nil {
			errs = append(errs, cerrors.Errorf("task %s failed to close: %w", task.ID(), err))
		}
	}

	err := w.DLQ.Close(ctx)
	if err != nil {
		errs = append(errs, cerrors.Errorf("failed to close DLQ: %w", err))
	}

	return cerrors.Join(errs...)
}

// Do processes records from the source until the worker is stopped. It returns
// no error if the worker is stopped gracefully.
func (w *Worker) Do(ctx context.Context) error {
	for {
		if w.stop.Load() || ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.doTask(ctx, 0, &Batch{}, w); err != nil {
			return err
		}
	}
}

func (w *Worker) doTask(ctx context.Context, currentIndex int, b *Batch, acker ackNacker) error {
	t := w.Tasks[currentIndex]

	w.logger.Trace(ctx).
		Str("task_id", t.ID()).
		Int("batch_size", len(b.records)).
		Msg("executing task")

	err := t.Do(ctx, b)
	if err != nil {
		return cerrors.Errorf("task %s: %w", t.ID(), err)
	}
	if currentIndex == 0 {
		// Store last time we read a batch from the source for metrics.
		w.lastReadAt = time.Now()
	}

	if !b.tainted {
		w.logger.Trace(ctx).
			Str("task_id", t.ID()).
			Msg("task returned clean batch")

		// Shortcut.
		if !w.hasNextTask(currentIndex) {
			// This is the last task, the batch has made it end-to-end, let's ack!
			return acker.Ack(ctx, b)
		}
		// There is at least one task after this one, let's continue.
		return w.nextTask(ctx, currentIndex, b, acker)
	}

	w.logger.Trace(ctx).
		Str("task_id", t.ID()).
		Msg("task returned tainted batch, splitting into sub-batches")

	// Batch is tainted, we need to go through all statuses and group them by
	// status before further processing.
	idx := 0
	for {
		subBatch := w.subBatchByFlag(b, idx)
		if subBatch == nil {
			w.logger.Trace(ctx).Msg("processed last batch")
			break
		}

		w.logger.Trace(ctx).
			Str("task_id", t.ID()).
			Int("batch_size", len(b.records)).
			Str("record_flag", b.recordStatuses[0].Flag.String()).
			Msg("collected sub-batch")

		switch subBatch.recordStatuses[0].Flag {
		case RecordFlagAck, RecordFlagSkip:
			if !w.hasNextTask(currentIndex) {
				// This is the last task, the batch has made it end-to-end, let's ack!
				// We need to ack all the records in the batch, not only active
				// ones, filtered ones should also be acked.
				err := acker.Ack(ctx, subBatch)
				if err != nil {
					return err
				}
				break // break switch
			}
			// There is at least one task after this one, let's continue.
			err := w.nextTask(ctx, currentIndex, subBatch, acker)
			if err != nil {
				return err
			}
		case RecordFlagNack:
			err := acker.Nack(ctx, subBatch, t.ID())
			if err != nil {
				return err
			}
		case RecordFlagRetry:
			err := w.doTask(ctx, currentIndex, subBatch, acker)
			if err != nil {
				return err
			}
		}

		idx = idx + len(subBatch.positions)
	}

	return nil
}

// subBatchByFlag collects a sub-batch of records with the same status starting
// from the given index. It returns nil if firstIndex is out of bounds.
func (w *Worker) subBatchByFlag(b *Batch, firstIndex int) *Batch {
	if firstIndex >= len(b.recordStatuses) {
		return nil
	}

	flags := make([]RecordFlag, 0, 2)
	flags = append(flags, b.recordStatuses[firstIndex].Flag)
	// Collect Skips and Acks together in the same batch.
	if flags[0] == RecordFlagSkip {
		flags = append(flags, RecordFlagAck)
	} else if flags[0] == RecordFlagAck {
		flags = append(flags, RecordFlagSkip)
	}

	lastIndex := firstIndex
OUTER:
	for _, status := range b.recordStatuses[firstIndex:] {
		for _, f := range flags {
			if status.Flag == f {
				lastIndex++
				// Record has matching status, let's continue.
				continue OUTER
			}
		}
		// Record has a different status, we're done.
		break
	}

	return b.sub(firstIndex, lastIndex)
}

func (w *Worker) hasNextTask(currentIndex int) bool {
	return len(w.Order[currentIndex]) > 0
}

func (w *Worker) nextTask(ctx context.Context, currentIndex int, b *Batch, acker ackNacker) error {
	nextIndices := w.Order[currentIndex]
	switch len(nextIndices) {
	case 0:
		// no next task, we're done
		return nil
	case 1:
		// single next task, let's pass the batch to it
		return w.doTask(ctx, nextIndices[0], b, acker)
	default:
		// TODO(multi-connector): remove error
		return cerrors.Errorf("multiple next tasks not supported yet")

		// multiple next tasks, let's clone the batch and pass it to them
		// concurrently
		multiAcker := newMultiAckNacker(acker, len(nextIndices))
		p := pool.New().WithErrors() // TODO WithContext?
		for _, i := range nextIndices {
			b := b.clone()
			p.Go(func() error {
				return w.doTask(ctx, i, b, multiAcker)
			})
		}
		err := p.Wait()
		if err != nil {
			return err // no need to wrap, it already contains the task ID
		}

		// TODO merge batch statuses?
		return nil
	}
}

func (w *Worker) Ack(ctx context.Context, batch *Batch) error {
	err := w.Source.Ack(ctx, batch.positions)
	if err != nil {
		return cerrors.Errorf("failed to ack %d records in source: %w", len(batch.records), err)
	}

	w.DLQ.Ack(ctx, batch)
	w.updateTimer(batch.records)
	return nil
}

func (w *Worker) Nack(ctx context.Context, batch *Batch, taskID string) error {
	n, err := w.DLQ.Nack(ctx, batch, taskID)
	if n > 0 {
		// Successfully nacked n records, let's ack them, as they reached
		// the end of the pipeline (in this case the DLQ).
		err := w.Source.Ack(ctx, batch.positions[:n])
		if err != nil {
			return cerrors.Errorf("task %s failed to ack %d records in source: %w", n, err)
		}

		w.updateTimer(batch.records[:n])
	}

	if err != nil {
		return cerrors.Errorf("failed to nack %d records: %w", len(batch.records)-n, err)
	}
	return nil
}

func (w *Worker) updateTimer(records []opencdc.Record) {
	for _, rec := range records {
		readAt, err := rec.Metadata.GetReadAt()
		if err != nil {
			// If the record metadata has changed and does not include ReadAt
			// fallback to the time the worker received the record.
			readAt = w.lastReadAt
		}
		w.timer.UpdateSince(readAt)
	}
}

type ackNacker interface {
	Ack(context.Context, *Batch) error
	Nack(context.Context, *Batch, string) error
}

// multiAckNacker is an ackNacker that expects multiple acks/nacks for the same
// batch. It keeps track of the number of acks/nacks and only acks/nacks the
// batch when all expected acks/nacks are received.
type multiAckNacker struct {
	parent ackNacker
	count  *atomic.Int32
}

func newMultiAckNacker(parent ackNacker, count int) *multiAckNacker {
	c := atomic.Int32{}
	c.Add(int32(count))
	return &multiAckNacker{
		parent: parent,
		count:  &c,
	}
}

func (m *multiAckNacker) Ack(ctx context.Context, batch *Batch) error {
	panic("not implemented")
}

func (m *multiAckNacker) Nack(ctx context.Context, batch *Batch, taskID string) error {
	panic("not implemented")
}
