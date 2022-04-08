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
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

func TestAckerNode_Run_StopAfterWait(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &AckerNode{
		Name:        "acker-node",
		Destination: dest,
	}

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// give Go a chance to run the node
	time.Sleep(time.Millisecond)

	// note that there should be no calls to the destination at all if we didn't
	// receive any ExpectedAck call

	// give the test 1 second to finish
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	node.Wait(waitCtx)

	select {
	case <-waitCtx.Done():
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}
}

func TestAckerNode_Run_StopAfterExpectAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest := mock.NewDestination(ctrl)

	node := &AckerNode{
		Name:        "acker-node",
		Destination: dest,
	}

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// give Go a chance to run the node
	time.Sleep(time.Millisecond)

	// up to this point there should have been no calls to the destination
	// only after the call to ExpectAck should the node try to fetch any acks
	msg := &Message{
		Record: record.Record{Position: record.Position("test-position")},
	}
	c1 := dest.EXPECT().Ack(gomock.Any()).Return(msg.Record.Position, nil)         // first return position
	dest.EXPECT().Ack(gomock.Any()).Return(nil, plugin.ErrStreamNotOpen).After(c1) // second return closed stream

	err := node.ExpectAck(msg)
	is.NoErr(err)

	// give the test 1 second to finish
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	select {
	case <-waitCtx.Done():
		is.Fail() // expected node to stop running
	case <-nodeDone:
		// all good
	}
}
