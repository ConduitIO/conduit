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
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/js"
)

//nolint:govet // a more descriptive example description
func ExampleJavaScriptProcessor_Simple() {
	p := js.New(log.Nop())

	RunExample(p, example{
		Description: "",
		Config: map[string]string{
			"script": `function process(records) {
					records[0].Metadata["processed"] = "true";
					let existing = String.fromCharCode.apply(String, records[0].Payload.After);
					records[0].Payload.After = RawData("hello, " + existing);
					return records;
				}`,
		},
		Have: opencdc.Record{
			Metadata: map[string]string{
				"existing-key": "existing-value",
			},
			Payload: opencdc.Change{
				After: opencdc.RawData("world"),
			},
		},
		Want: sdk.SingleRecord{
			Metadata: map[string]string{
				"existing-key": "existing-value",
				"processed":    "true",
			},
			Payload: opencdc.Change{
				After: opencdc.RawData("hello, world"),
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,12 +1,13 @@
	//  {
	//    "position": null,
	//    "operation": "Operation(0)",
	//    "metadata": {
	// -    "existing-key": "existing-value"
	// +    "existing-key": "existing-value",
	// +    "processed": "true"
	//    },
	//    "key": null,
	//    "payload": {
	//      "before": null,
	//-    "after": "d29ybGQ="
	//+    "after": "aGVsbG8sIHdvcmxk"
	//    }
	//  }
}
