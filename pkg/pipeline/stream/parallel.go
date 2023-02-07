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
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

// ParallelNode wraps a PubSubNode and parallelizes it by running it in separate
// workers.
type ParallelNode struct {
	Name    string
	NewNode func(i int) PubSubNode
	Workers int

	base   pubSubNodeBase
	logger log.CtxLogger
}

func (n *ParallelNode) ID() string {
	return n.Name
}

func (n *ParallelNode) Run(ctx context.Context) error {
	// each worker spawns 2 goroutines, allow each to store an error in the channel
	errs := make(chan error, n.Workers*2)
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, errs)
	if err != nil {
		return err
	}
	defer cleanup()

	jobs := make(chan *Message)
	var wg sync.WaitGroup
	var sem semaphore.Simple

	for i := 0; i < n.Workers; i++ {
		node := n.NewNode(i)
		worker := newParallelNodeWorker(node, jobs, errs, &sem, n.logger, n.base.Send)
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker.Run(ctx)
		}()
	}
	defer func() {
		close(jobs)
		wg.Wait()
		for {
			select {
			case workerErr := <-errs:
				err = cerrors.LogOrReplace(err, workerErr, func() {
					n.logger.Warn(ctx).Err(workerErr).Msg("parallel worker node failed")
				})
			default:
				return
			}
		}
	}()

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		msg.RegisterStatusHandler(func(msg *Message, _ StatusChange) error {
			if t := msg.Ctx.Value(parallelNodeTicketCtxKey{}); t != nil {
				n.logger.Trace(ctx).Msg("discarding ticket")
				sem.Discard(t.(semaphore.Ticket))
			}
			return nil
		})

		t := sem.Enqueue()
		msg.Ctx = context.WithValue(msg.Ctx, parallelNodeTicketCtxKey{}, t)

		jobs <- msg
	}
}

func (n *ParallelNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

func (n *ParallelNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *ParallelNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}

type parallelNodeTicketCtxKey struct{}

type parallelNodeWorker struct {
	node   PubSubNode
	errs   chan<- error
	sem    *semaphore.Simple
	logger log.CtxLogger
	send   func(ctx context.Context, logger log.CtxLogger, msg *Message) error

	out <-chan *Message
}

func newParallelNodeWorker(
	node PubSubNode,
	jobs <-chan *Message,
	errs chan<- error,
	sem *semaphore.Simple,
	logger log.CtxLogger,
	send func(ctx context.Context, logger log.CtxLogger, msg *Message) error,
) *parallelNodeWorker {
	logger.Logger = logger.With().Str(log.ParallelWorkerIDField, node.ID()).Logger()
	SetLogger(node, logger, LoggerWithComponent)
	node.Sub(jobs)
	out := node.Pub()
	return &parallelNodeWorker{
		node:   node,
		errs:   errs,
		sem:    sem,
		logger: logger,
		send:   send,

		out: out,
	}
}

func (w *parallelNodeWorker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.runWorker(ctx)
	}()
	go func() {
		defer wg.Done()
		w.runForwarder(ctx)
	}()
	wg.Wait()
}

func (w *parallelNodeWorker) runWorker(ctx context.Context) {
	err := w.node.Run(ctx)
	if err != nil {
		w.errs <- err
	}
}

func (w *parallelNodeWorker) runForwarder(ctx context.Context) {
	for msg := range w.out {
		err := w.forwardMessage(ctx, msg)
		if err != nil {
			w.errs <- err
			break
		}
	}
}

func (w *parallelNodeWorker) forwardMessage(ctx context.Context, msg *Message) error {
	t := msg.Ctx.Value(parallelNodeTicketCtxKey{}).(semaphore.Ticket)
	w.logger.Trace(msg.Ctx).Msg("acquiring ticket")
	l := w.sem.Acquire(t)
	defer func() {
		w.logger.Trace(msg.Ctx).Msg("releasing ticket")
		w.sem.Release(l)
	}()
	w.logger.Trace(msg.Ctx).Msg("acquired ticket")

	// "remove" ticket from context to prevent it from being discarded in the
	// message status handler
	msg.Ctx = context.WithValue(msg.Ctx, parallelNodeTicketCtxKey{}, nil)
	err := w.send(ctx, w.logger, msg)
	if err != nil {
		return msg.Nack(err, w.node.ID())
	}
	return nil
}
