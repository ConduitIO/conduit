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

//go:generate paramgen -output=jsonDecode_paramgen.go jsonDecodeConfig

package builtin

import (
	"context"
	"encoding/json"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type jsonDecode struct {
	referenceResolver sdk.ReferenceResolver

	sdk.UnimplementedProcessor
}

func newJSONDecode() *jsonDecode {
	return &jsonDecode{}
}

type jsonDecodeConfig struct {
	// Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).
	// you can only decode fields that are under .Key and .Payload.
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*"`
}

func (p *jsonDecode) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "json.decode",
		Summary: "Decodes a specific field from JSON raw data (string) to structured data.",
		Description: `The processor takes JSON raw data (string) from the target field, parses it as JSON structured data
and stores the decoded structured data in the target field.
This processor is only applicable to fields under .Key, .Payload.Before and .Payload.After, as they accept structured data format.
`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: jsonDecodeConfig{}.Parameters(),
	}, nil
}

func (p *jsonDecode) Configure(ctx context.Context, m map[string]string) error {
	cfg := jsonDecodeConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, jsonDecodeConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}
	resolver, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param: %w", "field", err)
	}
	p.referenceResolver = resolver
	return nil
}

func (p *jsonDecode) Open(context.Context) error {
	return nil
}

func (p *jsonDecode) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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
		case opencdc.StructuredData:
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

func (p *jsonDecode) Teardown(context.Context) error {
	return nil
}

func (p *jsonDecode) setJSONData(bytes []byte, ref sdk.Reference) error {
	var jsonData opencdc.StructuredData
	if len(bytes) == 0 {
		// value is an empty json
		return ref.Set(jsonData)
	}
	err := json.Unmarshal(bytes, &jsonData)
	if err != nil {
		return cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
	}
	return ref.Set(jsonData)
}
