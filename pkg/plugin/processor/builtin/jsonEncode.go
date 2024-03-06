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

//go:generate paramgen -output=jsonEncode_paramgen.go jsonEncodeConfig

package builtin

import (
	"context"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type jsonEncode struct {
	referenceResolver sdk.ReferenceResolver

	sdk.UnimplementedProcessor
}

func newJSONEncode() *jsonEncode {
	return &jsonEncode{}
}

type jsonEncodeConfig struct {
	// Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).
	// you can only encode fields that are under .Key, .Payload.Before and .Payload.After.
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*,exclusion=.Payload"`
}

func (p *jsonEncode) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "json.encode",
		Summary: "Encodes a specific field from structured data to JSON raw data (string).",
		Description: `The processor takes structured data from the target field, encodes it into JSON raw data (string)
and stores the encoded string in the target field.

This processor is only applicable to fields under ` + "`.Key`" + `, ` + "`.Payload`.Before" + ` and
` + "`.Payload.After`" + `, as they can contain structured data.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: jsonEncodeConfig{}.Parameters(),
	}, nil
}

func (p *jsonEncode) Configure(ctx context.Context, m map[string]string) error {
	cfg := jsonEncodeConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, jsonEncodeConfig{}.Parameters())
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

func (p *jsonEncode) Open(context.Context) error {
	return nil
}

func (p *jsonEncode) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec, err := p.jsonEncode(record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, rec)
	}
	return out
}

func (p *jsonEncode) Teardown(context.Context) error {
	return nil
}

func (p *jsonEncode) jsonEncode(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	ref, err := p.referenceResolver.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed to resolve the field: %w", err)
	}

	var raw []byte
	switch val := ref.Get().(type) {
	case []byte:
		raw = val
		// do we want to fail if the []byte or string is not a valid json string?
	case opencdc.RawData:
		raw = val
	case opencdc.StructuredData:
		raw = val.Bytes()
	case nil:
		return sdk.SingleRecord(rec), nil
	default:
		return nil, cerrors.Errorf("unexpected data type %T", val)
	}

	err = ref.Set(opencdc.RawData(raw))
	if err != nil {
		return nil, cerrors.Errorf("failed to set the JSON encoded value into the record: %w", err)
	}

	return sdk.SingleRecord(rec), nil
}
