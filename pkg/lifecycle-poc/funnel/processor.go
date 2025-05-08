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

func (t *ProcessorTask) Do(ctx context.Context, b *Batch) error {
	start := time.Now()
	recsIn := b.ActiveRecords()
	recsOut := t.processor.Process(ctx, recsIn)

	if len(recsOut) == 0 {
		return cerrors.Errorf("processor didn't return any records")
	}
	t.metrics.Observe(len(recsOut), start)

	// Map active records back to their original indices in the batch, but only
	// if we have filtered records. Otherwise, we can just use the original indices.
	var activeIndices []int
	if b.filterCount > 0 {
		activeIndices = make([]int, 0, len(b.records))
		for i, status := range b.recordStatuses {
			if status.Flag != RecordFlagFilter {
				activeIndices = append(activeIndices, i)
			}
		}
	}

	// Mark records in the batch as processed, filtered or errored.
	// We do this a bit smarter, by collecting ranges of records that are
	// processed, filtered or errored, and then marking them in one go.

	from := 0
	for i := 1; i <= len(recsOut); i++ {
		if i == len(recsOut) ||
			!t.isSameType(recsOut[i-1], recsOut[i]) ||
			!t.isConsecutive(activeIndices, i-1, i) {
			// We have a range of records that are the same type, and
			// consecutive in the original batch.
			// Mark them in one go.

			idx := from
			if activeIndices != nil {
				// If we have filtered records, we need to map the from index
				// back to the original batch index.
				idx = activeIndices[from]
			}
			t.markBatchRecords(b, idx, recsOut[from:i])
			from = i
		}
	}

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
		b.Filter(from, from+len(records))
	case sdk.ErrorRecord:
		errs := make([]error, len(records))
		for i, rec := range records {
			errs[i] = rec.(sdk.ErrorRecord).Error
		}
		b.Nack(from, errs...)
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
	default:
		return false
	}
}

// isConsecutive checks if two indices are consecutive in the original batch.
func (t *ProcessorTask) isConsecutive(indices []int, i, j int) bool {
	if indices == nil {
		return true // No filtering, so all records are consecutive.
	}
	return indices[i]+1 == indices[j]
}
