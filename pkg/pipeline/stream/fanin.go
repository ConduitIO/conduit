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
)

type FaninNode struct {
	Name string

	in      []<-chan *Message
	out     chan<- *Message
	running bool
}

func (n *FaninNode) ID() string {
	return n.Name
}

func (n *FaninNode) Run(ctx context.Context) error {
	if n.out == nil {
		panic("tried to run FaninNode without hooking the out channel up to another node")
	}
	if n.in == nil {
		panic("tried to run FaninNode without hooking the in channel up to another node")
	}
	if n.running {
		panic("tried to run FaninNode twice")
	}

	n.running = true
	defer func() {
		close(n.out)
		n.out = nil
		n.running = false
	}()

	trigger := n.trigger(ctx)

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		select {
		case <-ctx.Done():
			return msg.Nack(ctx.Err(), n.ID())
		case n.out <- msg:
		}
	}
}

func (n *FaninNode) trigger(ctx context.Context) func() (*Message, error) {
	in := make([]<-chan *Message, len(n.in))
	copy(in, n.in)

	f := n.chooseSelectFunc(ctx, in)

	return func() (*Message, error) {
		for {
			chosen, msg, ok := f()
			// ok will be true if the channel has not been closed.
			if !ok {
				if chosen == 0 {
					// context is done
					return nil, ctx.Err()
				}
				// one of the in channels is closed, remove it from select case
				in = append(in[:chosen-1], in[chosen:]...)
				if len(in) == 0 {
					// only context is left, we're done
					return nil, nil
				}

				f = n.chooseSelectFunc(ctx, in)
				continue // keep selecting with new select func
			}
			return msg, nil
		}
	}
}

func (n *FaninNode) Sub(in <-chan *Message) {
	n.in = append(n.in, in)
}

func (n *FaninNode) Pub() <-chan *Message {
	if n.out != nil {
		panic("can't connect FaninNode to more than one out")
	}
	out := make(chan *Message)
	n.out = out
	return out
}
