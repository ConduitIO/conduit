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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/semaphore"
	"github.com/conduitio/conduit/pkg/processor"
)

type ProcessorNode struct {
	Name           string
	Processor      processor.Interface
	ProcessorTimer metrics.Timer
	Workers        int

	base   pubSubNodeBase
	logger log.CtxLogger

	sem semaphore.Simple
}

func (n *ProcessorNode) ID() string {
	return n.Name
}

func (n *ProcessorNode) Run(ctx context.Context) (err error) {
	if n.Workers == 0 {
		n.Workers = 1
	}

	errs := make(chan error, n.Workers)
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, errs)
	if err != nil {
		return err
	}
	defer cleanup()

	type job struct {
		Msg    *Message
		Ticket semaphore.Ticket
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	defer func() {
		close(jobs)
		wg.Wait()
		for {
			select {
			case workerErr := <-errs:
				err = cerrors.LogOrReplace(err, workerErr, func() {
					n.logger.Warn(ctx).Err(workerErr).Msg("processor worker failed")
				})
			default:
				return
			}
		}
	}()

	wg.Add(n.Workers)
	for i := 0; i < n.Workers; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				err := n.work(ctx, j.Msg, j.Ticket)
				if err != nil {
					errs <- err
				}
			}
		}()
	}

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		ticket := n.sem.Enqueue()
		jobs <- job{Msg: msg, Ticket: ticket}
	}
}

func (n *ProcessorNode) work(ctx context.Context, msg *Message, ticket semaphore.Ticket) error {
	executeTime := time.Now()
	rec, err := n.Processor.Process(msg.Ctx, msg.Record)
	n.ProcessorTimer.Update(time.Since(executeTime))

	l := n.sem.Acquire(ticket)
	defer n.sem.Release(l)

	if err != nil {
		// Check for Skipped records
		switch err {
		case processor.ErrSkipRecord:
			// NB: Ack skipped messages since they've been correctly handled
			err := msg.Ack()
			if err != nil {
				return cerrors.Errorf("failed to ack skipped message: %w", err)
			}
		default:
			err = msg.Nack(err, n.ID())
			if err != nil {
				return cerrors.Errorf("error executing processor: %w", err)
			}
		}
		// error was handled successfully, we recovered
		return nil
	}
	msg.Record = rec

	err = n.base.Send(ctx, n.logger, msg)
	if err != nil {
		return msg.Nack(err, n.ID())
	}
	return nil
}

func (n *ProcessorNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

func (n *ProcessorNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *ProcessorNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
