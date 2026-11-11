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

package field

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleHoistProcessor_payload() {
	p := NewHoistProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Wrap the payload under a field name",
		Description: "This example wraps `.Payload.After` under the field `row`, as Kafka Connect's `HoistField$Value` with `field=row` does.",
		Config:      config.Config{"name": "row"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Payload:   opencdc.Change{After: opencdc.StructuredData{"id": 1, "name": "Wong"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Payload:   opencdc.Change{After: opencdc.StructuredData{"row": map[string]any{"id": 1, "name": "Wong"}}},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,13 +1,15 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": null,
	// -    "after": {
	// +    "after": {
	// +      "row": {
	// -      "id": 1,
	// +        "id": 1,
	// -      "name": "Wong"
	// +        "name": "Wong"
	// +      }
	//      }
	//    }
	//  }
}

func ExampleHoistProcessor_key() {
	p := NewHoistProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Wrap a raw key",
		Description: "This example wraps a raw `.Key` under the field `id`. Raw data is stored as bytes, as Kafka Connect wraps a byte array; JSON shows the bytes in base64.",
		Config:      config.Config{"field": ".Key", "name": "id"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData("42"),
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.StructuredData{"id": []byte("42")},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,12 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	// -  "key": "42",
	// +  "key": {
	// +    "id": "NDI="
	// +  },
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}
