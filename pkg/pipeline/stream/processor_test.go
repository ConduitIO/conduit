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
	"github.com/matryer/is"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

func TestProcessorNode_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	wantRec := record.Record{
		Position: []byte(uuid.NewString()),
		Metadata: map[string]string{"foo": "bar"},
	}
	newPosition := []byte(uuid.NewString())

	processor := mock.NewProcessor(ctrl)
	processor.
		EXPECT().
		Process(ctx, wantRec).
		DoAndReturn(func(_ context.Context, got record.Record) (record.Record, error) {
			got.Position = newPosition
			return got, nil
		})

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
	wantMsg.Record.Position = newPosition // position was transformed
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
	processor.EXPECT().Process(ctx, gomock.Any()).Return(record.Record{}, wantErr)

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
	is.True(cerrors.Is(err, wantErr)) //expected underlying error to be the processor error

	// after the node stops the out channel should be closed
	_, ok := <-out
	is.Equal(false, ok)
}

func TestProcessorNode_ErrorWithNackHandler(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	wantErr := cerrors.New("something bad happened")
	processor := mock.NewProcessor(ctrl)
	processor.EXPECT().Process(ctx, gomock.Any()).Return(record.Record{}, wantErr)

	n := ProcessorNode{
		Name:           "test",
		Processor:      processor,
		ProcessorTimer: noop.Timer{},
	}

	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	msg := &Message{Ctx: ctx}
	msg.RegisterNackHandler(func(msg *Message, nackMetadata NackMetadata) error {
		is.True(cerrors.Is(nackMetadata.Reason, wantErr)) // expected underlying error to be the processor error
		return nil                                        // the error should be regarded as handled
	})
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

func TestProcessorNode_Skip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	// create a dummy processor
	proc := mock.NewProcessor(ctrl)
	proc.EXPECT().Process(ctx, gomock.Any()).Return(record.Record{}, processor.ErrSkipRecord)

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
	msg := &Message{Ctx: ctx, Record: record.Record{}}

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
