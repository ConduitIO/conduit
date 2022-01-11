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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/processor"
)

type ProcessorNode struct {
	Name           string
	Processor      processor.Processor
	ProcessorTimer metrics.Timer

	base   pubSubNodeBase
	logger log.CtxLogger
}

func (n *ProcessorNode) ID() string {
	return n.Name
}

func (n *ProcessorNode) Run(ctx context.Context) error {
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger)
	if err != nil {
		return err
	}

	defer cleanup()
	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		executeTime := time.Now()
		rec, err := n.Processor.Execute(msg.Ctx, msg.Record)
		n.ProcessorTimer.Update(time.Since(executeTime))
		if err != nil {
			// Check for Skipped records
			if err == processor.ErrSkipRecord {
				// NB: Ack skipped messages since they've been correctly handled
				err := msg.Ack()
				if err != nil {
					return cerrors.Errorf("failed to ack skipped message: %w", err)
				}
				continue
			}
			err = msg.Nack(err)
			if err != nil {
				msg.Drop()
				return cerrors.Errorf("error applying transform: %w", err)
			}
			// nack was handled successfully, we recovered
			continue
		}
		msg.Record = rec

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			msg.Drop()
			return err
		}
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
