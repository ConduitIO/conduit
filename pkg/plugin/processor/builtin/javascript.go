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
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

type jsProcessor struct {
}

func (j jsProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "custom.javascript",
		Summary:     "",
		Description: "",
		Version:     "",
		Author:      "",
		Parameters: map[string]sdk.Parameter{
			"script": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "Processor code.",
				Validations: nil,
			},
			"script.path": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "Path to a .js file containing the processor code.",
				Validations: nil,
			},
		},
	}, nil
}

func (j jsProcessor) Configure(ctx context.Context, m map[string]string) error {
	//TODO implement me
	panic("implement me")
}

func (j jsProcessor) Open(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (j jsProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	//TODO implement me
	panic("implement me")
}

func (j jsProcessor) Teardown(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (j jsProcessor) mustEmbedUnimplementedProcessor() {
	//TODO implement me
	panic("implement me")
}
