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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline/stream/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDestinationAckerNode_Cache(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &DestinationAckerNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	const count = 1000
	currentPosition := 0

	// create wait group that will be done once we send all messages to the node
	var msgProducerWg sync.WaitGroup
	msgProducerWg.Add(1)

	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			// wait for all messages to be produced, this means the node should
			// be caching them
			msgProducerWg.Wait()
			pos := fmt.Sprintf("test-position-%d", currentPosition)
			currentPosition++
			return []connector.DestinationAck{{Position: opencdc.Position(pos)}}, nil
		}).Times(count)

	var ackHandlerWg sync.WaitGroup
	ackHandlerWg.Add(count)
	for i := 0; i < count; i++ {
		pos := fmt.Sprintf("test-position-%d", i)
		msg := &Message{
			Record: opencdc.Record{Position: opencdc.Position(pos)},
		}
		msg.RegisterAckHandler(func(msg *Message) error {
			ackHandlerWg.Done()
			return nil
		})
		in <- msg
	}
	msgProducerWg.Done()

	// note that there should be no calls to the destination at all if the node
	// didn't receive any messages
	close(in)

	select {
	case <-time.After(time.Second):
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}

	ackHandlerWg.Wait() // all ack handler should be called by now
}

func TestDestinationAckerNode_ForwardAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &DestinationAckerNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// up to this point there should have been no calls to the destination
	// only after a received message should the node try to fetch the ack
	msg := &Message{
		Record: opencdc.Record{Position: opencdc.Position("test-position")},
	}
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			return []connector.DestinationAck{{Position: msg.Record.Position}}, nil
		})
	ackHandlerDone := make(chan struct{})
	msg.RegisterAckHandler(func(got *Message) error {
		defer close(ackHandlerDone)
		is.Equal(msg, got)
		return nil
	})
	in <- msg // send message to incoming channel

	select {
	case <-time.After(time.Second):
		is.Fail() // expected ack handler to be called
	case <-ackHandlerDone:
		// all good
	}

	// note that there should be no calls to the destination at all if the node
	// didn't receive any messages
	close(in)

	select {
	case <-time.After(time.Second):
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}
}

func TestDestinationAckerNode_ForwardNack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &DestinationAckerNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// up to this point there should have been no calls to the destination
	// only after a received message should the node try to fetch the ack
	msg := &Message{
		Record: opencdc.Record{Position: opencdc.Position("test-position")},
	}
	wantErr := cerrors.New("test error")
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			// destination returns nack
			return []connector.DestinationAck{{Position: msg.Record.Position, Error: wantErr}}, nil
		})
	nackHandlerDone := make(chan struct{})
	msg.RegisterNackHandler(func(got *Message, nackMetadata NackMetadata) error {
		defer close(nackHandlerDone)
		is.Equal(msg, got)
		is.Equal(NackMetadata{
			Reason: wantErr,
			NodeID: node.ID(),
		}, nackMetadata)
		return nil
	})
	in <- msg // send message to incoming channel

	select {
	case <-time.After(time.Second):
		is.Fail() // expected nack handler to be called
	case <-nackHandlerDone:
		// all good
	}

	// note that there should be no calls to the destination at all if the node
	// didn't receive any messages
	close(in)

	select {
	case <-time.After(time.Second):
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}
}

func TestDestinationAckerNode_UnexpectedPosition(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &DestinationAckerNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.True(err != nil) // expected node to fail
	}()

	msg := &Message{
		Record: opencdc.Record{Position: opencdc.Position("test-position")},
	}
	ackCall := dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			// destination returns unexpected position
			return []connector.DestinationAck{{Position: opencdc.Position("something-unexpected")}}, nil
		})
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			return nil, cerrors.New("stream closed") // second Ack call is to drain outstanding acks
		}).After(ackCall.Call).AnyTimes()

	// nack should be still called when node exits
	nackHandlerDone := make(chan struct{})
	msg.RegisterNackHandler(func(got *Message, nackMetadata NackMetadata) error {
		defer close(nackHandlerDone)
		is.True(nackMetadata.Reason != nil)
		return nil
	})
	in <- msg // send message to incoming channel

	select {
	case <-time.After(time.Second):
		is.Fail() // expected nack handler to be called
	case <-nackHandlerDone:
		// all good
	}

	// note that we don't close the in channel this time and still expect the
	// node to stop running

	select {
	case <-time.After(time.Second):
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}
}

func TestDestinationAckerNode_DestinationAckError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &DestinationAckerNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)

	wantErr := cerrors.New("test error")
	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.True(cerrors.Is(err, wantErr)) // expected node to fail with specific error
	}()

	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			return nil, wantErr // destination returns unexpected error
		}).
		// 1st call is from the message that stops the node, 2nd call is from
		// the cleanup goroutine that is spawned to drain the acks, but that can
		// happen even after the test is done so we use MinTimes(1) instead of
		// Times(2)
		MinTimes(1)

	in <- &Message{
		Record: opencdc.Record{Position: opencdc.Position("test-position")},
	}

	// note that we don't close the in channel this time and still expect the
	// node to stop running

	select {
	case <-time.After(time.Second):
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}

	// expect that messages can't be sent to in
	select {
	case <-time.After(time.Millisecond * 100):
		// all good
	case in <- &Message{}:
		is.Fail() // expected node to stop receiving new messages
	}
}

func TestDestinationAckerNode_MessageAckError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &DestinationAckerNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)

	wantErr := cerrors.New("test error")
	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.True(cerrors.Is(err, wantErr)) // expected node to fail with specific error
	}()

	msg := &Message{
		Record: opencdc.Record{Position: opencdc.Position("test-position")},
	}
	ackCall := dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			return []connector.DestinationAck{{Position: msg.Record.Position}}, nil
		})
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
			return nil, cerrors.New("stream closed") // second Ack call is to drain outstanding acks
		}).After(ackCall.Call).AnyTimes()

	ackHandlerDone := make(chan struct{})
	msg.RegisterAckHandler(func(*Message) error {
		defer close(ackHandlerDone)
		return wantErr // ack handler fails
	})

	in <- msg

	select {
	case <-time.After(time.Second):
		is.Fail() // expected ack handler to be called
	case <-ackHandlerDone:
		// all good
	}

	// note that we don't close the in channel this time and still expect the
	// node to stop running

	select {
	case <-time.After(time.Second):
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}
}
