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

//go:generate mockgen -typed -destination=mock/dlq.go -package=mock -mock_names=DLQHandler=DLQHandler . DLQHandler

package stream

import (
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

var dlqHandlerNodeStateBroken nodeState = "broken"

type DLQHandler interface {
	Open(context.Context) error
	Write(context.Context, opencdc.Record) error
	Close(context.Context) error
}

// DLQHandlerNode is called by each SourceAckerNode in a pipeline to store
// nacked messages in a dead-letter-queue.
type DLQHandlerNode struct {
	Name    string
	Handler DLQHandler

	WindowSize          uint64
	WindowNackThreshold uint64

	Timer     metrics.Timer
	Histogram metrics.RecordBytesHistogram

	// window keeps track of the last N acks and nacks
	window *dlqWindow

	logger log.CtxLogger
	// state stores the current node state
	state csync.ValueWatcher[nodeState]
	// wg waits until all dependent components call Done
	wg sync.WaitGroup
	// m guards access to Handler and window
	m sync.Mutex

	handlerCtxCancel context.CancelFunc
}

func (n *DLQHandlerNode) ID() string {
	return n.Name
}

// Run runs the DLQ handler node until all components depending on this node
// call Done. Dependents can be added or removed while the node is running.
func (n *DLQHandlerNode) Run(ctx context.Context) (err error) {
	defer n.state.Set(nodeStateStopped)
	n.window = newDLQWindow(n.WindowSize, n.WindowNackThreshold)

	// start a fresh connector context to make sure the handler is running until
	// this method returns
	var handlerCtx context.Context
	handlerCtx, n.handlerCtxCancel = context.WithCancel(context.Background())
	defer n.handlerCtxCancel()

	err = n.Handler.Open(handlerCtx)
	if err != nil {
		return cerrors.Errorf("could not open DLQ handler: %w", err)
	}
	defer func() {
		closeErr := n.Handler.Close(handlerCtx)
		err = cerrors.LogOrReplace(err, closeErr, func() {
			n.logger.Err(ctx, closeErr).Msg("could not close DLQ handler")
		})
	}()

	n.state.Set(nodeStateRunning)

	// keep running until all components depending on this node call Done or a
	// force stop is initiated
	return (*csync.WaitGroup)(&n.wg).Wait(handlerCtx)
}

// Add should be called before Run to increase the counter of components
// depending on DLQHandlerNode. The node will keep running until the counter
// reaches 0 (see Done).
//
// Note a call with positive delta to Add should happen before Run.
func (n *DLQHandlerNode) Add(delta int) {
	n.wg.Add(delta)
}

// Done should be called before a component depending on DLQHandlerNode stops
// running and guarantees there will be no more calls to DLQHandlerNode.
func (n *DLQHandlerNode) Done() {
	n.wg.Done()
}

func (n *DLQHandlerNode) Ack(msg *Message) {
	state, err := n.state.Watch(msg.Ctx, csync.WatchValues(nodeStateRunning, nodeStateStopped, dlqHandlerNodeStateBroken))
	if err != nil {
		// message context is cancelled, the pipeline is shutting down
		return
	}
	if state != nodeStateRunning {
		// node is not running, this must mean that the DLQHandler node failed
		// to be opened, the pipeline will soon stop because of that error
		return
	}

	n.m.Lock()
	defer n.m.Unlock()
	n.window.Ack()
}

func (n *DLQHandlerNode) Nack(msg *Message, nackMetadata NackMetadata) (err error) {
	state, err := n.state.Watch(msg.Ctx,
		csync.WatchValues(nodeStateRunning, nodeStateStopped, dlqHandlerNodeStateBroken))
	if err != nil {
		// message context is cancelled, the pipeline is shutting down
		return err
	}
	if state != nodeStateRunning {
		// node is not running, this must mean that the DLQHandler node failed
		// to be opened, the pipeline will soon stop because of that error
		return cerrors.New("DLQHandlerNode is not running or broken")
	}

	n.m.Lock()
	defer n.m.Unlock()

	ok := n.window.Nack()
	if !ok {
		return cerrors.Errorf(
			"DLQ nack threshold exceeded (%d/%d), original error: %w",
			n.WindowNackThreshold, n.WindowSize, nackMetadata.Reason,
		)
	}

	defer func() {
		if err != nil {
			// write to the DLQ failed, this message is essentially lost
			// we need to stop processing more nacks and stop the pipeline
			n.state.Set(dlqHandlerNodeStateBroken)
		}
	}()

	dlqRecord, err := n.dlqRecord(msg, nackMetadata)
	if err != nil {
		return err
	}

	writeTime := time.Now()
	err = n.Handler.Write(msg.Ctx, dlqRecord)
	if err != nil {
		return err
	}
	n.Timer.Update(time.Since(writeTime))
	n.Histogram.Observe(dlqRecord)
	return nil
}

func (n *DLQHandlerNode) dlqRecord(msg *Message, nackMetadata NackMetadata) (opencdc.Record, error) {
	r := opencdc.Record{
		Position:  opencdc.Position(msg.ID()),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       nil,
		Payload: opencdc.Change{
			Before: nil,
			After:  opencdc.StructuredData(msg.Record.Map()), // failed record is stored here
		},
	}
	r.Metadata.SetCreatedAt(time.Now())
	r.Metadata.SetConduitDLQNackError(nackMetadata.Reason.Error())
	r.Metadata.SetConduitDLQNackNodeID(nackMetadata.NodeID)
	return r, nil
}

func (n *DLQHandlerNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}

func (n *DLQHandlerNode) ForceStop(ctx context.Context) {
	n.logger.Warn(ctx).Msg("force stopping DLQ handler node")
	n.handlerCtxCancel()
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
	nackThreshold uint64

	ackCount  uint64
	nackCount uint64
}

func newDLQWindow(size, threshold uint64) *dlqWindow {
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
func (w *dlqWindow) Ack() {
	w.store(false)
}

// Nack stores a nack in the window and returns true (ok). If the nack threshold
// gets exceeded the window will be frozen and will return false for all further
// calls to Nack.
func (w *dlqWindow) Nack() bool {
	w.store(true)
	return w.nackThreshold >= w.nackCount
}

func (w *dlqWindow) store(nacked bool) {
	if len(w.window) == 0 || w.nackThreshold < w.nackCount {
		return // window disabled or threshold already reached
	}

	// move cursor before updating the window
	w.cursor = (w.cursor + 1) % len(w.window)
	if w.window[w.cursor] == nacked {
		return // the old message has the same status, nothing changes
	}

	w.window[w.cursor] = nacked
	switch nacked {
	case false:
		w.nackCount--
		w.ackCount++
	case true:
		w.nackCount++
		w.ackCount--
	}
}
