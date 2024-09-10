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

	if len(n.out) == 1 {
		// shortcut if there's only 1 destination
		return n.select1(ctx)
	}

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
			remainingAcks := int32(len(n.out)) //nolint:gosec // no risk of overflow

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
							}
						}),
					)
					newMsg.RegisterNackHandler(
						// wrap nack handler to make sure msg is not overwritten
						// by the time nack handler is called
						n.wrapNackHandler(msg, func(msg *Message, nm NackMetadata) error {
							return msg.Nack(nm.Reason, nm.NodeID)
						}),
					)

					select {
					case <-ctx.Done():
						// we can ignore the error, it will show up in the
						// original msg
						_ = newMsg.Nack(ctx.Err(), n.ID())
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

			// check if the context is still alive
			if ctx.Err() != nil {
				// context was closed - if the message was nacked there's a high
				// chance it was nacked in this node by one of the goroutines
				if msg.Status() == MessageStatusNacked {
					// check if the message nack returned an error (Nack is
					// idempotent and will return the same error as in the first
					// call), return it if it returns an error
					if err := msg.Nack(nil, n.ID()); err != nil {
						return err
					}
				}
				// the message is not nacked, it must have been sent to all
				// downstream nodes just before the context got cancelled, we
				// don't care about the message anymore, so we just return the
				// context error
				return ctx.Err()
			}
		}
	}
}

func (n *FanoutNode) select1(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-n.in:
			if !ok {
				// pipeline closed
				return nil
			}
			select {
			case <-ctx.Done():
				return msg.Nack(ctx.Err(), n.ID())
			case n.out[0] <- msg:
				// all good
			}
		}
	}
}

// wrapAckHandler modifies the ack handler, so it's called with the original
// message received by FanoutNode instead of the new message created by
// FanoutNode.
func (n *FanoutNode) wrapAckHandler(origMsg *Message, f AckHandler) AckHandler {
	return func(_ *Message) error {
		return f(origMsg)
	}
}

// wrapNackHandler modifies the nack handler, so it's called with the original
// message received by FanoutNode instead of the new message created by
// FanoutNode.
func (n *FanoutNode) wrapNackHandler(origMsg *Message, f NackHandler) NackHandler {
	return func(_ *Message, nm NackMetadata) error {
		return f(origMsg, nm)
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
