// Copyright © 2025 Meroxa, Inc.
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

//go:generate paramgen -output=split_paramgen.go splitConfig

package impl

import (
	"context"
	"reflect"
	"strconv"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type SplitProcessor struct {
	sdk.UnimplementedProcessor

	referenceResolver sdk.ReferenceResolver
	config            splitConfig
}

func NewSplitProcessor(log.CtxLogger) *SplitProcessor { return &SplitProcessor{} }

type splitConfig struct {
	// Field is the target field that should be split. Note that the target
	// field has to contain an array so it can be split, otherwise the processor
	// returns an error. This also means you can only split on fields in
	// structured data under `.Key` and `.Payload`.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*"`
}

func (p *SplitProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "split",
		Summary: "Split records.",
		Description: `Split the records into multiple records based on the field
provided in the configuration. If the field is an array, each element of the
array will be converted into a separate record. The new record will contain the
same data as the original record, but the field specified in the configuration
will be replaced with the element of the array. The index of the element in the
array will be stored in the metadata of the new record under the key
` + "`split.index`" + `.

This processor is only applicable to ` + "`.Key`" + `, ` + "`.Payload.Before`" + `
and ` + "`.Payload.After`" + ` prefixes, and only applicable if said fields
contain structured data. If the record contains raw JSON data, then use the
processor [` + "`json.decode`" + `](/docs/using/processors/builtin/json.decode)
to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: splitConfig{}.Parameters(),
	}, nil
}

func (p *SplitProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, splitConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	resolver, err := sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param: %w", "field", err)
	}
	p.referenceResolver = resolver
	return nil
}

func (p *SplitProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		ref, err := p.referenceResolver.Resolve(&rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}

		val := reflect.ValueOf(ref.Get())
		if val.Kind() != reflect.Slice {
			return append(out, sdk.ErrorRecord{
				Error: cerrors.Errorf("field %q is not a slice", p.config.Field),
			})
		}

		mr := make(sdk.MultiRecord, val.Len())
		for i := 0; i < val.Len(); i++ {
			newRec := rec.Clone()
			newRef, _ := p.referenceResolver.Resolve(&newRec)
			item := val.Index(i)

			if err := newRef.Set(item.Interface()); err != nil {
				return append(out, sdk.ErrorRecord{Error: cerrors.Errorf("failed to set the item: %w", err)})
			}
			if newRec.Metadata == nil {
				newRec.Metadata = make(map[string]string)
			}
			newRec.Metadata["split.index"] = strconv.Itoa(i)

			mr[i] = newRec
		}
		out = append(out, mr)
	}
	return out
}
