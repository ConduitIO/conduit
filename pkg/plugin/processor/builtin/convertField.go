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

package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type convertField struct {
	referenceResolver sdk.ReferenceResolver
	typ               string

	sdk.UnimplementedProcessor
}

func (p *convertField) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.convert",
		Summary: "Convert the type of a field.",
		Description: `Convert takes the field of one type and converts it into another type (e.g. string to integer). 
The applicable types are string, int, float and bool. Converting can be done between any combination of types. Note that
booleans will be converted to numeric values 1 (true) and 0 (false). Processor is only applicable to .Key, .Payload.Before
and .Payload.After prefixes, and only applicable if said fields are structured data.`,
		Version: "v0.1.0",
		Author:  "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"field": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "The target field, as it would be addressed in a Go template.",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					},
				},
			},
			"type": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "The target field type after conversion.",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					}, {
						Type:  sdk.ValidationTypeInclusion,
						Value: "string,int,float,bool",
					},
				},
			},
		},
	}, nil
}

func (p *convertField) Configure(_ context.Context, cfg map[string]string) error {
	field, ok := cfg["field"]
	if !ok {
		return cerrors.Errorf("%w (%q)", ErrRequiredParamMissing, "field")
	}
	if !strings.HasPrefix(field, ".Payload") && !strings.HasPrefix(field, ".Key") {
		return cerrors.Errorf("processor is only applicable to .Key and .Payload prefixes.")
	}
	typ, ok := cfg["type"]
	if !ok {
		return cerrors.Errorf("%w (%q)", ErrRequiredParamMissing, "type")
	}
	if typ != "float" && typ != "int" && typ != "bool" && typ != "string" {
		return cerrors.Errorf("invalid type %q, applicable types are string, int, float and bool", typ)
	}
	resolver, err := sdk.NewReferenceResolver(field)
	if err != nil {
		return err
	}
	p.referenceResolver = resolver
	p.typ = typ

	return nil
}

func (p *convertField) Open(context.Context) error {
	return nil
}

func (p *convertField) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		ref, err := p.referenceResolver.Resolve(&record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		newVal, err := p.stringToType(p.toString(ref.Get()), p.typ)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		err = ref.Set(newVal)
		out = append(out, sdk.SingleRecord(record))
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
		if p.typ == "int" || p.typ == "float" {
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
