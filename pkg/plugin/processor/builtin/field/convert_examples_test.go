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

package field

import (
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_stringToInt() {
	p := NewConvertProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     `Change field type in key`,
		Description: "In this example we take the string in field `.Key.id` and change its data type to `int`.",
		Config:      map[string]string{"field": ".Key.id", "type": "int"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": 123},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	// -    "id": "123"
	// +    "id": 123
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	//        "foo": "bar"
	//      }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_intToBool() {
	p := NewConvertProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `change .Payload.After.done type to bool`,
		Config:  map[string]string{"field": ".Payload.After.done", "type": "bool"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"done": "1"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"done": true}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	//      "id": "123"
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "done": "1"
	// +      "done": true
	//      }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_floatToString() {
	p := NewConvertProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `change .Key.id type to string`,
		Config:  map[string]string{"field": ".Key.id", "type": "string"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": 123.345},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123.345"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	// -    "id": 123.345
	// +    "id": "123.345"
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	//        "foo": "bar"
	//      }
	//    }
	//  }
}
