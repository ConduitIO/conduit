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
	"github.com/conduitio/conduit-commons/config"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

const (
	debeziumOpCreate = "c"
	debeziumOpUpdate = "u"
	debeziumOpDelete = "d"
	debeziumOpRead   = "r"      // snapshot
	debeziumOpUnset  = "$unset" // mongoDB unset operation

	debeziumFieldBefore    = "before"
	debeziumFieldAfter     = "after"
	debeziumFieldSource    = "source"
	debeziumFieldOp        = "op"
	debeziumFieldTimestamp = "ts_ms"
)

type unwrapDebezium struct {
	sdk.UnimplementedProcessor
}

func (u *unwrapDebezium) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.debezium",
		Summary: "Unwraps a Debezium record from the input OpenCDC record.",
		Description: `This processor unwraps a Debezium record from the input OpenCDC record.

This is useful in cases where Conduit acts as an intermediary between a Debezium source and a Debezium destination. 
In such cases, the Debezium record is set as the OpenCDC record's payload, and needs to be unwrapped for further usage.`,
		Version: "v0.1.0",
		Author:  "Meroxa, Inc.",
		Parameters: config.Parameters{
			"field": {
				Default: ".Payload.After",
				Description: `Reference to the field which contains the wrapped Debezium record.

This should be a valid reference within an OpenCDC record, as specified here: https://github.com/ConduitIO/conduit-processor-sdk/blob/main/util.go#L66
`,
				Type: config.ParameterTypeString,
				Validations: []config.Validation{
					config.ValidationRequired{},
				},
			},
		},
	}, nil
}

func (u *unwrapDebezium) Configure(ctx context.Context, m map[string]string) error {
	//TODO implement me
	panic("implement me")
}

func (u *unwrapDebezium) Open(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (u *unwrapDebezium) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	//TODO implement me
	panic("implement me")
}

func (u *unwrapDebezium) Teardown(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (u *unwrapDebezium) mustEmbedUnimplementedProcessor() {
	//TODO implement me
	panic("implement me")
}
