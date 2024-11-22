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

package stream

import (
	"bytes"
	"context"
	"sync"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gammazero/deque"
)

// DestinationAckerNode is responsible for handling acknowledgments received
// from the destination and forwarding them to the correct message.
type DestinationAckerNode struct {
	Name        string
	Destination Destination

	// queue is used to store messages
	queue deque.Deque[*Message]

	queueMutex sync.Mutex

	// mctx guards access to the contextCtxCancel function
	mctx sync.Mutex

	base   subNodeBase
	logger log.CtxLogger

	connectorCtxCancel context.CancelFunc
}

func (n *DestinationAckerNode) ID() string {
	return n.Name
}

func (n *DestinationAckerNode) Run(ctx context.Context) (err error) {
	// start a fresh connector context to make sure the connector is running
	// until this method returns
	var connectorCtx context.Context
	n.mctx.Lock()
	connectorCtx, n.connectorCtxCancel = context.WithCancel(context.Background())
	n.mctx.Unlock()
	defer n.connectorCtxCancel()

	// signalChan is buffered to ensure signals don't get lost if worker is busy
	signalChan := make(chan struct{}, 1)
	errChan := make(chan error)

	defer func() {
		close(signalChan)
		workerErr := <-errChan
		err = cerrors.LogOrReplace(err, workerErr, func() {
			n.logger.Err(ctx, workerErr).Msg("destination acker node worker failed")
		})
		teardownErr := n.teardown(connectorCtx, err)
		err = cerrors.LogOrReplace(err, teardownErr, func() {
			n.logger.Err(ctx, teardownErr).Msg("destination acker node stopped before processing all messages")
		})
	}()

	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, errChan)
	if err != nil {
		close(errChan) // need to close errChan to not block deferred function
		return err
	}

	// start worker that will fetch acks from the connector and forward them to
	// internal messages
	// the worker uses the node context, so it can stop in case another node
	// stops with an error and cancels the context, this way the worker stops as
	// soon as possible and the outstanding acks/nacks will be fetched via the
	// cleanup function
	go n.worker(ctx, signalChan, errChan)

	defer cleanup()
	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		n.queueMutex.Lock()
		n.queue.PushBack(msg)
		n.queueMutex.Unlock()
		select {
		case signalChan <- struct{}{}:
			// triggered the start of listening to acks in worker goroutine
		default:
			// worker goroutine is already busy, it will pick up the message
			// because it is already stored in the queue
		}
	}
}

func (n *DestinationAckerNode) worker(
	ctx context.Context,
	signalChan <-chan struct{},
	errChan chan<- error,
) {
	handleError := func(msg *Message, err error) {
		// push message back to the front of the queue and return error
		n.queueMutex.Lock()
		n.queue.PushFront(msg)
		n.queueMutex.Unlock()

		errChan <- err
	}

	var acks []connector.DestinationAck

	defer close(errChan)
	for range signalChan {
		// signal is received when a new message is in the queue
		// let's start fetching acks for messages in the queue
		for {
			// check if there are more messages waiting in the queue
			n.queueMutex.Lock()
			if n.queue.Len() == 0 {
				n.queueMutex.Unlock()
				break
			}
			msg := n.queue.PopFront()
			n.queueMutex.Unlock()

			if msg.filtered {
				n.logger.Info(ctx).
					Str(log.MessageIDField, msg.ID()).
					Msg("acking filtered message")
				err := n.handleAck(msg, nil)
				if err != nil {
					errChan <- err
					return
				}
				continue
			}

			if len(acks) == 0 {
				// Ack can return multiple acks, store them and check the position
				// for the current message
				var err error
				acks, err = n.Destination.Ack(ctx)
				if err != nil {
					handleError(msg, cerrors.Errorf("error while fetching acks: %w", err))
					return
				}
			}

			ack := acks[0]
			acks = acks[1:]

			if !bytes.Equal(msg.Record.Position, ack.Position) {
				handleError(msg, cerrors.Errorf("received unexpected ack, expected position %q but got %q", msg.Record.Position, ack.Position))
				return
			}
			err := n.handleAck(msg, ack.Error)
			if err != nil {
				errChan <- err
				return
			}
		}
	}
}

// handleAck either acks or nacks the message, depending on the supplied error.
// If the nacking or acking fails the error is returned.
func (n *DestinationAckerNode) handleAck(msg *Message, err error) error {
	switch {
	case err != nil:
		n.logger.Trace(msg.Ctx).Err(err).Msg("nacking message")
		err = msg.Nack(err, n.ID())
		if err != nil {
			return cerrors.Errorf("error while nacking message: %w", err)
		}
	default:
		n.logger.Trace(msg.Ctx).Msg("acking message")
		err = msg.Ack()
		if err != nil {
			return cerrors.Errorf("error while acking message: %w", err)
		}
	}
	return nil
}

// teardown will nack all messages still in the cache and return an error in
// case there were still unprocessed messages in the cache.
func (n *DestinationAckerNode) teardown(connectorCtx context.Context, reason error) error {
	// no need to lock, at this point the worker is not running anymore

	nacked := n.queue.Len()
	var errs []error
	for n.queue.Len() > 0 {
		msg := n.queue.PopFront()
		if err := msg.Nack(reason, n.ID()); err != nil {
			errs = append(errs, err)
		}
	}

	var err error
	if len(errs) > 0 {
		err = cerrors.Errorf("nacked %d messages when stopping destination acker node, %d nacks failed: %w", nacked, len(errs), cerrors.Join(errs...))
	} else if nacked > 0 {
		err = cerrors.Errorf("nacked %d messages when stopping destination acker node", nacked)
	}

	// Spin up goroutine that will keep fetching acks.
	// This is needed in case the destination plugin fails to write a record and
	// returns a nack while the pipeline doesn't have a nack handler configured.
	// In that case the destination will keep on processing new messages (it
	// can't know that there is no nack handler so it needs to keep on going),
	// while DestinationAckerNode will stop running and listening to new acks,
	// which can cause a deadlock in the destination plugin. That's why we spin
	// up a goroutine that stops once the remaining messages have been fetched.
	if nacked > 0 {
		go func() {
			for i := 0; i < nacked; i++ {
				pos, err := n.Destination.Ack(connectorCtx)
				_ = err // ignore error
				if pos == nil {
					return // stream was closed, we can stop
				}
			}
		}()
	}

	return err
}

// Sub will subscribe this node to an incoming channel.
func (n *DestinationAckerNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

// SetLogger sets the logger.
func (n *DestinationAckerNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}

func (n *DestinationAckerNode) ForceStop(ctx context.Context) {
	n.logger.Warn(ctx).Msg("force stopping destination acker node")
	n.mctx.Lock()
	n.connectorCtxCancel()
	n.mctx.Unlock()
}
