// Copyright © 2025 Meroxa, Inc.
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

package impl

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleSplitProcessor_simple() {
	p := NewSplitProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Split array into multiple records",
		Description: "This example takes the array in field `.Payload.After.users` and splits it into separate records, each containing one element.",
		Config:      config.Config{"field": ".Payload.After.users"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": 123},
			Payload: opencdc.Change{After: opencdc.StructuredData{
				"users": []map[string]any{
					{"name": "Alice", "age": 30},
					{"name": "Bob", "age": 25},
					{"name": "Charlie", "age": 35},
				},
			}},
		},
		Want: sdk.MultiRecord{
			opencdc.Record{
				Operation: opencdc.OperationUpdate,
				Metadata:  map[string]string{"split.index": "0"},
				Key:       opencdc.StructuredData{"id": 123},
				Payload: opencdc.Change{After: opencdc.StructuredData{
					"users": map[string]any{"name": "Alice", "age": 30},
				}},
			},
			opencdc.Record{
				Operation: opencdc.OperationUpdate,
				Metadata:  map[string]string{"split.index": "1"},
				Key:       opencdc.StructuredData{"id": 123},
				Payload: opencdc.Change{After: opencdc.StructuredData{
					"users": map[string]any{"name": "Bob", "age": 25},
				}},
			},
			opencdc.Record{
				Operation: opencdc.OperationUpdate,
				Metadata:  map[string]string{"split.index": "2"},
				Key:       opencdc.StructuredData{"id": 123},
				Payload: opencdc.Change{After: opencdc.StructuredData{
					"users": map[string]any{"name": "Charlie", "age": 35},
				}},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,27 +1,59 @@
	// -{
	// +[
	// +  {
	// -  "position": null,
	// +    "position": null,
	// -  "operation": "update",
	// +    "operation": "update",
	// -  "metadata": null,
	// +    "metadata": {
	// +      "split.index": "0"
	// +    },
	// -  "key": {
	// +    "key": {
	// -    "id": 123
	// +      "id": 123
	// -  },
	// +    },
	// -  "payload": {
	// -    "before": null,
	// -    "after": {
	// -      "users": [
	// -        {
	// -          "age": 30,
	// -          "name": "Alice"
	// -        },
	// -        {
	// -          "age": 25,
	// +    "payload": {
	// +      "before": null,
	// +      "after": {
	// +        "users": {
	// +          "age": 30,
	// +          "name": "Alice"
	// +        }
	// +      }
	// +    }
	// +  },
	// +  {
	// +    "position": null,
	// +    "operation": "update",
	// +    "metadata": {
	// +      "split.index": "1"
	// +    },
	// +    "key": {
	// +      "id": 123
	// +    },
	// +    "payload": {
	// +      "before": null,
	// +      "after": {
	// +        "users": {
	// +          "age": 25,
	// +          "name": "Bob"
	// +        }
	// +      }
	// +    }
	// +  },
	// +  {
	// +    "position": null,
	// +    "operation": "update",
	// +    "metadata": {
	// +      "split.index": "2"
	// +    },
	// +    "key": {
	// +      "id": 123
	// +    },
	// +    "payload": {
	// +      "before": null,
	// -          "name": "Bob"
	// +      "after": {
	// -        },
	// -        {
	// +        "users": {
	//            "age": 35,
	//            "name": "Charlie"
	//          }
	// -      ]
	// +      }
	//      }
	//    }
	// -}
	// +]
}
