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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// nodeState is used to represent the state of a node (in nodes that need it).
type nodeState string

var (
	nodeStateRunning nodeState = "running"
	nodeStateStopped nodeState = "stopped"
)

// triggerFunc is returned from base nodes and should be called periodically to
// fetch a new message. If the function returns nil or an error the caller
// should stop calling the trigger and discard the trigger function.
type triggerFunc func() (*Message, error)

// msgFetcherFunc is used in pubNodeBase to fetch the next message.
type msgFetcherFunc func(context.Context) ([]*Message, error)

// cleanupFunc should be called to release any resources.
type cleanupFunc func()

// pubSubNodeBase can be used as the base for nodes that implement PubSubNode.
type pubSubNodeBase struct {
	pubNodeBase pubNodeBase
	subNodeBase subNodeBase
}

// Trigger returns a function that will block until the PubSubNode receives a
// new message. After the trigger returns an error or an empty object it is done
// and should be discarded. The trigger needs to be called continuously until
// that happens. The returned cleanup function has to be called after the last
// call of the trigger function to release resources.
func (n *pubSubNodeBase) Trigger(
	ctx context.Context,
	logger log.CtxLogger,
	externalErrChan <-chan error,
) (triggerFunc, cleanupFunc, error) {
	trigger, cleanup1, err := n.subNodeBase.Trigger(ctx, logger, externalErrChan)
	if err != nil {
		return nil, nil, err
	}

	// call Trigger to set up node as running and get cleanup func
	_, cleanup2, err := n.pubNodeBase.Trigger(ctx, logger, nil, nil)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		cleanup1()
		cleanup2()
	}

	return trigger, cleanup, nil
}

func (n *pubSubNodeBase) Sub(in <-chan *Message) {
	n.subNodeBase.Sub(in)
}

// In returns the inbound message channel this node reads from. It is exposed so
// a PubSubNode (e.g. ProcessorNode) can select over the inbound channel together
// with its own control signals (such as a live-reconfigure wake) instead of
// blocking solely in Trigger's Receive. The channel is set by Sub during wiring;
// In returns nil until then.
func (n *pubSubNodeBase) In() <-chan *Message {
	return n.subNodeBase.in
}

func (n *pubSubNodeBase) Pub() <-chan *Message {
	return n.pubNodeBase.Pub()
}

// Send is a utility function to send the message to the next node.
// See pubNodeBase.Send.
func (n *pubSubNodeBase) Send(
	ctx context.Context,
	logger log.CtxLogger,
	msg *Message,
) error {
	return n.pubNodeBase.Send(ctx, logger, msg)
}

// pubNodeBase can be used as the base for nodes that implement PubNode.
type pubNodeBase struct {
	nodeBase
	// out is the channel to which outgoing messages will be sent.
	out chan<- *Message
	// running is true when the node is running and false when it's not.
	running bool
	// lock guards private fields from concurrent changes.
	lock sync.Mutex

	// msgChan is an internal channel where messages from msgFetcher are collected
	msgChan chan *Message
	// done is closed as soon as nothing will read from msgChan anymore: the
	// trigger returned its final error, or cleanup was called, whichever
	// happens first. Every send into msgChan and every internal error
	// forward must select on it, otherwise it can block forever (#2969).
	// Guarded by lock, the channel itself is closed without holding lock.
	done chan struct{}
}

