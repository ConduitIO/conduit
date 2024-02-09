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
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type renameField struct {
	sdk.UnimplementedProcessor

	// cfg
	mapping map[string]string
}

func (p *renameField) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.rename",
		Summary: "rename a group of fields",
		Description: "rename a group of fields. It is not allowed to rename top-level fields (.Operation, " +
			".Position, .Key, .Metadata, .Payload.Before, .Payload.After)",
		Version: "v1.0",
		Author:  "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"mapping": {
				Default: "",
				Type:    sdk.ParameterTypeString,
				Description: "a comma separated list of keys and values, for fields and their new names, " +
					"ex: .Metadata.key:id,.Payload.After.foo:bar",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					},
				},
			},
		},
	}, nil
}

func (p *renameField) Configure(_ context.Context, cfg map[string]string) error {
	list, ok := cfg["mapping"]
	if !ok || list == "" {
		return cerrors.Errorf("%w (%q)", ErrRequiredParamMissing, "mapping")
	}

	result := make(map[string]string)
	pairs := strings.Split(list, ",")
	for _, pair := range pairs {
		parts := strings.Split(pair, ":")
		if len(parts) != 2 {
			return cerrors.Errorf("wrong format for the %q param, should be a comma separated list of keys and values,"+
				"ex: .Metadata.key:id,.Payload.After.foo:bar", "mapping")
		}
		key := strings.TrimSpace(parts[0])
		if key == OperationReference || key == KeyReference || key == PayloadReference || key ==
			PayloadBeforeReference || key == PayloadAfterReference || key == PositionReference || key == MetadataReference {
			return cerrors.Errorf("cannot rename one of the top-level fields %q", key)
		}
		value := strings.TrimSpace(parts[1])
		result[key] = value
	}
	p.mapping = result

	return nil
}

func (p *renameField) Open(context.Context) error {
	return nil
}

func (p *renameField) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, record := range records {
		for key, val := range p.mapping {
			err := p.rename(record, key, val)
			if err != nil {
				return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
			}
		}
		out[i] = sdk.SingleRecord(record)
	}
	return out
}

func (p *renameField) rename(record opencdc.Record, key, val string) error {
	resolver1, err := sdk.NewReferenceResolver(key)
	if err != nil {
		return err
	}
	ref1, err := resolver1.Resolve(&record)
	if err != nil {
		return err
	}
	resolver2, err := sdk.NewReferenceResolver(p.getNameWithPrefix(val, key))
	if err != nil {
		return err
	}
	// create a second reference to the new name
	ref2, err := resolver2.Resolve(&record)
	if err != nil {
		return err
	}
	// copy the value over to the new name
	err = ref2.Set(ref1.Get())
	if err != nil {
		return err
	}
	// delete the old name field
	err = ref1.Delete()
	if err != nil {
		return err
	}
	return nil
}

func (p *renameField) getNameWithPrefix(newName, oldName string) string {
	// split the oldName by dots
	parts := strings.Split(oldName, ".")

	// replace the last value with the new name
	parts[len(parts)-1] = newName
	// len(parts) is always 2 or more, because top-level renames are not allowed (would've failed before).

	return strings.Join(parts, ".")
}

func (p *renameField) Teardown(context.Context) error {
	return nil
}
