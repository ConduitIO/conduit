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

//go:generate mockgen -typed -destination=worker_mock_test.go -package=funnel . Task

package funnel

import (
	"context"
	"iter"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/rollback"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/sourcegraph/conc/pool"
)

// Task is a unit of work that can be executed by a Worker. Each Task in a
// pipeline is executed sequentially, except for tasks related to different
// destinations, which can be executed in parallel.
type Task interface {
	// ID returns the identifier of this Task. Each Task in a pipeline must be
	// uniquely identified by the ID.
	ID() string

	// Open opens the Task for processing. It is called once before the worker
	// starts processing records.
	Open(context.Context) error
	// Close closes the Task. It is called once after the worker has stopped
	// processing records.
	Close(context.Context) error
	// Do processes the given batch of records. It is called for each batch of
	// records that the worker processes.
	Do(context.Context, *Batch) error
}

// Worker collects the tasks that need to be executed in a pipeline for a
// specific source. It processes records from the source through the tasks until
// it is stopped. The worker is responsible for coordinating tasks and
// acking/nacking records.
//
// Batches are processed in the following way:
//   - The first task is always a source task which reads a batch of records
//     from the source. The batch is then passed to the next task.
//   - Any task between the source and the destination can process the batch by
//     updating the records or their status (see [RecordStatus]). If a record in
//     the batch is marked as filtered, the next task will skip processing it
//     and consider it as already processed. If a record is marked as nacked,
//     the record will be sent to the DLQ. If a record is marked as retry, the
//     record will be reprocessed by the same task (relevant if a task processed
//     only part of the batch, experienced an error and skipped the rest).
//   - The last task is always a destination task which writes the batch of
//     records to the destination. The batch is then acked.
//
// Note that if a task marks a record in the middle of a batch as nacked, the
// batch is split into sub-batches. The records that were successfully processed
// continue to the next task (and ideally to the end of the pipeline), because
// Conduit provides ordering guarantees. Only once the records before the nacked
// record are end-to-end processed, will the nacked record be sent to the DLQ.
// The rest of the records are processed as a sub-batch, and the same rules
// apply to them.
type Worker struct {
	Source    Source
	FirstTask *TaskNode
	DLQ       *DLQ

	lastReadAt time.Time
	timer      metrics.Timer

	// processingLock is a lock in form of a channel with a buffer size of 1 to
	// be able to acquire the lock with a context timeout.
	processingLock chan struct{}
	// stop stores the information if a graceful stop was triggered.
	stop atomic.Bool

	logger log.CtxLogger
}

func NewWorker(
	firstTask *TaskNode,
	dlq *DLQ,
	logger log.CtxLogger,
	timer metrics.Timer,
) (*Worker, error) {
	firstTask.first = true // mark the first task as the first task in the pipeline
	err := validateTasks(firstTask)
	if err != nil {
		return nil, cerrors.Errorf("invalid task order: %w", err)
	}

	st, ok := firstTask.Task.(interface{ GetSource() Source })
	if !ok {
		return nil, cerrors.Errorf("first task must be a source task, got %T", firstTask.Task)
	}

	return &Worker{
		Source:    st.GetSource(),
		FirstTask: firstTask,
		DLQ:       dlq,
		logger:    logger.WithComponent("funnel.Worker"),
		timer:     timer,

		processingLock: make(chan struct{}, 1),
	}, nil
}

func validateTasks(task *TaskNode) error {
	// Traverse the tasks according to the order and validate that each task
	// is included exactly once.
	seen := make(map[string]bool)

	for t := range task.Tasks() {
		if seen[t.ID()] {
			return cerrors.Errorf("task %s included multiple times in order", task.Task.ID())
		}
		seen[t.ID()] = true
	}

	return nil
}

