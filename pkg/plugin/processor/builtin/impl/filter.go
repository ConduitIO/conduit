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

package impl

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type filterProcessor struct {
	sdk.UnimplementedProcessor
}

func NewFilterProcessor(log.CtxLogger) sdk.Processor {
	return &filterProcessor{}
}

func (p *filterProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "filter",
		Summary: "Acknowledges all records that get passed to the filter.",
		Description: `Acknowledges all records that get passed to the filter, so
the records will be filtered out if the condition provided to the processor is
evaluated to ` + "`true`" + `.

**Important:** Make sure to add a [condition](https://conduit.io/docs/processors/conditions)
to this processor, otherwise all records will be filtered out.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: map[string]config.Parameter{},
	}, nil
}

func (p *filterProcessor) Configure(_ context.Context, _ map[string]string) error {
	return nil
}

func (p *filterProcessor) Open(context.Context) error {
	return nil
}

func (p *filterProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i := range records {
		out[i] = sdk.FilterRecord{}
	}
	return out
}

func (p *filterProcessor) Teardown(context.Context) error {
	return nil
}
