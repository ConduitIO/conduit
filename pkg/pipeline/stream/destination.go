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
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/record"
)

// DestinationNode wraps a Destination connector and implements the Sub node interface
type DestinationNode struct {
	Name           string
	Destination    connector.Destination
	ConnectorTimer metrics.Timer

	base   subNodeBase
	logger log.CtxLogger
}

func (n *DestinationNode) ID() string {
	return n.Name
}

func (n *DestinationNode) Run(ctx context.Context) (err error) {
	// start a fresh connector context to make sure the connector is running
	// until this method returns
	connectorCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// first open connector, this means we actually start the plugin process
	err = n.Destination.Open(connectorCtx)
	if err != nil {
		return cerrors.Errorf("could not open destination connector: %w", err)
	}

	ackHandler := newDestinationAckHandler(n.logger, n.Destination)
	go ackHandler.HandleAcks(ctx)

	stopDemux := make(chan struct{})
	defer close(stopDemux)
	mainErrors := n.demuxErrors(ackHandler.Errors(), n.Destination.Errors(), stopDemux)

	defer func() {
		// wait for all acks to be received, wait for at most 30 seconds
		// if ctx is cancelled we won't wait for acks
		waitCtx, cancel := context.WithTimeout(ctx, time.Second*30)
		defer cancel()
		ackHandler.Wait(waitCtx)

		ackHandler.Stop()
		// TODO drain errors chan
		// teardown will kill the plugin process
		tdErr := n.Destination.Teardown(connectorCtx)
		if tdErr != nil {
			if err == nil {
				// return error to caller
				err = tdErr
			} else {
				// we are already returning an error, just log this error
				n.logger.Err(ctx, err).Msg("could not tear down destination connector")
			}
		}
	}()

	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, mainErrors)
	if err != nil {
		return err
	}
	defer cleanup()

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		n.logger.Trace(msg.Ctx).Msg("writing record to destination connector")

		// first signal ack handler we might receive an ack, since this could
		// already happen before write returns
		ackHandler.ExpectAck(msg)

		writeTime := time.Now()
		err = n.Destination.Write(msg.Ctx, msg.Record)
		n.ConnectorTimer.Update(time.Since(writeTime))
		if err != nil {
			ackHandler.ForgetAndDrop(msg)
			return cerrors.Errorf("error writing to destination: %w", err)
		}
	}
}

// demuxErrors is a utility that demultiplexes errors from two channels into
// one. The function blocks until the stop channel is closed.
func (n *DestinationNode) demuxErrors(c1, c2 <-chan error, stop chan struct{}) <-chan error {
	out := make(chan error)
	go func() {
		var err error
		for {
			select {
			case err = <-c1:
			case err = <-c2:
			case <-stop:
				return
			}
			select {
			case out <- err:
				// all good, clear err var
				err = nil
			case <-stop:
				return
			}
		}
	}()
	return out
}

