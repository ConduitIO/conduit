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
	"github.com/conduitio/conduit/pkg/foundation/csync"
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

	sem         semaphore.Simple
	workerCount csync.ValueWatcher[int]

	hackyLock sync.Mutex
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

	var wg sync.WaitGroup
	defer func() {
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

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		_, err = n.workerCount.Watch(msg.Ctx, func(count int) bool {
			return count < n.Workers
		})
		if err != nil {
			return err
		}

		n.hackyLock.Lock()
		n.workerCount.Set(n.workerCount.Get() + 1)
		n.hackyLock.Unlock()

		ticket := n.sem.Enqueue()
		wg.Add(1)
		go func(ticket semaphore.Ticket, msg *Message) {
			defer func() {
				n.hackyLock.Lock()
				n.workerCount.Set(n.workerCount.Get() - 1)
				n.hackyLock.Unlock()
				wg.Done()
			}()
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
						errs <- cerrors.Errorf("failed to ack skipped message: %w", err)
					}
				default:
					err = msg.Nack(err, n.ID())
					if err != nil {
						errs <- cerrors.Errorf("error executing processor: %w", err)
					}
				}
				// error was handled successfully, we recovered
				return
			}
			msg.Record = rec

			err = n.base.Send(ctx, n.logger, msg)
			if err != nil {
				errs <- msg.Nack(err, n.ID())
			}
		}(ticket, msg)
	}
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
