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
	"bytes"
	"context"
	"io"
	"strconv"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
)

type DecodeProcessor struct {
	sdk.UnimplementedProcessor

	referenceResolver sdk.ReferenceResolver
}

func NewDecodeProcessor(log.CtxLogger) *DecodeProcessor {
	return &DecodeProcessor{}
}

type decodeConfig struct {
	// Field is a reference to the target field. Only fields that are under
	// `.Key` and `.Payload` can be decoded.
	//
	// For more information about the format, see [Referencing fields](https://conduitdata.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*,exclusion=.Payload"`
}

func (p *DecodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "json.decode",
		Summary: "Decodes a specific field from JSON raw data (string) to structured data.",
		Description: `The processor takes JSON raw data (` + "`string`" + ` or ` + "`[]byte`" + `)
from the target field, parses it as JSON structured data and stores the decoded
structured data in the target field.

JSON numbers are decoded without loss of precision: an integer that fits in
` + "`int64`" + ` becomes an ` + "`int64`" + `, one that fits in ` + "`uint64`" + ` becomes a ` + "`uint64`" + `, and
any other number becomes a ` + "`float64`" + `. Earlier versions decoded every number to
` + "`float64`" + `, which rounds integers above 2^53.

This processor is only applicable to fields under ` + "`.Key`" + `, ` + "`.Payload`.Before" + ` and
` + "`.Payload.After`" + `, as they can contain structured data.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: decodeConfig{}.Parameters(),
	}, nil
}

func (p *DecodeProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := decodeConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, decodeConfig{}.Parameters())
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

func (p *DecodeProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *DecodeProcessor) setJSONData(raw []byte, ref sdk.Reference) error {
	if len(raw) == 0 {
		// value is an empty json
		return ref.Set(nil)
	}

	// Invariant 6: decode with UseNumber so a JSON integer above 2^53 is not
	// silently rounded through a float64. The numbers are then narrowed below,
	// because downstream processors type-switch on int64 and float64.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var jsonData any
	if err := dec.Decode(&jsonData); err != nil {
		return cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
	}
	// Decode reads a single value and stops; reject trailing data exactly as
	// json.Unmarshal would.
	if _, err := dec.Token(); !cerrors.Is(err, io.EOF) {
		if err == nil {
			err = cerrors.New("invalid character after top-level value")
		}
		return cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
	}

	jsonData, err := decodeNumbers(jsonData)
	if err != nil {
		return cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
	}
	return ref.Set(jsonData)
}

// decodeNumbers converts the json.Number values produced by Decoder.UseNumber
// into concrete Go types, recursively, so that no json.Number reaches a
// downstream processor (which type-switches on int64 and float64).
func decodeNumbers(v any) (any, error) {
	switch v := v.(type) {
	case json.Number:
		return decodeJSONNumber(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			nv, err := decodeNumbers(val)
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			nv, err := decodeNumbers(val)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	default:
		return v, nil
	}
}

// decodeJSONNumber narrows a json.Number to the smallest lossless Go type: an
// integer that fits in int64 becomes int64, an integer that fits in uint64
// becomes uint64, and any other number becomes float64, as before the
// UseNumber change. An integer that fits in neither int64 nor uint64 falls
// back to float64, matching the pre-change behavior.
func decodeJSONNumber(n json.Number) (any, error) {
	if i, err := n.Int64(); err == nil {
		return i, nil
	}
	if u, err := strconv.ParseUint(string(n), 10, 64); err == nil {
		return u, nil
	}
	f, err := n.Float64()
	if err != nil {
		// strconv.NumError repeats the number; keep only its cause.
		var ne *strconv.NumError
		if cerrors.As(err, &ne) {
			err = ne.Err
		}
		return nil, cerrors.Errorf("a %d byte number is outside the float64 range: %w", len(n), err)
	}
	return f, nil
}
