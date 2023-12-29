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

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/run"
)

func main() {
	run.Run(&testProcessor{})
}

type testProcessor struct {
}

func (p *testProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "test-processor",
		Summary:     "test processor's summary",
		Description: "test processor's description",
		Version:     "v1.3.5",
		Author:      "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"path": {
				Default:     "/",
				Type:        sdk.ParameterTypeString,
				Description: "path to something",
				Validations: []sdk.Validation{
					{
						Type:  sdk.ValidationTypeRegex,
						Value: "abc.*",
					},
				},
			},
		},
	}, nil
}

func (p *testProcessor) Configure(context.Context, map[string]string) error {
	// TODO implement me
	panic("implement me")
}

func (p *testProcessor) Open(context.Context) error {
	// TODO implement me
	panic("implement me again")
}

func (p *testProcessor) Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord {
	// TODO implement me
	panic("implement me")
}

func (p *testProcessor) Teardown(context.Context) error {
	return nil
}
