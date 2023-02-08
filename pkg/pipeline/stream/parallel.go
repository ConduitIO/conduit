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

	"github.com/conduitio/conduit/pkg/foundation/cchan"
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
	// allow each worker to store an error in the channel
	errs := make(chan error, n.Workers)
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, errs)
	if err != nil {
		return err
	}
	defer cleanup()

	workerJobs := make(chan parallelNodeJob)
	var workerWg sync.WaitGroup
	var sem semaphore.Simple

	for i := 0; i < n.Workers; i++ {
		node := n.NewNode(i)
		worker := newParallelNodeWorker(node, workerJobs, &sem, n.logger)
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			worker.Run(ctx)
		}()
	}
	coordinatorJobs := make(chan parallelNodeJob, n.Workers)
	var coordinatorWg sync.WaitGroup
	coordinatorWg.Add(1)
	coordinator := newParallelNodeCoordinator(n.ID(), coordinatorJobs, errs, n.logger, n.base.Send)
	go func() {
		defer coordinatorWg.Done()
		coordinator.Run(ctx)
	}()

	workersDone := make(chan struct{})
	go func() {
		workerWg.Wait()
		close(workersDone)
	}()

	defer func() {
		close(workerJobs)
		close(coordinatorJobs)
		workerWg.Wait()
		coordinatorWg.Wait()
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

		job := parallelNodeJob{
			Message: msg,
			Done:    make(chan struct{}), // TODO use channel pool
		}

		select {
		case workerJobs <- job:
			coordinatorJobs <- job
		case <-workersDone:
			noWorkerRunningErr := cerrors.New("no worker is running")
			err = msg.Nack(noWorkerRunningErr, n.ID())
			if err != nil {
				return err
			}
			return noWorkerRunningErr // stop here, no processing can be done if no workers are running anymore
		}
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

type parallelNodeJob struct {
	Message *Message
	Done    chan struct{}
}

type parallelNodeCoordinator struct {
	name   string
	jobs   <-chan parallelNodeJob
	errs   chan<- error
	logger log.CtxLogger
	send   func(ctx context.Context, logger log.CtxLogger, msg *Message) error
}

func newParallelNodeCoordinator(
	name string,
	jobs <-chan parallelNodeJob,
	errs chan<- error,
	logger log.CtxLogger,
	send func(ctx context.Context, logger log.CtxLogger, msg *Message) error,
) *parallelNodeCoordinator {
	logger.Logger = logger.With().Str(log.ParallelWorkerIDField, name+"-coordinator").Logger()
	return &parallelNodeCoordinator{
		name:   name,
		jobs:   jobs,
		errs:   errs,
		logger: logger,
		send:   send,
	}
}

func (c *parallelNodeCoordinator) Run(ctx context.Context) {
	fail := false
	for job := range c.jobs {
		// wait for job to be done
		<-job.Done

		// check if the message was successfully processed or not
		err := job.Message.StatusError()
		if err != nil {
			// propagate error to main node and trigger short circuit, all
			// messages need to fail from here on out
			c.errs <- err
			fail = true
			continue
		}
		if job.Message.Status() != MessageStatusOpen {
			// message already acked or nacked
			continue
		}
		if fail {
			err = job.Message.Nack(cerrors.Errorf("another message failed to be processed successfully"), c.name)
			if err != nil {
				c.errs <- err
			}
			continue
		}
		err = c.send(ctx, c.logger, job.Message)
		if err != nil {
			err = job.Message.Nack(err, c.name)
			if err != nil {
				c.errs <- err
				fail = true
			}
		}
	}
}

type parallelNodeWorker struct {
	node   PubSubNode
	sem    *semaphore.Simple
	logger log.CtxLogger

	jobs cchan.ChanOut[parallelNodeJob]
}

func newParallelNodeWorker(
	node PubSubNode,
	jobs <-chan parallelNodeJob,
	sem *semaphore.Simple,
	logger log.CtxLogger,
) *parallelNodeWorker {
	nodeLogger := logger
	nodeLogger.Logger = logger.With().Str(log.ParallelWorkerIDField, node.ID()).Logger()
	SetLogger(node, nodeLogger, LoggerWithComponent)

	logger.Logger = logger.With().Str(log.ParallelWorkerIDField, node.ID()+"-worker").Logger()
	return &parallelNodeWorker{
		node:   node,
		sem:    sem,
		logger: logger,

		jobs: jobs,
	}
}

func (w *parallelNodeWorker) Run(ctx context.Context) {
	in := make(chan *Message)
	w.node.Sub(in)
	out := w.node.Pub()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		w.runWorker(ctx)
	}()

	forwarderDone := make(chan struct{})
	go func() {
		defer func() {
			<-workerDone
			close(forwarderDone)
		}()
		w.runForwarder(ctx, in, out)
	}()

	<-forwarderDone
}

func (w *parallelNodeWorker) runWorker(ctx context.Context) {
	// we can ignore errors, if an error happens they are propagated through
	// a message nack/ack to the forwarder node and further to the coordinator
	_ = w.node.Run(ctx)
}

func (w *parallelNodeWorker) runForwarder(ctx context.Context, in chan<- *Message, out <-chan *Message) {
	defer close(in)
	for job := range w.jobs {
		select {
		case _, ok := <-out:
			if ok {
				panic("worker node produced a message without receiving a message")
			}
			// out is closed, worker node stopped running (closed context?),
			// nack in-flight message and ignore error, it will be picked up
			// by the coordinator
			_ = job.Message.Nack(cerrors.New("worker not running"), w.node.ID())
			job.Done <- struct{}{}
			return
		case in <- job.Message:
			// message submitted to worker node
		}

		select {
		case <-job.Message.Acked():
			// message was acked, i.e. filtered out
		case <-job.Message.Nacked():
			// message was nacked, i.e. sent to DLQ or dropped
		case <-out:
			// message was processed (if out returned a message it was
			// successful, if out was closed it was unsuccessful and the
			// worker stopped running)
		}

		// get error before we give the message to the coordinator
		err := job.Message.StatusError()

		// give message to coordinator
		job.Done <- struct{}{}

		if err != nil {
			// message processing failed, worker stopped running, stop accepting new jobs
			return
		}
	}
}
