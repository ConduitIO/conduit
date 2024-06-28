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
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/pipeline/stream/mock"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestSourceNode_Run(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, wantRecords := newMockSource(ctrl, 10, nil)
	node := &SourceNode{
		Name:          "source-node",
		Source:        src,
		PipelineTimer: noop.Timer{},
	}
	out := node.Pub()

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	for _, wantRecord := range wantRecords {
		gotMsg, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Second)
		is.NoErr(err)
		is.True(ok)
		is.Equal(gotMsg.Record, wantRecord)

		err = gotMsg.Ack()
		is.NoErr(err)
	}

	// we stop the node now, but the mock will simulate as if it still needs to
	// produce the records, the source should drain them
	err := node.Stop(ctx, nil)
	is.NoErr(err)

	// stopping the node after a successful stop results in an error
	err = node.Stop(ctx, nil)
	is.True(err != nil)

	_, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to close outgoing channel
	is.True(!ok)  // expected node to close outgoing channel

	_, ok, err = cchan.ChanOut[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to stop running
	is.True(!ok)  // expected nodeDone to be closed
}

func TestSourceNode_Stop_ConcurrentFail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src := mock.NewSource(ctrl)

	wantErr := cerrors.New("test error")
	startRead := make(chan struct{})
	unblockRead := make(chan struct{})
	src.EXPECT().ID().Return("source-connector").AnyTimes()
	src.EXPECT().Open(gomock.Any()).Return(nil)
	src.EXPECT().Errors().Return(make(chan error))
	src.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) ([]opencdc.Record, error) {
		close(startRead)
		<-unblockRead
		return nil, connectorPlugin.ErrStreamNotOpen
	})
	startStop := make(chan struct{})
	unblockStop := make(chan struct{})
	src.EXPECT().Stop(gomock.Any()).DoAndReturn(func(ctx context.Context) (opencdc.Position, error) {
		select {
		case <-startStop:
			// startStop already closed, ignore
		default:
			close(startStop)
		}
		<-unblockStop
		return nil, wantErr
	}).Times(3)
	src.EXPECT().Teardown(gomock.Any()).Return(nil)

	node := &SourceNode{
		Name:          "source-node",
		Source:        src,
		PipelineTimer: noop.Timer{},
	}
	out := node.Pub()

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(ctx)
		is.True(cerrors.Is(err, connectorPlugin.ErrStreamNotOpen))
	}()

	_, ok, err := cchan.ChanOut[struct{}](startRead).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected read to start running
	is.True(!ok)  // expected read to start running

	var wg sync.WaitGroup
	wg.Add(2)
	stopErrs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			// we stop the node now, the mock will simulate a failure
			stopErrs <- node.Stop(ctx, nil)
		}()
	}

	// wait for the stop function to start
	_, ok, err = cchan.ChanOut[struct{}](startStop).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected stop to start running
	is.True(!ok)  // expected stop to start running

	// sleep for another millisecond to give the other stop call a chance to
	// start blocking as well
	time.Sleep(time.Millisecond)

	// unblock stop
	close(unblockStop)

	// make sure we got the same error from both goroutines
	for i := 0; i < 2; i++ {
		gotErr, ok, _ := cchan.ChanOut[error](stopErrs).RecvTimeout(ctx, time.Second)
		is.True(ok)
		is.True(cerrors.Is(gotErr, wantErr))
	}

	// we should be able to try stopping the node again and get the same error
	err = node.Stop(ctx, nil)
	is.True(cerrors.Is(err, wantErr))

	// simulate that read stops running
	close(unblockRead)

	_, ok, err = cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to close outgoing channel
	is.True(!ok)  // expected node to close outgoing channel

	_, ok, err = cchan.ChanOut[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to stop running
	is.True(!ok)  // expected nodeDone to be closed
}

func TestSourceNode_StopWhileNextNodeIsStuck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	nodeCtx, cancelNodeCtx := context.WithCancel(context.Background())
	defer cancelNodeCtx()
	ctrl := gomock.NewController(t)

	src, _ := newMockSource(ctrl, 1, nil)
	node := &SourceNode{
		Name:          "source-node",
		Source:        src,
		PipelineTimer: noop.Timer{},
	}
	out := node.Pub()

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := node.Run(nodeCtx)
		is.True(cerrors.Is(err, context.Canceled))
	}()

	// give the source a chance to read the record and start sending it to the
	// next node, we won't read the record from the channel though
	time.Sleep(time.Millisecond * 10)

	// a normal stop won't work because the node is stuck and can't receive the
	// stop signal message
	stopCtx, cancelStopCtx := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancelStopCtx()
	err := node.Stop(stopCtx, nil)
	is.True(cerrors.Is(err, context.DeadlineExceeded))

	// the node is stuck, cancel the context to stop node from running
	cancelNodeCtx()

	_, ok, err := cchan.ChanOut[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to stop running
	is.True(!ok)  // expected nodeDone to be closed

	_, ok, err = cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to close outgoing channel
	is.True(!ok)  // expected node to close outgoing channel
}

