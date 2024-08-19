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

//go:generate paramgen -output=exclude_paramgen.go excludeConfig

package field

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
)

type excludeProcessor struct {
	config             excludeConfig
	referenceResolvers []sdk.ReferenceResolver

	sdk.UnimplementedProcessor
}

func NewExcludeProcessor(log.CtxLogger) sdk.Processor {
	return &excludeProcessor{}
}

type excludeConfig struct {
	// Fields is a comma separated list of target fields which should be excluded.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	Fields []string `json:"fields" validate:"required"`
}

func (p *excludeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.exclude",
		Summary: "Remove a subset of fields from the record.",
		Description: `Remove a subset of fields from the record, all the other fields are left untouched.
If a field is excluded that contains nested data, the whole tree will be removed.
It is not allowed to exclude ` + "`.Position`" + ` or ` + "`.Operation`" + ` fields.

Note that this processor only runs on structured data, if the record contains
raw JSON data, then use the processor [` + "`json.decode`" + `](/docs/processors/builtin/json.decode)
to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: excludeConfig{}.Parameters(),
	}, nil
}

func (p *excludeProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, excludeConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}
	p.referenceResolvers = make([]sdk.ReferenceResolver, len(p.config.Fields))
	for i, field := range p.config.Fields {
		if field == internal.PositionReference || field == internal.OperationReference {
			return cerrors.Errorf("it is not allowed to exclude the fields %q and %q", internal.OperationReference, internal.PositionReference)
		}
		p.referenceResolvers[i], err = sdk.NewReferenceResolver(field)
		if err != nil {
			return cerrors.Errorf("invalid reference: %w", err)
		}
	}
	return nil
}

func (p *excludeProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec := record
		for i := range p.config.Fields {
			ref, err := p.referenceResolvers[i].Resolve(&rec)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
			err = ref.Delete()
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}
