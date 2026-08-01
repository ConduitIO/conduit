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
	"fmt"
	"io"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/rollback"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
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
	// teardownMu guards sourceTornDown and serializes teardown so the source is
	// torn down at most once across the Stop/Close race.
	teardownMu sync.Mutex
	// sourceTornDown is set only after Source.Teardown SUCCEEDS, so a failed
	// teardown is retried by a later call rather than masked as done.
	sourceTornDown bool

	logger log.CtxLogger
}

// tearDownSource releases the source connector's resources. It serves two
// purposes: on a graceful stop it interrupts a blocked source Read, and on every
// path it frees the source. It is called from both Stop (graceful) and Close (all
// paths, including a fatal error where Stop is never called), so it is idempotent
// — but only a *successful* teardown is remembered: if Source.Teardown fails, the
// source is left un-torn-down so the next caller retries, rather than masking the
// failure and leaking the source.
func (w *Worker) tearDownSource(ctx context.Context) error {
	w.teardownMu.Lock()
	defer w.teardownMu.Unlock()
	if w.sourceTornDown {
		return nil
	}
	if err := w.Source.Teardown(ctx); err != nil {
		return err // not marked torn down; a later Stop/Close call retries
	}
	w.sourceTornDown = true
	return nil
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

	// Lock acquired: no batch is currently being processed, but a new batch's
	// first task can already be blocked in (or about to start) a source Read,
	// unprotected by processingLock by design (see doTask) so a slow Read
	// doesn't hold up Stop. Set stop *before* tearing down the source: doTask
	// only treats a plugin.ErrPluginNotRunning from that Read as a graceful
	// stop when w.stop.Load() is already true (see doTask's IsFirst check). If
	// tearDownSource ran first, a Read racing the teardown could observe the
	// torn-down plugin (ErrPluginNotRunning) before this flag flips, and get
	// misreported as a real failure instead of a graceful stop — confirmed by
	// repro under `-race -count=800` (pkg/lifecycle-poc/service_test.go
	// TestServiceLifecycle_PipelineStop flaking with "failed to read from
	// source: plugin is not running").
	w.stop.Store(true)
	if err := w.tearDownSource(ctx); err != nil {
		return cerrors.Errorf("failed to tear down source: %w", err)
	}
	return nil
}

