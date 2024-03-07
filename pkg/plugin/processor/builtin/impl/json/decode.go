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

//go:generate paramgen -output=decode_paramgen.go decodeConfig

package json

import (
	"context"
	"encoding/json"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type decodeProcessor struct {
	sdk.UnimplementedProcessor

	referenceResolver sdk.ReferenceResolver
}

func NewDecodeProcessor(log.CtxLogger) sdk.Processor {
	return &decodeProcessor{}
}

type decodeConfig struct {
	// Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).
	// you can only decode fields that are under .Key and .Payload.
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*,exclusion=.Payload"`
}

func (p *decodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "json.decode",
		Summary: "Decodes a specific field from JSON raw data (string) to structured data.",
		Description: `The processor takes JSON raw data (` + "`string`" + ` or ` + "`[]byte`" + `)
from the target field, parses it as JSON structured data and stores the decoded
structured data in the target field.

This processor is only applicable to fields under ` + "`.Key`" + `, ` + "`.Payload`.Before" + ` and
` + "`.Payload.After`" + `, as they can contain structured data.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: decodeConfig{}.Parameters(),
	}, nil
}

func (p *decodeProcessor) Configure(ctx context.Context, m map[string]string) error {
	cfg := decodeConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, decodeConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}
	resolver, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf(`failed to parse the "field" parameter: %w`, err)
	}
	p.referenceResolver = resolver
	return nil
}

func (p *decodeProcessor) Open(context.Context) error {
	return nil
}

func (p *decodeProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec := record
		ref, err := p.referenceResolver.Resolve(&rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}

		data := ref.Get()
		switch d := data.(type) {
		case opencdc.RawData:
			bytes := d.Bytes()
			err := p.setJSONData(bytes, ref)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
		case string:
			bytes := []byte(d)
			err := p.setJSONData(bytes, ref)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
		case []byte:
			err := p.setJSONData(d, ref)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
		case opencdc.StructuredData, map[string]any:
			// data is already structured
		case nil:
			// if the field is nil leave it as it is
		default:
			return append(out, sdk.ErrorRecord{Error: cerrors.Errorf("unexpected data type %T", data)})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

func (p *decodeProcessor) Teardown(context.Context) error {
	return nil
}

func (p *decodeProcessor) setJSONData(bytes []byte, ref sdk.Reference) error {
	if len(bytes) == 0 {
		// value is an empty json
		return ref.Set(nil)
	}
	var jsonData any
	err := json.Unmarshal(bytes, &jsonData)
	if err != nil {
		return cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
	}
	return ref.Set(jsonData)
}
