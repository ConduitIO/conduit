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

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

func TestSourceAckerNode_ForwardAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	src := mock.NewSource(ctrl)
	helper := sourceAckerNodeTestHelper{}

	_, in, out := helper.newSourceAckerNode(ctx, is, src)

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
	helper := sourceAckerNodeTestHelper{}

	_, in, out := helper.newSourceAckerNode(ctx, is, src)
	// send 1000 messages through the node
	messages := helper.sendMessages(ctx, 1000, in, out)
	// expect all messages to be acked
	expectedCalls := helper.expectAcks(ctx, messages, src)
	gomock.InOrder(expectedCalls...) // enforce order of acks

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
	helper := sourceAckerNodeTestHelper{}

	_, in, out := helper.newSourceAckerNode(ctx, is, src)
	// send 1000 messages through the node
	messages := helper.sendMessages(ctx, 1000, in, out)
	// expect first 500 to be acked successfully
	expectedCalls := helper.expectAcks(ctx, messages[:500], src)
	gomock.InOrder(expectedCalls...) // enforce order of acks
	// the 500th message should be acked unsuccessfully
	wantErr := cerrors.New("test error")
	src.EXPECT().
		Ack(ctx, messages[500].Record.Position).
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
			is.Equal(err, wantErr) // expected the middle message ack to fail with specific error
		},
	)
	helper.ackMessagesConcurrently(&wg, messages[501:],
		func(msg *Message, err error) {
			is.True(err != nil) // expected messages from the second half to be acked unsuccessfully
			is.True(err != wantErr)
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
	helper := sourceAckerNodeTestHelper{}

	_, in, out := helper.newSourceAckerNode(ctx, is, src)
	// send 1000 messages through the node
	messages := helper.sendMessages(ctx, 1000, in, out)
	// expect first 500 to be acked successfully
	expectedCalls := helper.expectAcks(ctx, messages[:500], src)
	gomock.InOrder(expectedCalls...) // enforce order of acks
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
	err := messages[500].Nack(wantErr)
	is.True(err != nil) // expected the 500th message nack to fail with specific error

	// gracefully stop node and give the test 1 second to finish
	close(in)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err) // expected to receive acks in time
}

// sourceAckerNodeTestHelper groups together helper functions for tests related
// to SourceAckerNode.
type sourceAckerNodeTestHelper struct{}

func (sourceAckerNodeTestHelper) newSourceAckerNode(
	ctx context.Context,
	is *is.I,
	src connector.Source,
) (*SourceAckerNode, chan<- *Message, <-chan *Message) {
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

	return node, in, out
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
			Record: record.Record{
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
			Ack(ctx, messages[i].Record.Position).
			Return(nil)
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
			//nolint:gosec // math/rand is good enough for a test
			time.Sleep(time.Duration(rand.Int63n(int64(maxSleep/time.Nanosecond))) * time.Nanosecond)
			err := msg.Ack()
			assertAckErr(msg, err)
		}(messages[i])
	}
}