// Stopping reports whether this worker has armed its stop — i.e. w.stop was set
// by a Stop call, so the Do loop will exit and no further batch will be read.
// It becomes true the instant Stop sets the flag, BEFORE source teardown runs,
// so it stays true even when Stop returns a teardown error. Callers use this to
// distinguish "Stop returned an error but the worker is genuinely stopping"
// (teardown failed after the flag was set) from "Stop failed before arming"
// (lock-acquisition timeout) — the two have opposite implications for whether a
// deliberate-stop marker should be rolled back. See
// lifecycle-poc.Service.stopRunnablePipeline.
func (w *Worker) Stopping() bool {
	return w.stop.Load()
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

	// Guarantee the source is torn down on every shutdown path. On a graceful
	// stop Stop already tore it down and this is a no-op; on a fatal-error path
	// Stop is never called, so this is where the source's resources are released
	// (without it the source connector/plugin would leak).
	if err := w.tearDownSource(ctx); err != nil {
		errs = append(errs, cerrors.Errorf("failed to tear down source: %w", err))
	}

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

		// Invariant 2/3: capture the span this sub-batch covers in the PARENT
		// batch BEFORE handing it to a task, and advance idx by that — never by
		// the sub-batch's length afterwards.
		//
		// A task may GROW the sub-batch: Batch.SplitRecord appends, and both the
		// RecordFlagRetry branch (which re-enters doTask) and doNextTask can
		// trigger it. Because subBatchByFlag three-index-clips, that growth
		// reallocates and leaves the parent untouched — so after the call
		// len(subBatch.positions) is larger than the span it actually covered.
		// Advancing by the grown length made idx overshoot, and the records in
		// between were never handed to any downstream task, never acked — while
		// a later sub-batch's ack still advanced the persisted source position
		// PAST them. Silent, permanent record loss with no error and no gap in
		// the position sequence. See issue #2722.
		span := len(subBatch.positions)

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

		idx += span
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

// doNextTask advances the batch b to whatever comes after taskNode. A single
// next task is the common case (linear pipeline). Multiple next tasks means
// taskNode is the fan-out point into M destination branches (see slice 3a of
// the arch-v2 multi-connector epic): the batch is cloned once per branch and
// the branches run concurrently, each writing to a different destination.
//
// Only the destination-fan-out axis is supported here (single source, M
// destinations) — spawning multiple *workers* for multiple *sources* is a
// separate, later slice; see the multi-source guard in
// pkg/lifecycle-poc/service.go's buildSourceTasks.
func (w *Worker) doNextTask(ctx context.Context, taskNode *TaskNode, b *Batch, acker ackNacker) error {
	switch len(taskNode.Next) {
	case 0:
		// no next task, we're done
		return nil
	case 1:
		// single next task, let's pass the batch to it
		return w.doTask(ctx, taskNode.Next[0], b, acker)
	default:
		// Multiple next tasks: fan the batch out to each destination branch
		// concurrently and track per-record ack/nack outcomes across all of
		// them with multiAckNacker.
		//
		// Invariant 1/3 (enforcement site): capture the batch's ORIGINAL
		// (pre-split) positions up front, before any branch runs. Branches
		// diverge independently from this point on — a per-branch processor
		// may split a record into pieces that a sibling branch does not — so
		// multiAckNacker must key its per-position tally on a position that
		// is stable across all M branches. b.originalBatch() is exactly the
		// batch-wide "no splits yet" view; any splitting an upstream SHARED
		// processor already did (before this fan-out point) is collapsed
		// here so the tally is keyed by the true root position, not an
		// intermediate one.
		orig := b.originalBatch()
		multiAcker, err := newMultiAckNacker(acker, len(taskNode.Next), orig.positions)
		if err != nil {
			// Duplicate positions in the batch: fail the pipeline with the
			// coded error rather than fan out into a silent, unbounded
			// never-acked stall. See CodeDuplicateSourcePosition.
			return err
		}

		p := pool.New().WithErrors()
		for _, nextTask := range taskNode.Next {
			branchBatch := b.clone()
			p.Go(func() error {
				return w.doTask(ctx, nextTask, branchBatch, multiAcker)
			})
		}
		if err := p.Wait(); err != nil {
			return err // no need to wrap, it already contains the task ID
		}

		return nil
	}
}

func (w *Worker) Ack(ctx context.Context, batch *Batch) error {
	originalBatch := batch.originalBatch()

	// Invariant 2: positions are monotonic and crash-safe. connector.Source.Ack
	// persists State.Position = p[len(p)-1] unconditionally, so handing it an
	// empty/nil position OVERWRITES the durable source position with nothing —
	// on restart the source resumes from an empty position, which for Postgres
	// means a full re-snapshot and for file/Kafka means offset 0. That is a
	// monotonicity violation with a far worse blast radius than the stall this
	// check's sibling guard (CodeDuplicateSourcePosition) prevents.
	//
	// A nil position here is never the source's fault: Batch.SplitRecord marks
	// every piece after the first with a nil position, and a sub-batch that
	// happens to cover only that tail collapses to nils. Failing loud is the
	// only safe response — the records are simply not acked, so they replay on
	// restart (invariant 3 preserved), whereas proceeding corrupts the position.
	if err := validateAckPositions(originalBatch.positions); err != nil {
		return err
	}

	err := w.Source.Ack(ctx, originalBatch.positions)
	if err != nil && !isClosedSourceStream(err) {
		return cerrors.Errorf("failed to ack %d records in source: %w", len(originalBatch.positions), err)
	}
	// A closed source stream (io.EOF) means the source already stopped, typically
	// because it errored; forwarding the ack is a no-op and this derived error must
	// not mask the source's real cause (#1659). Safe under invariant 3: Source.Ack
	// does not persist a position on this error path.

	w.DLQ.Ack(ctx, batch)
	w.updateTimer(batch.records)
	return nil
}

// validateAckPositions rejects a position slice that is about to be handed to
// connector.Source.Ack but contains an empty/nil entry. See Worker.Ack for why
// this is invariant-2 corruption rather than a cosmetic problem, and
// CodeEmptySourcePosition for who is actually at fault.
func validateAckPositions(positions []opencdc.Position) error {
	for i, p := range positions {
		if len(p) != 0 {
			continue
		}
		ce := conduiterr.New(CodeEmptySourcePosition, fmt.Sprintf(
			"refusing to ack an empty position (index %d of %d) to the source: "+
				"acking it would overwrite the durable source position and force a full re-read on restart",
			i, len(positions)))
		ce.Suggestion = "this usually means a processor split a record and a later processor returned " +
			"only part of the split run; the records are not acked and will be redelivered, but the " +
			"pipeline is stopped to protect the source position"
		return ce
	}
	return nil
}

func (w *Worker) Nack(ctx context.Context, batch *Batch, taskID string) error {
	originalBatch := batch.originalBatch()
	n, err := w.DLQ.Nack(ctx, originalBatch, taskID)
	if n > 0 {
		// Successfully nacked n records, let's ack them, as they reached
		// the end of the pipeline (in this case the DLQ).
		ackErr := w.Source.Ack(ctx, originalBatch.positions[:n])
		if ackErr != nil && !isClosedSourceStream(ackErr) {
			return cerrors.Errorf("task %s failed to ack %d records in source: %w", taskID, n, ackErr)
		}
		// io.EOF suppressed, same as Ack (#1659).

		w.updateTimer(batch.records[:n])
	}

	if err != nil {
		return cerrors.Errorf("failed to nack %d records: %w", len(batch.records)-n, err)
	}
	return nil
}

// isClosedSourceStream reports whether err is the sentinel a source connector's
// stream returns once it has closed (io.EOF, which plugin.ErrStreamNotOpen
// aliases). Forwarding an ack to a closed source stream is a no-op, and the
// derived error must not mask the source's real failure. See #1659.
func isClosedSourceStream(err error) bool {
	return cerrors.Is(err, io.EOF)
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

// multiAckNacker is the ackNacker used at a destination fan-out point (see
// Worker.doNextTask): it sits between M concurrently-running destination
// branches and the single parent ackNacker (the Worker itself, or an outer
// multiAckNacker), and is responsible for turning M independent per-branch
// votes for each source record into exactly one Ack or Nack call on the
// parent.
//
// # Why per-batch counting is wrong
//
// A naive implementation would count acks/nacks per BATCH (one counter,
// decremented once per branch, act once it hits zero). That is a data-loss
// bug: destinations diverge per RECORD, not per batch. Destination 1 might
// ack records 0-4 of a 5-record batch while destination 2 nacks record 3 —
// there is no single moment where "the batch" is uniformly ack'd or nack'd.
// A per-batch counter has no way to represent record 3 being nacked while
// records 0, 1, 2 and 4 are acked, and either drops the divergence (silently
// acking a record that a destination never durably wrote — invariant 1) or
// blocks forever (waiting for a nack vote on a record every branch actually
// acked).
//
// # Required semantics (per original source position)
//
//  1. Ack-only-when-unanimous (invariant 1): a position is acked to the
//     parent only once ALL M branches have voted ack for it.
//  2. Nack-wins (invariant 3): if ANY branch nacks a position, it is routed
//     to the parent's DLQ exactly once, regardless of how the other
//     branches voted. A record written durably to destination 1 but failed
//     on destination 2 is NOT a partial success — the whole record goes to
//     the DLQ, then is acked to the source (the DLQ write is itself the
//     durable handling that earns the source ack). A partially-written
//     record is never acked to the source as if it were a full success.
//  3. Idempotent, race-free: branches run concurrently and call Ack/Nack
//     from separate goroutines; once a position reaches a terminal decision
//     (acked-by-all or nacked-by-one), any further vote for it (a slow
//     branch's ack arriving after a sibling already nacked the same
//     position) is a no-op. This assumes each branch votes exactly once per
//     position (ack xor nack) — the same assumption the rest of the batch
//     pipeline already makes (see Batch's tainted-splitting in doTask).
//  4. In-source-order release (invariant 4): positions are released to the
//     parent in ascending source order, never in branch-completion order —
//     a branch that finishes record 10 before another branch finishes
//     record 3 must not let record 10 reach the parent first, or
//     Source.Ack's monotonically-advancing position would skip ahead of a
//     record that has not actually reached a terminal decision yet.
//     Contiguous runs of ack decisions are batched into a single parent.Ack
//     call to preserve the parent's batch semantics; nacks are released one
//     position at a time (see the comment on releaseLocked for why).
//  5. Fatal-error propagation: if the parent's Nack returns a fatal error
//     (e.g. the DLQ nack-threshold was exceeded), that error is returned to
//     the caller so the branch pool errors out and the worker tombs with it
//     — identical to what happens on the single-destination path.
//
// This is intentionally a mutex-guarded per-position tally rather than a
// lock-free or channel-based design: it is Tier-1, highest-data-loss-risk
// code, and boring-and-obviously-correct beats clever here. See
// docs/design-documents/20260731-archv2-multiconnector.md and
// docs/architecture-decision-records/20260731-archv2-fanout-ack-model.md.
type multiAckNacker struct {
	parent   ackNacker
	branches int

	// positions holds the ORIGINAL (pre-split) source positions the fan-out
	// batch contained, captured once at construction time (see
	// Worker.doNextTask). Index i in every other slice below corresponds to
	// positions[i].
	positions []opencdc.Position
	// posIndex maps a position's byte content to its index in positions, so
	// Ack/Nack can look up which slot an incoming (already-collapsed-to-
	// original) record belongs to.
	posIndex map[string]int

	mu sync.Mutex
	// ackVotes[i] counts how many of the M branches have voted ack for
	// positions[i] so far. Only meaningful while !terminal[i].
	ackVotes []int
	// terminal[i] is true once positions[i] has reached a final decision
	// (ack'd by all branches, or nack'd by any one of them) — further votes
	// for it are no-ops (requirement 3 above).
	terminal []bool
	// acked[i] is only meaningful once terminal[i] is true: true means the
	// terminal decision was "ack", false means "nack".
	acked []bool
	// record[i] holds a record to represent positions[i] in the eventual
	// parent Ack/Nack call. For an ack it can come from any branch (only the
	// position matters for Source.Ack; content is used only for metrics/DLQ-
	// window bookkeeping). For a nack it MUST be the record and error from
	// the branch that actually nacked it, so the DLQ entry reflects the real
	// failure.
	record []opencdc.Record
	// nackErr[i] and nackTaskID[i] are set alongside acked[i]=false: the
	// error and originating task ID of the branch that nacked positions[i],
	// needed to reconstruct the single-record batch passed to parent.Nack.
	nackErr    []error
	nackTaskID []string

	// released is the count of leading positions (a prefix of positions)
	// that have already been handed to the parent. Positions are only ever
	// released in order, released..len(positions), never out of order
	// (invariant 4).
	released int
}

// newMultiAckNacker creates a multiAckNacker for a fan-out of a batch whose
// original (pre-split) source positions are given by positions, split across
// branches concurrently-running destination branches. positions must be
// captured from the fan-out batch's originalBatch() before cloning it for
// the branches — see Worker.doNextTask.
func newMultiAckNacker(parent ackNacker, branches int, positions []opencdc.Position) (*multiAckNacker, error) {
	posIndex := make(map[string]int, len(positions))
	for i, p := range positions {
		// Invariant 4: positions are released to the source strictly in
		// order, so every slot must be individually resolvable. A duplicate
		// (including two records that both carry an empty/nil position) would
		// silently shadow the earlier slot in this map: it could never
		// accumulate votes, never become terminal, and the in-order release
		// would stop there forever — the ENTIRE batch would then never be
		// acked to the source, with no error surfaced. Fail loud instead.
		// See CodeDuplicateSourcePosition.
		// An EMPTY position is checked first and attributed separately: two nils
		// would also trip the duplicate check below, but blaming the source for
		// them is wrong — nil positions come from Batch.SplitRecord, i.e. the
		// processor chain. A single nil never trips the duplicate check at all,
		// which is why validateAckPositions guards the corruption site too.
		if len(p) == 0 {
			ce := conduiterr.New(CodeEmptySourcePosition, fmt.Sprintf(
				"record %d of %d in a fanned-out batch has an empty position", i, len(positions)))
			ce.Suggestion = "this usually means a processor split a record and a later processor " +
				"returned only part of the split run; fix the processor chain so split runs stay intact"
			return nil, ce
		}
		if prev, dup := posIndex[string(p)]; dup {
			ce := conduiterr.New(CodeDuplicateSourcePosition, fmt.Sprintf(
				"records %d and %d in the same batch carry the identical position %q; "+
					"a position must uniquely identify a record", prev, i, p))
			ce.Suggestion = "if these records came straight from the source, this is a source-connector " +
				"bug: every record in a batch must carry a distinct, non-empty position"
			return nil, ce
		}
		posIndex[string(p)] = i
	}

	n := len(positions)
	return &multiAckNacker{
		parent:     parent,
		branches:   branches,
		positions:  positions,
		posIndex:   posIndex,
		ackVotes:   make([]int, n),
		terminal:   make([]bool, n),
		acked:      make([]bool, n),
		record:     make([]opencdc.Record, n),
		nackErr:    make([]error, n),
		nackTaskID: make([]string, n),
	}, nil
}

// indexOf returns the tally slot for pos, or an error if pos is not part of
// the original fan-out batch. That would mean a branch produced a record
// whose position doesn't trace back (via originalBatch) to one of the
// positions captured when the fan-out started — an internal bug in the task
// graph, not a runtime/data condition, so it's reported as such rather than
// silently dropped.
func (m *multiAckNacker) indexOf(pos opencdc.Position) (int, error) {
	idx, ok := m.posIndex[string(pos)]
	if !ok {
		return 0, cerrors.Errorf("(bug) multiAckNacker: position %q is not part of the original fan-out batch", pos)
	}
	return idx, nil
}

// Ack records an ack vote from one branch for every position in batch.
// batch.originalBatch() is applied first, so a branch that split records
// internally still votes using the same original positions every other
// branch votes on — see the doc comment on the type for why this matters.
//
// Invariant 1 (enforcement site): a position is only ever forwarded to
// parent.Ack once ALL m.branches have voted ack for it.
func (m *multiAckNacker) Ack(ctx context.Context, batch *Batch) error {
	ob := batch.originalBatch()

	m.mu.Lock()
	defer m.mu.Unlock()

	for i, pos := range ob.positions {
		idx, err := m.indexOf(pos)
		if err != nil {
			return err
		}

		if m.terminal[idx] {
			// Requirement 3: this position already reached a terminal
			// decision. The only way to get here is a sibling branch having
			// nacked it already (an ack can only ever be the LAST vote to
			// arrive, since it takes exactly m.branches acks to go
			// terminal) — nack wins, this vote is a no-op.
			continue
		}

		m.record[idx] = ob.records[i]
		m.ackVotes[idx]++
		if m.ackVotes[idx] == m.branches {
			m.terminal[idx] = true
			m.acked[idx] = true
		}
	}

	return m.releaseLocked(ctx)
}

// Nack records a nack vote from one branch (identified by taskID) for every
// position in batch. Like Ack, batch.originalBatch() is applied first.
//
// Invariant 3 (enforcement site): nack wins. The first nack vote for a
// position is terminal — it is routed to the parent's DLQ exactly once, and
// any later vote (ack or nack) for the same position from another branch is
// a no-op. A record durably written by some but not all branches is treated
// as a failure of the whole record, never partially acked.
func (m *multiAckNacker) Nack(ctx context.Context, batch *Batch, taskID string) error {
	ob := batch.originalBatch()

	m.mu.Lock()
	defer m.mu.Unlock()

	for i, pos := range ob.positions {
		idx, err := m.indexOf(pos)
		if err != nil {
			return err
		}

		if m.terminal[idx] {
			// Already resolved (either a sibling nack got here first, which
			// is idempotent, or - should never happen given the one-vote-
			// per-branch-per-position invariant - an ack already went
			// terminal). Either way, nothing to do.
			continue
		}

		m.terminal[idx] = true
		m.acked[idx] = false
		m.record[idx] = ob.records[i]
		m.nackErr[idx] = ob.recordStatuses[i].Error
		m.nackTaskID[idx] = taskID
	}

	return m.releaseLocked(ctx)
}

// releaseLocked walks the positions starting at m.released and forwards
// every leading run of terminal positions to the parent, stopping at the
// first position that hasn't reached a terminal decision yet. Must be called
// with m.mu held.
//
// Invariant 4 (enforcement site): positions are only ever released as a
// prefix, in ascending source order — m.released only ever moves forward,
// and never past a position that is not yet terminal - so the parent (and
// transitively Source.Ack's monotonically-advancing State.Position) never
// observes a position out of order or skips over one still in flight.
//
// Contiguous ack runs are coalesced into a single parent.Ack call to
// preserve the parent's batch semantics (fewer, larger Source.Ack calls).
// Nacks are released one position at a time: parent.Nack takes a single
// taskID for the whole batch it's given (used for DLQ metadata on every
// record in that call), and a run of nacked positions can legitimately come
// from different branches/tasks. Coalescing them under one taskID would
// misattribute the DLQ failure reason for some records. Nacks are the
// exceptional, rare path, so the extra parent calls are an acceptable
// tradeoff for that correctness.
func (m *multiAckNacker) releaseLocked(ctx context.Context) error {
	for m.released < len(m.positions) {
		if !m.terminal[m.released] {
			return nil
		}

		if m.acked[m.released] {
			from := m.released
			to := from
			for to < len(m.positions) && m.terminal[to] && m.acked[to] {
				to++
			}

			if err := m.parent.Ack(ctx, m.ackBatch(from, to)); err != nil {
				return err
			}
			m.released = to
			continue
		}

		idx := m.released
		if err := m.parent.Nack(ctx, m.nackBatch(idx), m.nackTaskID[idx]); err != nil {
			// Requirement 5: a fatal DLQ error (nack threshold exceeded)
			// must surface exactly like it does on the single-destination
			// path, so the branch pool errors out and the worker tombs.
			return err
		}
		m.released = idx + 1
	}
	return nil
}

// ackBatch builds a Batch representing the already-resolved, all-branches-
// acked positions [from, to) for a parent.Ack call. The result carries no
// split records (they were already collapsed by Ack/Nack via
// batch.originalBatch()), so the parent's own originalBatch() call is a
// no-op on it.
func (m *multiAckNacker) ackBatch(from, to int) *Batch {
	return &Batch{
		records:        m.record[from:to],
		recordStatuses: make([]RecordStatus, to-from), // zero value is RecordFlagAck
		positions:      m.positions[from:to],
	}
}

// nackBatch builds a single-record Batch for positions[idx], which some
// branch nacked, for a parent.Nack call.
func (m *multiAckNacker) nackBatch(idx int) *Batch {
	return &Batch{
		records:        []opencdc.Record{m.record[idx]},
		recordStatuses: []RecordStatus{{Flag: RecordFlagNack, Error: m.nackErr[idx]}},
		positions:      []opencdc.Position{m.positions[idx]},
		tainted:        true,
	}
}
