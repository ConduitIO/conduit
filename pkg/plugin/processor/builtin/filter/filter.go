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

package filter

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

type processor struct {
	sdk.UnimplementedProcessor
}

func New() sdk.Processor {
	return &processor{}
}

func (p *processor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "filter",
		Summary: "Acknowledges all records that get passed to the filter.",
		Description: `Acknowledges all records that get passed to the filter, so the records will be filtered out if 
the condition provided to the processor is evaluated to "true".
Make sure to add a condition to this processor, otherwise all records will be filtered out."`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: map[string]config.Parameter{},
	}, nil
}

func (p *processor) Configure(_ context.Context, _ map[string]string) error {
	return nil
}

func (p *processor) Open(context.Context) error {
	return nil
}

func (p *processor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i := range records {
		out[i] = sdk.FilterRecord{}
	}
	return out
}

func (p *processor) Teardown(context.Context) error {
	return nil
}
