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

	cases := make([]reflect.SelectCase, len(n.in)+1)
	cases[0] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}
	for i, ch := range n.in {
		cases[i+1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}
	}

	for {
		chosen, value, ok := reflect.Select(cases)
		// ok will be true if the channel has not been closed.
		if !ok {
			if chosen == 0 {
				// context is done
				return ctx.Err()
			}
			// one of the in channels is closed, remove it from select case
			cases = append(cases[:chosen], cases[chosen+1:]...)
			if len(cases) == 1 {
				// only context is left, we're done
				return nil
			}
			continue
		}

		msg := value.Interface().(*Message)

		select {
		case <-ctx.Done():
			return msg.Nack(ctx.Err(), n.ID())
		case n.out <- msg:
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
