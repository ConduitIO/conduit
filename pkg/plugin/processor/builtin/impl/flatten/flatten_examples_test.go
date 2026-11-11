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

package flatten

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleProcessor_payload() {
	p := NewProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Flatten a nested payload",
		Description: "This example flattens the nested `customer` object of `.Payload.After` into fields named with " +
			"the default delimiter `.`. The `tags` array is kept as it is.",
		Config: config.Config{},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Payload: opencdc.Change{After: opencdc.StructuredData{
				"id":       42,
				"customer": map[string]any{"name": "Wong", "address": map[string]any{"city": "Lisbon"}},
				"tags":     []any{"a", "b"},
			}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Payload: opencdc.Change{After: opencdc.StructuredData{
				"id":                    42,
				"customer.name":         "Wong",
				"customer.address.city": "Lisbon",
				"tags":                  []any{"a", "b"},
			}},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,22 +1,18 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "customer": {
	// -        "address": {
	// -          "city": "Lisbon"
	// -        },
	// +      "customer.address.city": "Lisbon",
	// -        "name": "Wong"
	// -      },
	// +      "customer.name": "Wong",
	//        "id": 42,
	//        "tags": [
	//          "a",
	//          "b"
	//        ]
	//      }
	//    }
	//  }
}

func ExampleProcessor_key() {
	p := NewProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Flatten the key with a custom delimiter",
		Description: "This example flattens `.Key` with the delimiter `_`, as Kafka Connect's `Flatten$Key` with `delimiter=_` does.",
		Config:      config.Config{"field": ".Key", "delimiter": "_"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"tenant": map[string]any{"id": 7}, "order": 1},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"tenant_id": 7, "order": 1},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,15 +1,13 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	//      "order": 1,
	// -    "tenant": {
	// -      "id": 7
	// -    }
	// +    "tenant_id": 7
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}
