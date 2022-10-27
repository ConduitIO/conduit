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
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
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
	// A pipeline can't be stopped gracefully if the next node after a source
	// node blocks forever (or for a long time), because we can't inject the
	// stop message into the stream.
	t.Skip("bug isn't fixed yet")

	is := is.New(t)
	ctx := context.Background()
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
		err := node.Run(ctx)
		is.NoErr(err)
	}()

	// give the source a chance to read the record and start sending it to the
	// next node, we won't read the record from the channel though
	time.Sleep(time.Millisecond * 100)

	// stop the node while it's trying to send the record into the channel
	// TODO this call blocks forever, we need to fix the behavior
	err := node.Stop(ctx, nil)
	is.NoErr(err)

	_, ok, err := cchan.Chan[*Message](out).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to close outgoing channel
	is.True(!ok)  // expected node to close outgoing channel

	_, ok, err = cchan.Chan[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
	is.NoErr(err) // expected node to stop running
	is.True(!ok)  // expected nodeDone to be closed
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
