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

package base64

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // a more descriptive example description
func ExampleDecodeProcessor() {
	p := NewDecodeProcessor(log.Nop())
	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Decode a base64 encoded string",
		Description: `This example decodes the base64 encoded string stored in
` + "`.Payload.After`" + `. Note that the result is a string, so if you want to
further process the result (e.g. parse the string as JSON), you need to chain
other processors (e.g. [` + "`json.decode`" + `](/docs/processors/builtin/json.decode)).`,
		Config: config.Config{
			"field": ".Payload.After.foo",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "YmFy",
				},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": "test-key",
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "foo": "YmFy"
	// +      "foo": "bar"
	//      }
	//    }
	//  }
}
