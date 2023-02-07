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

	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	pmock "github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

type parallelTestNode struct {
	Name string
	F    func(ctx context.Context) error

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
	return n.F(ctx)
}

func TestParallel_Success(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	controlChan := cchan.Chan[struct{}](make(chan struct{}))

	const workerCount = 10

	newPubSubNode := func(i int) PubSubNode {
		n := &parallelTestNode{Name: fmt.Sprintf("test-node-%d", i)}
		n.F = func(ctx context.Context) error {
			defer close(n.pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](n.sub).Recv(ctx)
				if !ok {
					return err
				}
				controlChan <- struct{}{}
				n.pub <- msg
			}
		}
		return n
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
		in <- &Message{Ctx: ctx, Record: record.Record{Key: record.StructuredData{"id": i}}}
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
		is.Equal(msg.Record.Key, record.StructuredData{"id": i})
		i++
	}
	is.Equal(i, workerCount+1)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
}

func TestParallel_ErrorAll(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	controlChan := cchan.Chan[struct{}](make(chan struct{}))

	const workerCount = 10

	newPubSubNode := func(i int) PubSubNode {
		n := &parallelTestNode{Name: fmt.Sprintf("test-node-%d", i)}
		n.F = func(ctx context.Context) error {
			defer close(n.pub)
			_, ok, err := cchan.ChanOut[*Message](n.sub).Recv(ctx)
			is.True(ok)
			is.NoErr(err)
			controlChan <- struct{}{}
			return cerrors.Errorf("test-error")
		}
		return n
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
		in <- &Message{Ctx: ctx, Record: record.Record{Key: record.StructuredData{"id": i}}}
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

func TestParallel_ErrorSingle(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	controlChan := cchan.Chan[struct{}](make(chan struct{}))

	const workerCount = 10

	newPubSubNode := func(i int) PubSubNode {
		n := &parallelTestNode{Name: fmt.Sprintf("test-node-%d", i)}
		n.F = func(ctx context.Context) error {
			defer close(n.pub)
			for {
				msg, ok, err := cchan.ChanOut[*Message](n.sub).Recv(ctx)
				if !ok {
					return err
				}
				controlChan <- struct{}{}
				if msg.Record.Key.(record.StructuredData)["id"].(int) == 1 {
					// only message with id 1 fails
					return cerrors.New("test error")
				}
				n.pub <- msg
			}
		}
		return n
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
		in <- &Message{Ctx: ctx, Record: record.Record{Key: record.StructuredData{"id": i}}}
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
	is.Equal(msg.Record.Key, record.StructuredData{"id": 0})

	// out should get closed after that, as all other messages are nacked
	_, ok, err = cchan.ChanOut[*Message](out).RecvTimeout(ctx, time.Millisecond*100)
	is.NoErr(err)
	is.True(!ok)

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
}

func TestParallel_Processor(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	var workersCurrent int32       // current number of parallel workers
	var workersHighWatermark int32 // highest number of parallel workers

	const workerCount = 100
	const msgCount = 1000

	// create a dummy processor
	proc := pmock.NewProcessor(ctrl)
	proc.EXPECT().Process(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, r record.Record) (record.Record, error) {
		current := atomic.AddInt32(&workersCurrent, 1)
		atomic.CompareAndSwapInt32(&workersHighWatermark, current-1, current)
		time.Sleep(time.Millisecond) // sleep to let other workers catch up
		atomic.AddInt32(&workersCurrent, -1)
		return r, nil
	}).Times(msgCount)

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
			in <- &Message{Ctx: ctx, Record: record.Record{Key: record.StructuredData{"id": i}}}
		}
		close(in)
	}()

	i := 0
	for msg := range out {
		is.Equal(msg.Record.Key, record.StructuredData{"id": i})
		i++
	}
	is.Equal(i, msgCount)

	err := (*csync.WaitGroup)(&wg).WaitTimeout(ctx, time.Second)
	is.NoErr(err)
	is.True(workersHighWatermark <= workerCount) // more workers spawned than expected
	is.True(workersHighWatermark >= 2)           // no parallel workers spawned
}
