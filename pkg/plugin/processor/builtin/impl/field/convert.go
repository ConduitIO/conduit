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

//go:generate paramgen -output=convert_paramgen.go convertConfig

package field

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type ConvertProcessor struct {
	referenceResolver sdk.ReferenceResolver
	config            convertConfig

	sdk.UnimplementedProcessor
}

func NewConvertProcessor(log.CtxLogger) *ConvertProcessor {
	return &ConvertProcessor{}
}

type convertConfig struct {
	// Field is the target field that should be converted.
	// Note that you can only convert fields in structured data under `.Key` and
	// `.Payload`.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*"`
	// Type is the target field type after conversion, available options are: `string`, `int`, `float`, `bool`, `time`.
	Type string `json:"type" validate:"required,inclusion=string|int|float|bool|time"`
}

func (p *ConvertProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.convert",
		Summary: "Convert the type of a field.",
		Description: `Convert takes the field of one type and converts it into another type (e.g. string to integer). 
The applicable types are string, int, float and bool. Converting can be done between any combination of types. Note that
booleans will be converted to numeric values 1 (true) and 0 (false). Processor is only applicable to ` + "`.Key`" + `, ` + "`.Payload.Before`" + `
and ` + "`.Payload.After`" + ` prefixes, and only applicable if said fields contain structured data.
If the record contains raw JSON data, then use the processor [` + "`json.decode`" + `](/docs/using/processors/builtin/json.decode)
to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: convertConfig{}.Parameters(),
	}, nil
}

func (p *ConvertProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, convertConfig{}.Parameters())
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

func (p *ConvertProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *ConvertProcessor) stringToType(value, typ string) (any, error) {
	switch typ {
	case "string":
		return value, nil
	case "int":
		return strconv.Atoi(value)
	case "float":
		return strconv.ParseFloat(value, 64)
	case "bool":
		return strconv.ParseBool(value)
	case "time":
		// see if it's a number
		unixnano, err := strconv.Atoi(value)
		if err == nil {
			// it's a number, use it as a unix nanosecond timestamp
			return time.Unix(0, int64(unixnano)).UTC(), nil
		}
		// try to parse it as a time string
		return time.Parse(time.RFC3339Nano, value)
	default:
		return nil, cerrors.Errorf("undefined type %q", typ)
	}
}

func (p *ConvertProcessor) toString(value any) string {
	switch v := value.(type) {
	case []byte:
		return string(v)
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

func (p *ConvertProcessor) boolToStringNumber(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
