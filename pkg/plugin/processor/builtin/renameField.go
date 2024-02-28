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

//go:generate paramgen -output=renameField_paramgen.go renameFieldConfig

package builtin

import (
	"context"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"golang.org/x/exp/slices"
)

type renameField struct {
	newNames           []string
	referenceResolvers []sdk.ReferenceResolver

	sdk.UnimplementedProcessor
}

func newRenameField() *renameField {
	return &renameField{}
}

var forbiddenFields = []string{MetadataReference, PayloadReference, PayloadBeforeReference, PayloadAfterReference,
	PositionReference, KeyReference, OperationReference}

type renameFieldConfig struct {
	// Mapping is a comma separated list of keys and values for fields and their new names (keys and values
	// are separated by colons ":"). For example: `.Metadata.key:id,.Payload.After.foo:bar`.
	Mapping []string `json:"mapping" validate:"required"`
}

func (p *renameField) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.rename",
		Summary: "Rename a group of fields.",
		Description: `Rename a group of field names to new names. It is not allowed to rename top-level fields (.Operation, .Position, 
.Key, .Metadata, .Payload.Before, .Payload.After).
Note that this processor only runs on structured data, if the record contains raw JSON data, then use the processor "decode.json" to parse it into structured data first.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: renameFieldConfig{}.Parameters(),
	}, nil
}

func (p *renameField) Configure(ctx context.Context, m map[string]string) error {
	cfg := renameFieldConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, renameFieldConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configurations: %w", err)
	}
	p.referenceResolvers = make([]sdk.ReferenceResolver, len(cfg.Mapping))
	p.newNames = make([]string, len(cfg.Mapping))
	for i, pair := range cfg.Mapping {
		parts := strings.Split(pair, ":")
		if len(parts) != 2 {
			return cerrors.Errorf("wrong format for the %q param, should be a comma separated list of keys and values,"+
				"ex: .Metadata.key:id,.Payload.After.foo:bar", "mapping")
		}

		key := strings.TrimSpace(parts[0])
		if slices.Contains(forbiddenFields, key) {
			return cerrors.Errorf("cannot rename one of the top-level fields %q", key)
		}
		p.referenceResolvers[i], err = sdk.NewReferenceResolver(key)
		if err != nil {
			return cerrors.Errorf("invalid reference: %w", err)
		}

		value := strings.TrimSpace(parts[1])
		if len(value) == 0 {
			return cerrors.Errorf("cannot rename the key %q to an empty string", key)
		}
		p.newNames[i] = value
	}

	return nil
}

func (p *renameField) Open(context.Context) error {
	return nil
}

func (p *renameField) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		for i, newName := range p.newNames {
			ref, err := p.referenceResolvers[i].Resolve(&record)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
			_, err = ref.Rename(newName)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
		}
		out = append(out, sdk.SingleRecord(record))
	}
	return out
}

func (p *renameField) Teardown(context.Context) error {
	return nil
}
