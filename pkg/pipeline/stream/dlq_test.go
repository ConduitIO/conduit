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

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/pipeline/stream/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDLQHandlerNode_AddDone(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).Return(nil)

	n := &DLQHandlerNode{
		Name:    "dlq-test",
		Handler: dlqHandler,
	}
	n.Add(1) // add 1 dependent component

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	// add another dependent component (simulates a message)
	n.Add(1)

	err := (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.Equal(err, context.DeadlineExceeded) // expected the node to keep running

	// remove a dependent component (simulates a message being acked/nacked)
	n.Done()

	err = (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.Equal(err, context.DeadlineExceeded) // expected the node to keep running

	// remove a dependent component (simulates the source acker node to stop)
	// after we remove the last dependent component we expect close to be called
	dlqHandler.EXPECT().Close(gomock.Any()).Return(nil)
	n.Done()

	err = (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.NoErr(err) // expected the node to stop
}

func TestDLQHandlerNode_OpenError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	wantErr := cerrors.New("test error")

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).Return(wantErr)

	n := &DLQHandlerNode{
		Name:    "dlq-test",
		Handler: dlqHandler,
	}
	n.Add(1) // add 1 dependent component, should not matter

	err := n.Run(ctx)
	is.True(cerrors.Is(err, wantErr))
}

func TestDLQHandlerNode_ForceStop(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	openCalled := make(chan struct{})

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(openCalled)
		<-ctx.Done()
		return ctx.Err()
	})

	n := &DLQHandlerNode{
		Name:    "dlq-test",
		Handler: dlqHandler,
	}
	n.Add(1) // add 1 dependent component, should not matter

	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		err := n.Run(ctx)
		is.True(cerrors.Is(err, context.Canceled))
	}()

	// wait for open to be called and openCalled to be closed
	_, ok, err := cchan.ChanOut[struct{}](openCalled).RecvTimeout(ctx, time.Second)
	is.True(!ok)
	is.NoErr(err)

	n.ForceStop(ctx)

	// wait for node to stop running
	_, ok, err = cchan.ChanOut[struct{}](nodeDone).RecvTimeout(ctx, time.Second)
	is.True(!ok)
	is.NoErr(err)
}

func TestDLQHandlerNode_Ack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).Return(nil)
	dlqHandler.EXPECT().Close(gomock.Any()).Return(nil)

	n := &DLQHandlerNode{
		Name:    "dlq-test",
		Handler: dlqHandler,
	}
	n.Add(1) // add 1 dependent component, should not matter

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	n.Ack(&Message{Ctx: ctx})

	n.Done()
	err := (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.NoErr(err) // expected the node to stop
}

func TestDLQHandlerNode_Nack_ThresholdExceeded(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).Return(nil)
	dlqHandler.EXPECT().Close(gomock.Any()).Return(nil)

	n := &DLQHandlerNode{
		Name:                "dlq-test",
		Handler:             dlqHandler,
		WindowSize:          1,
		WindowNackThreshold: 0, // nack threshold is 0, essentially the DLQ is disabled
	}
	n.Add(1) // add 1 dependent component, should not matter

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	wantErr := cerrors.New("reason")
	err := n.Nack(&Message{Ctx: ctx}, NackMetadata{Reason: wantErr})
	is.True(cerrors.Is(err, wantErr))

	// node should still keep running though, there might be other acks/nacks coming in
	err = (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.Equal(err, context.DeadlineExceeded) // expected the node to keep running

	// we can still ack without a problem
	n.Ack(&Message{Ctx: ctx})

	// only after dependents are done the node should stop
	n.Done()
	err = (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.NoErr(err) // expected the node to stop
}

func TestDLQHandlerNode_Nack_ForwardToDLQ_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).Return(nil)
	dlqHandler.EXPECT().Close(gomock.Any()).Return(nil)

	n := &DLQHandlerNode{
		Name:    "dlq-test",
		Handler: dlqHandler,
		// allow 100 of the last 101 messages to be nacks
		WindowSize:          101,
		WindowNackThreshold: 100,
		Timer:               noop.Timer{},
		Histogram:           metrics.NewRecordBytesHistogram(noop.Histogram{}),
	}
	n.Add(1) // add 1 dependent component, should not matter

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	for i := 0; i < n.WindowNackThreshold; i++ {
		msg := &Message{
			Ctx:    ctx,
			Record: opencdc.Record{Position: []byte(uuid.NewString())},
		}
		wantErr := cerrors.New("test error")
		wantNackMetadata := NackMetadata{
			Reason: wantErr,
			NodeID: "test-node",
		}

		wantRec, err := n.dlqRecord(msg, wantNackMetadata)
		is.NoErr(err)

		dlqHandler.EXPECT().
			Write(msg.Ctx, gomock.Any()).
			DoAndReturn(func(ctx context.Context, got opencdc.Record) error {
				// ignore created at
				at, _ := got.Metadata.GetCreatedAt()
				wantRec.Metadata.SetCreatedAt(at)
				is.Equal(wantRec, got)
				return nil
			})

		err = n.Nack(msg, wantNackMetadata)
		is.NoErr(err)
	}

	// only after dependents are done the node should stop
	n.Done()
	err := (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.NoErr(err) // expected the node to stop
}

