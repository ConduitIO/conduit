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

package base64

import (
	"context"
	"encoding/base64"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type decodeProcessor struct {
	sdk.UnimplementedProcessor

	config            decodeConfig
	referenceResolver sdk.ReferenceResolver
}

type decodeConfig struct {
	// Field is the reference to the target field. Note that it is not allowed to
	// base64 decode the `.Position` field.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	Field string `json:"field" validate:"required,exclusion=.Position"`
}

func NewDecodeProcessor(log.CtxLogger) sdk.Processor {
	return &decodeProcessor{}
}

func (p *decodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "base64.decode",
		Summary: "Decode a field to base64.",
		Description: `The processor will decode the value of the target field from base64 and store the
result in the target field. It is not allowed to decode the ` + "`.Position`" + ` field.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: decodeConfig{}.Parameters(),
	}, nil
}

func (p *decodeProcessor) Configure(ctx context.Context, c config.Config) error {
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

func (p *decodeProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		rec, err := p.base64Decode(rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, rec)
	}
	return out
}

func (p *decodeProcessor) base64Decode(rec opencdc.Record) (sdk.ProcessedRecord, error) {
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
	case nil:
		return sdk.SingleRecord(rec), nil
	default:
		return nil, cerrors.Errorf("unexpected data type %T", val)
	}

	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(raw)))
	n, err := base64.StdEncoding.Decode(decoded, raw)
	if err != nil {
		return nil, cerrors.Errorf("failed to decode the value: %w", err)
	}

	err = ref.Set(string(decoded[:n]))
	if err != nil {
		return nil, cerrors.Errorf("failed to set the decoded value into the record: %w", err)
	}

	return sdk.SingleRecord(rec), nil
}
