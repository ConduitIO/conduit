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
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/pipeline/stream/mock"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
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
		gotMsg, ok, err := cchan.Chan[*Message](out).RecvTimeout(ctx, time.Second)
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

	_, ok, err := cchan.Chan[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to close outgoing channel
	is.True(!ok)  // expected node to close outgoing channel

	_, ok, err = cchan.Chan[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
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

	_, ok, err := cchan.Chan[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to stop running
	is.True(!ok)  // expected nodeDone to be closed

	_, ok, err = cchan.Chan[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to close outgoing channel
	is.True(!ok)  // expected node to close outgoing channel
}

func TestSourceNode_ForceStop(t *testing.T) {
	ctrl := gomock.NewController(t)

	testCases := []struct {
		name       string
		mockSource *mock.Source
		wantErr    error
	}{{
		name: "Source.Open blocks",
		mockSource: func() *mock.Source {
			src := mock.NewSource(ctrl)
			src.EXPECT().ID().Return("source-connector").AnyTimes()
			src.EXPECT().Errors().Return(make(chan error)).Times(1)
			src.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
			src.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
				<-ctx.Done() // block until context is done
				return ctx.Err()
			}).Times(1)
			return src
		}(),
		wantErr: context.Canceled,
	}, {
		name: "Source.Read and Source.Stop block",
		mockSource: func() *mock.Source {
			var connectorCtx context.Context
			src := mock.NewSource(ctrl)
			src.EXPECT().ID().Return("source-connector").AnyTimes()
			src.EXPECT().Errors().Return(make(chan error)).Times(1)
			src.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
			src.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
				// the connector opens the stream in open and keeps it open
				// until the context is open
				connectorCtx = ctx
				return nil
			}).Times(1)
			src.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Record, error) {
				<-connectorCtx.Done()          // block until connector stream is closed
				return record.Record{}, io.EOF // io.EOF is returned when the stream is closed
			}).Times(1)
			src.EXPECT().Stop(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Position, error) {
				<-ctx.Done() // block until context is done
				return nil, ctx.Err()
			}).Times(1)
			return src
		}(),
		wantErr: io.EOF,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			node := &SourceNode{
				Name:          "source-node",
				Source:        tc.mockSource,
				PipelineTimer: noop.Timer{},
			}
			out := node.Pub()

			nodeDone := make(chan struct{})
			go func() {
				defer close(nodeDone)
				err := node.Run(ctx)
				is.True(cerrors.Is(err, tc.wantErr))
			}()

			// give the source a chance to read the record and start sending it to the
			// next node, we won't read the record from the channel though
			time.Sleep(time.Millisecond * 100)

			// a normal stop won't work because the node is stuck and can't receive the
			// stop signal message
			stopCtx, cancelStopCtx := context.WithTimeout(ctx, time.Millisecond*100)
			defer cancelStopCtx()
			err := node.Stop(stopCtx, nil)
			is.True(cerrors.Is(err, context.DeadlineExceeded))

			// try force stopping the node
			node.ForceStop(ctx)

			_, ok, err := cchan.Chan[struct{}](nodeDone).RecvTimeout(ctx, time.Second*300)
			is.NoErr(err) // expected node to stop running
			is.True(!ok)  // expected nodeDone to be closed

			_, ok, err = cchan.Chan[*Message](out).RecvTimeout(ctx, time.Second)
			is.NoErr(err) // expected node to close outgoing channel
			is.True(!ok)  // expected node to close outgoing channel
		})
	}
}

// newMockSource creates a connector source and record that will be produced by
// the source connector. After producing the requested number of records it
// returns wantErr or blocks until the context is closed.
func newMockSource(ctrl *gomock.Controller, recordCount int, wantErr error) (*mock.Source, []record.Record) {
	position := 0
	records := make([]record.Record, recordCount)
	for i := 0; i < recordCount; i++ {
		records[i] = record.Record{
			Operation: record.OperationCreate,
			Key:       record.RawData{Raw: []byte(uuid.NewString())},
			Payload: record.Change{
				Before: nil,
				After:  record.RawData{Raw: []byte(uuid.NewString())},
			},
			Position: record.Position(strconv.Itoa(i)),
		}
	}

	source := mock.NewSource(ctrl)

	teardown := make(chan struct{})
	source.EXPECT().ID().Return("source-connector").AnyTimes()
	source.EXPECT().Open(gomock.Any()).Return(nil).Times(1)
	source.EXPECT().Errors().Return(make(chan error)).Times(1)
	source.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Record, error) {
		if position == recordCount {
			if wantErr != nil {
				return record.Record{}, wantErr
			}
			<-teardown
			return record.Record{}, plugin.ErrStreamNotOpen
		}
		r := records[position]
		position++
		return r, nil
	}).
		MinTimes(recordCount).    // can be recordCount if the source receives stop before producing last record
		MaxTimes(recordCount + 1) // can be recordCount+1 if the source checks for another record before the stop signal is received
	source.EXPECT().Stop(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Position, error) {
		if len(records) == 0 {
			return nil, nil
		}
		return records[len(records)-1].Position, nil
	}).Times(1)
	source.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(teardown)
		return nil
	}).Times(1)

	return source, records
}
