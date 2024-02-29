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

//go:generate paramgen -output=convertField_paramgen.go convertFieldConfig

package builtin

import (
	"context"
	"fmt"
	"strconv"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type convertField struct {
	referenceResolver sdk.ReferenceResolver
	config            convertFieldConfig

	sdk.UnimplementedProcessor
}

func newConvertField() *convertField {
	return &convertField{}
}

type convertFieldConfig struct {
	// Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).
	// you can only convert fields that are under .Key and .Payload, and said fields should contain structured data.
	Field string `json:"field" validate:"required,regex=^\.(Payload|Key).*"`
	// Type is the target field type after conversion, available options are: string, int, float, bool.
	Type string `json:"type" validate:"required,inclusion=string|int|float|bool"`
}

func (p *convertField) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.convert",
		Summary: "Convert the type of a field.",
		Description: `Convert takes the field of one type and converts it into another type (e.g. string to integer). 
The applicable types are string, int, float and bool. Converting can be done between any combination of types. Note that
booleans will be converted to numeric values 1 (true) and 0 (false). Processor is only applicable to .Key, .Payload.Before
and .Payload.After prefixes, and only applicable if said fields contain structured data.
if the record contains raw JSON data, then use the processor "decode.json" to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: convertFieldConfig{}.Parameters(),
	}, nil
}

func (p *convertField) Configure(ctx context.Context, m map[string]string) error {
	err := sdk.ParseConfig(ctx, m, &p.config, convertFieldConfig{}.Parameters())
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

func (p *convertField) Open(context.Context) error {
	return nil
}

func (p *convertField) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec := record
		ref, err := p.referenceResolver.Resolve(&rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		newVal, err := p.stringToType(p.toString(ref.Get()), p.config.Type)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		err = ref.Set(newVal)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

func (p *convertField) stringToType(value, typ string) (any, error) {
	switch typ {
	case "string":
		return value, nil
	case "int":
		newVal, err := strconv.Atoi(value)
		if err != nil {
			return nil, err
		}
		return newVal, nil
	case "float":
		newVal, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, err
		}
		return newVal, nil
	case "bool":
		newVal, err := strconv.ParseBool(value)
		if err != nil {
			return nil, err
		}
		return newVal, nil
	default:
		return nil, cerrors.Errorf("undefined type %q", typ)
	}
}

func (p *convertField) toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if p.config.Type == "int" || p.config.Type == "float" {
			return p.boolToStringNumber(v)
		}
		return strconv.FormatBool(v)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func (p *convertField) boolToStringNumber(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (p *convertField) Teardown(context.Context) error {
	return nil
}
