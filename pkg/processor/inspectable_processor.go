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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/record"
)

// inspectableProcessor decorates a sdk.Processor with inspection methods.
type inspectableProcessor struct {
	sdk.UnimplementedProcessor

	proc    sdk.Processor
	inInsp  *inspector.Inspector
	outInsp *inspector.Inspector
}

func newInspectableProcessor(proc sdk.Processor, logger log.CtxLogger) *inspectableProcessor {
	return &inspectableProcessor{
		proc:    proc,
		inInsp:  inspector.New(logger, inspector.DefaultBufferSize),
		outInsp: inspector.New(logger, inspector.DefaultBufferSize),
	}
}

func (p *inspectableProcessor) Specification() (sdk.Specification, error) {
	return p.proc.Specification()
}

func (p *inspectableProcessor) Configure(ctx context.Context, cfgMap map[string]string) error {
	return p.proc.Configure(ctx, cfgMap)
}

func (p *inspectableProcessor) Open(ctx context.Context) error {
	return p.proc.Open(ctx)
}

func (p *inspectableProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *inspectableProcessor) Teardown(ctx context.Context) error {
	p.inInsp.Close()
	p.outInsp.Close()
	return p.proc.Teardown(ctx)
}

func (p *inspectableProcessor) InspectIn(ctx context.Context, id string) *inspector.Session {
	return p.inInsp.NewSession(ctx, id)
}

func (p *inspectableProcessor) InspectOut(ctx context.Context, id string) *inspector.Session {
	return p.outInsp.NewSession(ctx, id)
}
