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

	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
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
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			pos := fmt.Sprintf("test-position-%d", currentPosition)
			currentPosition++
			return record.Position(pos), nil
		}).Times(count)

	var ackHandlerWg sync.WaitGroup
	ackHandlerWg.Add(count)
	for i := 0; i < count; i++ {
		pos := fmt.Sprintf("test-position-%d", i)
		msg := &Message{
			Record: record.Record{Position: record.Position(pos)},
		}
		msg.RegisterAckHandler(func(msg *Message) error {
			ackHandlerWg.Done()
			return nil
		})
		in <- msg
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
		Record: record.Record{Position: record.Position("test-position")},
	}
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			return msg.Record.Position, nil
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
		Record: record.Record{Position: record.Position("test-position")},
	}
	wantErr := cerrors.New("test error")
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			return msg.Record.Position, wantErr // destination returns nack
		})
	nackHandlerDone := make(chan struct{})
	msg.RegisterNackHandler(func(got *Message, reason error) error {
		defer close(nackHandlerDone)
		is.Equal(msg, got)
		is.Equal(wantErr, reason)
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
		Record: record.Record{Position: record.Position("test-position")},
	}
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			return record.Position("something-unexpected"), nil // destination returns unexpected position
		})

	// nack should be still called when node exits
	nackHandlerDone := make(chan struct{})
	msg.RegisterNackHandler(func(got *Message, reason error) error {
		defer close(nackHandlerDone)
		is.True(reason != nil)
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
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			return nil, wantErr // destination returns unexpected error
		})

	in <- &Message{
		Record: record.Record{Position: record.Position("test-position")},
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
		Record: record.Record{Position: record.Position("test-position")},
	}
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			return msg.Record.Position, nil
		})
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
