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

package processor

import (
	"context"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
)

// RunnableProcessor is a stream.Processor which has been
// initialized and is ready to be used in a pipeline.
type RunnableProcessor struct {
	*Instance
	proc sdk.Processor
	cond *processorCondition
}

func newRunnableProcessor(
	proc sdk.Processor,
	cond *processorCondition,
	i *Instance,
) *RunnableProcessor {
	return &RunnableProcessor{
		Instance: i,
		proc:     proc,
		cond:     cond,
	}
}

func (p *RunnableProcessor) Open(ctx context.Context) error {
	err := p.proc.Configure(ctx, p.Config.Settings)
	if err != nil {
		return cerrors.Errorf("failed configuring processor: %w", err)
	}

	err = p.proc.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed opening processor: %w", err)
	}

	return nil
}

func (p *RunnableProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	for _, inRec := range records {
		p.inInsp.Send(ctx, record.FromOpenCDC(inRec))
	}

	var outRecs []sdk.ProcessedRecord
	if p.cond == nil {
		outRecs = p.proc.Process(ctx, records)
	} else {
		// We need to first evaluate condition for each record.

		// TODO reuse these slices or at least use a pool
		// keptRecords are records that will be sent to the processor
		keptRecords := make([]opencdc.Record, 0, len(records))
		// passthroughRecordIndexes are indexes of records that are just passed
		// through to the other side.
		passthroughRecordIndexes := make([]int, 0, len(records))

		var err error

		for i, rec := range records {
			var keep bool
			keep, err = p.cond.Evaluate(rec)
			if err != nil {
				err = cerrors.Errorf("failed evaluating condition: %w", err)
				break
			}

			if keep {
				keptRecords = append(keptRecords, rec)
			} else {
				passthroughRecordIndexes = append(passthroughRecordIndexes, i)
			}
		}

		if len(keptRecords) > 0 {
			outRecs = p.proc.Process(ctx, keptRecords)
			if len(outRecs) > len(keptRecords) {
				return []sdk.ProcessedRecord{
					sdk.ErrorRecord{Error: cerrors.New("processor returned more records than input")},
				}
			}
		}
		if err != nil {
			outRecs = append(outRecs, sdk.ErrorRecord{Error: err})
		}

		// Add passthrough records back into the resultset and keep the
		// original order of the records.
		if len(passthroughRecordIndexes) == len(records) {
			// Optimization for the case where no records are kept
			outRecs = make([]sdk.ProcessedRecord, len(records))
			for i, rec := range records {
				outRecs[i] = sdk.SingleRecord(rec)
			}
		} else if len(passthroughRecordIndexes) > 0 {
			tmp := make([]sdk.ProcessedRecord, len(outRecs)+len(passthroughRecordIndexes))
			prevIndex := -1
			for i, index := range passthroughRecordIndexes {
				// TODO index-i can be out of bounds if the processor returns
				//  fewer records than the input.
				copy(tmp[prevIndex+1:index], outRecs[prevIndex-i+1:index-i])
				tmp[index] = sdk.SingleRecord(records[index])
				prevIndex = index
			}
			// if the last index is not the last record, copy the rest
			if passthroughRecordIndexes[len(passthroughRecordIndexes)-1] != len(tmp)-1 {
				copy(tmp[prevIndex+1:], outRecs[prevIndex-len(passthroughRecordIndexes)+1:])
			}
			outRecs = tmp
		}
	}

	for _, outRec := range outRecs {
		singleRec, ok := outRec.(sdk.SingleRecord)
		if ok {
			p.outInsp.Send(ctx, record.FromOpenCDC(opencdc.Record(singleRec)))
		}
	}

	return outRecs
}

func (p *RunnableProcessor) Teardown(ctx context.Context) error {
	err := p.proc.Teardown(ctx)
	p.running = false
	return err
}