func TestDLQHandlerNode_Nack_ForwardToDLQ_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dlqHandler := mock.NewDLQHandler(ctrl)
	dlqHandler.EXPECT().Open(gomock.Any()).Return(nil)
	dlqHandler.EXPECT().Close(gomock.Any()).Return(nil)

	n := &DLQHandlerNode{
		Name:    "dlq-test",
		Handler: dlqHandler,
		// allow 100 of the last 101 messages to be nacks
		WindowSize:          101,
		WindowNackThreshold: 100,
	}
	n.Add(1) // add 1 dependent component, should not matter

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	wantErr := cerrors.New("test error")
	dlqHandler.EXPECT().
		Write(gomock.Any(), gomock.Any()).
		Return(wantErr)

	err := n.Nack(&Message{Ctx: ctx}, NackMetadata{Reason: cerrors.New("reason")})
	is.True(cerrors.Is(err, wantErr))

	// all further calls to Nack should fail, we can't guarantee order in the
	// DLQ anymore
	err = n.Nack(&Message{Ctx: ctx}, NackMetadata{Reason: cerrors.New("reason")})
	is.True(err != nil)

	// only after dependents are done the node should stop
	n.Done()
	err = (*csync.WaitGroup)(&wg).WaitTimeout(context.Background(), 10*time.Millisecond)
	is.NoErr(err) // expected the node to stop
}

func TestDLQWindow_WindowDisabled(t *testing.T) {
	is := is.New(t)
	w := newDLQWindow(0, 0)

	w.Ack()
	ok := w.Nack()
	is.True(ok)
}

func TestDLQWindow_NackThresholdExceeded(t *testing.T) {
	testCases := []struct {
		windowSize    int
		nackThreshold int
	}{
		{1, 0},
		{2, 0},
		{2, 1},
		{100, 0},
		{100, 99},
	}

	for _, tc := range testCases {
		t.Run(
			fmt.Sprintf("w%d-t%d", tc.windowSize, tc.nackThreshold),
			func(t *testing.T) {
				is := is.New(t)
				w := newDLQWindow(tc.windowSize, tc.nackThreshold)

				// fill up window with nacks up to the threshold
				for i := 0; i < tc.nackThreshold; i++ {
					ok := w.Nack()
					is.True(ok)
				}

				// fill up window again with acks
				for i := 0; i < tc.windowSize; i++ {
					w.Ack()
				}

				// since window is full of acks we should be able to fill up
				// the window with nacks again
				for i := 0; i < tc.nackThreshold; i++ {
					ok := w.Nack()
					is.True(ok)
				}

				// the next nack should push us over the threshold
				ok := w.Nack()
				is.True(!ok)

				// adding acks after that should make no difference, all nacks
				// need to fail after the threshold is reached
				for i := 0; i < tc.windowSize; i++ {
					w.Ack()
				}
				ok = w.Nack()
				is.True(!ok)
			},
		)
	}
}
