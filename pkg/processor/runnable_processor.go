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

	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		keep, err := p.evaluateCondition(rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: cerrors.Errorf("failed evaluating condition: %w", err)})
		}
		if !keep {
			out = append(out, sdk.FilterRecord{})
			continue
		}

		proc := p.proc.Process(ctx, records)
		// todo check if processor returned more than 1 result
		singleRec, ok := proc[0].(sdk.SingleRecord)
		if ok {
			p.outInsp.Send(ctx, record.FromOpenCDC(opencdc.Record(singleRec)))
		}
		out = append(out, proc[0])
	}

	return out
}

func (p *RunnableProcessor) Teardown(ctx context.Context) error {
	err := p.proc.Teardown(ctx)
	p.running = false
	return err
}

func (p *RunnableProcessor) evaluateCondition(rec opencdc.Record) (bool, error) {
	if p.cond == nil {
		return true, nil
	}

	return p.cond.Evaluate(rec)
}
