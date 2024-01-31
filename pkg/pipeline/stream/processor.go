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
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/record"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/processor"
)

type ProcessorNode struct {
	Name           string
	Processor      processor.Interface
	ProcessorTimer metrics.Timer

	base   pubSubNodeBase
	logger log.CtxLogger
}

func (n *ProcessorNode) ID() string {
	return n.Name
}

func (n *ProcessorNode) Run(ctx context.Context) error {
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, nil)
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
		// todo needs to be filled out
		recs := n.Processor.Process(msg.Ctx, []opencdc.Record{msg.Record.ToOpenCDC()})
		n.ProcessorTimer.Update(time.Since(executeTime))

		// todo a "bad" processor might have not returned any records
		switch v := recs[0].(type) {
		case sdk.SingleRecord:
			msg.Record = record.FromOpenCDC(v)
			err = n.base.Send(ctx, n.logger, msg)
			if err != nil {
				return msg.Nack(err, n.ID())
			}
		case sdk.FilterRecord:
			// NB: Ack skipped messages since they've been correctly handled
			err := msg.Ack()
			if err != nil {
				return cerrors.Errorf("failed to ack skipped message: %w", err)
			}
		case sdk.ErrorRecord:
			err = msg.Nack(v.Error, n.ID())
			if err != nil {
				return cerrors.Errorf("error executing processor: %w", err)
			}
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
