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

//go:generate mockgen -typed -destination=mock/processor.go -package=mock -mock_names=Processor=Processor . Processor

package stream

import (
	"bytes"
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
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
	// Teardown needs to be called even if Open() fails
	// (to mark the processor as not running)
	defer func() {
		n.logger.Debug(ctx).Msg("tearing down processor")
		tdErr := n.Processor.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down processor")
		})
	}()

	n.logger.Debug(ctx).Msg("opening processor")
	err = n.Processor.Open(ctx)
	if err != nil {
		n.logger.Err(ctx, err).Msg("failed opening processor")
		return cerrors.Errorf("couldn't open processor: %w", err)
	}

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		executeTime := time.Now()
		recsIn := []opencdc.Record{msg.Record}
		recsOut := n.Processor.Process(msg.Ctx, recsIn)
		n.ProcessorTimer.Update(time.Since(executeTime))

		if len(recsIn) != len(recsOut) {
			err := cerrors.Errorf("processor was given %v record(s), but returned %v", len(recsIn), len(recsOut))
			// todo when processors can accept multiple records
			// make sure that we ack as many records as possible
			// (here we simply nack all of them, which is always only one)
			if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
				return nackErr
			}
			return err
		}

		switch v := recsOut[0].(type) {
		case sdk.SingleRecord:
			err := n.handleSingleRecord(ctx, msg, v)
			// handleSingleRecord already checks the nack error (if any)
			// so it's enough to just return the error from it
			if err != nil {
				return err
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
				return cerrors.FatalError(cerrors.Errorf("error executing processor: %w", err))
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

// handleSingleRecord handles a sdk.SingleRecord by checking the position,
// setting the new record on the message and sending it downstream.
// If there are any errors, the method nacks the message and returns
// an appropriate error (if nack-ing failed, it returns the nack error)
func (n *ProcessorNode) handleSingleRecord(ctx context.Context, msg *Message, rec sdk.SingleRecord) error {
	if !bytes.Equal(rec.Position, msg.Record.Position) {
		err := cerrors.Errorf(
			"processor changed position from '%v' to '%v' "+
				"(not allowed because source connector cannot correctly acknowledge messages)",
			msg.Record.Position,
			rec.Position,
		)

		if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
			return nackErr
		}
		// correctly nacked (sent to the DLQ)
		// so we return the "original" error here
		return err
	}

	msg.Record = opencdc.Record(rec)
	err := n.base.Send(ctx, n.logger, msg)
	if err != nil {
		return msg.Nack(err, n.ID())
	}

	return nil
}
