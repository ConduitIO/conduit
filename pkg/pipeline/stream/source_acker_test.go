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
	"math/rand"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

func TestSourceAckerNode_ForwardAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)

	node := &SourceAckerNode{
		Name:   "acker-node",
		Source: src,
	}
	in := make(chan *Message)
	out := node.Pub()
	node.Sub(in)

	go func() {
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	want := &Message{Ctx: ctx, Record: record.Record{Position: []byte("foo")}}
	// expect to receive an ack in the source after the message is acked
	src.EXPECT().Ack(want.Ctx, want.Record.Position).Return(nil)

	in <- want
	got := <-out
	is.Equal(got, want)

	// ack should be propagated to the source, the mock will do the assertion
	err := got.Ack()
	is.NoErr(err)

	// gracefully stop node and give the test 1 second to finish
	close(in)

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	select {
	case <-waitCtx.Done():
		is.Fail() // expected node to stop running
	case <-out:
		// all good
	}
}

func TestSourceAckerNode_AckOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)

	const count = 1000
	const maxSleep = 1 * time.Millisecond

	node := &SourceAckerNode{
		Name:   "acker-node",
		Source: src,
	}
	in := make(chan *Message)
	out := node.Pub()
	node.Sub(in)

	go func() {
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// first send messages through the node in the correct order
	messages := make([]*Message, count)
	for i := 0; i < count; i++ {
		m := &Message{
			Ctx: ctx,
			Record: record.Record{
				Position: []byte(strconv.Itoa(i)), // position is monotonically increasing
			},
		}
		in <- m
		<-out
		messages[i] = m
	}

	// expect to receive an acks in the same order as the order of the messages
	expectedPosition := 0
	expectedCalls := make([]*gomock.Call, count)
	for i := 0; i < count; i++ {
		expectedCalls[i] = src.EXPECT().
			Ack(ctx, messages[i].Record.Position).
			Do(func(context.Context, record.Position) { expectedPosition++ }).
			Return(nil)
	}
	gomock.InOrder(expectedCalls...) // enforce order

	// ack messages concurrently in random order
	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(msg *Message) {
			defer wg.Done()
			// sleep for a random amount of time and ack the message
			//nolint:gosec // math/rand is good enough for a test
			time.Sleep(time.Duration(rand.Int63n(int64(maxSleep/time.Nanosecond))) * time.Nanosecond)
			err := msg.Ack()
			is.NoErr(err)
		}(messages[i])
	}

	// gracefully stop node and give the test 1 second to finish
	close(in)

	wgDone := make(chan struct{})
	go func() {
		defer close(wgDone)
		wg.Wait()
	}()

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	select {
	case <-waitCtx.Done():
		is.Fail() // expected to receive all acks in time
	case <-wgDone:
		// all good
	}
}
