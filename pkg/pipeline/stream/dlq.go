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

//go:generate mockgen -destination=mock/dlq.go -package=mock -mock_names=DLQHandler=DLQHandler . DLQHandler

package stream

import (
	"context"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
)

type DLQHandler interface {
	Open(context.Context) error
	Write(context.Context, record.Record, error) error
	Close() error
}

// DLQHandlerNode is called by each SourceAckerNode in a pipeline to store
// nacked messages in a dead-letter-queue.
type DLQHandlerNode struct {
	Name    string
	Handler DLQHandler

	WindowSize          int
	WindowNackThreshold int

	window *dlqWindow

	initOnce csync.Init
	wg       sync.WaitGroup
	logger   log.CtxLogger
}

func (n *DLQHandlerNode) ID() string {
	return n.Name
}

func (n *DLQHandlerNode) Run(ctx context.Context) (err error) {
	defer n.initOnce.Done()
	n.window = newDLQWindow(n.WindowSize, n.WindowNackThreshold)

	err = n.Handler.Open(ctx) // TODO use separate context? Probably!
	if err != nil {
		return cerrors.Errorf("could not open DLQ handler: %w", err)
	}
	defer func() {
		closeErr := n.Handler.Close()
		err = cerrors.LogOrReplace(err, closeErr, func() {
			n.logger.Err(ctx, closeErr).Msg("could not close DLQ handler")
		})
	}()

	n.initOnce.Done()

	// keep running until all nodes depending on this DLQHandlerNode call Done
	return (*csync.WaitGroup)(&n.wg).Wait(ctx)
}

// Add should be called before Run to increase the counter of components
// depending on DLQHandlerNode. The node will keep running until the counter
// reaches 0 (see Done).
func (n *DLQHandlerNode) Add(delta int) {
	n.wg.Add(delta)
}

// Done should be called before a component depending on DLQHandlerNode stops
// running and guarantees there will be no more calls to DLQHandlerNode.
func (n *DLQHandlerNode) Done() {
	n.wg.Done()
}

func (n *DLQHandlerNode) Ack(msg *Message) {
	err := n.initOnce.Wait(msg.Ctx)
	if err != nil {
		// message context is cancelled, the pipeline is shutting down
		return
	}
	n.window.Ack()
}

func (n *DLQHandlerNode) Nack(msg *Message, reason error) error {
	err := n.initOnce.Wait(msg.Ctx)
	if err != nil {
		// message context is cancelled, the pipeline is shutting down
		return err
	}

	err = n.window.Nack()
	if err != nil {
		return err // window threshold is exceeded
	}

	return n.Handler.Write(msg.Ctx, msg.Record, reason)
}

func (n *DLQHandlerNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
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
func (w *dlqWindow) Ack() {
	w.store(false)
}

// Nack stores a nack in the window. If the nack threshold gets exceeded the
// window will be frozen and will return an error for all further calls to Nack.
func (w *dlqWindow) Nack() error {
	w.store(true)
	if w.nackThreshold < w.nackCount {
		return cerrors.Errorf(
			"nack threshold exceeded (%d/%d)",
			w.nackThreshold, len(w.window),
		)
	}
	return nil
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
