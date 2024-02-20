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

//go:generate paramgen -output=excludeField_paramgen.go excludeFieldConfig

package builtin

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type excludeField struct {
	config excludeFieldConfig

	sdk.UnimplementedProcessor
}

type excludeFieldConfig struct {
	// Fields to be excluded from the record, as it would be addressed in a Go template, as a comma separated list.
	Fields []string `json:"fields" validate:"required"`
}

func (p *excludeField) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name: "field.subset.exclude",
		Summary: `Remove a subset of fields from the record, all the other fields are left untouched. 
If a field is excluded that contains nested data, the whole tree will be removed.  
It is not allowed to exclude .Position or .Operation fields.
Note that this processor only runs on structured data, if the record contains JSON data, then use the processor "decode.json" to parse it into structured data first.`,
		Description: "Remove a subset of fields from the record.",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  excludeFieldConfig{}.Parameters(),
	}, nil
}

func (p *excludeField) Configure(_ context.Context, m map[string]string) error {
	cfg := excludeFieldConfig{}
	inputCfg := config.Config(m).
		Sanitize().
		ApplyDefaults(cfg.Parameters())

	err := inputCfg.Validate(cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("invalid configuration: %w", err)
	}
	err = inputCfg.DecodeInto(&cfg)
	if err != nil {
		return cerrors.Errorf("failed decoding configuration: %w", err)
	}

	for _, field := range cfg.Fields {
		if field == PositionReference || field == OperationReference {
			return cerrors.Errorf("it is not allowed to exclude the fields %q and %q", OperationReference, PositionReference)
		}
	}
	p.config = cfg
	return nil
}

func (p *excludeField) Open(context.Context) error {
	return nil
}

func (p *excludeField) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		for _, field := range p.config.Fields {
			resolver, err := sdk.NewReferenceResolver(field)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
			ref, err := resolver.Resolve(&record)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
			err = ref.Delete()
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
		}
		out = append(out, sdk.SingleRecord(record))
	}
	return out
}

func (p *excludeField) Teardown(context.Context) error {
	return nil
}
