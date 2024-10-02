// Copyright © 2024 Meroxa, Inc.
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

package funnel

import (
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

type ProcessorTask struct {
	id        string
	processor Processor
	logger    log.CtxLogger
	timer     metrics.Timer
}

type Processor interface {
	// Open configures and opens a processor plugin
	Open(ctx context.Context) error
	Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord
	// Teardown tears down a processor plugin.
	// In case of standalone plugins, that means stopping the WASM module.
	Teardown(context.Context) error
}

func NewProcessorTask(
	id string,
	processor Processor,
	logger log.CtxLogger,
	timer metrics.Timer,
) *ProcessorTask {
	logger = logger.WithComponent("task:processor")
	logger.Logger = logger.With().Str(log.ProcessorIDField, id).Logger()
	return &ProcessorTask{
		id:        id,
		processor: processor,
		logger:    logger,
		timer:     timer,
	}
}

func (t *ProcessorTask) ID() string {
	return t.id
}

func (t *ProcessorTask) Open(ctx context.Context) error {
	t.logger.Debug(ctx).Msg("opening processor")
	err := t.processor.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open processor: %w", err)
	}
	t.logger.Debug(ctx).Msg("processor open")
	return nil
}

func (t *ProcessorTask) Close(ctx context.Context) error {
	t.logger.Debug(ctx).Msg("tearing down processor")
	return t.processor.Teardown(ctx)
}

func (t *ProcessorTask) Do(ctx context.Context, b *Batch) error {
	executeTime := time.Now()
	recsIn := b.ActiveRecords()
	recsOut := t.processor.Process(ctx, recsIn)

	// TODO should this be called N times for each record? It's what we do in the destination
	t.timer.Update(time.Since(executeTime))

	if len(recsIn) != len(recsOut) {
		// TODO accept this and mark the rest as skipped
		err := cerrors.Errorf("processor was given %v record(s), but returned %v", len(recsIn), len(recsOut))
		return cerrors.FatalError(err)
	}

	// TODO mark records
	/*
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
		default:
			err := cerrors.Errorf("processor returned unknown record type: %T", v)
			if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
				return cerrors.FatalError(nackErr)
			}
			return cerrors.FatalError(err)
		}
	*/

	return nil
}
