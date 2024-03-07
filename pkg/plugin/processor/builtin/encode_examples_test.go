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
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/json"
)

//nolint:govet // a more descriptive example description
func ExampleDecodeProcessor_structuredKey() {
	p := json.NewEncodeProcessor(log.Nop())

	RunExample(p, example{
		Description: `Encode a .Key structured data to JSON.`,
		Config:      map[string]string{"field": ".Key"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Key: opencdc.StructuredData{
				"tables": []string{"table1,table2"},
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData(`{"tables":["table1,table2"]}`),
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,10 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	// -  "key": {
	// -    "tables": [
	// -      "table1,table2"
	// -    ]
	// -  },
	// +  "key": "{\"tables\":[\"table1,table2\"]}",
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleDecodeProcessor_mapToJSON() {
	p := json.NewEncodeProcessor(log.Nop())

	RunExample(p, example{
		Description: `Encode a map under .Payload.Before.foo into a JSON value.`,
		Config:      map[string]string{"field": ".Payload.Before.foo"},
		Have: opencdc.Record{
			Operation: opencdc.OperationSnapshot,
			Payload: opencdc.Change{
				Before: opencdc.StructuredData{
					"foo": map[string]any{
						"before": map[string]any{"data": float64(4), "id": float64(3)},
						"baz":    "bar",
					},
				},
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationSnapshot,
			Payload: opencdc.Change{
				Before: opencdc.StructuredData{
					"foo": []uint8(`{"baz":"bar","before":{"data":4,"id":3}}`)},
			},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,18 +1,12 @@
	//  {
	//    "position": null,
	//    "operation": "snapshot",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": {
	// -      "foo": {
	// -        "baz": "bar",
	// -        "before": {
	// -          "data": 4,
	// -          "id": 3
	// -        }
	// -      }
	// +      "foo": "eyJiYXoiOiJiYXIiLCJiZWZvcmUiOnsiZGF0YSI6NCwiaWQiOjN9fQ=="
	//      },
	//      "after": null
	//    }
	//  }
}
