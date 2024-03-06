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

//go:generate paramgen -output=encode_paramgen.go encodeConfig

package json

import (
	"context"
	"encoding/json"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type encodeProcessor struct {
	referenceResolver sdk.ReferenceResolver

	sdk.UnimplementedProcessor
}

func newEncodeProcessor(log.CtxLogger) sdk.Processor {
	return &encodeProcessor{}
}

type encodeConfig struct {
	// Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).
	// you can only encode fields that are under .Key, .Payload.Before and .Payload.After.
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*,exclusion=.Payload"`
}

func (p *encodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "json.encode",
		Summary: "Encodes a specific field from structured data to JSON raw data (string).",
		Description: `The processor takes data from the target field, encodes it into s JSON value
and stores the encoded value in the target field.

This processor is only applicable to fields under ` + "`.Key`" + `, ` + "`.Payload`.Before" + ` and
` + "`.Payload.After`" + `, as they can contain structured data.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: encodeConfig{}.Parameters(),
	}, nil
}

func (p *encodeProcessor) Configure(ctx context.Context, m map[string]string) error {
	cfg := encodeConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, encodeConfig{}.Parameters())
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

func (p *encodeProcessor) Open(context.Context) error {
	return nil
}

func (p *encodeProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec, err := p.encode(record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, rec)
	}
	return out
}

func (p *encodeProcessor) Teardown(context.Context) error {
	return nil
}

func (p *encodeProcessor) encode(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	ref, err := p.referenceResolver.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed to resolve the field: %w", err)
	}
	value, err := json.Marshal(ref.Get())
	if err != nil {
		return nil, err
	}
	err = ref.Set(value)
	if err != nil {
		return nil, cerrors.Errorf("failed to set the JSON encoded value into the record: %w", err)
	}
	return sdk.SingleRecord(rec), nil
}