// Trigger sets up 2 goroutines, one that listens to the external error channel
// and forwards it to the trigger function and one that continuously calls the
// supplied msgFetcherFunc and supplies the result to the trigger function. The
// trigger function should be continuously called to retrieve a message or an
// error. After the trigger returns an error or an empty object, it is done and
// should be discarded. The trigger needs to be called continuously until that
// happens. The returned cleanup function has to be called after the last call
// of the trigger function to release resources.
func (n *pubNodeBase) Trigger(
	ctx context.Context,
	logger log.CtxLogger,
	externalErrChan <-chan error,
	msgFetcher msgFetcherFunc,
) (triggerFunc, cleanupFunc, error) {
	n.lock.Lock()
	defer n.lock.Unlock()

	if n.out == nil {
		return nil, nil, cerrors.New("tried to run PubNode without hooking the out channel up to another node")
	}
	if n.running {
		return nil, nil, cerrors.New("tried to run PubNode twice")
	}

	n.running = true
	n.msgChan = make(chan *Message)
	done := make(chan struct{})
	n.done = done
	var doneOnce sync.Once
	markDone := func() { doneOnce.Do(func() { close(done) }) }
	internalErrChan := make(chan error)

	if externalErrChan != nil {
		// spawn goroutine that forwards external errors into the internal error
		// channel
		go forwardErrors(ctx, done, externalErrChan, internalErrChan)
	}

	var firstTriggerCall sync.Once
	trigger := func() (*Message, error) {
		// first time the trigger is called spawn goroutine that is fetching
		// messages and either forwards an error into the internal error channel
		// or a message into the message channel
		firstTriggerCall.Do(func() {
			if msgFetcher == nil {
				return
			}
			go fetchMessages(ctx, done, msgFetcher, n.msgChan, internalErrChan)
		})
		msg, err := n.nodeBase.Receive(ctx, logger, n.msgChan, internalErrChan)
		if err != nil || msg == nil {
			// The trigger is finished, nothing reads msgChan from here on.
			// Release anything blocked on sending into it (InjectControlMessage
			// in particular) before the owner starts tearing down (#2969).
			markDone()
		}
		return msg, err
	}
	cleanup := func() {
		// Also covers nodes that stop without the trigger failing (e.g. a
		// failed Send). Must happen before n.cleanup, which needs n.lock.
		// The spawned goroutines select on done and exit; a msgFetcher call
		// that is still blocked in the connector returns once the connector
		// is torn down or ctx is canceled, then exits at its next send.
		markDone()
		n.cleanup(ctx, logger)
	}

	return trigger, cleanup, nil
}

