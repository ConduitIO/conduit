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

//go:generate paramgen -output=set_paramgen.go setConfig

package field

import (
	"bytes"
	"context"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type SetProcessor struct {
	referenceResolver sdk.ReferenceResolver
	tmpl              *template.Template

	sdk.UnimplementedProcessor
}

func NewSetProcessor(log.CtxLogger) *SetProcessor {
	return &SetProcessor{}
}

type setConfig struct {
	// Field is the target field that will be set. Note that it is not allowed
	// to set the `.Position` field.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"required,exclusion=.Position"`
	// Value is a Go template expression which will be evaluated and stored in `field` (e.g. `{{ .Payload.After }}`).
	Value string `json:"value" validate:"required"`
}

func (p *SetProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.set",
		Summary: "Set the value of a certain field.",
		Description: `Set the value of a certain field to any value. It is not allowed to set the ` + "`.Position`" + ` field.
The new value can be a Go template expression, the processor will evaluate the output and assign the value to the target field.
If the provided ` + "`field`" + ` doesn't exist, the processor will create that field and assign its value.
This processor can be used for multiple purposes, like extracting fields, hoisting data, inserting fields, copying fields, masking fields, etc.
Note that this processor only runs on structured data, if the record contains raw JSON data, then use the processor
[` + "`json.decode`" + `](/docs/using/processors/builtin/json.decode) to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: setConfig{}.Parameters(),
	}, nil
}

func (p *SetProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := setConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, setConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	tmpl, err := template.New("").Funcs(sprig.FuncMap()).Parse(cfg.Value)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param template: %w", "value", err)
	}
	p.tmpl = tmpl
	resolver, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param: %w", "field", err)
	}
	p.referenceResolver = resolver
	return nil
}

func (p *SetProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec := record
		var b bytes.Buffer
		// evaluate the new value
		err := p.tmpl.Execute(&b, rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		ref, err := p.referenceResolver.Resolve(&rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		err = ref.Set(b.String())
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}
