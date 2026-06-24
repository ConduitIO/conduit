// Copyright © 2023 Meroxa, Inc.
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

//go:build wasm

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	sdk "github.com/conduitio/conduit-processor-sdk"
	procschema "github.com/conduitio/conduit-processor-sdk/schema"
)

func main() {
	sdk.Run(&chaosProcessor{})
}

type chaosProcessor struct {
	sdk.UnimplementedProcessor
	cfg map[string]string
}

func (p *chaosProcessor) Specification() (sdk.Specification, error) {
	param := config.Parameter{
		Default:     "success",
		Type:        config.ParameterTypeString,
		Description: "prefix",
		Validations: []config.Validation{
			config.ValidationInclusion{List: []string{"success", "error", "panic"}},
		},
	}
	return sdk.Specification{
		Name:        "chaos-processor",
		Summary:     "chaos processor summary",
		Description: "chaos processor description",
		Version:     "v1.3.5",
		Author:      "Meroxa, Inc.",
		Parameters: map[string]config.Parameter{
			"configure": param,
			"open":      param,
			"process.prefix": {
				Default:     "",
				Type:        config.ParameterTypeString,
				Description: "prefix to be added to the payload's after",
				Validations: []config.Validation{
					config.ValidationRequired{},
				},
			},
			"process":  param,
			"teardown": param,
		},
	}, nil
}

func (p *chaosProcessor) Configure(ctx context.Context, cfg config.Config) error {
	p.cfg = cfg

	return p.methodBehavior(ctx, "configure")
}

func (p *chaosProcessor) Open(ctx context.Context) error {
	return p.methodBehavior(ctx, "open")
}

func (p *chaosProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	err := p.methodBehavior(ctx, "process")
	if err != nil {
		// on error we return a single record with the error
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}

	_, ok := p.cfg["process.prefix"]
	if !ok {
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: errors.New("missing prefix")}}
	}

	out := make([]sdk.ProcessedRecord, len(records))
	for i, record := range records {
		original := record.Payload.After.(opencdc.RawData)
		record.Payload.After = opencdc.RawData(p.cfg["process.prefix"] + string(original.Bytes()))

		out[i] = sdk.SingleRecord(record)
	}

	return out
}

func (p *chaosProcessor) Teardown(ctx context.Context) error {
	return p.methodBehavior(ctx, "teardown")
}

func (p *chaosProcessor) methodBehavior(ctx context.Context, name string) error {
	switch p.cfg[name] {
	case "error":
		return errors.New("boom")
	case "panic":
		panic(name + " panic")
	case "create_and_get_schema":
		want := schema.Schema{
			Subject: "chaosProcessor",
			Version: 1,
			Type:    schema.TypeAvro,
			Bytes:   []byte("int"),
		}

		sch1, err := procschema.Create(ctx, want.Type, want.Subject, want.Bytes)
		if err != nil {
			return fmt.Errorf("failed to create schema: %w", err)
		}
		switch {
		case sch1.ID == 0:
			return fmt.Errorf("id is 0")
		case sch1.Subject != want.Subject:
			return fmt.Errorf("subjects do not match: %v != %v", sch1.Subject, want.Subject)
		case sch1.Version != want.Version:
			return fmt.Errorf("versions do not match: %v != %v", sch1.Version, want.Version)
		case sch1.Type != want.Type:
			return fmt.Errorf("types do not match: %v != %v", sch1.Type, want.Type)
		case !bytes.Equal(sch1.Bytes, want.Bytes):
			return fmt.Errorf("schemas do not match: %s != %s", sch1.Bytes, want.Bytes)
		}

		sch2, err := procschema.Get(ctx, sch1.Subject, sch1.Version)
		if err != nil {
			return fmt.Errorf("failed to get schema: %w", err)
		}
		switch {
		case sch1.ID != sch2.ID:
			return fmt.Errorf("ids do not match: %v != %v", sch1.ID, sch2.ID)
		case sch1.Subject != sch2.Subject:
			return fmt.Errorf("subjects do not match: %v != %v", sch1.Subject, sch2.Subject)
		case sch1.Version != sch2.Version:
			return fmt.Errorf("versions do not match: %v != %v", sch1.Version, sch2.Version)
		case sch1.Type != sch2.Type:
			return fmt.Errorf("types do not match: %v != %v", sch1.Type, sch2.Type)
		case !bytes.Equal(sch1.Bytes, sch2.Bytes):
			return fmt.Errorf("schemas do not match: %s != %s", sch1.Bytes, sch2.Bytes)
		}

		return nil
	case "", "success":
		return nil
	default:
		panic("unknown mode: " + p.cfg[name])
	}
}
