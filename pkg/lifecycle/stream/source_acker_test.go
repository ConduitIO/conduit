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

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestSourceAckerNode_ForwardAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)
	helper := sourceAckerNodeTestHelper{ctrl: ctrl}

	_, in, out := helper.newSourceAckerNode(ctx, t, src)

	want := &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte("foo")}}
	// expect to receive an ack in the source after the message is acked
	src.EXPECT().Ack(want.Ctx, []opencdc.Position{want.Record.Position}).Return(nil)

	in <- want
	got := <-out
	is.Equal(got, want)

	// ack should be propagated to the source, the mock will do the assertion
	err := got.Ack()
	is.NoErr(err)

	// gracefully stop node
	close(in)

	// make sure out channel is closed
	_, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err)
	is.True(!ok)
}

func TestSourceAckerNode_AckOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)
	helper := sourceAckerNodeTestHelper{ctrl: ctrl}

	_, in, out := helper.newSourceAckerNode(ctx, t, src)
	// send 1000 messages through the node
	messages := helper.sendMessages(ctx, 1000, in, out)
	// expect all messages to be acked
	expectedCalls := helper.expectAcks(ctx, messages, src)
	inOrder(expectedCalls) // enforce order of acks

	// ack messages concurrently in random order, expect no errors
	var wg sync.WaitGroup
	helper.ackMessagesConcurrently(
		&wg,
		messages,
		func(msg *Message, err error) {
			is.NoErr(err)
		},
	)

	// gracefully stop node and give the test 1 second to finish
	close(in)

	err := (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err) // expected to receive acks in time
}

func TestSourceAckerNode_FailedAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)
	helper := sourceAckerNodeTestHelper{ctrl: ctrl}

	_, in, out := helper.newSourceAckerNode(ctx, t, src)
	// send 1000 messages through the node
	messages := helper.sendMessages(ctx, 1000, in, out)
	// expect first 500 to be acked successfully
	expectedCalls := helper.expectAcks(ctx, messages[:500], src)
	inOrder(expectedCalls) // enforce order of acks
	// the 500th message should be acked unsuccessfully
	wantErr := cerrors.New("test error")
	src.EXPECT().
		Ack(ctx, []opencdc.Position{messages[500].Record.Position}).
		Return(wantErr).
		After(expectedCalls[len(expectedCalls)-1]) // should happen after last acked call

	// ack messages concurrently in random order, expect errors for second half
	var wg sync.WaitGroup
	helper.ackMessagesConcurrently(&wg, messages[:500],
		func(msg *Message, err error) {
			is.NoErr(err) // expected messages from the first half to be acked successfully
		},
	)
	helper.ackMessagesConcurrently(&wg, messages[500:501],
		func(msg *Message, err error) {
			is.True(cerrors.Is(err, wantErr)) // expected the middle message ack to fail with specific error
		},
	)
	helper.ackMessagesConcurrently(&wg, messages[501:],
		func(msg *Message, err error) {
			is.True(err != nil) // expected messages from the second half to be acked unsuccessfully
			is.True(!cerrors.Is(err, wantErr))
		},
	)

	// gracefully stop node and give the test 1 second to finish
	close(in)

	err := (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err) // expected to receive acks in time
}

func TestSourceAckerNode_FailedNack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)
	helper := sourceAckerNodeTestHelper{ctrl: ctrl}

	_, in, out := helper.newSourceAckerNode(ctx, t, src)
	// send 1000 messages through the node
	messages := helper.sendMessages(ctx, 1000, in, out)
	// expect first 500 to be acked successfully
	expectedCalls := helper.expectAcks(ctx, messages[:500], src)
	inOrder(expectedCalls) // enforce order of acks
	// the 500th message will be nacked unsuccessfully, no more acks should be received after that

	// ack messages concurrently in random order
	var wg sync.WaitGroup
	helper.ackMessagesConcurrently(&wg, messages[:500],
		func(msg *Message, err error) {
			is.NoErr(err) // expected messages from the first half to be acked successfully
		},
	)
	helper.ackMessagesConcurrently(&wg, messages[501:],
		func(msg *Message, err error) {
			is.True(err != nil) // expected messages from the second half to be acked unsuccessfully
		},
	)

	wantErr := cerrors.New("test error")
	err := messages[500].Nack(wantErr, "test-node")
	is.True(err != nil) // expected the 500th message nack to fail with specific error

	// gracefully stop node and give the test 1 second to finish
	close(in)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err) // expected to receive acks in time
}