func TestSourceNode_ForceStop(t *testing.T) {
	ctrl := gomock.NewController(t)

	testCases := []struct {
		name       string
		mockSource func(chan struct{}) *mock.Source
		wantErr    error
	}{{
		name: "Source.Open blocks",
		mockSource: func(onStuck chan struct{}) *mock.Source {
			src := mock.NewSource(ctrl)
			src.EXPECT().ID().Return("source-connector").AnyTimes()
			src.EXPECT().Errors().Return(make(chan error))
			src.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
				close(onStuck)
				<-ctx.Done() // block until context is done
				return ctx.Err()
			})
			return src
		},
		wantErr: context.Canceled,
	}, {
		name: "Source.Read and Source.Stop block",
		mockSource: func(onStuck chan struct{}) *mock.Source {
			var connectorCtx context.Context
			src := mock.NewSource(ctrl)
			src.EXPECT().ID().Return("source-connector").AnyTimes()
			src.EXPECT().Errors().Return(make(chan error))
			src.EXPECT().Teardown(gomock.Any()).Return(nil)
			src.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
				// the connector opens the stream in open and keeps it open
				// until the context is open
				connectorCtx = ctx
				return nil
			})
			src.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) ([]opencdc.Record, error) {
				close(onStuck)
				<-connectorCtx.Done() // block until connector stream is closed
				return nil, io.EOF    // io.EOF is returned when the stream is closed
			})
			src.EXPECT().Stop(gomock.Any()).DoAndReturn(func(ctx context.Context) (opencdc.Position, error) {
				<-ctx.Done() // block until context is done
				return nil, ctx.Err()
			})
			return src
		},
		wantErr: io.EOF,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			// onStuck will be closed once the connector is stuck, needed to
			// make race detector happy
			onStuck := make(chan struct{})

			node := &SourceNode{
				Name:          "source-node",
				Source:        tc.mockSource(onStuck),
				PipelineTimer: noop.Timer{},
			}
			out := node.Pub()

			nodeDone := make(chan struct{})
			go func() {
				defer close(nodeDone)
				err := node.Run(ctx)
				is.True(cerrors.Is(err, tc.wantErr))
			}()

			// wait for node to get stuck
			_, ok, err := cchan.ChanOut[struct{}](onStuck).RecvTimeout(ctx, time.Millisecond*100)
			is.True(!ok)
			is.NoErr(err)

			// a normal stop won't work because the node is stuck and can't receive the
			// stop signal message
			stopCtx, cancelStopCtx := context.WithTimeout(ctx, time.Millisecond*100)
			defer cancelStopCtx()
			err = node.Stop(stopCtx, nil)
			is.True(cerrors.Is(err, context.DeadlineExceeded))

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

// newMockSource creates a connector source and record that will be produced by
// the source connector. After producing the requested number of records it
// returns wantErr or blocks until the context is closed.
func newMockSource(ctrl *gomock.Controller, recordCount int, wantErr error) (*mock.Source, []opencdc.Record) {
	position := 0
	records := make([]opencdc.Record, recordCount)
	for i := 0; i < recordCount; i++ {
		records[i] = opencdc.Record{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData(uuid.NewString()),
			Payload: opencdc.Change{
				Before: nil,
				After:  opencdc.RawData(uuid.NewString()),
			},
			Position: opencdc.Position(strconv.Itoa(i)),
		}
	}

	source := mock.NewSource(ctrl)

	teardown := make(chan struct{})
	source.EXPECT().ID().Return("source-connector").AnyTimes()
	source.EXPECT().Open(gomock.Any()).Return(nil)
	source.EXPECT().Errors().Return(make(chan error))
	source.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) ([]opencdc.Record, error) {
		if position == recordCount {
			if wantErr != nil {
				return nil, wantErr
			}
			<-teardown
			return nil, connectorPlugin.ErrStreamNotOpen
		}
		r := records[position]
		position++
		return []opencdc.Record{r}, nil
	}).
		MinTimes(recordCount).    // can be recordCount if the source receives stop before producing last record
		MaxTimes(recordCount + 1) // can be recordCount+1 if the source checks for another record before the stop signal is received
	source.EXPECT().Stop(gomock.Any()).DoAndReturn(func(ctx context.Context) (opencdc.Position, error) {
		if len(records) == 0 {
			return nil, nil
		}
		return records[len(records)-1].Position, nil
	})
	source.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(teardown)
		return nil
	})

	return source, records
}
