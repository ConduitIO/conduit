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
	"io"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDestinationNode_ForceStop(t *testing.T) {
	ctrl := gomock.NewController(t)

	testCases := []struct {
		name            string
		mockDestination func(chan struct{}) *mock.Destination
		wantMsg         bool
		wantErr         error
	}{{
		name: "Destination.Open blocks",
		mockDestination: func(onStuck chan struct{}) *mock.Destination {
			dest := mock.NewDestination(ctrl)
			dest.EXPECT().ID().Return("destination-connector").AnyTimes()
			dest.EXPECT().Errors().Return(make(chan error))
			dest.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
				close(onStuck)
				<-ctx.Done() // block until context is done
				return ctx.Err()
			})
			return dest
		},
		wantMsg: false,
		wantErr: context.Canceled,
	}, {
		name: "Destination.Write blocks",
		mockDestination: func(onStuck chan struct{}) *mock.Destination {
			var connectorCtx context.Context
			dest := mock.NewDestination(ctrl)
			dest.EXPECT().ID().Return("destination-connector").AnyTimes()
			dest.EXPECT().Errors().Return(make(chan error))
			dest.EXPECT().Teardown(gomock.Any()).Return(nil)
			dest.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
				// the connector opens the stream in open and keeps it open
				// until the context is open
				connectorCtx = ctx
				return nil
			})
			dest.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, r []opencdc.Record) error {
				close(onStuck)
				<-connectorCtx.Done() // block until connector stream is closed
				return io.EOF         // io.EOF is returned when the stream is closed
			})
			dest.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(nil)
			return dest
		},
		wantMsg: true,
		wantErr: io.EOF,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			// onStuck will be closed once the connector is stuck, needed to
			// make race detector happy
			onStuck := make(chan struct{})

			node := &DestinationNode{
				Name:           "destination-node",
				Destination:    tc.mockDestination(onStuck),
				ConnectorTimer: noop.Timer{},
			}
			msgChan := make(chan *Message)
			node.Sub(msgChan)
			out := node.Pub()

			nodeDone := make(chan struct{})
			go func() {
				defer close(nodeDone)
				err := node.Run(ctx)
				is.True(cerrors.Is(err, tc.wantErr))
			}()

			err := cchan.ChanIn[*Message](msgChan).SendTimeout(ctx, &Message{}, time.Millisecond*100)
			if tc.wantMsg {
				is.NoErr(err)
			} else {
				is.True(cerrors.Is(err, context.DeadlineExceeded))
			}

			// wait for node to get stuck
			_, ok, err := cchan.ChanOut[struct{}](onStuck).RecvTimeout(ctx, time.Millisecond*100)
			is.True(!ok)
			is.NoErr(err)

			// a normal stop won't work because the node is stuck and can't receive the
			// stop signal message
			close(msgChan)
			_, _, err = cchan.ChanOut[struct{}](nodeDone).RecvTimeout(ctx, time.Millisecond*200)
			is.True(cerrors.Is(err, context.DeadlineExceeded)) // expected node to keep running

			// try force stopping the node
			node.ForceStop(ctx)

			_, ok, err = cchan.ChanOut[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
			is.NoErr(err) // expected node to stop running
			is.True(!ok)  // expected nodeDone to be closed

			_, ok, err = cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Second)
			is.NoErr(err) // expected node to close outgoing channel
			is.True(!ok)  // expected node to close outgoing channel
		})
	}
}

func TestDestinationNode_HandleFilteredMessage(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)
	// A filtered message is passing through this node without being sent
	// to the destination, hence no Destination.Write() call here.
	dest.EXPECT().Errors().Return(make(chan error))
	dest.EXPECT().Open(gomock.Any()).Return(nil)
	dest.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(nil)
	dest.EXPECT().Teardown(gomock.Any()).Return(nil)

	node := &DestinationNode{
		Name:        "destination-acker-node",
		Destination: dest,
	}

	in := make(chan *Message)
	node.Sub(in)
	out := node.Pub()

	var nodeStopped sync.WaitGroup
	nodeStopped.Add(1)

	go func() {
		defer nodeStopped.Done()
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// up to this point there should have been no calls to the destination
	// only after a received message should the node try to fetch the ack
	msg := &Message{
		filtered: true,
		Record:   opencdc.Record{Position: opencdc.Position("test-position")},
	}
	go func() {
		// send message to incoming channel
		err := cchan.ChanIn[*Message](in).SendTimeout(ctx, msg, 100*time.Millisecond)
		is.NoErr(err) // expected message to be sent to the destination node's Sub channel

		// note that there should be no calls to the destination at all if the node
		// didn't receive any messages
		close(in)
	}()

	gotMsg, gotMsgBool, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)       // expected node to close outgoing channel
	is.True(gotMsgBool) // expected node to close outgoing channel
	is.Equal(msg, gotMsg)

	err = (*csync.WaitGroup)(&nodeStopped).WaitTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err) // timed out waiting for node to be done running
}
