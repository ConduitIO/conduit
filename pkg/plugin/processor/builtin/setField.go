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

//go:generate paramgen -output=setField_paramgen.go setFieldConfig

package builtin

import (
	"bytes"
	"context"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type setField struct {
	referenceResolver sdk.ReferenceResolver
	tmpl              *template.Template

	sdk.UnimplementedProcessor
}

type setFieldConfig struct {
	// Field The target field, as it would be addressed in a Go template.
	Field string `json:"field" validate:"required"`
	// Value A Go template expression which will be evaluated and stored in "field".
	Value string `json:"value" validate:"required"`
}

func (p *setField) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.set",
		Summary: "Set the value of a certain field.",
		Description: `Set the value of a certain field to any value. 
Note that this processor only runs on structured data, if the record contains JSON data, then use the processor
"decode.json" to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: setFieldConfig{}.Parameters(),
	}, nil
}

func (p *setField) Configure(_ context.Context, m map[string]string) error {
	cfg := setFieldConfig{}
	inputCfg := config.Config(m).
		Sanitize().
		ApplyDefaults(cfg.Parameters())

	err := inputCfg.Validate(cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("invalid configuration: %w", err)
	}
	err = inputCfg.DecodeInto(&cfg)
	if err != nil {
		return cerrors.Errorf("failed decoding configuration: %w", err)
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

func (p *setField) Open(context.Context) error {
	return nil
}

func (p *setField) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		var b bytes.Buffer
		// evaluate the new value
		err := p.tmpl.Execute(&b, record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		ref, err := p.referenceResolver.Resolve(&record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		err = ref.Set(b.String())
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(record))
	}
	return out
}

func (p *setField) Teardown(context.Context) error {
	return nil
}
