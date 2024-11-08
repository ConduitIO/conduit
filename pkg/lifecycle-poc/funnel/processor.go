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

//go:generate mockgen -typed -destination=mock/processor.go -package=mock -mock_names=Processor=Processor . Processor

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
	start := time.Now()
	recsIn := b.ActiveRecords()
	recsOut := t.processor.Process(ctx, recsIn)

	if len(recsOut) == 0 {
		return cerrors.Errorf("processor didn't return any records")
	}
	t.observeMetrics(len(recsOut), start)

	// Mark records in the batch as processed, filtered or errored.
	// We do this a bit smarter, by collecting ranges of records that are
	// processed, filtered or errored, and then marking them in one go.

	from := 0      // Start of the current range of records with the same type
	rangeType := 0 // 0 = SingleRecord, 1 = FilterRecord, 2 = ErrorRecord

	for i, rec := range recsOut {
		var currentType int
		switch rec.(type) {
		case sdk.SingleRecord:
			currentType = 0
		case sdk.FilterRecord:
			currentType = 1
		case sdk.ErrorRecord:
			currentType = 2
		default:
			err := cerrors.Errorf("processor returned unknown record type: %T", rec)
			return cerrors.FatalError(err)
		}

		if currentType == rangeType {
			continue
		}

		t.markBatchRecords(b, from, recsOut[from:i])
		from, rangeType = i, currentType
	}

	// Mark the last range of records.
	t.markBatchRecords(b, from, recsOut[from:])

	if len(recsIn) > len(recsOut) {
		// Processor skipped some records, mark them to be retried.
		b.Retry(len(recsOut), len(recsIn))
	}

	return nil
}

// markBatchRecords marks a range of records in a batch as processed, filtered or
// errored, based on the type of records returned by the processor. The worker
// can then use this information to continue processing the batch.
func (t *ProcessorTask) markBatchRecords(b *Batch, from int, records []sdk.ProcessedRecord) {
	if len(records) == 0 {
		return // This can happen if the first record is not a SingleRecord.
	}
	switch records[0].(type) {
	case sdk.SingleRecord:
		recs := make([]opencdc.Record, len(records))
		for i, rec := range records {
			recs[i] = opencdc.Record(rec.(sdk.SingleRecord))
		}
		b.SetRecords(from, recs)
	case sdk.FilterRecord:
		b.Filter(from, len(records))
	case sdk.ErrorRecord:
		errs := make([]error, len(records))
		for i, rec := range records {
			errs[i] = rec.(sdk.ErrorRecord).Error
		}
		b.Nack(from, errs...)
	}
}

func (t *ProcessorTask) observeMetrics(n int, start time.Time) {
	tookPerRecord := time.Since(start) / time.Duration(n)
	go func() {
		for range n {
			t.timer.Update(tookPerRecord)
		}
	}()
}
