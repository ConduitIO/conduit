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
	"context"
	"errors"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
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

func (p *chaosProcessor) Configure(_ context.Context, cfg map[string]string) error {
	p.cfg = cfg

	err := p.methodBehavior("configure")
	if err != nil {
		return err
	}

	return nil
}

func (p *chaosProcessor) Open(context.Context) error {
	return p.methodBehavior("open")
}

func (p *chaosProcessor) methodBehavior(name string) error {
	switch p.cfg[name] {
	case "error":
		return errors.New("boom")
	case "panic":
		panic(name + " panic")
	case "", "success":
		return nil
	default:
		panic("unknown mode: " + p.cfg[name])
	}
}

func (p *chaosProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	err := p.methodBehavior("process")
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

func (p *chaosProcessor) Teardown(context.Context) error {
	return p.methodBehavior("teardown")
}
