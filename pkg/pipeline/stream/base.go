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
	internalErrChan := make(chan error)

	if externalErrChan != nil {
		// spawn goroutine that forwards external errors into the internal error
		// channel
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case err := <-externalErrChan:
					internalErrChan <- err
				}
			}
		}()
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
			go func() {
				for {
					msgs, err := msgFetcher(ctx)
					if err != nil {
						if !cerrors.Is(err, context.Canceled) {
							// ignore context error because it is going to be caught
							// by nodeBase.Receive anyway
							internalErrChan <- err
						}
						return
					}
					for _, msg := range msgs {
						n.msgChan <- msg
					}
				}
			}()
		})
		return n.nodeBase.Receive(ctx, logger, n.msgChan, internalErrChan)
	}
	cleanup := func() {
		// TODO make sure spawned goroutines are stopped and internal channels
		//  are drained
		n.cleanup(ctx, logger)
	}

	return trigger, cleanup, nil
}

// InjectControlMessage can be used to inject a message into the message stream.
// This is used to inject control messages like the last position message when
// stopping a source connector. It is a bit hacky, but it doesn't require us to
// create a separate channel for signals which makes it performant and easiest
// to implement.
func (n *pubNodeBase) InjectControlMessage(ctx context.Context, msgType ControlMessageType, r opencdc.Record) error {
	n.lock.Lock()
	defer n.lock.Unlock()
	if !n.running {
		return cerrors.New("tried to inject control message but PubNode is not running")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case n.msgChan <- &Message{controlMessageType: msgType, Record: r}:
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
