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
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestProcessorNode_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	wantRec := opencdc.Record{
		Position: []byte(uuid.NewString()),
		Metadata: map[string]string{"foo": "bar"},
	}
	newMetaKey := "bar2"

	processor := mock.NewProcessor(ctrl)
	processor.EXPECT().Open(gomock.Any())
	processor.EXPECT().
		Process(ctx, []opencdc.Record{wantRec}).
		DoAndReturn(func(_ context.Context, got []opencdc.Record) []sdk.ProcessedRecord {
			got[0].Metadata["foo"] = newMetaKey
			return []sdk.ProcessedRecord{sdk.SingleRecord(got[0])}
		})
	processor.EXPECT().Teardown(gomock.Any())

	n := ProcessorNode{
		Name:           "test",
		Processor:      processor,
		ProcessorTimer: noop.Timer{},
	}

	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		// publisher
		in <- &Message{
			Ctx:    ctx,
			Record: wantRec,
		}
		close(in)
	}()
	go func() {
		defer wg.Done()
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	got := <-out
	wantMsg := &Message{
		Ctx:    ctx,
		Record: wantRec,
	}
	wantMsg.Record.Metadata["foo"] = newMetaKey // position was transformed
	is.Equal(wantMsg, got)

	wg.Wait() // wait for node to stop running

	// after the node stops the out channel should be closed
	_, ok := <-out
	is.Equal(false, ok)
}

func TestProcessorNode_ErrorWithoutNackHandler(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	wantErr := cerrors.New("something bad happened")
	processor := mock.NewProcessor(ctrl)
	processor.EXPECT().Open(gomock.Any())
	processor.EXPECT().
		Process(ctx, gomock.Any()).
		Return([]sdk.ProcessedRecord{sdk.ErrorRecord{Error: wantErr}})
	processor.EXPECT().Teardown(gomock.Any())

	n := ProcessorNode{
		Name:           "test",
		Processor:      processor,
		ProcessorTimer: noop.Timer{},
	}

	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	msg := &Message{Ctx: ctx}
	go func() {
		// publisher
		in <- msg
		close(in)
	}()

	err := n.Run(ctx)
	is.True(cerrors.Is(err, wantErr)) // expected underlying error to be the processor error

	// after the node stops the out channel should be closed
	_, ok := <-out
	is.Equal(false, ok)
}

func TestProcessorNode_ErrorWithNackHandler(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	wantErr := cerrors.New("something bad happened")
	processor := mock.NewProcessor(gomock.NewController(t))
	processor.EXPECT().Open(gomock.Any())
	processor.EXPECT().
		Process(ctx, gomock.Any()).
		Return([]sdk.ProcessedRecord{sdk.ErrorRecord{Error: wantErr}})
	processor.EXPECT().Teardown(gomock.Any())

	nackHandler := func(msg *Message, nackMetadata NackMetadata) error {
		is.New(t).True(cerrors.Is(nackMetadata.Reason, wantErr)) // expected underlying error to be the processor error
		return nil                                               // the error should be regarded as handled
	}

	n := ProcessorNode{
		Name:           "test",
		Processor:      processor,
		ProcessorTimer: noop.Timer{},
	}

	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	msg := &Message{Ctx: ctx}
	msg.RegisterNackHandler(nackHandler)
	go func() {
		// publisher
		in <- msg
		close(in)
	}()

	err := n.Run(ctx)
	is.NoErr(err)
	is.Equal(MessageStatusNacked, msg.Status())

	// after the node stops the out channel should be closed
	_, ok := <-out
	is.Equal(false, ok)
}

func TestProcessorNode_BadProcessor_ReturnsMoreRecords(t *testing.T) {
	is := is.New(t)

	processor := mock.NewProcessor(gomock.NewController(t))
	processor.EXPECT().Open(gomock.Any())
	processor.EXPECT().
		Process(context.Background(), gomock.Any()).
		// processor returns 2 records instead of one
		Return([]sdk.ProcessedRecord{sdk.SingleRecord{}, sdk.SingleRecord{}})
	processor.EXPECT().Teardown(gomock.Any())

	nackHandler := func(msg *Message, nackMetadata NackMetadata) error {
		// expected underlying error to be the processor error
		is.New(t).Equal("processor was given 1 record(s), but returned 2", nackMetadata.Reason.Error())
		return nil // the error should be regarded as handled
	}

	testNodeWithError(is, processor, nackHandler)
}

func TestProcessorNode_BadProcessor_ChangesPosition(t *testing.T) {
	is := is.New(t)

	processor := mock.NewProcessor(gomock.NewController(t))
	processor.EXPECT().Open(gomock.Any())
	processor.EXPECT().
		Process(context.Background(), gomock.Any()).
		// processor returns 2 records instead of one
		Return([]sdk.ProcessedRecord{sdk.SingleRecord{Position: opencdc.Position("new position")}})
	processor.EXPECT().Teardown(gomock.Any())

	nackHandler := func(msg *Message, nackMetadata NackMetadata) error {
		// expected underlying error to be the processor error
		is.New(t).Equal(
			"processor changed position from 'test position' to 'new position' "+
				"(not allowed because source connector cannot correctly acknowledge messages)",
			nackMetadata.Reason.Error(),
		)
		return nil // the error should be regarded as handled
	}

	testNodeWithError(is, processor, nackHandler)
}

func testNodeWithError(is *is.I, processor *mock.Processor, nackHandler NackHandler) {
	ctx := context.Background()
	n := ProcessorNode{
		Name:           "test",
		Processor:      processor,
		ProcessorTimer: noop.Timer{},
	}

	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	msg := &Message{
		Ctx: ctx,
		Record: opencdc.Record{
			Position: opencdc.Position("test position"),
		},
	}
	msg.RegisterNackHandler(nackHandler)
	go func() {
		// publisher
		in <- msg
		close(in)
	}()

	err := n.Run(ctx)
	is.True(err != nil)
	is.Equal(MessageStatusNacked, msg.Status())

	// after the node stops the out channel should be closed
	_, ok := <-out
	is.Equal(false, ok)
}

func TestProcessorNode_Skip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	// create a dummy processor
	proc := mock.NewProcessor(ctrl)
	proc.EXPECT().Open(gomock.Any())
	proc.EXPECT().
		Process(ctx, gomock.Any()).
		Return([]sdk.ProcessedRecord{sdk.FilterRecord{}})
	proc.EXPECT().Teardown(gomock.Any())

	n := ProcessorNode{
		Name:           "test",
		Processor:      proc,
		ProcessorTimer: noop.Timer{},
	}

	// setup the test pipeline
	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	// send a message on the pipeline that will be skipped
	msg := &Message{Ctx: ctx, Record: opencdc.Record{}}

	// register a dummy AckHandler and NackHandler for tests.
	counter := 0
	msg.RegisterAckHandler(func(msg *Message) error {
		counter++
		return nil
	})
	msg.RegisterNackHandler(func(msg *Message, nm NackMetadata) error {
		// Our NackHandler shouldn't ever be hit if we're correctly skipping
		// so fail the test if we get here at all.
		t.Fail()
		return nil
	})

	go func() {
		// publisher
		in <- msg
		close(in)
	}()

	// run the pipeline and assert that there are no underlying pipeline errors
	err := n.Run(ctx)
	is.Equal(err, nil)
	is.Equal(counter, 1)

	// after the node stops the out channel should be closed
	_, ok := <-out
	is.Equal(false, ok)
}
