// Copyright © 2026 Meroxa, Inc.
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

package unwrap

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// A JSON syntax error quotes the offending character of the record; errors
// reach the DLQ, the API and CLI output, so they report the offset only.
func TestUnwrapProcessors_InvalidJSONDoesNotContainTheValue(t *testing.T) {
	ctx := context.Background()
	for name, proc := range map[string]sdk.Processor{
		"debezium":      NewDebeziumProcessor(log.Nop()),
		"kafka connect": NewKafkaConnectProcessor(log.Nop()),
	} {
		for kind, after := range map[string]opencdc.Data{
			"raw":    opencdc.RawData(`{"payload": SECRET-4711}`),
			"string": nil,
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				is := is.New(t)
				cfg := config.Config{}
				rec := opencdc.Record{Payload: opencdc.Change{After: after}}
				if kind == "string" {
					cfg = config.Config{"field": ".Payload.After.msg"}
					rec.Payload.After = opencdc.StructuredData{"msg": `{"payload": SECRET-4711}`}
				}
				is.NoErr(proc.Configure(ctx, cfg))
				out := proc.Process(ctx, []opencdc.Record{rec})
				er, ok := out[0].(sdk.ErrorRecord)
				is.True(ok)
				is.True(strings.Contains(er.Error.Error(), "invalid JSON syntax at offset"))
				is.True(!strings.Contains(er.Error.Error(), "SECRET"))
				is.True(!strings.Contains(er.Error.Error(), "invalid character"))
			})
		}
	}
}
