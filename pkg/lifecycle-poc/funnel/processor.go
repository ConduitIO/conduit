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
)

type ProcessorTask struct {
	id        string
	processor Processor
	logger    log.CtxLogger

	metrics ProcessorMetrics
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
	metrics ProcessorMetrics,
) *ProcessorTask {
	logger = logger.WithComponent("task:processor")
	logger.Logger = logger.With().Str(log.ProcessorIDField, id).Logger()
	return &ProcessorTask{
		id:        id,
		processor: processor,
		logger:    logger,
		metrics:   metrics,
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

// Do processes a batch of records using the processor plugin. It returns
// an error if the processor fails to process the records, or if the
// processor returns an invalid number of records.
//
// If the batch contains filtered records, the processor will only process
// the active records.
// For instance:
//   - Consider a batch with 5 records, 2 of which are filtered. A represents
//     the active records, and F represents the filtered records:
//     [A, A, F, A, F]
//   - The processor will only process the active records, so it will receive
//     [A, A, A].
//   - The processor will return the processed records, which will be
//     [X, X, X], where X represents the processed records. The records are
//     either processed, filtered or errored.
//   - When marking the records in the batch as processed, filtered or
//     errored, the ProcessorTask takes into account the indices of the filtered
//     records, so it marks the correct records in the batch. In the example that
//     we used, the processor will mark the batch as [X, X, F, X, F], leaving the
//     filtered records as is.
func (t *ProcessorTask) Do(ctx context.Context, b *Batch) error {
	start := time.Now()
	recsIn := b.ActiveRecords()
	recsOut := t.processor.Process(ctx, recsIn)

	if len(recsOut) == 0 {
		return cerrors.Errorf("processor didn't return any records")
	}
	t.metrics.Observe(len(recsOut), start)

	if len(recsIn) > len(recsOut) {
		// Processor skipped some records, append empty records, so that we can
		// mark them to be retried.
		recsOut = append(recsOut, make([]sdk.ProcessedRecord, len(recsIn)-len(recsOut))...)
	}

	// Mark records in the batch as processed, filtered or errored.
	// We do this a bit smarter, by collecting ranges of records that are
	// processed, filtered or errored, and then marking them in one go.
	// We need to account for the fact that the batch might have filtered
	// records, so we mark them from the end towards the start. This way we
	// don't have to worry about filtered records messing with the indices of
	// active records.

	to := len(recsOut)
	for i := len(recsOut) - 1; i >= 0; i-- {
		if i == 0 ||
			!t.isSameType(recsOut[i-1], recsOut[i]) {
			// We have a range of records that are the same type.
			// Mark them in one go.
			t.markBatchRecords(b, i, recsOut[i:to])
			to = i
		}
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
		b.Filter(from, from+len(records))
	case sdk.ErrorRecord:
		errs := make([]error, len(records))
		for i, rec := range records {
			errs[i] = rec.(sdk.ErrorRecord).Error
		}
		b.Nack(from, errs...)
	case sdk.MultiRecord:
		for i, rec := range records {
			multiRec := rec.(sdk.MultiRecord)

			switch len(multiRec) {
			case 0:
				// MultiRecord with no records, we mark it to be filtered.
				b.Filter(from + i)
			case 1:
				// MultiRecord with a single record, we don't split it.
				b.SetRecords(from+i, multiRec)
			default:
				// MultiRecord with multiple records, we split it.
				b.SplitRecord(from+i, multiRec)
			}
		}
	case nil:
		// Empty records are not processed, we mark them to be retried.
		// This can happen if the processor returns fewer records than it
		// received.
		b.Retry(from, from+len(records))
	}
}

// isSameType checks if two records are of the same type. This is used to
// determine if we can mark a range of records in the batch as processed,
// filtered or errored.
func (t *ProcessorTask) isSameType(a, b sdk.ProcessedRecord) bool {
	switch a.(type) {
	case sdk.SingleRecord:
		_, ok := b.(sdk.SingleRecord)
		return ok
	case sdk.FilterRecord:
		_, ok := b.(sdk.FilterRecord)
		return ok
	case sdk.ErrorRecord:
		_, ok := b.(sdk.ErrorRecord)
		return ok
	case sdk.MultiRecord:
		_, ok := b.(sdk.MultiRecord)
		return ok
	case nil:
		return b == nil
	default:
		return false
	}
}