// sourceAckerNodeTestHelper groups together helper functions for tests related
// to SourceAckerNode.
type sourceAckerNodeTestHelper struct {
	ctrl *gomock.Controller
}

func (h sourceAckerNodeTestHelper) newSourceAckerNode(
	ctx context.Context,
	t *testing.T,
	src Source,
) (*SourceAckerNode, chan<- *Message, <-chan *Message) {
	is := is.New(t)
	node := &SourceAckerNode{
		Name:           "acker-node",
		Source:         src,
		DLQHandlerNode: h.newDLQHandlerNode(ctx, t),
	}
	in := make(chan *Message)
	out := node.Pub()
	node.Sub(in)

	go func() {
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	return node, in, out
}

func (h sourceAckerNodeTestHelper) newDLQHandlerNode(
	ctx context.Context,
	t *testing.T,
) *DLQHandlerNode {
	is := is.New(t)
	handler := mock.NewDLQHandler(h.ctrl)
	handler.EXPECT().Open(gomock.Any()).Return(nil)
	handler.EXPECT().Close(gomock.Any())

	var wg csync.WaitGroup
	wg.Add(1)

	node := &DLQHandlerNode{
		Name:    "dlq-handler-node",
		Handler: handler,
		// window is configured so that no nacks are allowed
		WindowSize:          1,
		WindowNackThreshold: 0,
	}
	node.Add(1)

	go func() {
		defer wg.Done()
		err := node.Run(ctx)
		is.NoErr(err)
	}()
	t.Cleanup(func() {
		err := wg.WaitTimeout(ctx, time.Second) // wait for node to finish running
		is.NoErr(err)
	})

	return node
}

func (sourceAckerNodeTestHelper) sendMessages(
	ctx context.Context,
	count int,
	in chan<- *Message,
	out <-chan *Message,
) []*Message {
	messages := make([]*Message, count)
	for i := 0; i < count; i++ {
		m := &Message{
			Ctx: ctx,
			Record: opencdc.Record{
				Position: []byte(strconv.Itoa(i)), // position is monotonically increasing
			},
		}
		in <- m
		<-out
		messages[i] = m
	}
	return messages
}

func (sourceAckerNodeTestHelper) expectAcks(
	ctx context.Context,
	messages []*Message,
	src *mock.Source,
) []*gomock.Call {
	count := len(messages)

	// expect to receive acks successfully
	expectedCalls := make([]*gomock.Call, count)
	for i := 0; i < count; i++ {
		expectedCalls[i] = src.EXPECT().
			Ack(ctx, []opencdc.Position{messages[i].Record.Position}).
			Return(nil).Call
	}

	return expectedCalls
}

func (sourceAckerNodeTestHelper) ackMessagesConcurrently(
	wg *sync.WaitGroup,
	messages []*Message,
	assertAckErr func(*Message, error),
) {
	const maxSleep = time.Millisecond
	count := len(messages)

	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(msg *Message) {
			defer wg.Done()
			// sleep for a random amount of time and ack the message
			time.Sleep(time.Duration(rand.Int63n(int64(maxSleep/time.Nanosecond))) * time.Nanosecond)
			err := msg.Ack()
			assertAckErr(msg, err)
		}(messages[i])
	}
}

// inOrder is a utility method that passes []*gomock.Call to gomock.InOrder as []any.
func inOrder(calls []*gomock.Call) {
	callsRepacked := make([]any, len(calls))
	for k, v := range calls {
		callsRepacked[k] = v
	}
	gomock.InOrder(callsRepacked...)
}
