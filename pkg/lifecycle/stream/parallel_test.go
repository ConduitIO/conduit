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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

type parallelTestNode struct {
	Name string
	F    func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error

	pub chan *Message
	sub <-chan *Message
}

func (n *parallelTestNode) ID() string { return n.Name }
func (n *parallelTestNode) Pub() <-chan *Message {
	n.pub = make(chan *Message)
	return n.pub
}

func (n *parallelTestNode) Sub(in <-chan *Message) {
	n.sub = in
}

func (n *parallelTestNode) Run(ctx context.Context) error {
	return n.F(ctx, n.sub, n.pub)
}

func TestParallelNode_Worker(t *testing.T) {
	logger := log.Nop()
	ctx := context.Background()

	testError := cerrors.New("test error")

	testCases := []struct {
		name            string
		nodeFunc        func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error
		wantStatus      MessageStatus
		wantStatusError error
	}{{
		name: "open",
		nodeFunc: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
			defer close(pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
				if !ok {
					return err
				}
				pub <- msg
			}
		},
		wantStatus: MessageStatusOpen,
	}, {
		name: "ack",
		nodeFunc: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
			defer close(pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
				if !ok {
					return err
				}
				err = msg.Ack()
				if err != nil {
					return err
				}
			}
		},
		wantStatus: MessageStatusAcked,
	}, {
		name: "nack",
		nodeFunc: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
			defer close(pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
				if !ok {
					return err
				}
				msg.RegisterNackHandler(func(*Message, NackMetadata) error {
					// register nack handler so nack can succeed
					return nil
				})
				err = msg.Nack(testError, "test-node")
				if err != nil {
					return err
				}
			}
		},
		wantStatus:      MessageStatusNacked,
		wantStatusError: nil,
	}, {
		name: "failed ack",
		nodeFunc: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
			defer close(pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
				if !ok {
					return err
				}
				msg.RegisterAckHandler(func(*Message) error {
					// register failing ack handler so ack fails
					return testError
				})
				err = msg.Ack()
				if err != nil {
					return err
				}
			}
		},
		wantStatus:      MessageStatusAcked,
		wantStatusError: testError,
	}, {
		name: "failed nack",
		nodeFunc: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
			defer close(pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
				if !ok {
					return err
				}
				err = msg.Nack(testError, "test-node")
				if err != nil {
					return err
				}
			}
		},
		wantStatus:      MessageStatusNacked,
		wantStatusError: testError,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			n := &parallelTestNode{
				Name: "test-node",
				F:    tc.nodeFunc,
			}

			jobs := make(chan parallelNodeJob)
			worker := newParallelNodeWorker(n, jobs, logger)

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				worker.Run(ctx)
			}()

			job := parallelNodeJob{
				Message: &Message{},
				done:    make(chan struct{}),
			}
			jobs <- job

			_, ok, err := cchan.ChanOut[struct{}](job.done).RecvTimeout(ctx, time.Millisecond*100)
			is.NoErr(err)
			is.True(ok)

			is.Equal(job.Message.Status(), tc.wantStatus)
			is.True(cerrors.Is(job.Message.StatusError(), tc.wantStatusError))

			close(jobs)

			err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
			is.NoErr(err)
		})
	}
}

