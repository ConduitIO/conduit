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
	"reflect"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// triggerFunc is returned from base nodes and should be called periodically to
// fetch a new message. If the function returns nil or an error the caller
// should stop calling the trigger and discard the trigger function.
type triggerFunc func() (*Message, error)

// msgFetcherFunc is used in pubNodeBase to transform the object returned from
// the trigger channel into a message.
type msgFetcherFunc func(interface{}) (*Message, error)

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
) (triggerFunc, cleanupFunc, error) {
	trigger, cleanup1, err := n.subNodeBase.Trigger(ctx, logger, nil)
	if err != nil {
		return nil, nil, err
	}

	// call Trigger only to get the cleanup function, we won't use the trigger
	_, cleanup2, err := n.pubNodeBase.Trigger(ctx, logger, nil, nil, nil)
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
	// out is the channel to which outgoing messages will be sent.
	out chan<- *Message
	// running is true when the node is running and false when it's not.
	running bool
	// lock guards private fields from concurrent changes.
	lock sync.Mutex
}

// Trigger returns a function that will block until the PubNode should emit a
// new message. After the trigger returns an error or an empty object it is done
// and should be discarded. The trigger needs to be called continuously until
// that happens. The returned cleanup function has to be called after the last
// call of the trigger function to release resources.
func (n *pubNodeBase) Trigger(
	ctx context.Context,
	logger log.CtxLogger,
	triggerChan interface{},
	errChan <-chan error,
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

	// cleanup should be called with defer in the caller
	cleanup := func() {
		logger.Trace(ctx).Msg("cleaning up PubNode")
		n.lock.Lock()
		close(n.out)
		n.out = nil
		n.running = false
		n.lock.Unlock()
		logger.Trace(ctx).Msg("PubNode cleaned up")
	}

	if errChan == nil {
		// create dummy channel
		errChan = make(chan error)
	}

	cases := []reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(errChan)},
	}
	if triggerChan != nil {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(triggerChan)})
	} else {
		// there is no channel, use default case instead
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
	}

	trigger := func() (*Message, error) {
		chosen, value, ok := reflect.Select(cases)
		switch chosen {
		case 0:
			logger.Debug(ctx).Msg("context closed while waiting for message")
			return nil, ctx.Err()
		case 1:
			err := value.Interface().(error)
			logger.Debug(ctx).Err(err).Msg("received error on error channel")
			return nil, err
		default:
			// supplied channel sent a value (or default case)
			if !ok && triggerChan != nil {
				logger.Debug(ctx).Msg("incoming messages channel closed")
				return nil, nil
			}

			var v interface{}
			if triggerChan != nil {
				v = value.Interface()
			}
			return msgFetcher(v)
		}
	}
	return trigger, cleanup, nil
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
	if msg.Ctx == nil {
		msg.Ctx = ctxutil.ContextWithMessageID(ctx, msg.ID())
	}

	select {
	case <-ctx.Done():
		logger.Debug(msg.Ctx).Msg("context closed while sending message")
		return ctx.Err()
	case n.out <- msg:
		logger.Trace(msg.Ctx).Msg("sent message to outgoing channel")
	}
	return nil
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

	// cleanup should be called with defer in the caller
	cleanup := func() {
		logger.Trace(ctx).Msg("cleaning up SubNode")
		n.lock.Lock()
		defer n.lock.Unlock()

		n.running = false
		logger.Trace(ctx).Msg("SubNode cleaned up")
	}

	if errChan == nil {
		// create dummy channel
		errChan = make(chan error)
	}

	trigger := func() (*Message, error) {
		select {
		case <-ctx.Done():
			logger.Debug(ctx).Msg("context closed while waiting for message")
			return nil, ctx.Err()
		case err := <-errChan:
			logger.Debug(ctx).Err(err).Msg("received error on error channel")
			return nil, err
		case msg, ok := <-n.in:
			if !ok {
				logger.Debug(ctx).Msg("incoming messages channel closed")
				return nil, nil
			}

			return msg, nil
		}
	}

	return trigger, cleanup, nil
}

func (n *subNodeBase) Sub(in <-chan *Message) {
	n.lock.Lock()
	defer n.lock.Unlock()

	if n.in != nil {
		panic(cerrors.New("can't connect SubNode to more than one in"))
	}
	n.in = in
}