// forwardErrors forwards errors from the external error channel into the
// internal one until ctx is canceled or done is closed. It never blocks past
// either (#2969).
func forwardErrors(ctx context.Context, done <-chan struct{}, external <-chan error, internal chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case err := <-external:
			select {
			case internal <- err:
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

// fetchMessages continuously calls fetch and hands the result to msgChan or
// internalErrChan until fetch fails or done is closed. Every hand-over selects
// on done so the goroutine can't leak once nobody receives anymore (#2969).
func fetchMessages(
	ctx context.Context,
	done <-chan struct{},
	fetch msgFetcherFunc,
	msgChan chan<- *Message,
	internalErrChan chan<- error,
) {
	for {
		msgs, err := fetch(ctx)
		if err != nil {
			if !cerrors.Is(err, context.Canceled) {
				// ignore context error because it is going to be caught
				// by nodeBase.Receive anyway
				select {
				case internalErrChan <- err:
				case <-done:
				}
			}
			return
		}
		for _, msg := range msgs {
			select {
			case msgChan <- msg:
			case <-done:
				return
			}
		}
	}
}

// InjectControlMessage can be used to inject a message into the message stream.
// This is used to inject control messages like the last position message when
// stopping a source connector. It is a bit hacky, but it doesn't require us to
// create a separate channel for signals which makes it performant and easiest
// to implement.
func (n *pubNodeBase) InjectControlMessage(ctx context.Context, msgType ControlMessageType, r opencdc.Record) error {
	// Invariant 7 (#2969): never block on the send while holding n.lock.
	// cleanup needs n.lock, so a send nobody receives would wedge shutdown
	// forever. Take what we need under the lock, then send without it.
	n.lock.Lock()
	if !n.running {
		n.lock.Unlock()
		return cerrors.New("tried to inject control message but PubNode is not running")
	}
	msgChan, done := n.msgChan, n.done
	n.lock.Unlock()

	// msgChan is unbuffered and never closed, so a send either completes
	// because the node received the message, or we give up. Giving up is an
	// error, never a silent success: the caller must know the stop message was
	// not delivered. Invariants 1 and 3 are untouched, no record passes through
	// here, and the run's cause stays the error that made the node stop.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return cerrors.New("tried to inject control message but PubNode has stopped running")
	case msgChan <- &Message{controlMessageType: msgType, Record: r}:
		return nil
	}
}

func (n *pubNodeBase) cleanup(ctx context.Context, logger log.CtxLogger) {
	n.lock.Lock()
	defer n.lock.Unlock()

	logger.Trace(ctx).Msg("cleaning up PubNode")
	close(n.out)
	n.out = nil
	n.running = false
	logger.Trace(ctx).Msg("PubNode cleaned up")
}

// Send is a utility function to send the message to the next node. Before
// sending the message out it checks if the message contains a context and adds
// it if needed. It listens for context cancellation while trying to send the
// message and returns an error if the context is canceled in the meantime.
func (n *pubNodeBase) Send(
	ctx context.Context,
	logger log.CtxLogger,
	msg *Message,
) error {
	return n.nodeBase.Send(ctx, logger, msg, n.out)
}

func (n *pubNodeBase) Pub() <-chan *Message {
	n.lock.Lock()
	defer n.lock.Unlock()

	if n.out != nil {
		panic(cerrors.New("can't connect PubNode to more than one out"))
	}
	out := make(chan *Message)
	n.out = out
	return out
}

// subNodeBase can be used as the base for nodes that implement SubNode.
type subNodeBase struct {
	nodeBase
	// in is the channel on which incoming messages will be received.
	in <-chan *Message
	// running is true when the node is running and false when it's not.
	running bool
	// lock guards private fields from concurrent changes.
	lock sync.Mutex
}

// Trigger returns a function that will block until the SubNode receives a new
// message. After the trigger returns an error or an empty object it is done and
// should be discarded. The trigger needs to be called continuously until that
// happens. The returned cleanup function has to be called after the last call
// of the trigger function to release resources.
func (n *subNodeBase) Trigger(
	ctx context.Context,
	logger log.CtxLogger,
	errChan <-chan error,
) (triggerFunc, cleanupFunc, error) {
	n.lock.Lock()
	defer n.lock.Unlock()

	if n.in == nil {
		return nil, nil, cerrors.New("tried to run SubNode without hooking the in channel up to another node")
	}
	if n.running {
		return nil, nil, cerrors.New("tried to run SubNode twice")
	}

	n.running = true

	trigger := func() (*Message, error) {
		return n.nodeBase.Receive(ctx, logger, n.in, errChan)
	}

	cleanup := func() {
		n.cleanup(ctx, logger)
	}

	return trigger, cleanup, nil
}

func (n *subNodeBase) cleanup(ctx context.Context, logger log.CtxLogger) {
	n.lock.Lock()
	defer n.lock.Unlock()

	logger.Trace(ctx).Msg("cleaning up SubNode")
	n.running = false
	logger.Trace(ctx).Msg("SubNode cleaned up")
}

func (n *subNodeBase) Sub(in <-chan *Message) {
	n.lock.Lock()
	defer n.lock.Unlock()

	if n.in != nil {
		panic(cerrors.New("can't connect SubNode to more than one in"))
	}
	n.in = in
}

type nodeBase struct{}

// Receive is a utility function that blocks until any of these things happen:
// * Context is closed - in this case ctx.Err() is returned
// * An error is received on errChan - in this case the error is returned
// * A message is received on in channel - in this case the message is returned
// * The message channel is closed - in this case nil is returned
func (n *nodeBase) Receive(
	ctx context.Context,
	logger log.CtxLogger,
	in <-chan *Message,
	errChan <-chan error,
) (*Message, error) {
	select {
	case <-ctx.Done():
		logger.Debug(ctx).Msg("context closed while waiting for message")
		return nil, ctx.Err()
	case err := <-errChan:
		logger.Debug(ctx).Err(err).Msg("received error on error channel")
		return nil, err
	case msg, ok := <-in:
		if !ok {
			logger.Debug(ctx).Msg("incoming messages channel closed")
			return nil, nil
		}
		return msg, nil
	}
}

// Send is a utility function to send the message to the next node. Before
// sending the message out it checks if the message contains a context and adds
// it if needed. It listens for context cancellation while trying to send the
// message and returns an error if the context is canceled in the meantime.
func (n *nodeBase) Send(
	ctx context.Context,
	logger log.CtxLogger,
	msg *Message,
	out chan<- *Message,
) error {
	if msg.Ctx == nil {
		msg.Ctx = ctxutil.ContextWithMessageID(ctx, msg.ID())
	}
	// copy context into a local variable, we shouldn't access it anymore after
	// we send the message to out, this prevents race conditions in case the
	// field gets changed
	msgCtx := msg.Ctx

	select {
	case <-ctx.Done():
		logger.Debug(msgCtx).Msg("context closed while sending message")
		return ctx.Err()
	case out <- msg:
		logger.Trace(msgCtx).Msg("sent message to outgoing channel")
	}
	return nil
}
