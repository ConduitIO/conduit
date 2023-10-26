// Copyright © 2023 Meroxa, Inc.
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

func (n *FaninNode) chooseSelectFunc(ctx context.Context, in []<-chan *Message) func() (int, *Message, bool) {
	switch len(in) {
	case 1:
		return func() (int, *Message, bool) { return n.select1(ctx, in[0]) }
	case 2:
		return func() (int, *Message, bool) { return n.select2(ctx, in[0], in[1]) }
	case 3:
		return func() (int, *Message, bool) { return n.select3(ctx, in[0], in[1], in[2]) }
	case 4:
		return func() (int, *Message, bool) { return n.select4(ctx, in[0], in[1], in[2], in[3]) }
	case 5:
		return func() (int, *Message, bool) { return n.select5(ctx, in[0], in[1], in[2], in[3], in[4]) }
	case 6:
		return func() (int, *Message, bool) { return n.select6(ctx, in[0], in[1], in[2], in[3], in[4], in[5]) }
	default:
		// use reflection for more channels
		cases := make([]reflect.SelectCase, len(in)+1)
		cases[0] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}
		for i, ch := range in {
			cases[i+1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}
		}
		return func() (int, *Message, bool) {
			chosen, value, ok := reflect.Select(cases)
			if !ok { // a channel was closed
				return chosen, nil, ok
			}
			return chosen, value.Interface().(*Message), ok
		}
	}
}

func (*FaninNode) select1(
	ctx context.Context,
	c1 <-chan *Message,
) (int, *Message, bool) {
	select {
	case <-ctx.Done():
		return 0, nil, false
	case val, ok := <-c1:
		return 1, val, ok
	}
}

func (*FaninNode) select2(
	ctx context.Context,
	c1 <-chan *Message,
	c2 <-chan *Message,
) (int, *Message, bool) {
	select {
	case <-ctx.Done():
		return 0, nil, false
	case val, ok := <-c1:
		return 1, val, ok
	case val, ok := <-c2:
		return 2, val, ok
	}
}

func (*FaninNode) select3(
	ctx context.Context,
	c1 <-chan *Message,
	c2 <-chan *Message,
	c3 <-chan *Message,
) (int, *Message, bool) {
	select {
	case <-ctx.Done():
		return 0, nil, false
	case val, ok := <-c1:
		return 1, val, ok
	case val, ok := <-c2:
		return 2, val, ok
	case val, ok := <-c3:
		return 3, val, ok
	}
}

func (*FaninNode) select4(
	ctx context.Context,
	c1 <-chan *Message,
	c2 <-chan *Message,
	c3 <-chan *Message,
	c4 <-chan *Message,
) (int, *Message, bool) {
	select {
	case <-ctx.Done():
		return 0, nil, false
	case val, ok := <-c1:
		return 1, val, ok
	case val, ok := <-c2:
		return 2, val, ok
	case val, ok := <-c3:
		return 3, val, ok
	case val, ok := <-c4:
		return 4, val, ok
	}
}

func (*FaninNode) select5(
	ctx context.Context,
	c1 <-chan *Message,
	c2 <-chan *Message,
	c3 <-chan *Message,
	c4 <-chan *Message,
	c5 <-chan *Message,
) (int, *Message, bool) {
	select {
	case <-ctx.Done():
		return 0, nil, false
	case val, ok := <-c1:
		return 1, val, ok
	case val, ok := <-c2:
		return 2, val, ok
	case val, ok := <-c3:
		return 3, val, ok
	case val, ok := <-c4:
		return 4, val, ok
	case val, ok := <-c5:
		return 5, val, ok
	}
}

func (*FaninNode) select6(
	ctx context.Context,
	c1 <-chan *Message,
	c2 <-chan *Message,
	c3 <-chan *Message,
	c4 <-chan *Message,
	c5 <-chan *Message,
	c6 <-chan *Message,
) (int, *Message, bool) {
	select {
	case <-ctx.Done():
		return 0, nil, false
	case val, ok := <-c1:
		return 1, val, ok
	case val, ok := <-c2:
		return 2, val, ok
	case val, ok := <-c3:
		return 3, val, ok
	case val, ok := <-c4:
		return 4, val, ok
	case val, ok := <-c5:
		return 5, val, ok
	case val, ok := <-c6:
		return 6, val, ok
	}
}
