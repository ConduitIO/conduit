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
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type excludeFields struct {
	sdk.UnimplementedProcessor

	// cfg
	fields []string
}

func (p *excludeFields) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name: "field.subset.exclude",
		Summary: "remove a subset of fields from the record, all other fields are left untouched. If a field is " +
			"excluded that contains nested data, the whole tree will be removed. It is not allowed to exclude .Position or .Operation.",
		Description: "remove a subset of fields from the record",
		Version:     "v1.0",
		Author:      "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"fields": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "fields to be excluded from the record",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					},
				},
			},
		},
	}, nil
}

func (p *excludeFields) Configure(_ context.Context, cfg map[string]string) error {
	fields, ok := cfg["fields"]
	if !ok {
		return cerrors.Errorf("%w (%q)", ErrRequiredParamMissing, "fields")
	}
	p.fields = strings.Split(fields, ",")
	for _, field := range p.fields {
		if field == ".Position" || field == ".Operation" {
			return cerrors.Errorf("it is not allowed to exclude the fields %q and %q", ".Operation", ".Position")
		}
	}

	return nil
}

func (p *excludeFields) Open(context.Context) error {
	return nil
}

func (p *excludeFields) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, record := range records {
		for _, field := range p.fields {
			resolver, err := sdk.NewReferenceResolver(field)
			if err != nil {
				return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
			}
			ref, err := resolver.Resolve(&record)
			if err != nil {
				return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
			}
			err = ref.Delete()
			if err != nil {
				return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
			}
		}
		out[i] = sdk.SingleRecord(record)
	}
	return out
}

func (p *excludeFields) Teardown(context.Context) error {
	return nil
}