// Sub will subscribe this node to an incoming channel.
func (n *DestinationNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

// SetLogger sets the logger.
func (n *DestinationNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}

// destinationAckHandler is responsible for handling acknowledgments received
// from the destination and forwarding them to the correct message.
type destinationAckHandler struct {
	logger      log.CtxLogger
	destination connector.Destination

	// cache stores the messages that are still waiting for an ack/nack.
	cache *positionMessageMap
	// outstanding stores the number of acks the handler is still waiting for.
	// Essentially it is the length of the map.
	outstanding uint32

	// notifyWait is used to notify the Wait function there are no outstanding
	// acks left.
	notifyWait chan struct{}
	// stop the context and make HandleAcks return.
	stop context.CancelFunc
	// errs is used to signal the node that the handler experienced an error
	// when it was processing an ack asynchronously.
	errs chan error
}

func newDestinationAckHandler(logger log.CtxLogger, destination connector.Destination) *destinationAckHandler {
	return &destinationAckHandler{
		logger:      logger,
		destination: destination,

		cache:       &positionMessageMap{},
		outstanding: 0,
		notifyWait:  make(chan struct{}),
		errs:        make(chan error),
	}
}

// HandleAcks continuously fetches acks from the destination and forwards them
// to the correct message by calling Ack or Nack on that message.
func (h *destinationAckHandler) HandleAcks(ctx context.Context) {
	h.logger.Debug(ctx).Msg("starting destination node ack handler")
	defer func() {
		h.logger.Debug(ctx).Msg("destination node ack handler stopped")
	}()
	defer close(h.notifyWait)

	ctx, h.stop = context.WithCancel(ctx)
	for {
		pos, err := h.destination.Ack(ctx)
		if pos == nil {
			// empty position is returned only if an actual error happened
			h.returnOrLogError(ctx, err)
			return
		}
		msg, ok := h.cache.LoadAndDelete(pos)
		if !ok {
			h.logger.Error(ctx).
				Str(log.RecordPositionField, pos.String()).
				Msg("received unexpected ack (could be an internal bug or a badly written connector), ignoring the ack and continuing, please report the issue to the Conduit team")
			continue
		}

		err = h.handleAck(msg, err)
		if err != nil && !h.returnOrLogError(ctx, err) {
			// was not able to return error, context was canceled
			return
		}

		remaining := atomic.AddUint32(&h.outstanding, ^uint32(0)) // decrement
		if remaining == 0 {
			// counter dropped to 0, try to notify goroutines that called Wait
			select {
			case h.notifyWait <- struct{}{}:
				// notified someone waiting for the outstanding count to drop to 0
			default:
				// Wait wasn't called
			}
		}
	}
}

// handleAck either acks or nacks the message, depending on the supplied error.
// If the nacking or acking fails, the message is dropped and the error is
// returned.
func (h *destinationAckHandler) handleAck(msg *Message, err error) error {
	switch {
	case err != nil:
		h.logger.Trace(msg.Ctx).Err(err).Msg("nacking message")
		err = msg.Nack(err)
		if err != nil {
			msg.Drop()
			return cerrors.Errorf("error while nacking message: %w", err)
		}
	default:
		h.logger.Trace(msg.Ctx).Msg("acking message")
		err = msg.Ack()
		if err != nil {
			msg.Drop()
			return cerrors.Errorf("error while acking message: %w", err)
		}
	}
	return nil
}

// returnOrLogError will try to send err to the errs channel and return true if
// it succeeds. If the context gets canceled the error gets logged instead and
// the function returns false.
func (h *destinationAckHandler) returnOrLogError(ctx context.Context, err error) (returned bool) {
	select {
	case <-ctx.Done():
		// context got canceled in the meantime, log error instead
		h.logger.Err(ctx, err).Msg("destination ack handler experienced an error")
		return false
	case h.errs <- err:
		return true
	}
}

// ExpectAck makes the handler aware of the message and signals to it that an
// ack for this message might be received at some point.
func (h *destinationAckHandler) ExpectAck(msg *Message) {
	atomic.AddUint32(&h.outstanding, 1)
	_, loaded := h.cache.LoadOrStore(msg.Record.Position, msg)
	if loaded {
		// we already have a message with the same position in the cache
		panic(cerrors.Errorf("encountered two records with the same position %q, can't differentiate them (could be that you are using a pipeline with two same source connectors and they both produced a record with the same position at the same time, could also be a badly written source connector that doesn't assign unique positions to records)", msg.Record.Position.String()))
	}
}

// ForgetAndDrop signals the handler that an ack for this message won't be
// received, and it should remove it from its cache. In case an ack for this
// message wasn't yet received it drops the message, otherwise it does nothing.
func (h *destinationAckHandler) ForgetAndDrop(msg *Message) {
	_, ok := h.cache.LoadAndDelete(msg.Record.Position)
	if !ok {
		// message wasn't found in the cache, looks like the message was already
		// acked / nacked
		return
	}
	msg.Drop()
	atomic.AddUint32(&h.outstanding, ^uint32(0)) // decrement
}

// Errors returns a channel that is used to signal the node that the handler
// experienced an error when it was processing an ack asynchronously.
func (h *destinationAckHandler) Errors() <-chan error {
	return h.errs
}

// Wait can be used to wait for the count of outstanding acks to drop to 0 or
// the context gets canceled. This method is not safe for concurrent use, if
// there are outstanding acks and multiple goroutines called Wait, then only one
// of them will return.
func (h *destinationAckHandler) Wait(ctx context.Context) {
	if atomic.LoadUint32(&h.outstanding) == 0 {
		return // don't wait
	}
	// wait until notifyWait is either closed or it receives a value or the context gets canceled
	select {
	case <-ctx.Done():
	case <-h.notifyWait:
	}
}

// Stop the ack handler main method.
func (h *destinationAckHandler) Stop() {
	if h.stop != nil {
		h.stop()
	}
}

// positionMessageMap is like a Go map[record.Position]*Message but is safe for
// concurrent use by multiple goroutines. See documentation for sync.Map for
// more information (it's being used under the hood).
type positionMessageMap struct {
	sync.Map
}

func (m *positionMessageMap) Load(pos record.Position) (*Message, bool) {
	msg, ok := m.Map.Load(m.key(pos))
	return msg.(*Message), ok
}
func (m *positionMessageMap) Store(pos record.Position, msg *Message) {
	m.Map.Store(m.key(pos), msg)
}
func (m *positionMessageMap) LoadAndDelete(pos record.Position) (*Message, bool) {
	msg, ok := m.Map.LoadAndDelete(m.key(pos))
	return msg.(*Message), ok
}
func (m *positionMessageMap) LoadOrStore(pos record.Position, msg *Message) (*Message, bool) {
	actual, ok := m.Map.LoadOrStore(m.key(pos), msg)
	return actual.(*Message), ok
}
func (m *positionMessageMap) Delete(pos record.Position) {
	m.Map.Delete(m.key(pos))
}

// key takes a position and converts it into a hashable object that can be used
// as a key in a map.
func (m *positionMessageMap) key(pos record.Position) interface{} {
	return string(pos)
}