func TestParallelNode_Coordinator(t *testing.T) {
	logger := log.Nop()
	ctx := context.Background()

	testError := cerrors.New("test error")

	testCases := []struct {
		name           string
		processMessage func(*Message)
		wantMessage    bool
		wantError      error
	}{{
		name:           "open",
		processMessage: func(*Message) { /* do nothing */ },
		wantMessage:    true,
		wantError:      nil,
	}, {
		name: "ack",
		processMessage: func(msg *Message) {
			_ = msg.Ack()
		},
		wantMessage: false,
		wantError:   nil,
	}, {
		name: "nack",
		processMessage: func(msg *Message) {
			msg.RegisterNackHandler(func(*Message, NackMetadata) error {
				// register nack handler so nack can succeed
				return nil
			})
			_ = msg.Nack(testError, "test-node")
		},
		wantMessage: false,
		wantError:   nil,
	}, {
		name: "failed ack",
		processMessage: func(msg *Message) {
			msg.RegisterAckHandler(func(*Message) error {
				// register failing ack handler so ack fails
				return testError
			})
			_ = msg.Ack()
		},
		wantMessage: false,
		wantError:   testError,
	}, {
		name: "failed nack",
		processMessage: func(msg *Message) {
			_ = msg.Nack(testError, "test-node")
		},
		wantMessage: false,
		wantError:   testError,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			jobs := make(chan parallelNodeJob)
			errs := make(chan error)
			out := make(chan *Message)
			send := func(ctx context.Context, _ log.CtxLogger, msg *Message) error {
				return cchan.ChanIn[*Message](out).Send(ctx, msg)
			}
			donePool := &sync.Pool{
				New: func() any { return make(chan struct{}) },
			}
			coordinator := newParallelNodeCoordinator("test-node", jobs, errs, logger, send, donePool)

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				coordinator.Run(ctx)
			}()

			job := parallelNodeJob{
				Message: &Message{},
				done:    donePool.Get().(chan struct{}),
			}
			jobs <- job

			tc.processMessage(job.Message)

			job.Done()

			_, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*10)
			if tc.wantMessage {
				is.NoErr(err)
				is.True(ok)
			} else {
				is.True(cerrors.Is(err, context.DeadlineExceeded))
			}

			gotErr, ok, err := cchan.ChanOut[error](errs).RecvTimeout(ctx, time.Millisecond*10)
			if tc.wantError != nil {
				is.NoErr(err)
				is.True(ok)
				is.True(cerrors.Is(gotErr, tc.wantError))
			} else {
				is.True(cerrors.Is(err, context.DeadlineExceeded))
			}

			// regardless of what happened above we should be able to send another job in
			job = parallelNodeJob{
				Message: &Message{},
				done:    donePool.Get().(chan struct{}),
			}
			jobs <- job
			job.Done()

			_, ok, err = cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*10)
			// if we did not expect an error in the previous message then this one should go through
			if tc.wantError == nil {
				is.NoErr(err)
				is.True(ok)
			} else {
				is.True(cerrors.Is(err, context.DeadlineExceeded))
			}

			gotErr, ok, err = cchan.ChanOut[error](errs).RecvTimeout(ctx, time.Millisecond*10)
			// if we expected an error above we expect one for all following messages
			if tc.wantError != nil {
				is.NoErr(err)
				is.True(ok)
				is.True(gotErr != nil)
			} else {
				is.True(cerrors.Is(err, context.DeadlineExceeded))
			}

			// closing jobs should stop the coordinator
			close(jobs)

			err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
			is.NoErr(err)
		})
	}
}

func TestParallelNode_Success(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	controlChan := cchan.Chan[struct{}](make(chan struct{}))

	const workerCount = 10

	newPubSubNode := func(i int) PubSubNode {
		return &parallelTestNode{
			Name: fmt.Sprintf("test-node-%d", i),
			F: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
				defer close(pub)
				for {
					msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
					if !ok {
						return err
					}
					controlChan <- struct{}{}
					pub <- msg
				}
			},
		}
	}

	pn := &ParallelNode{
		NewNode: newPubSubNode,
		Workers: workerCount,
	}
	pn.SetLogger(logger)

	out := pn.Pub()
	in := make(chan *Message)
	pn.Sub(in)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := pn.Run(ctx)
		is.NoErr(err)
	}()

	// send messages to workers until each has one message + 1 for the parallel node itself
	for i := 0; i < workerCount+1; i++ {
		in <- &Message{Ctx: ctx, Record: opencdc.Record{Key: opencdc.StructuredData{"id": i}}}
	}

	// sending another message to the node results in a timeout, because it is blocking
	err := cchan.ChanIn[*Message](in).SendTimeout(ctx, &Message{}, time.Millisecond*100)
	is.True(cerrors.Is(err, context.DeadlineExceeded))

	// unblock all workers and allow them to send the messages forward
	for i := 0; i < workerCount+1; i++ {
		_, ok, err := controlChan.RecvTimeout(ctx, time.Millisecond*100)
		is.NoErr(err)
		is.True(ok)
	}

	// stop node
	close(in)

	// ensure messages are received in order and out is closed
	i := 0
	for {
		msg, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*100)
		is.NoErr(err)
		if !ok {
			break
		}
		is.Equal(msg.Record.Key, opencdc.StructuredData{"id": i})
		i++
	}
	is.Equal(i, workerCount+1)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
}

