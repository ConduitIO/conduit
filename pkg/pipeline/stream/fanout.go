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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type FanoutNode struct {
	Name string

	in      <-chan *Message
	out     []chan<- *Message
	running bool
}

func (n *FanoutNode) ID() string {
	return n.Name
}

func (n *FanoutNode) Run(ctx context.Context) error {
	if n.out == nil {
		panic("tried to run FanoutNode without hooking the out channel up to another node")
	}
	if n.in == nil {
		panic("tried to run FanoutNode without hooking the in channel up to another node")
	}
	if n.running {
		panic("tried to run FanoutNode twice")
	}

	n.running = true
	defer func() {
		for _, out := range n.out {
			close(out)
		}
		n.out = nil
		n.running = false
	}()

	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-n.in:
			if !ok {
				// pipeline closed
				return nil
			}

			// remainingAcks tracks how many acks we are still waiting for
			// before the ack can be propagated upstream to the original message
			remainingAcks := int32(len(n.out))

			wg.Add(len(n.out))
			for i := range n.out {
				go func(i int) {
					defer wg.Done()
					// create new message and handle ack/nack
					newMsg := msg.Clone()
					newMsg.RegisterAckHandler(
						// wrap ack handler to make sure msg is not overwritten
						// by the time ack handler is called
						n.wrapAckHandler(msg, func(msg *Message) (err error) {
							remaining := atomic.AddInt32(&remainingAcks, -1)
							if remaining == 0 {
								// this was the last ack, let's propagate it
								return msg.Ack()
							}
							// wait for other nodes to send their ack/nack
							select {
							case <-msg.Acked():
								// call ack just to get the same return value as the
								// routine that actually acked the message
								return msg.Ack()
							case <-msg.Nacked():
								return cerrors.New("message was nacked by another node")
							case <-msg.Dropped():
								return ErrMessageDropped
							}
						}),
					)
					newMsg.RegisterNackHandler(
						// wrap nack handler to make sure msg is not overwritten
						// by the time nack handler is called
						n.wrapNackHandler(msg, func(msg *Message, reason error) error {
							return msg.Nack(reason)
						}),
					)
					newMsg.RegisterDropHandler(
						// wrap drop handler to make sure msg is not overwritten
						// by the time drop handler is called
						n.wrapDropHandler(msg, func(msg *Message, reason error) {
							defer func() {
								if err := recover(); err != nil {
									if cerrors.Is(err.(error), ErrUnexpectedMessageStatus) {
										// the unexpected message status is expected (I know, right?)
										// this rare case might happen if one downstream node first
										// nacks the message and afterwards another node tries to drop
										// the message
										// this is a valid use case, the panic is trying to make us
										// notice all other invalid use cases
										return
									}
									panic(err) // re-panic
								}
							}()
							msg.Drop()
						}),
					)

					select {
					case <-ctx.Done():
						msg.Drop()
						return
					case n.out[i] <- newMsg:
					}
				}(i)
			}
			// we need to wait for all go routines to push the message to
			// downstream nodes otherwise we risk changing the order of messages
			// also there is no need to listen to ctx.Done because that's what
			// the go routines are doing already
			wg.Wait()
		}
	}
}

// wrapAckHandler modifies the ack handler, so it's called with the original
// message received by FanoutNode instead of the new message created by
// FanoutNode.
func (n *FanoutNode) wrapAckHandler(origMsg *Message, f AckHandler) AckMiddleware {
	return func(newMsg *Message, next AckHandler) error {
		err := f(origMsg)
		if err != nil {
			return err
		}
		// next handler is called again with new message
		return next(newMsg)
	}
}

// wrapNackHandler modifies the nack handler, so it's called with the original
// message received by FanoutNode instead of the new message created by
// FanoutNode.
func (n *FanoutNode) wrapNackHandler(origMsg *Message, f NackHandler) NackMiddleware {
	return func(newMsg *Message, reason error, next NackHandler) error {
		err := f(origMsg, reason)
		if err != nil {
			return err
		}
		// next handler is called again with new message
		return next(newMsg, err)
	}
}

// wrapDropHandler modifies the drop handler, so it's called with the original
// message received by FanoutNode instead of the new message created by
// FanoutNode.
func (n *FanoutNode) wrapDropHandler(origMsg *Message, f DropHandler) DropMiddleware {
	return func(newMsg *Message, reason error, next DropHandler) {
		f(origMsg, reason)
		next(newMsg, reason)
	}
}

func (n *FanoutNode) Sub(in <-chan *Message) {
	if n.in != nil {
		panic("can't connect FanoutNode to more than one in")
	}
	n.in = in
}

func (n *FanoutNode) Pub() <-chan *Message {
	out := make(chan *Message)
	n.out = append(n.out, out)
	return out
}
