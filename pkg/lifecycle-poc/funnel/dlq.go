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
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type DLQ struct {
	task *DestinationTask

	windowSize          int
	windowNackThreshold int

	// window keeps track of the last N acks and nacks
	window *dlqWindow
	m      sync.Mutex
}

func NewDLQ(
	id string,
	destination Destination,
	logger log.CtxLogger,
	metrics ConnectorMetrics,

	windowSize int,
	windowNackThreshold int,
) *DLQ {
	return &DLQ{
		task:                NewDestinationTask(id, destination, logger, metrics),
		windowSize:          windowSize,
		windowNackThreshold: windowNackThreshold,

		window: newDLQWindow(windowSize, windowNackThreshold),
	}
}

func (d *DLQ) ID() string {
	return d.task.id
}

func (d *DLQ) Open(ctx context.Context) error {
	return d.task.Open(ctx)
}

func (d *DLQ) Close(ctx context.Context) error {
	return d.task.Close(ctx)
}

func (d *DLQ) Ack(_ context.Context, batch *Batch) {
	if batch.originalRecordCount == 0 {
		return
	}

	d.m.Lock()
	defer d.m.Unlock()

	d.window.Ack(batch.originalRecordCount)
}

func (d *DLQ) Nack(ctx context.Context, batch *Batch, taskID string) (int, error) {
	if batch.originalRecordCount == 0 {
		return 0, nil
	}

	d.m.Lock()
	defer d.m.Unlock()

	nacked := d.window.Nack(batch.originalRecordCount)

	if nacked > 0 {
		b := batch
		if nacked < batch.originalRecordCount {
			// TODO make sure to send all split records to the DLQ, we don't have
			//  the original anymore. This means that if [0:nacked] includes
			//  split records, we need to increase it to [0:nacked+splitRecordCount]
			b = batch.sub(0, nacked)
		}

		// The window successfully accepted nacks (at least some of them), send
		// them to the DLQ.
		successCount, err := d.sendToDLQ(ctx, b, taskID)
		// TODO successCount might be _more_ than nacked, if the batch contained
		//  split records. Make sure that sendToDLQ returns the correct number,
		//  counting only original records.
		if err != nil {
			// The DLQ write failed, we need to stop the pipeline, as recovering
			// could lead to an endless loop of restarts.
			return successCount, cerrors.FatalError(err)
		}
	}
	if nacked < batch.originalRecordCount {
		// Not all records were nacked, we need to return an error.
		if d.windowNackThreshold > 0 {
			// If the threshold is greater than 0 the DLQ is enabled and we
			// need to respect the threshold by stopping the pipeline with a
			// fatal error.
			return nacked, cerrors.FatalError(
				cerrors.Errorf(
					"DLQ nack threshold exceeded (%d/%d), original error: %w",
					d.windowNackThreshold, d.windowSize, batch.recordStatuses[nacked].Error,
				),
			)
		}
		// DLQ is disabled, we don't need to wrap the error message
		return nacked, batch.recordStatuses[nacked].Error
	}

	return nacked, nil
}

func (d *DLQ) sendToDLQ(ctx context.Context, batch *Batch, taskID string) (int, error) {
	// Create a new batch with the DLQ records and write it to the destination.
	dlqRecords := make([]opencdc.Record, len(batch.records))
	for i, req := range batch.records {
		dlqRecords[i] = d.dlqRecord(req, batch.recordStatuses[i], taskID)
	}
	dlqBatch := NewBatch(dlqRecords)

	err := d.task.Do(ctx, dlqBatch)
	if err != nil {
		return 0, cerrors.Errorf("failed to write %d records to the DLQ: %w", len(dlqRecords), err)
	}

	ackCount := 0
	for ackCount < len(dlqRecords) && dlqBatch.recordStatuses[ackCount].Flag == RecordFlagAck {
		ackCount++
	}
	if ackCount < len(dlqRecords) {
		// Not all records were acked, we need to return an error.
		return ackCount, cerrors.Errorf("failed to write record %d to the DLQ: %w", ackCount, dlqBatch.recordStatuses[ackCount].Error)
	}

	return ackCount, nil
}

func (d *DLQ) dlqRecord(r opencdc.Record, status RecordStatus, taskID string) opencdc.Record {
	out := opencdc.Record{
		Position:  r.Position,
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       nil,
		Payload: opencdc.Change{
			Before: nil,
			After:  opencdc.StructuredData(r.Map()), // failed record is stored here
		},
	}

	connID, _ := r.Metadata.GetConduitSourceConnectorID()

	out.Metadata.SetCreatedAt(time.Now())
	out.Metadata.SetConduitSourceConnectorID(connID)
	out.Metadata.SetConduitDLQNackError(status.Error.Error())
	out.Metadata.SetConduitDLQNackNodeID(taskID) // TODO rename to DLQNackTaskID
	return out
}

// dlqWindow is responsible for tracking the last N nacks/acks and enforce a
// threshold of nacks that should not be exceeded.
type dlqWindow struct {
	// window acts as a ring buffer for storing acks/nacks (true = nack).
	// When initialized it contains only acks.
	window []bool
	// cursor is the index pointing to the last message in the window.
	cursor int
	// nackThreshold represents the number of tolerated nacks, if the threshold
	// is exceeded the window is frozen and returns an error for all further
	// nacks.
	nackThreshold int

	ackCount  int
	nackCount int
}

func newDLQWindow(size, threshold int) *dlqWindow {
	if size > 0 && threshold == 0 {
		// optimization - if threshold is 0 the window size does not matter,
		// setting it to 1 ensures we don't use more memory than needed
		size = 1
	}
	return &dlqWindow{
		window:        make([]bool, size),
		cursor:        0,
		nackThreshold: threshold,

		ackCount:  size,
		nackCount: 0,
	}
}

// Ack stores an ack in the window.
func (w *dlqWindow) Ack(count int) {
	if w.nackCount == 0 {
		return // Shortcut for the common case where no nacks are present
	}
	_ = w.store(count, false)
}

// Nack stores a nack in the window and returns true (ok). If the nack threshold
// gets exceeded the window will be frozen and will return false for all further
// calls to Nack.
func (w *dlqWindow) Nack(count int) int {
	return w.store(count, true)
}

func (w *dlqWindow) store(count int, nacked bool) int {
	if len(w.window) == 0 || w.nackThreshold < w.nackCount {
		return 0 // window disabled or threshold already reached
	}

	for i := range count {
		// move cursor before updating the window
		w.cursor = (w.cursor + 1) % len(w.window)
		if w.window[w.cursor] == nacked {
			continue // the old message has the same status, nothing changes
		}

		w.window[w.cursor] = nacked
		switch nacked {
		case false:
			w.nackCount--
			w.ackCount++
		case true:
			w.nackCount++
			w.ackCount--
			if w.nackThreshold < w.nackCount {
				return i
			}
		}
	}

	return count
}
