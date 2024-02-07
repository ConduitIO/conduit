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

type RunnableProcessor struct {
	*Instance
	proc sdk.Processor
}

func newRunnableProcessor(
	proc sdk.Processor,
	i *Instance,
) *RunnableProcessor {
	return &RunnableProcessor{
		Instance: i,
		proc:     proc,
	}
}

func (p *RunnableProcessor) Open(ctx context.Context) error {
	err := p.proc.Configure(ctx, p.Config.Settings)
	if err != nil {
		return cerrors.Errorf("failed configuring processor: %w", err)
	}

	err = p.proc.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed opening processors: %w", err)
	}

	return nil
}

func (p *RunnableProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	for _, inRec := range records {
		p.inInsp.Send(ctx, record.FromOpenCDC(inRec))
	}

	outRecs := p.proc.Process(ctx, records)
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
