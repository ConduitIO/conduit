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

//go:generate mockgen -destination=mock/processor.go -package=mock -mock_names=Processor=Processor . Processor

package stream

import (
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/record"
)

type ProcessorNode struct {
	Name           string
	Processor      Processor
	ProcessorTimer metrics.Timer

	base   pubSubNodeBase
	logger log.CtxLogger
}

type Processor interface {
	// Open configures and opens a processor plugin
	Open(ctx context.Context) error
	Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord
	// Teardown tears down a processor plugin.
	// In case of standalone plugins, that means stopping the WASM module.
	Teardown(context.Context) error
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

	err = n.Processor.Open(ctx)
	if err != nil {
		return cerrors.Errorf("couldn't open processor: %w", err)
	}
	defer func() {
		// teardown will kill the plugin process
		tdErr := n.Processor.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down processor")
		})
	}()

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		executeTime := time.Now()
		recsIn := []opencdc.Record{msg.Record.ToOpenCDC()}
		recsOut := n.Processor.Process(msg.Ctx, recsIn)
		n.ProcessorTimer.Update(time.Since(executeTime))

		if len(recsIn) != len(recsOut) {
			return cerrors.Errorf("processor was given %v records, but returned %v", len(recsIn), len(recsOut))
		}

		switch v := recsOut[0].(type) {
		case sdk.SingleRecord:
			msg.Record = record.FromOpenCDC(opencdc.Record(v))
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
