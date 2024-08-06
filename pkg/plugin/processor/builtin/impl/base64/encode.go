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

package base64

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type encodeProcessor struct {
	sdk.UnimplementedProcessor

	config            encodeConfig
	referenceResolver sdk.ReferenceResolver
}

type encodeConfig struct {
	// Field is a reference to the target field. Note that it is not allowed to
	// base64 encode the `.Position` field.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	Field string `json:"field" validate:"required,exclusion=.Position"`
}

func NewEncodeProcessor(log.CtxLogger) sdk.Processor {
	return &encodeProcessor{}
}

func (p *encodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "base64.encode",
		Summary: "Encode a field to base64.",
		Description: `The processor will encode the value of the target field to base64 and store the
result in the target field. It is not allowed to encode the ` + "`.Position`" + ` field.
If the provided field doesn't exist, the processor will create that field and
assign its value.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: encodeConfig{}.Parameters(),
	}, nil
}

func (p *encodeProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, p.config.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	resolver, err := sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return cerrors.Errorf(`failed to parse the "field" parameter: %w`, err)
	}
	p.referenceResolver = resolver

	return nil
}

func (p *encodeProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		rec, err := p.base64Encode(rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, rec)
	}
	return out
}

func (p *encodeProcessor) base64Encode(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	ref, err := p.referenceResolver.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed to resolve the field: %w", err)
	}

	var raw []byte
	switch val := ref.Get().(type) {
	case []byte:
		raw = val
	case opencdc.RawData:
		raw = val
	case string:
		raw = []byte(val)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		raw = []byte(fmt.Sprintf("%v", val))
	case nil:
		return sdk.SingleRecord(rec), nil
	default:
		return nil, cerrors.Errorf("unexpected data type %T", val)
	}

	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(encoded, raw)

	err = ref.Set(string(encoded))
	if err != nil {
		return nil, cerrors.Errorf("failed to set the encoded value into the record: %w", err)
	}

	return sdk.SingleRecord(rec), nil
}
