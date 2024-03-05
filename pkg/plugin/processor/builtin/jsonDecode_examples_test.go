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
)

//nolint:govet // a more descriptive example description
func ExampleJSONDecodeProcessor_RawKey() {
	p := newJSONDecode()

	RunExample(p, example{
		Description: `Decode the raw data .Key into structured data.`,
		Config:      map[string]string{"field": ".Key"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData(`{"after":{"data":4,"id":3}}`),
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Key: opencdc.StructuredData{
				"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,15 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	// -  "key": "{\"after\":{\"data\":4,\"id\":3}}",
	// +  "key": {
	// +    "after": {
	// +      "data": 4,
	// +      "id": 3
	// +    }
	// +  },
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleJSONDecodeProcessor_RawPayload() {
	p := newJSONDecode()

	RunExample(p, example{
		Description: `Decode the raw data .Payload.Before into structured data.`,
		Config:      map[string]string{"field": ".Payload.Before"},
		Have: opencdc.Record{
			Operation: opencdc.OperationSnapshot,
			Payload: opencdc.Change{
				Before: opencdc.RawData(`{"before":{"data":4},"foo":"bar"}`),
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationSnapshot,
			Payload: opencdc.Change{
				Before: opencdc.StructuredData{
					"before": map[string]interface{}{"data": float64(4)},
					"foo":    "bar",
				},
			}},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,15 @@
	//  {
	//    "position": null,
	//    "operation": "snapshot",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	// -    "before": "{\"before\":{\"data\":4},\"foo\":\"bar\"}",
	// +    "before": {
	// +      "before": {
	// +        "data": 4
	// +      },
	// +      "foo": "bar"
	// +    },
	//      "after": null
	//    }
	//  }
}
