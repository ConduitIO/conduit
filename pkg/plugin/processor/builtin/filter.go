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

package builtin

import (
	"context"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

type Filter struct {
	sdk.UnimplementedProcessor
}

func (p *Filter) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "filter",
		Summary:     "acknowledges all records that get passed to the filter",
		Description: "acknowledges all records that get passed to the filter",
		Version:     "v1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  map[string]sdk.Parameter{},
	}, nil
}

func (p *Filter) Configure(_ context.Context, _ map[string]string) error {
	return nil
}

func (p *Filter) Open(context.Context) error {
	return nil
}

func (p *Filter) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i := range records {
		out[i] = sdk.FilterRecord{}
	}
	return out
}

func (p *Filter) Teardown(context.Context) error {
	return nil
}