// Open opens the worker for processing. It opens all tasks and the DLQ. If any
// task fails to open, the worker is not opened and the error is returned.
// Once a worker is opened, it can start processing records. The worker should
// be closed using Close after it is no longer needed.
func (w *Worker) Open(ctx context.Context) (err error) {
	var r rollback.R
	defer func() {
		rollbackErr := r.Execute()
		err = cerrors.LogOrReplace(err, rollbackErr, func() {
			w.logger.Err(ctx, rollbackErr).Msg("failed to execute rollback")
		})
	}()

	for task := range w.FirstTask.Tasks() {
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
// current batch from being processed. If a batch is currently being processed,
// the method will block and trigger the stop after the batch is processed.
func (w *Worker) Stop(ctx context.Context) error {
	// The lock is locked every time a batch is being processed. We lock it
	// to be sure no batch is currently being processed.
	release, err := w.acquireProcessingLock(ctx)
	if err != nil {
		return err
	}
	defer release()

	// Lock acquired, teardown the source and set stop to true to signal the
	// worker it should stop processing, since it won't be able to deliver
	// any acks.
	err = w.Source.Teardown(ctx)
	if err != nil {
		return cerrors.Errorf("failed to tear down source: %w", err)
	}
	w.stop.Store(true)
	return nil
}

// acquireProcessingLock tries to acquire the processing lock. It returns a
// release function that should be called to release the lock. If the context is
// canceled before the lock is acquired, it returns the context error.
func (w *Worker) acquireProcessingLock(ctx context.Context) (release func(), err error) {
	select {
	case w.processingLock <- struct{}{}:
		return func() { <-w.processingLock }, nil
	case <-ctx.Done():
		// lock not acquired
		return func() {}, ctx.Err()
	}
}

func (w *Worker) Close(ctx context.Context) error {
	var errs []error

	for task := range w.FirstTask.Tasks() {
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
	for !w.stop.Load() {
		w.logger.Trace(ctx).Msg("starting next batch")
		if err := w.doTask(ctx, w.FirstTask, &Batch{}, w); err != nil {
			return err
		}
		w.logger.Trace(ctx).Msg("batch done")
	}
	return nil
}

//nolint:gocyclo // TODO: refactor
func (w *Worker) doTask(
	ctx context.Context,
	taskNode *TaskNode,
	b *Batch,
	acker ackNacker,
) error {
	t := taskNode.Task

	w.logger.Trace(ctx).
		Str("task_id", t.ID()).
		Int("batch_size", len(b.records)).
		Int("filtered_count", b.filterCount).
		Int("split_count", len(b.splitRecords)).
		Bool("tainted", b.tainted).
		Msg("executing task")

	err := t.Do(ctx, b)

	w.logger.Trace(ctx).
		Err(err).
		Str("task_id", t.ID()).
		Int("batch_size", len(b.records)).
		Int("filtered_count", b.filterCount).
		Int("split_count", len(b.splitRecords)).
		Bool("tainted", b.tainted).
		Msg("task done")

	if err != nil {
		// Canceled error can be returned if the worker is stopped while reading
		// the next batch from the source (graceful stop).
		// ErrPluginNotRunning can be returned if the plugin is stopped before
		// trying to read the next batch.
		// Both are considered as graceful stop, just return the context error, if any.
		if taskNode.IsFirst() && (cerrors.Is(err, context.Canceled) ||
			(cerrors.Is(err, plugin.ErrPluginNotRunning) && w.stop.Load())) {
			return ctx.Err()
		}
		return cerrors.Errorf("task %s: %w", t.ID(), err)
	}

	if taskNode.IsFirst() {
		// The first task has some specifics:
		// - Store last time we read a batch from the source for metrics.
		// - It locks the stop lock, so that no stop signal can be received while
		//   the batch is being processed.
		// - It checks if the source was torn down after receiving the batch and
		//   before acquiring the lock.
		w.lastReadAt = time.Now()

		release, err := w.acquireProcessingLock(ctx)
		if err != nil {
			return err
		}
		// Unlock after the batch is end-to-end processed.
		defer release()

		if w.stop.Load() {
			// The source was already torn down, we won't be able to deliver
			// any acks so throw away the batch and gracefully return.
			w.logger.Warn(ctx).
				Str("task_id", t.ID()).
				Int("batch_size", len(b.records)).
				Msg("stop signal received just before starting to process next batch, gracefully stopping without flushing the batch")
			return nil
		}
	}

	if !b.tainted {
		w.logger.Trace(ctx).
			Str("task_id", t.ID()).
			Msg("task returned clean batch")

		// Shortcut.
		if !taskNode.HasNext() || !b.HasActiveRecords() {
			// Either this is the last task (the batch has made it end-to-end),
			// or the batch has only filtered records. Let's ack!
			return acker.Ack(ctx, b)
		}
		// There is at least one task after this one, let's continue.
		return w.doNextTask(ctx, taskNode, b, acker)
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
		case RecordFlagAck, RecordFlagFilter:
			if !taskNode.HasNext() || !subBatch.HasActiveRecords() {
				// Either this is the last task (the batch has made it end-to-end),
				// or the batch has only filtered records. Let's ack!
				// We need to ack all the records in the batch, not only active
				// ones, filtered ones should also be acked.
				err := acker.Ack(ctx, subBatch)
				if err != nil {
					return err
				}
				break // break switch
			}
			// There is at least one task after this one, let's continue.
			err := w.doNextTask(ctx, taskNode, subBatch, acker)
			if err != nil {
				return err
			}
		case RecordFlagNack:
			err := acker.Nack(ctx, subBatch, t.ID())
			if err != nil {
				return err
			}
		case RecordFlagRetry:
			// Retry the sub-batch by passing it to the same task. We need to
			// mark the records as acked, as that's the default record status.
			subBatch.Ack(0, len(subBatch.records))
			err := w.doTask(ctx, taskNode, subBatch, acker)
			if err != nil {
				return err
			}
		}

		idx += len(subBatch.positions)
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
	// Collect Filters and Acks together in the same batch.
	switch flags[0] { //nolint:exhaustive // We only care about two flags.
	case RecordFlagFilter:
		flags = append(flags, RecordFlagAck)
	case RecordFlagAck:
		flags = append(flags, RecordFlagFilter)
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

func (w *Worker) doNextTask(ctx context.Context, taskNode *TaskNode, b *Batch, acker ackNacker) error {
	switch len(taskNode.Next) {
	case 0:
		// no next task, we're done
		return nil
	case 1:
		// single next task, let's pass the batch to it
		return w.doTask(ctx, taskNode.Next[0], b, acker)
	default:
		// TODO(multi-connector): remove error
		return cerrors.Errorf("multiple next tasks not supported yet")

		// multiple next tasks, let's clone the batch and pass it to them
		// concurrently
		//nolint:govet // TODO implement multi ack nacker
		multiAcker := newMultiAckNacker(acker, len(taskNode.Next))
		p := pool.New().WithErrors() // TODO WithContext?
		for _, nextTask := range taskNode.Next {
			b := b.clone()
			p.Go(func() error {
				return w.doTask(ctx, nextTask, b, multiAcker)
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
	originalBatch := batch.originalBatch()
	err := w.Source.Ack(ctx, originalBatch.positions)
	if err != nil {
		return cerrors.Errorf("failed to ack %d records in source: %w", len(originalBatch.positions), err)
	}

	w.DLQ.Ack(ctx, batch)
	w.updateTimer(batch.records)
	return nil
}

func (w *Worker) Nack(ctx context.Context, batch *Batch, taskID string) error {
	originalBatch := batch.originalBatch()
	n, err := w.DLQ.Nack(ctx, originalBatch, taskID)
	if n > 0 {
		// Successfully nacked n records, let's ack them, as they reached
		// the end of the pipeline (in this case the DLQ).
		err := w.Source.Ack(ctx, originalBatch.positions[:n])
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

// TaskNode represents a task in the pipeline. It contains the task itself and
// the next tasks to be executed after it.
type TaskNode struct {
	Task Task
	Next []*TaskNode

	first bool
}

// IsFirst returns true if this task is the first task in the pipeline.
func (t *TaskNode) IsFirst() bool {
	return t.first
}

// HasNext returns true if the task has at least one next task.
func (t *TaskNode) HasNext() bool {
	return len(t.Next) > 0
}

// AppendToEnd adds a new task to the end of the pipeline. Note that this doesn't
// mean that the supplied task will be executed directly after this task. Rather,
// it means that it will be executed after all tasks in the linked list are executed.
//
// If any task node in the list has more than 1 next task, the function will
// return an error, as it would be ambiguous where to append the task.
//
// If the task was appended successfully, it returns the created TaskNode.
func (t *TaskNode) AppendToEnd(next ...*TaskNode) error {
	switch len(t.Next) {
	case 0:
		// No next task, let's append the new task.
		t.Next = next
		return nil
	case 1:
		// Single next task, let's append the new task to it.
		return t.Next[0].AppendToEnd(next...)
	default:
		// Multiple next tasks, we can't append the new task to them.
		// If we hit this line it's an internal bug.
		return cerrors.Errorf("(bug) multiple next tasks, please append the task to the branch where you want it")
	}
}

// TaskNodes returns an iterator over the task nodes in the pipeline. It iterates
// the task nodes in the order they are defined in the pipeline, depth-first.
func (t *TaskNode) TaskNodes() iter.Seq[*TaskNode] {
	return func(yield func(*TaskNode) bool) {
		t.iterator()(yield)
	}
}

// Tasks returns an iterator over the tasks in the pipeline. It iterates
// the tasks in the order they are defined in the pipeline, depth-first.
func (t *TaskNode) Tasks() iter.Seq[Task] {
	return func(yield func(Task) bool) {
		t.iterator()(func(node *TaskNode) bool {
			return yield(node.Task)
		})
	}
}

// iterator is a private method that returns an iterator which also tells the
// caller if it should stop iterating. This is needed to break the loop in parent
// iterators, but doesn't match the Go interface for iter.Seq, so it's just a helper.
func (t *TaskNode) iterator() func(yield func(*TaskNode) bool) bool {
	return func(yield func(*TaskNode) bool) bool {
		// First yield the current task.
		if !yield(t) {
			return false
		}

		// Then process all children in order.
		for _, next := range t.Next {
			if !next.iterator()(yield) {
				return false
			}
		}
		return true
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
	c.Add(int32(count)) //nolint:gosec // no risk of overflow
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
