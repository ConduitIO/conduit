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

package main

import (
	"context"
	"errors"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/run"
)

func main() {
	run.Run(&testProcessor{})
}

type testProcessor struct {
	cfg map[string]string
}

func (t *testProcessor) Specification() (sdk.Specification, error) {
	param := sdk.Parameter{
		Default:     "success",
		Type:        sdk.ParameterTypeString,
		Description: "prefix",
		Validations: []sdk.Validation{
			{
				Type:  sdk.ValidationTypeInclusion,
				Value: "success,error,panic",
			},
		},
	}
	return sdk.Specification{
		Name:        "full-processor",
		Summary:     "full processor summary",
		Description: "full processor description",
		Version:     "v1.3.5",
		Author:      "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"configure": param,
			"open":      param,
			"process.prefix": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "prefix to be added to the payload's after",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					},
				},
			},
			"process":  param,
			"teardown": param,
		},
	}, nil
}

func (t *testProcessor) Configure(_ context.Context, cfg map[string]string) error {
	t.cfg = cfg

	err := t.methodBehavior("configure")
	if err != nil {
		return err
	}

	_, ok := cfg["process.prefix"]
	if !ok {
		return errors.New("missing prefix")
	}

	return nil
}

func (t *testProcessor) Open(ctx context.Context) error {
	return t.methodBehavior("open")
}

func (t *testProcessor) methodBehavior(name string) error {
	switch t.cfg[name] {
	case "error":
		return errors.New(name + " error")
	case "panic":
		panic(name + " panic")
	case "", "success":
		return nil
	default:
		panic("unknown mode: " + t.cfg[name])
	}
}

func (t *testProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	err := t.methodBehavior("process")
	if err != nil {
		return t.makeErrorRecords(len(records), err)
	}

	out := make([]sdk.ProcessedRecord, len(records))
	for i, record := range records {
		outRec := record.Clone()
		original := outRec.Payload.After.(opencdc.RawData)
		outRec.Payload.After = opencdc.RawData(t.cfg["process.prefix"] + string(original.Bytes()))

		out[i] = sdk.SingleRecord(outRec)
	}

	return out
}

func (p *testProcessor) makeErrorRecords(num int, err error) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, num)
	for i := 0; i < num; i++ {
		out[i] = sdk.ErrorRecord{Err: err}
	}

	return out
}

func (t *testProcessor) Teardown(ctx context.Context) error {
	return nil
}