func TestParallelNode_ErrorAll(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	controlChan := cchan.Chan[struct{}](make(chan struct{}))

	const workerCount = 10

	newPubSubNode := func(i int) PubSubNode {
		name := fmt.Sprintf("test-node-%d", i)
		return &parallelTestNode{
			Name: name,
			F: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
				defer close(pub)
				msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
				is.NoErr(err)
				is.True(ok)
				controlChan <- struct{}{}
				return msg.Nack(cerrors.New("test error"), name)
			},
		}
	}

	pn := &ParallelNode{
		NewNode: newPubSubNode,
		Workers: workerCount,
	}
	pn.SetLogger(logger)

	out := pn.Pub()
	in := make(chan *Message)
	pn.Sub(in)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := pn.Run(ctx)
		is.True(err != nil)
	}()

	// send messages to workers until each has one message
	for i := 0; i < workerCount; i++ {
		in <- &Message{Ctx: ctx, Record: opencdc.Record{Key: opencdc.StructuredData{"id": i}}}
	}
	// unblock all workers and allow them to send the messages forward
	for i := 0; i < workerCount; i++ {
		_, ok, err := controlChan.RecvTimeout(ctx, time.Millisecond*100)
		is.NoErr(err)
		is.True(ok)
	}

	// ensure out gets closed
	_, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*100)
	is.NoErr(err)
	is.True(!ok)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
}

func TestParallelNode_ErrorSingle(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	controlChan := cchan.Chan[struct{}](make(chan struct{}))

	const workerCount = 10

	newPubSubNode := func(i int) PubSubNode {
		name := fmt.Sprintf("test-node-%d", i)
		return &parallelTestNode{
			Name: name,
			F: func(ctx context.Context, sub <-chan *Message, pub chan<- *Message) error {
				defer close(pub)
				for {
					msg, ok, err := cchan.ChanOut[*Message](sub).Recv(ctx)
					if !ok {
						return err
					}
					controlChan <- struct{}{}
					if msg.Record.Key.(opencdc.StructuredData)["id"].(int) == 1 {
						// only message with id 1 fails
						return msg.Nack(cerrors.New("test error"), name)
					}
					pub <- msg
				}
			},
		}
	}

	pn := &ParallelNode{
		NewNode: newPubSubNode,
		Workers: workerCount,
	}
	pn.SetLogger(logger)

	out := pn.Pub()
	in := make(chan *Message)
	pn.Sub(in)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := pn.Run(ctx)
		is.True(err != nil)
	}()

	// send messages to workers until each has one message
	for i := 0; i < workerCount; i++ {
		in <- &Message{Ctx: ctx, Record: opencdc.Record{Key: opencdc.StructuredData{"id": i}}}
	}
	// unblock all workers and allow them to send the messages forward
	for i := 0; i < workerCount; i++ {
		_, ok, err := controlChan.RecvTimeout(ctx, time.Millisecond*100)
		is.NoErr(err)
		is.True(ok)
	}

	// one message should get through
	msg, ok, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*100)
	is.NoErr(err)
	is.True(ok)
	is.Equal(msg.Record.Key, opencdc.StructuredData{"id": 0})

	// out should get closed after that, as all other messages are nacked
	_, ok, err = cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*100)
	is.NoErr(err)
	is.True(!ok)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
}

func TestParallelNode_Processor(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	var workersCurrent int32       // current number of parallel workers
	var workersHighWatermark int32 // highest number of parallel workers

	const workerCount = 100
	const msgCount = 1000

	// create a dummy processor
	proc := mock.NewProcessor(ctrl)
	proc.EXPECT().
		Open(gomock.Any()).
		Times(workerCount)
	proc.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
			current := atomic.AddInt32(&workersCurrent, 1)
			atomic.CompareAndSwapInt32(&workersHighWatermark, current-1, current)
			time.Sleep(time.Millisecond) // sleep to let other workers catch up
			atomic.AddInt32(&workersCurrent, -1)

			out := make([]sdk.ProcessedRecord, len(records))
			for i, r := range records {
				out[i] = sdk.SingleRecord(r)
			}

			return out
		}).Times(msgCount)
	proc.EXPECT().
		Teardown(gomock.Any()).
		Times(workerCount)

	newProcNode := func(i int) PubSubNode {
		return &ProcessorNode{
			Name:           fmt.Sprintf("test-%d", i),
			Processor:      proc,
			ProcessorTimer: noop.Timer{},
		}
	}

	pn := &ParallelNode{
		NewNode: newProcNode,
		Workers: workerCount,
	}
	pn.SetLogger(logger)

	out := pn.Pub()
	in := make(chan *Message)
	pn.Sub(in)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := pn.Run(ctx)
		is.NoErr(err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < msgCount; i++ {
			in <- &Message{Ctx: ctx, Record: opencdc.Record{Key: opencdc.StructuredData{"id": i}}}
		}
		close(in)
	}()

	i := 0
	for msg := range out {
		is.Equal(msg.Record.Key, opencdc.StructuredData{"id": i})
		i++
	}
	is.Equal(i, msgCount)

	err := (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
	is.True(workersHighWatermark <= workerCount) // more workers spawned than expected
	is.True(workersHighWatermark >= 2)           // no parallel workers spawned
}
